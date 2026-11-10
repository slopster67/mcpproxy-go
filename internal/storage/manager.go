package storage

import (
	"bytes"
	"container/heap"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"

	"github.com/slopster67/mcpproxy-go/internal/database"
	"go.uber.org/zap"
)

// Manager provides a unified interface for storage operations
type Manager struct {
	db     *Database
	mu     sync.RWMutex
	logger *zap.SugaredLogger

	// toolCallMaxResponseBytes and toolCallMaxRecords bound the per-server
	// tool-call history. Both are guarded by mu, set once from config
	// via SetToolCallLimits and read on every RecordToolCall. Zero means "use
	// the documented default" — see toolCallLimits.
	toolCallMaxResponseBytes int
	toolCallMaxRecords       int

	// Dashboard change observers (runtime ChangeHub). touchedSessions collects
	// the sessions a write touched; it is guarded by mu like the write itself.
	activityObserver func(*ActivityRecord)
	sessionObserver  func(sessionID, clientID string)
	touchedSessions  []SessionRecord
}

// SetActivityObserver installs fn, called after every stored activity record.
func (m *Manager) SetActivityObserver(fn func(*ActivityRecord)) { m.activityObserver = fn }

// SetSessionObserver installs fn, called after a write that touched a session.
func (m *Manager) SetSessionObserver(fn func(sessionID, clientID string)) { m.sessionObserver = fn }

func (m *Manager) touchSession(s *SessionRecord) {
	if m.sessionObserver != nil && s != nil {
		m.touchedSessions = append(m.touchedSessions, *s)
	}
}

// notifyTouchedSessions runs (deferred, under mu) when a session write
// returns. A write that failed after touching only causes a spurious refresh.
func (m *Manager) notifyTouchedSessions() {
	touched := m.touchedSessions
	m.touchedSessions = nil
	for i := range touched {
		m.sessionObserver(touched[i].ID, touched[i].ClientID)
	}
}

// NewManager creates a new storage manager
func NewManager(dataDir string, key []byte, logger *zap.SugaredLogger) (*Manager, error) {
	db, err := NewDatabase(dataDir, key, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create encrypted database: %w", err)
	}

	return &Manager{
		db:     db,
		logger: logger,
	}, nil
}

// Close closes the encrypted database.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.db != nil {
		return m.db.Close()
	}
	return nil
}

// GetDB returns the underlying encrypted database for direct access
func (m *Manager) GetDB() *database.DB {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.db != nil {
		return m.db.db
	}
	return nil
}

// GetDatabase returns the wrapped Database instance for higher-level operations
func (m *Manager) GetDatabase() *Database {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.db
}

// Upstream operations

func serverRecordFromConfig(server *config.ServerConfig) *ServerRecord {
	return &ServerRecord{ServerConfig: *config.CopyServerConfig(server)}
}

func serverConfigFromRecord(record *ServerRecord) *config.ServerConfig {
	return config.CopyServerConfig(&record.ServerConfig)
}

// GetUpstreamServer retrieves an upstream server by name
func (m *Manager) GetUpstreamServer(name string) (*config.ServerConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	record, err := m.db.GetServer(name)
	if err != nil {
		return nil, err
	}

	return serverConfigFromRecord(record), nil
}

// ListUpstreamServers returns all upstream servers
func (m *Manager) ListUpstreamServers() ([]*config.ServerConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	records, err := m.db.ListServers()
	if err != nil {
		return nil, err
	}

	var servers []*config.ServerConfig
	for _, record := range records {
		servers = append(servers, serverConfigFromRecord(record))
	}

	return servers, nil
}

// Tool statistics operations

// IncrementToolUsage increments the usage count for a tool
func (m *Manager) IncrementToolUsage(toolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Debugf("Incrementing usage for tool: %s", toolName)
	return m.db.IncrementToolStats(toolName)
}

// GetToolUsage retrieves usage statistics for a tool
func (m *Manager) GetToolUsage(toolName string) (*ToolStatRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.db.GetToolStats(toolName)
}

// GetToolStatistics returns aggregated tool statistics
func (m *Manager) GetToolStatistics(topN int) (*config.ToolStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	records, err := m.db.ListToolStats()
	if err != nil {
		return nil, err
	}

	// Sort by usage count (descending)
	sort.Slice(records, func(i, j int) bool {
		return records[i].Count > records[j].Count
	})

	// Limit to topN
	if topN > 0 && len(records) > topN {
		records = records[:topN]
	}

	// Convert to config format
	var topTools []config.ToolStatEntry
	for _, record := range records {
		topTools = append(topTools, config.ToolStatEntry{
			ToolName: record.ToolName,
			Count:    record.Count,
		})
	}

	return &config.ToolStats{
		TotalTools: len(records),
		TopTools:   topTools,
	}, nil
}

