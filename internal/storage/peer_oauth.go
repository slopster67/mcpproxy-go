package storage

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/database"
)

var peerOAuthBucket = []byte("peer_oauth")

type peerOAuthState struct {
	LoggedOut bool                         `json:"logged_out"`
	Binding   commitview.CredentialBinding `json:"binding"`
	Writer    string                       `json:"writer"`
	Owner     string                       `json:"owner"`
	Lease     string                       `json:"lease"`
	Revision  uint64                       `json:"revision"`
	LastID    string                       `json:"last_id"`
	Key       string                       `json:"key"`
}

func peerOAuthStateTx(tx *database.Tx, name string) (*database.Bucket, peerOAuthState, error) {
	bucket, err := tx.CreateBucketIfNotExists(peerOAuthBucket)
	var state peerOAuthState
	if err != nil {
		return nil, state, err
	}
	if raw := bucket.Get([]byte(name)); raw != nil {
		err = json.Unmarshal(raw, &state)
	}
	return bucket, state, err
}
func savePeerOAuthState(bucket *database.Bucket, name string, state *peerOAuthState) error {
	seq, err := bucket.NextSequence()
	if err != nil {
		return err
	}
	state.Revision = seq
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return bucket.Put([]byte(name), raw)
}
func executionOwnerTx(tx *database.Tx, name, peer string, binding commitview.CredentialBinding) bool {
	bucket := tx.Bucket([]byte(ServersBucket))
	if bucket == nil {
		return false
	}
	raw := bucket.Get([]byte(name))
	if raw == nil {
		return false
	}
	var row ServerRecord
	return json.Unmarshal(raw, &row) == nil && row.ExecutionHost() == peer && row.Incarnation == binding.Incarnation && row.OAuthEpoch == binding.OAuthEpoch
}
func (b *Database) PeerOAuthSnapshot(peer, name, writer string, binding commitview.CredentialBinding) (OAuthSnapshot, error) {
	var result OAuthSnapshot
	err := b.db.Update(func(tx *database.Tx) error {
		if writer == "" || !executionOwnerTx(tx, name, peer, binding) {
			return commitview.ErrStaleGeneration
		}
		bucket, state, err := peerOAuthStateTx(tx, name)
		if err != nil {
			return err
		}
		if state.Key == "" {
			tokens := tx.Bucket([]byte(OAuthTokenBucket))
			err = tokens.ForEach(func(key, raw []byte) error {
				if len(key) != len(name)+17 || !strings.HasPrefix(string(key), name+"_") {
					return nil
				}
				var record OAuthTokenRecord
				if err := record.UnmarshalBinary(raw); err != nil {
					return err
				}
				if OAuthRecordVisible(&record, binding) {
					state.Key = string(key)
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		if state.Lease == "" || state.Binding != binding || state.Writer != writer || state.Owner != peer {
			state.Binding = binding
			state.Writer = writer
			state.Owner = peer
			state.Lease = uuid.NewString()
			state.LastID = ""
			if err = savePeerOAuthState(bucket, name, &state); err != nil {
				return err
			}
		}
		var rec *OAuthTokenRecord
		if state.Key != "" {
			rec, err = getOAuthRecordTx(tx.Bucket([]byte(OAuthTokenBucket)), state.Key)
			if err != nil {
				return err
			}
			if !OAuthRecordVisible(rec, binding) {
				rec = nil
			}
		}
		result = OAuthSnapshot{LoggedOut: state.LoggedOut, Binding: binding, Revision: state.Revision, Lease: state.Lease, Record: rec}
		return nil
	})
	return result, err
}
func (b *Database) SavePeerOAuth(peer, name, writer string, update OAuthUpdate) (uint64, error) {
	var revision uint64
	err := b.db.Update(func(tx *database.Tx) error {
		if !executionOwnerTx(tx, name, peer, update.Binding) {
			return commitview.ErrStaleGeneration
		}
		bucket, state, err := peerOAuthStateTx(tx, name)
		if err != nil {
			return err
		}
		if state.Writer != writer || state.Owner != peer || state.Binding != update.Binding || state.Lease != update.Lease {
			return commitview.ErrStaleGeneration
		}
		if update.ID != "" && state.LastID == update.ID {
			revision = state.Revision
			return nil
		}
		if update.ID == "" || state.Revision != update.Revision {
			return commitview.ErrStaleGeneration
		}
		suffix := strings.TrimPrefix(update.Key, name+"_")
		if len(suffix) != 16 || suffix == update.Key {
			return errors.New("invalid credential key")
		}
		if _, err = hex.DecodeString(suffix); err != nil {
			return errors.New("invalid credential key")
		}
		tokens := tx.Bucket([]byte(OAuthTokenBucket))
		if state.Key != "" && state.Key != update.Key {
			if err = tokens.Delete([]byte(state.Key)); err != nil {
				return err
			}
		}
		if update.Record == nil {
			err = tokens.Delete([]byte(update.Key))
		} else {
			record := *update.Record
			record.ServerName = update.Key
			record.DisplayName = name
			record.OAuthEpoch = update.Binding.OAuthEpoch
			if record.AccessToken != "" {
				state.LoggedOut = false
			}
			err = putOAuthRecord(tokens, update.Key, &record)
		}
		if err != nil {
			return err
		}
		state.Key = update.Key
		state.LastID = update.ID
		if err = savePeerOAuthState(bucket, name, &state); err != nil {
			return err
		}
		revision = state.Revision
		return nil
	})
	return revision, err
}

// RevokePeerOAuth advances the lease even when no token exists. A refresh that
// was already in flight can neither resurrect a logged-out grant nor replay it.
func (b *Database) RevokePeerOAuth(name string, binding commitview.CredentialBinding) (OAuthSnapshot, error) {
	var result OAuthSnapshot
	err := b.db.Update(func(tx *database.Tx) error {
		bucket, state, err := peerOAuthStateTx(tx, name)
		if err != nil {
			return err
		}
		if _, err = clearOAuthStateTx(tx.Bucket([]byte(OAuthTokenBucket)), name); err != nil {
			return err
		}
		state.Binding = binding
		state.LoggedOut = true
		state.Lease = uuid.NewString()
		state.LastID = ""
		if err = savePeerOAuthState(bucket, name, &state); err != nil {
			return err
		}
		result = OAuthSnapshot{LoggedOut: state.LoggedOut, Binding: binding, Revision: state.Revision, Lease: state.Lease}
		return nil
	})
	return result, err
}
