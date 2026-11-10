package storage

import (
	"bytes"
	stderrors "errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/database"
	"go.uber.org/zap"
)

// Database wraps encrypted database operations
type Database struct {
	db     *database.DB
	logger *zap.SugaredLogger
}

// NewDatabase creates a new Database instance
func NewDatabase(dataDir string, key []byte, logger *zap.SugaredLogger) (*Database, error) {
	db, err := database.Open(filepath.Join(dataDir, "primary.db"), key)
	if err != nil {
		return nil, err
	}
	store := &Database{db: db, logger: logger}
	if err := store.initBuckets(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the database
func (b *Database) Close() error {
	return b.db.Close()
}

// initBuckets creates required buckets and sets up schema
func (b *Database) initBuckets() error {
	return b.db.Update(func(tx *database.Tx) error {
		// Create buckets
		buckets := []string{
			ServersBucket,
			ToolStatsBucket,
			ToolApprovalBucket,
			OAuthTokenBucket,
			MetaBucket,
			ActivityRecordsBucket,
			ActivityStatsBucket,
			SessionsBucket,
		}

		for _, bucket := range buckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(bucket)); err != nil {
				return fmt.Errorf("failed to create bucket %s: %w", bucket, err)
			}
		}

		// Enforce the session retention cap when opening the database.
		if err := enforceSessionRetentionOnOpen(tx, b.logger); err != nil {
			return fmt.Errorf("failed to enforce session retention: %w", err)
		}

		return nil
	})
}

// GetServer retrieves a canonical server record by name.
func (b *Database) GetServer(id string) (*ServerRecord, error) {
	var record *ServerRecord

	err := b.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ServersBucket))
		data := bucket.Get([]byte(id))
		if data == nil {
			return ErrUpstreamNotFound
		}

		record = &ServerRecord{}
		return record.UnmarshalBinary(data)
	})

	return record, err
}

// ListServers returns all canonical server records
func (b *Database) ListServers() ([]*ServerRecord, error) {
	var records []*ServerRecord

	err := b.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ServersBucket))
		return bucket.ForEach(func(_, v []byte) error {
			record := &ServerRecord{}
			if err := record.UnmarshalBinary(v); err != nil {
				return err
			}
			records = append(records, record)
			return nil
		})
	})

	return records, err
}

// Tool statistics operations

// IncrementToolStats increments the usage count for a tool
func (b *Database) IncrementToolStats(toolName string) error {
	return b.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ToolStatsBucket))

		// Get existing record
		var record ToolStatRecord
		data := bucket.Get([]byte(toolName))
		if data != nil {
			if err := record.UnmarshalBinary(data); err != nil {
				return err
			}
		} else {
			record.ToolName = toolName
		}

		// Increment count and update timestamp
		record.Count++
		record.LastUsed = time.Now()

		// Save back
		newData, err := record.MarshalBinary()
		if err != nil {
			return err
		}

		return bucket.Put([]byte(toolName), newData)
	})
}

// GetToolStats retrieves tool statistics
func (b *Database) GetToolStats(toolName string) (*ToolStatRecord, error) {
	var record *ToolStatRecord

	err := b.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ToolStatsBucket))
		data := bucket.Get([]byte(toolName))
		if data == nil {
			return fmt.Errorf("tool stats not found")
		}

		record = &ToolStatRecord{}
		return record.UnmarshalBinary(data)
	})

	return record, err
}

// ListToolStats returns all tool statistics
func (b *Database) ListToolStats() ([]*ToolStatRecord, error) {
	var records []*ToolStatRecord

	err := b.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ToolStatsBucket))
		return bucket.ForEach(func(_, v []byte) error {
			record := &ToolStatRecord{}
			if err := record.UnmarshalBinary(v); err != nil {
				return err
			}
			records = append(records, record)
			return nil
		})
	})

	return records, err
}

// Tool approval operations (tool-level quarantine)