// Tool approval operations (tool-level quarantine)

// SaveToolApproval saves a tool approval record
func (m *Manager) SaveToolApproval(record *ToolApprovalRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.db.SaveToolApproval(record)
}

// GetToolApproval retrieves a tool approval record by server and tool name
func (m *Manager) GetToolApproval(serverName, toolName string) (*ToolApprovalRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.db.GetToolApproval(serverName, toolName)
}

// ListToolApprovals returns all tool approval records for a server.
// If serverName is empty, returns all records across all servers.
func (m *Manager) ListToolApprovals(serverName string) ([]*ToolApprovalRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.db.ListToolApprovals(serverName)
}

// DeleteToolApproval deletes a tool approval record
func (m *Manager) DeleteToolApproval(serverName, toolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.db.DeleteToolApproval(serverName, toolName)
}

// DeleteToolApprovalIf is the conditional, atomic form of DeleteToolApproval.
func (m *Manager) DeleteToolApprovalIf(serverName, toolName string, eligible func(*ToolApprovalRecord) bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.db.DeleteToolApprovalIf(serverName, toolName, eligible)
}

// GetToolStats gets tool statistics formatted for MCP responses
func (m *Manager) GetToolStats(topN int) ([]map[string]interface{}, error) {
	stats, err := m.GetToolStatistics(topN)
	if err != nil {
		return nil, err
	}

	// Non-nil so usage_summary.top_tools serializes as [], never null:
	// strict MCP clients fail iterating a null array.
	result := make([]map[string]interface{}, 0, len(stats.TopTools))
	for _, tool := range stats.TopTools {
		result = append(result, map[string]interface{}{
			"tool_name": tool.ToolName,
			"count":     tool.Count,
		})
	}

	return result, nil
}

// GetToolStatsFiltered is GetToolStats restricted to records keep admits:
// the sort-then-cut-to-topN happens AFTER filtering,
// unlike GetToolStatistics/GetToolStats which cut first — a caller whose
// authorized population excludes some records must never let a hidden or
// unapproved tool's usage count evict an authorized tool from the topN
// window. keep receives the full "server:tool" name exactly as recorded by
// IncrementToolUsage. A nil keep behaves like GetToolStats (nothing filtered).
func (m *Manager) GetToolStatsFiltered(keep func(toolName string) bool, topN int) ([]map[string]interface{}, error) {
	m.mu.RLock()
	records, err := m.db.ListToolStats()
	m.mu.RUnlock()
	if err != nil {
		return nil, err
	}

	filtered := make([]*ToolStatRecord, 0, len(records))
	for _, record := range records {
		if keep == nil || keep(record.ToolName) {
			filtered = append(filtered, record)
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Count > filtered[j].Count
	})
	if topN > 0 && len(filtered) > topN {
		filtered = filtered[:topN]
	}

	// Non-nil so usage_summary.top_tools serializes as [], never null.
	result := make([]map[string]interface{}, 0, len(filtered))
	for _, record := range filtered {
		result = append(result, map[string]interface{}{
			"tool_name": record.ToolName,
			"count":     record.Count,
		})
	}

	return result, nil
}

// Server Identity Management

// RegisterServerIdentity registers or updates a server identity
func (m *Manager) RegisterServerIdentity(server *config.ServerConfig) (*ServerIdentity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	serverID := GenerateServerID(server)

	// Try to get existing identity
	identity, err := m.getServerIdentityByID(serverID)
	if err != nil && err != database.ErrBucketNotFound {
		return nil, fmt.Errorf("failed to get server identity: %w", err)
	}

	if identity == nil {
		// Create new identity
		identity = NewServerIdentity(server)
		m.logger.Debugw("Created new server identity",
			"server_name", server.Name,
			"server_id", serverID)
	} else {
		// Update existing identity
		identity.UpdateLastSeen()
		m.logger.Debugw("Updated existing server identity",
			"server_name", server.Name,
			"server_id", serverID)
	}

	// Save identity
	err = m.saveServerIdentity(identity)
	if err != nil {
		return nil, fmt.Errorf("failed to save server identity: %w", err)
	}

	return identity, nil
}

// RecordToolCall records a tool call for a server
func (m *Manager) RecordToolCall(record *ToolCallRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	maxResponseBytes, maxRecords := m.toolCallLimits()
	bounded := boundToolCallResponse(record, maxResponseBytes)

	bucketName := toolCallsBucketName(record.ServerID)
	key := fmt.Sprintf("%d_%s", record.Timestamp.UnixNano(), record.ID)

	return m.db.db.Update(func(tx *database.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(bucketName))
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

		// Bound the bucket in the same transaction as the write, so the
		// history never exceeds the cap, even briefly.
		return pruneToolCallBucket(bucket, maxRecords)
	})
}

