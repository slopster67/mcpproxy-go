package storage

import (
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/slopster67/mcpproxy-go/internal/database"
)

// DefaultMaxResponseSize is the default maximum size for response truncation (64KB)
const DefaultMaxResponseSize = 64 * 1024

// activityKey generates the storage key for an activity record.
// Key format: {timestamp_ns}_{ulid} for natural reverse-chronological ordering.
// Using 20-digit nanosecond timestamp ensures consistent ordering.
func activityKey(timestamp time.Time, id string) []byte {
	return []byte(fmt.Sprintf("%020d_%s", timestamp.UnixNano(), id))
}

// parseActivityKey extracts the ULID from an activity key.
// Returns empty string if key format is invalid.
func parseActivityKey(key []byte) string {
	keyStr := string(key)
	// Key format: {20-digit timestamp}_{ulid}
	if len(keyStr) < 22 { // 20 digits + underscore + at least 1 char for id
		return ""
	}
	return keyStr[21:]
}

// truncateResponse truncates a response string if it exceeds maxSize.
// Returns the (potentially truncated) string and whether truncation occurred.
func truncateResponse(response string, maxSize int) (string, bool) {
	if maxSize <= 0 {
		maxSize = DefaultMaxResponseSize
	}
	if len(response) <= maxSize {
		return response, false
	}
	return response[:maxSize] + "...[truncated]", true
}

// SaveActivity stores an activity record.
// The record is stored with a composite key for efficient time-based queries.
func (m *Manager) SaveActivity(record *ActivityRecord) error {
	if record == nil {
		return fmt.Errorf("activity record cannot be nil")
	}

	// Generate ID if not set
	if record.ID == "" {
		record.ID = ulid.Make().String()
	}

	// Set timestamp if not set
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	}

	err := m.db.db.Update(func(tx *database.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(ActivityRecordsBucket))
		if err != nil {
			return fmt.Errorf("failed to create activity bucket: %w", err)
		}

		data, err := record.MarshalBinary()
		if err != nil {
			return fmt.Errorf("failed to marshal activity record: %w", err)
		}

		key := activityKey(record.Timestamp, record.ID)
		if err := bucket.Put(key, data); err != nil {
			return fmt.Errorf("failed to store activity record: %w", err)
		}

		return nil
	})
	if err == nil && m.activityObserver != nil {
		m.activityObserver(record)
	}
	return err
}

// GetActivity retrieves an activity record by ID.
// Returns nil if the record is not found.
func (m *Manager) GetActivity(id string) (*ActivityRecord, error) {
	if id == "" {
		return nil, fmt.Errorf("activity ID cannot be empty")
	}

	var record *ActivityRecord

	err := m.db.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ActivityRecordsBucket))
		if bucket == nil {
			return nil // No activities yet
		}

		// Scan to find the record by ID (ID is in the key suffix)
		cursor := bucket.Cursor()
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			if parseActivityKey(k) == id {
				record = &ActivityRecord{}
				if err := record.UnmarshalBinary(v); err != nil {
					return fmt.Errorf("failed to unmarshal activity record: %w", err)
				}
				return nil
			}
		}

		return nil // Not found
	})

	if err != nil {
		return nil, err
	}

	return record, nil
}

// ListActivities returns paginated activity records matching the filter.
// Records are returned in reverse chronological order (newest first).
// Returns the records, total matching count, and any error.
func (m *Manager) ListActivities(filter ActivityFilter) ([]*ActivityRecord, int, error) {
	filter.Validate()

	var records []*ActivityRecord
	var total int

	err := m.db.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ActivityRecordsBucket))
		if bucket == nil {
			return nil // No activities yet
		}

		// Iterate in reverse order (newest first)
		cursor := bucket.Cursor()
		skipped := 0

		for k, v := cursor.Last(); k != nil; k, v = cursor.Prev() {
			var record ActivityRecord
			if err := record.UnmarshalBinary(v); err != nil {
				m.logger.Warnw("Failed to unmarshal activity record",
					"key", string(k),
					"error", err)
				continue
			}

			// Check if record matches filter
			if !filter.Matches(&record) {
				continue
			}

			total++

			// Handle pagination
			if skipped < filter.Offset {
				skipped++
				continue
			}

			if len(records) < filter.Limit {
				// `record` is declared fresh each iteration, so taking a
				// copy's address is safe.
				rec := record
				records = append(records, &rec)
			}
		}

		return nil
	})

	return records, total, err
}

