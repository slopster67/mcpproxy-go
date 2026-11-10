package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/database"

	"github.com/slopster67/mcpproxy-go/internal/config"
)

// Incarnation-conditional writes of purge-set state by asynchronous writers
// that can outlive a removal: a connect worker registering the server
// identity, a tool call recording its history.
//
// Each method runs ONE database Update that reads the upstreams row and writes
// only when it exists and carries the expected incarnation. The removal purge
// deletes the row and the whole purge set in one transaction as well, so the
// two serialize: either the write committed first (and the purge deleted it)
// or it sees the row missing or re-incarnated and writes nothing. No lock
// beyond the database's own is taken, so this adds no lock-order edge.
//
// inc == 0 means "unbound" (a caller that has no committed view). For the
// tool-call writer the unbound write is limited to a database no commit has
// written: once a commit exists every configured server has an incarnation,
// so an unbound write comes from a caller that looked its server up after the
// removal, and writing would recreate the removed server's history. It is
// refused in the same transaction that would have written.

// rowIncarnationMatchesTx requires the canonical record to carry the captured identity.
func rowIncarnationMatchesTx(tx *database.Tx, name string, inc int64) (bool, error) {
	b := tx.Bucket([]byte(ServersBucket))
	if b == nil {
		return false, nil
	}
	data := b.Get([]byte(name))
	if data == nil {
		return false, nil
	}
	rec := &ServerRecord{}
	if err := rec.UnmarshalBinary(data); err != nil {
		return false, fmt.Errorf("decode upstream %s: %w", name, err)
	}
	return inc > 0 && rec.Incarnation == inc, nil
}

// writeAllowedTx reports whether a conditional write for name bound to inc
// may proceed: inc > 0 needs the row to carry it; inc == 0 (unbound) is only
// allowed while no config commit has been recorded.
func writeAllowedTx(tx *database.Tx, name string, inc int64) (bool, error) {
	if inc == 0 {
		if meta := tx.Bucket([]byte(MetaBucket)); meta != nil && meta.Get([]byte(ConfigCommitGenerationKey)) != nil {
			return false, nil
		}
		return true, nil
	}
	return rowIncarnationMatchesTx(tx, name, inc)
}

// RegisterServerIdentityIfIncarnation is RegisterServerIdentity conditional on
// the server's row carrying incarnation inc. written is false (and nothing is
// stored) when the server was removed or replaced.
func (m *Manager) RegisterServerIdentityIfIncarnation(server *config.ServerConfig, inc int64) (identity *ServerIdentity, written bool, err error) {
	if inc == 0 {
		identity, err = m.RegisterServerIdentity(server)
		return identity, err == nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	serverID := GenerateServerID(server)
	err = m.db.db.Update(func(tx *database.Tx) error {
		ok, err := rowIncarnationMatchesTx(tx, server.Name, inc)
		if err != nil || !ok {
			return err
		}
		bucket, err := tx.CreateBucketIfNotExists([]byte(serverIdentitiesBucket))
		if err != nil {
			return err
		}
		if data := bucket.Get([]byte(serverID)); data != nil {
			existing := &ServerIdentity{}
			if err := json.Unmarshal(data, existing); err != nil {
				return fmt.Errorf("decode server identity %s: %w", serverID, err)
			}
			existing.UpdateLastSeen()
			identity = existing
		} else {
			identity = NewServerIdentity(server)
		}
		data, err := json.Marshal(identity)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(identity.ID), data); err != nil {
			return err
		}
		written = true
		return nil
	})
	if err != nil || !written {
		return nil, false, err
	}
	return identity, true, nil
}

// RecordToolCallIfIncarnation is RecordToolCall conditional on the row of
// record.ServerName carrying incarnation inc (code_execution parent records
// are exempt: they belong to no upstream and are recorded unconditionally).
func (m *Manager) RecordToolCallIfIncarnation(record *ToolCallRecord, inc int64) (bool, error) {
	return m.recordToolCallIfIncarnation(record, inc, "", false)
}