// Private helper methods

func (m *Manager) getServerIdentityByID(serverID string) (*ServerIdentity, error) {
	var identity ServerIdentity

	err := m.db.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte("server_identities"))
		if bucket == nil {
			return database.ErrBucketNotFound
		}

		data := bucket.Get([]byte(serverID))
		if data == nil {
			return database.ErrBucketNotFound
		}

		return json.Unmarshal(data, &identity)
	})

	if err != nil {
		return nil, err
	}

	return &identity, nil
}

func (m *Manager) saveServerIdentity(identity *ServerIdentity) error {
	return m.db.db.Update(func(tx *database.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("server_identities"))
		if err != nil {
			return err
		}

		data, err := json.Marshal(identity)
		if err != nil {
			return err
		}

		return bucket.Put([]byte(identity.ID), data)
	})
}

// Session storage operations

// SessionRecord represents a stored MCP session
type SessionRecord struct {
	ID            string     `json:"id"`
	ClientName    string     `json:"client_name,omitempty"`
	ClientVersion string     `json:"client_version,omitempty"`
	Status        string     `json:"status"`
	StartTime     time.Time  `json:"start_time"`
	EndTime       *time.Time `json:"end_time,omitempty"`
	LastActivity  time.Time  `json:"last_activity"`
	ToolCallCount int        `json:"tool_call_count"`

	// MCP Client Capabilities
	HasRoots     bool     `json:"has_roots,omitempty"`    // Whether client supports roots
	HasSampling  bool     `json:"has_sampling,omitempty"` // Whether client supports sampling
	Experimental []string `json:"experimental,omitempty"` // Experimental capability names

	// Workspace / work session.
	//
	// WorkspaceRoot is the project the client is working in, fetched once from
	// the client's MCP roots. It is a LOCAL FILESYSTEM PATH: it stays on this
	// machine and is never sent to telemetry. WorkspaceName is its basename, and
	// is what the UI shows.
	//
	// WorkSessionID groups this connection with the other connections that make
	// up the same stretch of user work (see internal/runtime/worksession.go).
	// A client reconnecting every few minutes produces many session records that
	// all share one WorkSessionID.
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	WorkSessionID string `json:"work_session_id,omitempty"`

	TokenName     string `json:"token_name,omitempty"`
	ClientID      string `json:"client_id,omitempty"`
	Profile       string `json:"profile,omitempty"`
	ProfileSource string `json:"profile_source,omitempty"`
}

// SessionFilter selects sessions for GetRecentSessionsFiltered. Empty string
// fields are unfiltered; "-" (ScopeFilterUnattributed) selects sessions with
// no value. Profile is the session's latest effective profile; ClientID and
// TokenName are the credential at initialize.
type SessionFilter struct {
	Limit     int
	Status    string
	Profile   string
	ClientID  string
	TokenName string
}

func (f SessionFilter) matches(s *SessionRecord) bool {
	if f.Status != "" && s.Status != f.Status {
		return false
	}
	return scopeValueMatches(f.Profile, s.Profile) &&
		scopeValueMatches(f.ClientID, s.ClientID) &&
		scopeValueMatches(f.TokenName, s.TokenName)
}

// sessionRetentionLimit is the hard cap on stored session records. The cap is
// absolute: it holds even when every retained session is "active" (see
// trimSessionsToLimit).
const sessionRetentionLimit = 100

// retentionDeleteBatch bounds how many victim keys retention holds at once.
// Deleting cannot happen while a cursor is traversing the bucket, so victims
// are collected in batches.
//
// This bounds the victim slice only. It does NOT bound peak memory for the
// trim — database's own transaction state dominates that. See trimSessionsToLimit.
const retentionDeleteBatch = 512