// ToolUsageStat is a per-tool rollup of activity over a bounded window.
// Used by the global tools view. A zero LastUsed means the tool
// was never used within the window.
type ToolUsageStat struct {
	Count    int
	LastUsed time.Time
}

// LastCallPerServer returns each server's latest counted call (CountsAsCall)
// in the retained activity log.
func (m *Manager) LastCallPerServer() (map[string]time.Time, error) {
	last := make(map[string]time.Time)
	err := m.db.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ActivityRecordsBucket))
		if bucket == nil {
			return nil
		}
		cursor := bucket.Cursor()
		for k, v := cursor.Last(); k != nil; k, v = cursor.Prev() {
			var record ActivityRecord
			if err := record.UnmarshalBinary(v); err != nil {
				continue
			}
			if counted, _ := CountsAsCall(&record); !counted || record.ServerName == "" {
				continue
			}
			if at, ok := last[record.ServerName]; !ok || record.Timestamp.After(at) {
				last[record.ServerName] = record.Timestamp
			}
		}
		return nil
	})
	return last, err
}

// toolUsageKey builds the map key for AggregateToolUsage. A NUL separator is
// used so it cannot collide with ':' or other characters valid in server/tool
// names.
func toolUsageKey(serverName, toolName string) string {
	return serverName + "\x00" + toolName
}

// AggregateToolUsage performs a single pass over the activity bucket and
// returns per-(server,tool) call counts and last-used time for tool_call
// records with Timestamp >= since. Records outside the window and non
// tool_call records are skipped. An empty/absent bucket yields an empty map
// (not an error). This is the usage source for GET /api/v1/tools.
func (m *Manager) AggregateToolUsage(since time.Time) (map[string]ToolUsageStat, error) {
	stats := make(map[string]ToolUsageStat)

	err := m.db.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ActivityRecordsBucket))
		if bucket == nil {
			return nil // no activity yet
		}

		cursor := bucket.Cursor()
		for k, v := cursor.Last(); k != nil; k, v = cursor.Prev() {
			var record ActivityRecord
			if err := record.UnmarshalBinary(v); err != nil {
				m.logger.Warnw("Failed to unmarshal activity record during usage aggregation",
					"key", string(k), "error", err)
				continue
			}

			if record.Type != ActivityTypeToolCall {
				continue
			}
			if record.ToolName == "" {
				continue
			}
			if record.Timestamp.Before(since) {
				continue
			}

			key := toolUsageKey(record.ServerName, record.ToolName)
			st := stats[key]
			st.Count++
			if record.Timestamp.After(st.LastUsed) {
				st.LastUsed = record.Timestamp
			}
			stats[key] = st
		}

		return nil
	})

	return stats, err
}

// StreamActivities returns a channel that yields activity records matching the filter.
// The channel is closed when all matching records have been sent.
// This is useful for streaming large exports without loading all records into memory.
// Respects filter.Limit and filter.Offset for bounded streaming.
func (m *Manager) StreamActivities(filter ActivityFilter) <-chan *ActivityRecord {
	ch := make(chan *ActivityRecord, 100)

	go func() {
		defer close(ch)

		err := m.db.db.View(func(tx *database.Tx) error {
			bucket := tx.Bucket([]byte(ActivityRecordsBucket))
			if bucket == nil {
				return nil
			}

			cursor := bucket.Cursor()
			skipped := 0
			sent := 0

			for k, v := cursor.Last(); k != nil; k, v = cursor.Prev() {
				var record ActivityRecord
				if err := record.UnmarshalBinary(v); err != nil {
					continue
				}

				if !filter.Matches(&record) {
					continue
				}

				// Handle offset
				if filter.Offset > 0 && skipped < filter.Offset {
					skipped++
					continue
				}

				// Handle limit — stop after sending limit records
				if filter.Limit > 0 && sent >= filter.Limit {
					return nil
				}

				ch <- &record
				sent++
			}

			return nil
		})

		if err != nil {
			m.logger.Errorw("Error streaming activities", "error", err)
		}
	}()

	return ch
}

