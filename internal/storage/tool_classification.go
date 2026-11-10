package storage

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/database"
)

const toolClassificationBucket = "tool_classifications"

var ErrToolClassificationStale = errors.New("tool changed; refresh before classifying")
var ErrToolAlreadyAnnotated = errors.New("this tool already has server annotations")

type toolClassification struct {
	Tier string `json:"tier"`
	Hash string `json:"hash"`
}

// SetToolClassification annotates only the exact definition the operator saw.
// It never approves/enables a tool or modifies the server's annotations.
func (m *Manager) SetToolClassification(server, tool, tier, expectedHash string) error {
	if tier != "" && tier != "read" && tier != "write" && tier != "destructive" {
		return fmt.Errorf("invalid access level %q", tier)
	}
	if expectedHash == "" {
		return ErrToolClassificationStale
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.db.db.Update(func(tx *database.Tx) error {
		approvals := tx.Bucket([]byte(ToolApprovalBucket))
		if approvals == nil {
			return ErrToolApprovalNotFound
		}
		key := []byte(ToolApprovalKey(server, tool))
		raw := approvals.Get(key)
		if raw == nil {
			return ErrToolApprovalNotFound
		}
		var record ToolApprovalRecord
		if err := record.UnmarshalBinary(raw); err != nil {
			return err
		}
		if record.CurrentHash != expectedHash {
			return ErrToolClassificationStale
		}
		if tier != "" && contracts.AnnotationTier(record.CurrentAnnotations) != contracts.TierUnannotated {
			return ErrToolAlreadyAnnotated
		}
		bucket, err := tx.CreateBucketIfNotExists([]byte(toolClassificationBucket))
		if err != nil {
			return err
		}
		if tier == "" {
			return bucket.Delete(key)
		}
		data, err := json.Marshal(toolClassification{Tier: tier, Hash: expectedHash})
		if err != nil {
			return err
		}
		return bucket.Put(key, data)
	})
}

// GetToolClassification drops stale labels from the effective view immediately.
func (m *Manager) GetToolClassification(server, tool string) (string, error) {
	return m.GetToolClassificationAtHash(server, tool, "")
}
func (m *Manager) GetToolClassificationAtHash(server, tool, expectedHash string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var tier string
	err := m.db.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(toolClassificationBucket))
		if bucket == nil {
			return nil
		}
		key := []byte(ToolApprovalKey(server, tool))
		data := bucket.Get(key)
		if data == nil {
			return nil
		}
		var value toolClassification
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		approvals := tx.Bucket([]byte(ToolApprovalBucket))
		if approvals == nil {
			return nil
		}
		raw := approvals.Get(key)
		if raw == nil {
			return nil
		}
		var record ToolApprovalRecord
		if err := record.UnmarshalBinary(raw); err != nil {
			return err
		}
		if (expectedHash == "" || record.CurrentHash == expectedHash) && record.CurrentHash == value.Hash && (value.Tier == "read" || value.Tier == "write" || value.Tier == "destructive") && contracts.AnnotationTier(record.CurrentAnnotations) == contracts.TierUnannotated {
			tier = value.Tier
		}
		return nil
	})
	return tier, err
}