// CreateSession creates a new session record
func (m *Manager) CreateSession(session *SessionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.notifyTouchedSessions()

	return m.db.db.Update(func(tx *database.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(SessionsBucket))
		if err != nil {
			return fmt.Errorf("failed to create sessions bucket: %w", err)
		}

		// Check if session already exists - if so, update it
		var existingKey []byte
		var existingSession SessionRecord
		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			keyStr := string(k)
			// Check if key ends with _{session_id}
			if strings.HasSuffix(keyStr, "_"+session.ID) {
				existingKey = k
				if err := json.Unmarshal(v, &existingSession); err != nil {
					m.logger.Warnw("Failed to unmarshal existing session", "error", err)
					continue
				}
				// Merge new data with existing session (preserve certain fields)
				if session.ClientName != "" {
					existingSession.ClientName = session.ClientName
				}
				if session.ClientVersion != "" {
					existingSession.ClientVersion = session.ClientVersion
				}
				// Update capabilities
				existingSession.HasRoots = session.HasRoots
				existingSession.HasSampling = session.HasSampling
				existingSession.Experimental = session.Experimental
				// A re-created session is live again (re-initialize on a
				// soft-closed id must not leave the record closed).
				existingSession.Status = "active"
				existingSession.EndTime = nil
				session = &existingSession
				m.logger.Debugw("Updating existing session with new data", "session_id", session.ID, "client_name", session.ClientName)
				break
			}
		}

		data, err := json.Marshal(session)
		if err != nil {
			return fmt.Errorf("failed to marshal session: %w", err)
		}

		// Use existing key if found, otherwise create new key
		var key []byte
		if existingKey != nil {
			key = existingKey
		} else {
			// Key format: {timestamp_ns}_{session_id} for reverse chronological ordering
			keyStr := fmt.Sprintf("%d_%s", session.StartTime.UnixNano(), session.ID)
			key = []byte(keyStr)
			m.logger.Debugw("Creating new session", "session_id", session.ID, "client_name", session.ClientName)
		}

		m.touchSession(session)
		if err := bucket.Put(key, data); err != nil {
			return fmt.Errorf("failed to store session: %w", err)
		}

		// Enforce retention limit only when creating new sessions
		if existingKey == nil {
			return trimSessionsToLimit(bucket, sessionRetentionLimit, m.logger)
		}
		return nil
	})
}

// CloseSession marks a session as closed with end time
func (m *Manager) CloseSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.notifyTouchedSessions()

	return m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(SessionsBucket))
		if bucket == nil {
			return fmt.Errorf("sessions bucket not found")
		}

		// Find the session by iterating (session_id is in the key suffix)
		var sessionKey []byte
		var session SessionRecord

		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			// Key format: {timestamp_ns}_{session_id}
			keyStr := string(k)
			// Check if key ends with _{session_id}
			if strings.HasSuffix(keyStr, "_"+sessionID) {
				sessionKey = k
				if err := json.Unmarshal(v, &session); err != nil {
					return fmt.Errorf("failed to unmarshal session: %w", err)
				}
				break
			}
		}

		if sessionKey == nil {
			return fmt.Errorf("session not found: %s", sessionID)
		}

		// Update session status and end time
		now := time.Now()
		session.Status = "closed"
		session.EndTime = &now

		data, err := json.Marshal(session)
		if err != nil {
			return fmt.Errorf("failed to marshal session: %w", err)
		}

		m.logger.Debugw("Session closed", "session_id", sessionID)
		m.touchSession(&session)
		return bucket.Put(sessionKey, data)
	})
}

// ReopenSession marks a closed session active again and clears its end time. It
// is a no-op for an unknown or already-active session (no duplicate record).
func (m *Manager) ReopenSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.notifyTouchedSessions()

	return m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(SessionsBucket))
		if bucket == nil {
			return nil
		}
		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if !strings.HasSuffix(string(k), "_"+sessionID) {
				continue
			}
			var session SessionRecord
			if err := json.Unmarshal(v, &session); err != nil {
				return fmt.Errorf("failed to unmarshal session: %w", err)
			}
			if session.Status == "active" {
				return nil
			}
			session.Status = "active"
			session.EndTime = nil
			session.LastActivity = time.Now()
			data, err := json.Marshal(session)
			if err != nil {
				return fmt.Errorf("failed to marshal session: %w", err)
			}
			m.touchSession(&session)
			return bucket.Put(k, data)
		}
		return nil
	})
}

// GetRecentSessionsFiltered returns stored sessions, newest activity first,
// restricted by f and cut to f.Limit, plus the filtered total. Every filter is applied after the LastActivity sort and BEFORE the
// limit, so total is the filtered count and an old-but-matching session is
// never truncated out by a page of newer non-matching ones.
func (m *Manager) GetRecentSessionsFiltered(f SessionFilter) ([]*SessionRecord, int, error) {
	limit := f.Limit
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []*SessionRecord

	err := m.db.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(SessionsBucket))
		if bucket == nil {
			return nil // No sessions yet
		}

		all = make([]*SessionRecord, 0, bucket.Stats().KeyN)
		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var session SessionRecord
			if err := json.Unmarshal(v, &session); err != nil {
				m.logger.Warnw("Failed to unmarshal session", "error", err)
				continue
			}
			all = append(all, &session)
		}

		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	// Newest activity first. StartTime breaks ties so the order is stable for
	// records that share a LastActivity (or have none recorded at all).
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].LastActivity.Equal(all[j].LastActivity) {
			return all[i].LastActivity.After(all[j].LastActivity)
		}
		return all[i].StartTime.After(all[j].StartTime)
	})

	if f.Status != "" || f.Profile != "" || f.ClientID != "" || f.TokenName != "" {
		total := 0
		sessions := make([]*SessionRecord, 0, limit)
		for _, session := range all {
			if !f.matches(session) {
				continue
			}
			total++
			if len(sessions) < limit {
				sessions = append(sessions, session)
			}
		}
		return sessions, total, nil
	}

	if limit < len(all) {
		return all[:limit], len(all), nil
	}
	return all, len(all), nil
}