// SaveToolApproval saves a tool approval record
func (b *Database) SaveToolApproval(record *ToolApprovalRecord) error {
	return b.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ToolApprovalBucket))
		data, err := record.MarshalBinary()
		if err != nil {
			return err
		}
		return bucket.Put([]byte(record.Key()), data)
	})
}

func (b *Database) GetToolApproval(serverName, toolName string) (*ToolApprovalRecord, error) {
	var record *ToolApprovalRecord

	err := b.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ToolApprovalBucket))
		key := ToolApprovalKey(serverName, toolName)
		data := bucket.Get([]byte(key))
		if data == nil {
			return fmt.Errorf("%w: %s", ErrToolApprovalNotFound, key)
		}

		record = &ToolApprovalRecord{}
		return record.UnmarshalBinary(data)
	})

	return record, err
}

// ListToolApprovals returns all tool approval records for a server.
// If serverName is empty, returns all records across all servers.
func (b *Database) ListToolApprovals(serverName string) ([]*ToolApprovalRecord, error) {
	var records []*ToolApprovalRecord

	prefix := ""
	if serverName != "" {
		prefix = serverName + ":"
	}

	err := b.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ToolApprovalBucket))
		return bucket.ForEach(func(k, v []byte) error {
			if prefix != "" && !bytes.HasPrefix(k, []byte(prefix)) {
				return nil
			}

			record := &ToolApprovalRecord{}
			if err := record.UnmarshalBinary(v); err != nil {
				return err
			}
			records = append(records, record)
			return nil
		})
	})

	return records, err
}

// DeleteToolApproval deletes a tool approval record
func (b *Database) DeleteToolApproval(serverName, toolName string) error {
	return b.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ToolApprovalBucket))
		key := ToolApprovalKey(serverName, toolName)
		return bucket.Delete([]byte(key))
	})
}

// DeleteToolApprovalIf deletes the record only when it still exists and
// eligible returns true for its CURRENT stored value, evaluated in the same
// write transaction as the delete. It reports whether a record was removed.
func (b *Database) DeleteToolApprovalIf(serverName, toolName string, eligible func(*ToolApprovalRecord) bool) (bool, error) {
	deleted := false
	err := b.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ToolApprovalBucket))
		key := []byte(ToolApprovalKey(serverName, toolName))
		raw := bucket.Get(key)
		if raw == nil {
			return nil
		}
		record := &ToolApprovalRecord{}
		if err := record.UnmarshalBinary(raw); err != nil {
			return err
		}
		if !eligible(record) {
			return nil
		}
		deleted = true
		return bucket.Delete(key)
	})
	return deleted, err
}

// OAuth token operations

// GetOAuthToken retrieves an OAuth token record by server name
func (b *Database) GetOAuthToken(serverName string) (*OAuthTokenRecord, error) {
	var record *OAuthTokenRecord

	err := b.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(OAuthTokenBucket))
		data := bucket.Get([]byte(serverName))
		if data == nil {
			return fmt.Errorf("oauth token not found")
		}

		record = &OAuthTokenRecord{}
		return record.UnmarshalBinary(data)
	})

	return record, err
}

// DeleteOAuthToken deletes an OAuth token record
func (b *Database) DeleteOAuthToken(serverName string) error {
	return b.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(OAuthTokenBucket))
		return bucket.Delete([]byte(serverName))
	})
}

// ErrSkipOAuthTokenUpdate may be returned by an UpdateOAuthToken mutate
// function to leave the record untouched without reporting an error (e.g. a
// compare-and-swap whose expected generation no longer matches).
var ErrSkipOAuthTokenUpdate = stderrors.New("skip oauth token update")

// ListOAuthTokens returns all OAuth token records
func (b *Database) ListOAuthTokens() ([]*OAuthTokenRecord, error) {
	var records []*OAuthTokenRecord

	err := b.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(OAuthTokenBucket))
		return bucket.ForEach(func(_, v []byte) error {
			record := &OAuthTokenRecord{}
			if err := record.UnmarshalBinary(v); err != nil {
				return err
			}
			records = append(records, record)
			return nil
		})
	})

	return records, err
}