// PruneOldActivities deletes activity records older than the specified duration.
// Returns the number of records deleted.
func (m *Manager) PruneOldActivities(maxAge time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	cutoffKey := activityKey(cutoff, "")

	var deleted int

	err := m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ActivityRecordsBucket))
		if bucket == nil {
			return nil
		}

		var keysToDelete [][]byte
		cursor := bucket.Cursor()

		for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
			// Compare keys lexicographically
			if string(k) < string(cutoffKey) {
				keysToDelete = append(keysToDelete, append([]byte{}, k...))
			} else {
				break // Keys are sorted, no more old records
			}
		}

		for _, key := range keysToDelete {
			if err := bucket.Delete(key); err != nil {
				return fmt.Errorf("failed to delete old activity: %w", err)
			}
			deleted++
		}

		return nil
	})

	if err != nil {
		return deleted, err
	}

	if deleted > 0 {
		m.logger.Infow("Pruned old activity records",
			"deleted", deleted,
			"max_age", maxAge.String())
	}

	return deleted, nil
}

// PruneExcessActivities deletes oldest records when count exceeds maxRecords.
// Deletes records until count is at targetPercent of maxRecords (default 90%).
// Returns the number of records deleted.
func (m *Manager) PruneExcessActivities(maxRecords int, targetPercent float64) (int, error) {
	if targetPercent <= 0 || targetPercent > 1 {
		targetPercent = 0.9 // Default to 90%
	}

	var deleted int

	err := m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ActivityRecordsBucket))
		if bucket == nil {
			return nil
		}

		count := bucket.Stats().KeyN
		if count <= maxRecords {
			return nil
		}

		targetCount := int(float64(maxRecords) * targetPercent)
		toDelete := count - targetCount

		var keysToDelete [][]byte
		cursor := bucket.Cursor()

		// Delete oldest records (smallest keys)
		for k, _ := cursor.First(); k != nil && len(keysToDelete) < toDelete; k, _ = cursor.Next() {
			keysToDelete = append(keysToDelete, append([]byte{}, k...))
		}

		for _, key := range keysToDelete {
			if err := bucket.Delete(key); err != nil {
				return fmt.Errorf("failed to delete excess activity: %w", err)
			}
			deleted++
		}

		return nil
	})

	if err != nil {
		return deleted, err
	}

	if deleted > 0 {
		m.logger.Infow("Pruned excess activity records",
			"deleted", deleted,
			"max_records", maxRecords)
	}

	return deleted, nil
}

// PruneActivitiesToSize deletes the oldest activity records until the activity
// log's stored data (sum of key+value bytes) is at or below maxBytes. Activity
// keys are timestamp-ordered, so a single forward cursor pass removes
// oldest-first. The newest record is ALWAYS retained — the log is never emptied
// while any record exists, even if that newest record alone exceeds the budget.
// maxBytes <= 0 disables size pruning (no-op). Returns the number deleted.
func (m *Manager) PruneActivitiesToSize(maxBytes int64) (int, error) {
	if maxBytes <= 0 {
		return 0, nil
	}

	var deleted int

	err := m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ActivityRecordsBucket))
		if bucket == nil {
			return nil
		}

		keyCount := bucket.Stats().KeyN
		if keyCount <= 1 {
			return nil // never empty the log (always keep the newest record)
		}

		// Total stored bytes for the bucket.
		var total int64
		_ = bucket.ForEach(func(k, v []byte) error {
			total += int64(len(k) + len(v))
			return nil
		})
		if total <= maxBytes {
			return nil
		}

		// Delete oldest-first (smallest keys) until within budget, but NEVER the
		// last (newest) record — stop before processing it.
		var keysToDelete [][]byte
		cursor := bucket.Cursor()
		processed := 0
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			if total <= maxBytes || processed == keyCount-1 {
				break
			}
			keysToDelete = append(keysToDelete, append([]byte{}, k...))
			total -= int64(len(k) + len(v))
			processed++
		}

		for _, key := range keysToDelete {
			if err := bucket.Delete(key); err != nil {
				return fmt.Errorf("failed to delete activity for size cap: %w", err)
			}
			deleted++
		}
		return nil
	})

	if err != nil {
		return deleted, err
	}

	if deleted > 0 {
		m.logger.Infow("Pruned activity records to size budget",
			"deleted", deleted,
			"max_bytes", maxBytes)
	}

	return deleted, nil
}

// TruncateActivityResponse is a helper to truncate responses for storage.
func TruncateActivityResponse(response string, maxSize int) (string, bool) {
	return truncateResponse(response, maxSize)
}