// CloseAllActiveSessions marks all active sessions as closed
// This should be called on startup to clean up stale sessions from previous runs
func (m *Manager) CloseAllActiveSessions() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.notifyTouchedSessions()

	return m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(SessionsBucket))
		if bucket == nil {
			return nil // No sessions bucket yet
		}

		now := time.Now()
		var keysToUpdate [][]byte
		var sessionsToUpdate []SessionRecord

		// First pass: find all active sessions
		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var session SessionRecord
			if err := json.Unmarshal(v, &session); err != nil {
				continue
			}
			if session.Status == "active" {
				keysToUpdate = append(keysToUpdate, k)
				session.Status = "closed"
				session.EndTime = &now
				sessionsToUpdate = append(sessionsToUpdate, session)
			}
		}

		// Second pass: update all active sessions
		for i, key := range keysToUpdate {
			data, err := json.Marshal(sessionsToUpdate[i])
			if err != nil {
				continue
			}
			if err := bucket.Put(key, data); err != nil {
				continue
			}
			m.touchSession(&sessionsToUpdate[i])
		}

		if len(keysToUpdate) > 0 {
			m.logger.Infow("Closed stale sessions on startup", "count", len(keysToUpdate))
		}

		return nil
	})
}

// SetSessionWorkspace backfills the workspace on an already-persisted session.
//
// Needed because the workspace is discovered asynchronously (the client is asked
// for its roots only after the handshake completes — asking during it deadlocks),
// so a busy session can be persisted before the answer arrives.
func (m *Manager) SetSessionWorkspace(sessionID, workspaceRoot string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.notifyTouchedSessions()

	return m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(SessionsBucket))
		if bucket == nil {
			return fmt.Errorf("sessions bucket not found")
		}

		var sessionKey []byte
		var session SessionRecord
		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if strings.HasSuffix(string(k), "_"+sessionID) {
				sessionKey = k
				if err := json.Unmarshal(v, &session); err != nil {
					return fmt.Errorf("failed to unmarshal session: %w", err)
				}
				break
			}
		}
		if sessionKey == nil {
			return fmt.Errorf("session not found: %s", sessionID)
		}

		session.WorkspaceRoot = workspaceRoot
		session.WorkspaceName = workspaceDisplayName(workspaceRoot)

		data, err := json.Marshal(session)
		if err != nil {
			return fmt.Errorf("failed to marshal session: %w", err)
		}
		m.touchSession(&session)
		return bucket.Put(sessionKey, data)
	})
}

// SetSessionProfile writes a session's latest profile resolution through to its
// persisted row. A session that is not persisted yet is a
// no-op: EnsurePersisted copies the in-memory resolution when it persists.
func (m *Manager) SetSessionProfile(sessionID, profile, source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.notifyTouchedSessions()

	return m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(SessionsBucket))
		if bucket == nil {
			return nil
		}
		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if !strings.HasSuffix(string(k), "_"+sessionID) {
				continue
			}
			var session SessionRecord
			if err := json.Unmarshal(v, &session); err != nil {
				return fmt.Errorf("failed to unmarshal session: %w", err)
			}
			if session.Profile == profile && session.ProfileSource == source {
				return nil
			}
			session.Profile = profile
			session.ProfileSource = source
			data, err := json.Marshal(session)
			if err != nil {
				return fmt.Errorf("failed to marshal session: %w", err)
			}
			m.touchSession(&session)
			return bucket.Put(append([]byte(nil), k...), data)
		}
		return nil
	})
}

// workspaceDisplayName is the basename of a workspace root. Only the basename is
// ever displayed or exported — the full path is local and private.
func workspaceDisplayName(root string) string {
	root = strings.TrimSpace(root)
	root = strings.TrimPrefix(root, "file://")
	root = strings.TrimRight(root, "/")
	if root == "" {
		return ""
	}
	if i := strings.LastIndex(root, "/"); i >= 0 {
		return root[i+1:]
	}
	return root
}