// toolCallRecordExistsTx reports whether serverID's history bucket holds a
// record of callID. Keys are "<unix-nano>_<id>"; the bucket is bounded by
// tool_call_max_records, so the scan is bounded too.
func toolCallRecordExistsTx(tx *database.Tx, serverID, callID string) (bool, error) {
	if serverID == "" || callID == "" {
		return false, nil
	}
	b := tx.Bucket([]byte(toolCallsBucketName(serverID)))
	if b == nil {
		return false, nil
	}
	suffix := []byte("_" + callID)
	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		if bytes.HasSuffix(k, suffix) {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) recordToolCallIfIncarnation(record *ToolCallRecord, inc int64, originalCallID string, replay bool) (bool, error) {
	if record == nil || record.ServerID == codeExecutionServerID {
		if err := m.RecordToolCall(record); err != nil {
			return false, err
		}
		return true, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	maxResponseBytes, maxRecords := m.toolCallLimits()
	bounded := boundToolCallResponse(record, maxResponseBytes)
	key := fmt.Sprintf("%d_%s", record.Timestamp.UnixNano(), record.ID)
	written := false
	err := m.db.db.Update(func(tx *database.Tx) error {
		ok, err := writeAllowedTx(tx, record.ServerName, inc)
		if err != nil || !ok {
			return err
		}
		if replay {
			present, err := identityNamesServerTx(tx, record.ServerID, record.ServerName)
			if err != nil || !present {
				return err
			}
			present, err = toolCallRecordExistsTx(tx, record.ServerID, originalCallID)
			if err != nil || !present {
				return err
			}
		}
		// The history bucket is keyed by the server identity hash, and the
		// removal purge finds such buckets through server_identities. A call
		// can finish before the connect worker registered the identity, so
		// the identity is ensured in this transaction:
		// no history bucket ever exists without an identity naming its server.
		if err := ensureCallIdentityTx(tx, record.ServerName, record.ServerID); err != nil {
			return err
		}
		bucket, err := tx.CreateBucketIfNotExists([]byte(toolCallsBucketName(record.ServerID)))
		if err != nil {
			return err
		}
		data, err := json.Marshal(bounded)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(key), data); err != nil {
			return err
		}
		written = true
		return pruneToolCallBucket(bucket, maxRecords)
	})
	return written && err == nil, err
}

// ensureCallIdentityTx writes a server identity for serverID naming
// serverName unless one exists. Its attributes come from the stored row when
// the row's configuration hashes to serverID; otherwise it carries only the
// id and the name (enough for the purge and the orphan sweep).
func ensureCallIdentityTx(tx *database.Tx, serverName, serverID string) error {
	if serverID == "" {
		return nil
	}
	ids, err := tx.CreateBucketIfNotExists([]byte(serverIdentitiesBucket))
	if err != nil {
		return err
	}
	if ids.Get([]byte(serverID)) != nil {
		return nil
	}
	var identity *ServerIdentity
	if b := tx.Bucket([]byte(ServersBucket)); b != nil {
		if data := b.Get([]byte(serverName)); data != nil {
			rec := &ServerRecord{}
			if rec.UnmarshalBinary(data) == nil {
				if cfg := serverConfigFromRecord(rec); cfg != nil && GenerateServerID(cfg) == serverID {
					identity = NewServerIdentity(cfg)
				}
			}
		}
	}
	if identity == nil {
		now := time.Now()
		identity = &ServerIdentity{ID: serverID, ServerName: serverName, FirstSeen: now, LastSeen: now}
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	return ids.Put([]byte(serverID), data)
}

// ServerIncarnation returns the incarnation stamped on name's row. ok is false
// when no row exists.
func (m *Manager) ServerIncarnation(name string) (inc int64, ok bool, err error) {
	row, err := m.GetServerRecord(name)
	if err != nil {
		if errors.Is(err, ErrUpstreamNotFound) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return row.Incarnation, true, nil
}

// identityNamesServerTx reports whether the server identity serverID exists
// and names serverName.
func identityNamesServerTx(tx *database.Tx, serverID, serverName string) (bool, error) {
	if serverID == "" {
		return false, nil
	}
	ids := tx.Bucket([]byte(serverIdentitiesBucket))
	if ids == nil {
		return false, nil
	}
	data := ids.Get([]byte(serverID))
	if data == nil {
		return false, nil
	}
	identity := &ServerIdentity{}
	if err := json.Unmarshal(data, identity); err != nil {
		return false, fmt.Errorf("decode server identity %s: %w", serverID, err)
	}
	return identity.ServerName == serverName, nil
}