// UpdateSessionStats increments the session tool-call count
func (m *Manager) UpdateSessionStats(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.notifyTouchedSessions()

	return m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(SessionsBucket))
		if bucket == nil {
			return fmt.Errorf("sessions bucket not found")
		}

		// Find the session
		var sessionKey []byte
		var session SessionRecord

		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			keyStr := string(k)
			// Check if key ends with _{session_id}
			if strings.HasSuffix(keyStr, "_"+sessionID) {
				sessionKey = k
				if err := json.Unmarshal(v, &session); err != nil {
					return fmt.Errorf("failed to unmarshal session: %w", err)
				}
				break
			}
		}

		if sessionKey == nil {
			return fmt.Errorf("session not found: %s", sessionID)
		}

		// Update stats and last activity
		session.ToolCallCount++

		session.LastActivity = time.Now()

		data, err := json.Marshal(session)
		if err != nil {
			return fmt.Errorf("failed to marshal session: %w", err)
		}

		m.touchSession(&session)
		return bucket.Put(sessionKey, data)
	})
}

// UpdateSessionActivity updates LastActivity without incrementing tool call counts.
// Call this on any MCP message to keep the session alive.
func (m *Manager) UpdateSessionActivity(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.notifyTouchedSessions()

	return m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(SessionsBucket))
		if bucket == nil {
			return nil
		}

		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			keyStr := string(k)
			if strings.HasSuffix(keyStr, "_"+sessionID) {
				var session SessionRecord
				if err := json.Unmarshal(v, &session); err != nil {
					return err
				}
				session.LastActivity = time.Now()
				data, err := json.Marshal(session)
				if err != nil {
					return err
				}
				m.touchSession(&session)
				return bucket.Put(k, data)
			}
		}
		return nil // Session not found is not an error - may not be persisted yet
	})
}

// CloseInactiveSessions closes sessions that haven't had activity for the specified duration
func (m *Manager) CloseInactiveSessions(inactivityTimeout time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.notifyTouchedSessions()

	var closedCount int

	err := m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(SessionsBucket))
		if bucket == nil {
			return nil // No sessions bucket yet
		}

		now := time.Now()
		cutoff := now.Add(-inactivityTimeout)
		var keysToUpdate [][]byte
		var sessionsToUpdate []SessionRecord

		// Find all active sessions with no recent activity
		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var session SessionRecord
			if err := json.Unmarshal(v, &session); err != nil {
				continue
			}

			// Check if session is active and hasn't had activity within timeout
			if session.Status == "active" {
				lastActivity := session.LastActivity

				if lastActivity.Before(cutoff) {
					keysToUpdate = append(keysToUpdate, k)
					session.Status = "closed"
					session.EndTime = &now
					sessionsToUpdate = append(sessionsToUpdate, session)
				}
			}
		}

		// Update all inactive sessions
		for i, key := range keysToUpdate {
			data, err := json.Marshal(sessionsToUpdate[i])
			if err != nil {
				continue
			}
			if err := bucket.Put(key, data); err != nil {
				continue
			}
			m.touchSession(&sessionsToUpdate[i])
			closedCount++
		}

		return nil
	})

	if closedCount > 0 {
		m.logger.Infow("Closed inactive sessions", "count", closedCount, "timeout", inactivityTimeout.String())
	}

	return closedCount, err
}

// Eviction tiers, worst first. The tier is the primary ranking key; activity
// only breaks ties inside a tier.
const (
	// sessionTierUnreadable is a record that cannot be unmarshalled. Nothing can
	// display, close, or update it, so it is worth strictly less than any record
	// that parses. It is its own tier because an unmarshal failure leaves the
	// zero value, which is indistinguishable from a valid closed record with
	// unset timestamps.
	sessionTierUnreadable = iota
	// sessionTierClosed is finished work — nothing will ever write to it again.
	sessionTierClosed
	// sessionTierActive is a session the proxy still believes is live.
	sessionTierActive
)

// sessionEvictionCandidate is one stored session, reduced to the properties
// retention ranks by.
type sessionEvictionCandidate struct {
	key      []byte
	tier     int
	activity time.Time // LastActivity, falling back to StartTime when unset
}

// worseThan reports whether a should be evicted before b.
func (a sessionEvictionCandidate) worseThan(b sessionEvictionCandidate) bool {
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	if !a.activity.Equal(b.activity) {
		return a.activity.Before(b.activity) // stalest first
	}
	return bytes.Compare(a.key, b.key) < 0 // deterministic tiebreak
}

// survivorHeap is a min-heap under worseThan, so its root is the weakest record
// currently being kept — i.e. the next one to be displaced.
type survivorHeap []sessionEvictionCandidate

func (h survivorHeap) Len() int           { return len(h) }
func (h survivorHeap) Less(i, j int) bool { return h[i].worseThan(h[j]) }
func (h survivorHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *survivorHeap) Push(x any) {
	c, ok := x.(sessionEvictionCandidate)
	if !ok {
		return
	}
	*h = append(*h, c)
}

func (h *survivorHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// classifySessionForRetention reduces a stored record to its ranking properties.
func classifySessionForRetention(key, value []byte, logger *zap.SugaredLogger) sessionEvictionCandidate {
	cand := sessionEvictionCandidate{
		key:  append([]byte(nil), key...),
		tier: sessionTierUnreadable,
	}

	var session SessionRecord
	if err := json.Unmarshal(value, &session); err != nil {
		logger.Warnw("Unreadable session record during retention", "key", string(key), "error", err)
		return cand
	}

	if session.Status == "active" {
		cand.tier = sessionTierActive
	} else {
		cand.tier = sessionTierClosed
	}

	cand.activity = session.LastActivity
	if cand.activity.IsZero() {
		cand.activity = session.StartTime
	}
	return cand
}

// trimSessionsToLimit trims the sessions bucket down to maxSessions records.
//
// Victims are chosen by usefulness, not by key order. Session keys are
// {StartTime.UnixNano()}_{ID}, so deleting the lowest keys would delete the
// longest-lived session first, which is the one most likely to still be
// connected and working.
//
// Eviction order is therefore two-tiered:
//
//  1. closed (and unreadable) sessions, stalest first: finished work; nothing
//     will ever write to these records again;
//  2. only if tier 1 does not free enough room, active sessions ordered by last
//     activity, stalest first: a client that died without closing goes before a
//     client that is genuinely working.
//
// Tier 2 keeps the cap absolute: an all-active bucket (abandoned clients, a
// reconnect loop that never closes cleanly) must not grow without bound.
func trimSessionsToLimit(bucket *database.Bucket, maxSessions int, logger *zap.SugaredLogger) error {
	if maxSessions <= 0 {
		return nil
	}

	// Pass 1 — rank. bucket.Stats() is deliberately not used to count: inside a
	// write transaction it does not account for records Put earlier in the same
	// transaction.
	survivors := make(survivorHeap, 0, maxSessions)
	total := 0
	activeEvicted := 0

	c := bucket.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		total++
		cand := classifySessionForRetention(k, v, logger)
		switch {
		case len(survivors) < maxSessions:
			heap.Push(&survivors, cand)
		case cand.worseThan(survivors[0]):
			// Weaker than every record currently being kept, so it is a victim.
			// Its key is not retained; pass 2 rediscovers it as "not a survivor".
			if cand.tier == sessionTierActive {
				activeEvicted++
			}
		default:
			if displaced, ok := heap.Pop(&survivors).(sessionEvictionCandidate); ok &&
				displaced.tier == sessionTierActive {
				activeEvicted++
			}
			heap.Push(&survivors, cand)
		}
	}

	if total <= maxSessions {
		return nil
	}

	// The survivor set, by key: exactly maxSessions entries.
	keep := make(map[string]struct{}, len(survivors))
	for _, s := range survivors {
		keep[string(s.key)] = struct{}{}
	}

	// Pass 2 — delete. Deleting cannot happen while a cursor is traversing the
	// bucket: database documents mutation during traversal as unsafe ("Changing
	// data while traversing with a cursor may cause it to be invalidated and
	// return unexpected keys and/or values"). So each batch is collected, the
	// traversal abandoned, the batch deleted, and the scan resumed from the
	// first key the batch did not reach.
	deleted := 0
	var resume []byte
	for {
		batch := make([][]byte, 0, retentionDeleteBatch)

		cur := bucket.Cursor()
		var k []byte
		if resume == nil {
			k, _ = cur.First()
		} else {
			k, _ = cur.Seek(resume)
		}
		for ; k != nil && len(batch) < retentionDeleteBatch; k, _ = cur.Next() {
			if _, survives := keep[string(k)]; survives {
				continue
			}
			batch = append(batch, append([]byte(nil), k...))
		}

		// k is the first key this batch did not examine. It is never one of the
		// keys about to be deleted, so it is still there to seek back to.
		if k != nil {
			resume = append([]byte(nil), k...)
		}

		for _, key := range batch {
			if err := bucket.Delete(key); err != nil {
				return fmt.Errorf("failed to delete old session: %w", err)
			}
			deleted++
		}

		if k == nil {
			break
		}
	}

	if activeEvicted > 0 {
		// Only reachable when there was no closed or unreadable record left to
		// sacrifice.
		logger.Warnw("Session retention had to evict active sessions",
			"active_evicted", activeEvicted, "limit", maxSessions)
	}
	logger.Debugw("Enforced session retention", "deleted", deleted, "remaining", maxSessions)
	return nil
}

func enforceSessionRetentionOnOpen(tx *database.Tx, logger *zap.SugaredLogger) error {
	bucket := tx.Bucket([]byte(SessionsBucket))
	if bucket == nil {
		return nil
	}
	return trimSessionsToLimit(bucket, sessionRetentionLimit, logger)
}

// GetOAuthToken retrieves an OAuth token for a server from storage
func (m *Manager) GetOAuthToken(serverName string) (*OAuthTokenRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.db == nil {
		return nil, fmt.Errorf("storage not initialized")
	}

	return m.db.GetOAuthToken(serverName)
}

// ListOAuthTokens returns all OAuth token records from storage.
// Used by RefreshManager to initialize proactive refresh schedules on startup.
func (m *Manager) ListOAuthTokens() ([]*OAuthTokenRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.db == nil {
		return nil, fmt.Errorf("storage not initialized")
	}

	return m.db.ListOAuthTokens()
}

// ClearOAuthState clears all OAuth state for a server (tokens, client registration, etc.)
// This should be called when OAuth configuration changes to force re-authentication
func (m *Manager) ClearOAuthState(serverName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.db == nil {
		return fmt.Errorf("storage not initialized")
	}

	cleared := 0
	if err := m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(OAuthTokenBucket))
		if bucket == nil {
			return fmt.Errorf("oauth token bucket not found")
		}
		n, err := clearOAuthStateTx(bucket, serverName)
		cleared = n
		return err
	}); err != nil {
		return fmt.Errorf("failed to clear OAuth state: %w", err)
	}

	m.logger.Infow("Cleared OAuth state for server", "server", serverName)
	if cleared == 0 {
		m.logger.Debugw("No OAuth tokens found to clear (expected if already removed)", "server", serverName)
	}
	return nil
}

func clearOAuthStateTx(bucket *database.Bucket, serverName string) (int, error) {
	cleared := 0
	prefix := []byte(serverName + "_")
	var keys [][]byte
	cursor := bucket.Cursor()
	for k, _ := cursor.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = cursor.Next() {
		if oauthKeyBelongsTo(string(k), serverName) {
			keys = append(keys, append([]byte(nil), k...))
		}
	}
	for _, k := range keys {
		if err := bucket.Delete(k); err != nil {
			return cleared, err
		}
		cleared++
	}
	return cleared, nil
}

// oauthKeySuffixLen is the length of the hash suffix oauth.GenerateServerKey
// appends to the server name.
const oauthKeySuffixLen = 16

func oauthKeyBelongsTo(key, name string) bool {
	if len(key) != len(name)+1+oauthKeySuffixLen || !strings.HasPrefix(key, name+"_") {
		return false
	}
	for _, c := range key[len(name)+1:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// CleanupOrphanedOAuthTokens removes OAuth tokens for servers that are not in
// the configuration. It runs at startup to drop tokens left behind when servers
// were removed while mcpproxy was not running.
func (m *Manager) CleanupOrphanedOAuthTokens(validServerNames []string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.db == nil {
		return 0, fmt.Errorf("storage not initialized")
	}

	// Create a set of valid server names for fast lookup
	validServers := make(map[string]bool, len(validServerNames))
	for _, name := range validServerNames {
		validServers[name] = true
	}

	// Get all OAuth tokens
	tokens, err := m.db.ListOAuthTokens()
	if err != nil {
		return 0, fmt.Errorf("failed to list OAuth tokens: %w", err)
	}

	// Find orphaned tokens (tokens whose server no longer exists)
	var orphanedKeys []string
	for _, token := range tokens {
		serverName := token.GetServerName()
		if serverName == "" {
			// Token has no server name, consider it orphaned
			orphanedKeys = append(orphanedKeys, token.ServerName)
			continue
		}

		// Check if the server name matches a valid server
		if validServers[serverName] {
			continue
		}

		orphanedKeys = append(orphanedKeys, token.ServerName)
		m.logger.Infow("Found orphaned OAuth token",
			"storage_key", token.ServerName,
			"display_name", serverName)
	}

	if len(orphanedKeys) == 0 {
		m.logger.Debug("No orphaned OAuth tokens found")
		return 0, nil
	}

	// Delete orphaned tokens
	deleted := 0
	for _, key := range orphanedKeys {
		if err := m.db.DeleteOAuthToken(key); err != nil {
			m.logger.Warnw("Failed to delete orphaned OAuth token",
				"key", key,
				"error", err)
			continue
		}
		deleted++
	}

	m.logger.Infow("Cleaned up orphaned OAuth tokens",
		"credential_records", len(tokens),
		"orphaned_found", len(orphanedKeys),
		"deleted", deleted)

	return deleted, nil
}
