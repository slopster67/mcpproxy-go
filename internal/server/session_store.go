package server

import (
	"strings"
	"sync"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/v2/server"

	"github.com/slopster67/mcpproxy-go/internal/storage"
	"go.uber.org/zap"
)

// SessionInfo holds MCP session metadata
type SessionInfo struct {
	SessionID     string
	ClientName    string
	ClientVersion string

	// Workspace is the project the client is working in, as disclosed via MCP
	// roots. Filled in asynchronously AFTER the handshake completes — never
	// during initialize, which would deadlock. Empty for
	// clients that do not disclose roots (e.g. Codex).
	Workspace string

	// Capabilities captured at initialize, needed when we finally persist.
	hasRoots     bool
	hasSampling  bool
	experimental []string
	startTime    time.Time

	// persisted records whether this session has been written to storage.
	//
	// A connection that only performs the handshake and never does any
	// work is NOT persisted, so background agents that connect periodically
	// and do nothing do not bury real sessions or evict them from the
	// retention cap.
	//
	// The in-memory entry is still created at initialize (activity records
	// resolve their client name from it), only the durable write is deferred
	// until the session does something.
	persisted bool

	// workspaceFetchStarted guards the roots request so it is attempted at most
	// once per connection.
	workspaceFetchStarted bool

	// workspaceReady is closed once the roots answer has arrived OR we have given
	// up on it. Activity waits (briefly) on this before deriving a work session,
	// so the first tool call of a connection is not filed under a workspace-less
	// key while the second lands under a workspace-keyed one — which would split
	// one connection across two work sessions.
	workspaceReady chan struct{}

	// workSessionID is resolved once per connection and then reused, so every
	// record from this connection agrees on which work session it belongs to.
	workSessionID string

	// persistDone is closed once the durable record exists. Concurrent first
	// calls wait on it rather than racing ahead to UpdateSessionStats, which
	// errors if the row is not there yet.
	persistDone chan struct{}

	// TokenName and ClientID identify the credential that authenticated this
	// session: the client credential name and its client id. Both
	// empty for an admin session. Set once at initialize from the
	// AuthContext (SetSessionIdentity) — never re-derived per call, because
	// the credential does not change mid-connection.
	TokenName string
	ClientID  string
	// TokenPrefix is the credential's 12-char display prefix.
	// It is INTERNAL (never persisted or serialized): the activity funnels with
	// no request context read it back so an event carries the same ownership
	// proof the SSE renderer checks (name AND prefix).
	TokenPrefix string

	Profile       string
	ProfileSource string

	// server is the MCP server instance that serves this session (one of the
	// routing-mode servers), stamped from mcp-go's ServerFromContext at
	// initialize so a binding change can send tools/list_changed on the right
	// instance. Never persisted or serialised.
	server *mcpserver.MCPServer

	// closed marks a soft-closed session: the transport unregistered it, but the
	// entry (client name, workspace, persisted flag, work session) is kept so a
	// client that re-registers on the same id without a new initialize keeps its
	// attribution. Cleared by the next activity or register; evicted
	// after closedSessionTTL.
	closed   bool
	closedAt time.Time
}

// closedSessionTTL is how long a soft-closed session is retained. It matches the
// storage inactivity timeout (runtime.backgroundSessionCleanup).
const closedSessionTTL = 30 * time.Minute

// SessionStore manages MCP session information
type SessionStore struct {
	sessions map[string]*SessionInfo
	// activeProfiles holds the per-session active profile slug selected via the
	// set_profile MCP tool. Kept orthogonal to sessions so a
	// re-initialize (SetSession) never clobbers a live selection. Kept across
	// a soft close (RemoveSession) and dropped with the closed entry on eviction.
	activeProfiles map[string]string
	mu             sync.RWMutex
	logger         *zap.Logger
	storageManager *storage.Manager

	lifecycleMu sync.Mutex

	// writeThroughMu serializes the storage write-throughs of profile
	// resolutions (see UpdateSessionProfile).
	writeThroughMu sync.Mutex
}

// NewSessionStore creates a new session store
func NewSessionStore(logger *zap.Logger) *SessionStore {
	return &SessionStore{
		sessions:       make(map[string]*SessionInfo),
		activeProfiles: make(map[string]string),
		logger:         logger,
	}
}

// evictClosedLocked drops soft-closed sessions older than closedSessionTTL,
// along with their set_profile selection. Caller must hold the write lock.
func (s *SessionStore) evictClosedLocked() {
	cutoff := time.Now().Add(-closedSessionTTL)
	for id, info := range s.sessions {
		if info.closed && info.closedAt.Before(cutoff) {
			delete(s.sessions, id)
			delete(s.activeProfiles, id)
		}
	}
}

// reconcileStorage brings the stored status of a persisted session in line with
// its current in-memory open/closed state. Called only after a transition.
func (s *SessionStore) reconcileStorage(sessionID string) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.RLock()
	info, ok := s.sessions[sessionID]
	persisted, closed := ok && info.persisted, ok && info.closed
	done := (chan struct{})(nil)
	if ok {
		done = info.persistDone
	}
	mgr := s.storageManager
	s.mu.RUnlock()

	if !persisted || mgr == nil {
		return
	}
	<-done // the row may still be in flight
	if closed {
		if err := mgr.CloseSession(sessionID); err != nil {
			s.logger.Warn("failed to close session in storage",
				zap.String("session_id", sessionID), zap.Error(err))
		}
		return
	}
	if err := mgr.ReopenSession(sessionID); err != nil {
		s.logger.Debug("failed to reopen session in storage",
			zap.String("session_id", sessionID), zap.Error(err))
	}
}

// reopenLocked clears the soft-close flag. It reports whether the session was
// closed. Caller must hold the write lock.
func (s *SessionStore) reopenLocked(info *SessionInfo) bool {
	if !info.closed {
		return false
	}
	info.closed = false
	info.closedAt = time.Time{}
	return true
}

// Reopen revives a soft-closed session (the transport re-registered the same id
// without a new initialize). A no-op for unknown or already-open sessions.
func (s *SessionStore) Reopen(sessionID string) {
	s.mu.Lock()
	info, ok := s.sessions[sessionID]
	revived := ok && s.reopenLocked(info)
	s.mu.Unlock()
	if revived {
		s.reconcileStorage(sessionID)
	}
}

// SetStorageManager sets the storage manager for persistence
func (s *SessionStore) SetStorageManager(manager *storage.Manager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storageManager = manager
}

// SetSession registers a session in memory at initialize time.
//
// It deliberately does NOT write to storage. A connection earns a
// durable record by doing something (see EnsurePersisted); one that only shakes
// hands and leaves leaves no trace.
//
// The in-memory entry is still created here, immediately: activity records
// resolve their client name from this map (SetSessionClientResolver), so
// deferring it would strip the identity from the very first thing a session does.
func (s *SessionStore) SetSession(sessionID, clientName, clientVersion string, hasRoots, hasSampling bool, experimental []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	info := &SessionInfo{
		SessionID:      sessionID,
		ClientName:     clientName,
		ClientVersion:  clientVersion,
		hasRoots:       hasRoots,
		hasSampling:    hasSampling,
		experimental:   experimental,
		startTime:      time.Now(),
		workspaceReady: make(chan struct{}),
		persistDone:    make(chan struct{}),
	}
	// A client that cannot report roots will never make us wait for them.
	if !hasRoots {
		close(info.workspaceReady)
	}
	s.sessions[sessionID] = info
	s.evictClosedLocked()

	s.logger.Debug("session registered (not yet persisted — awaiting first activity)",
		zap.String("session_id", sessionID),
		zap.String("client_name", clientName),
		zap.String("client_version", clientVersion),
		zap.Bool("has_roots", hasRoots),
		zap.Bool("has_sampling", hasSampling),
	)
}

// SetWorkspace records the project this session is working in, discovered by
// asking the client for its MCP roots after the handshake completed.
//
// If the session has already been persisted (it did some work before the roots
// answer arrived — a real race, the roots round-trip is not instant), the stored
// record is updated too, so the project is not lost.
func (s *SessionStore) SetWorkspace(sessionID, workspaceRoot string) {
	s.mu.Lock()
	info, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return
	}
	info.Workspace = workspaceRoot
	persisted := info.persisted
	mgr := s.storageManager
	s.signalWorkspaceReadyLocked(info)
	s.mu.Unlock()

	s.logger.Debug("session workspace resolved",
		zap.String("session_id", sessionID),
		zap.String("workspace", workspaceRoot),
	)

	if persisted && mgr != nil {
		if err := mgr.SetSessionWorkspace(sessionID, workspaceRoot); err != nil {
			s.logger.Debug("failed to backfill workspace on persisted session",
				zap.String("session_id", sessionID), zap.Error(err))
		}
	}
}

// EnsurePersisted writes the session to storage the first time it does something
// real, and blocks concurrent callers until that row exists.
//
// The blocking matters. UpdateSessionStats requires the row and errors if it is
// missing, so if a second concurrent first-call merely saw "someone else is
// persisting" and raced on, its stats would be dropped with a warning. It waits
// instead.
//
// Returns the work-session id this connection belongs to. It is resolved ONCE
// per connection and reused, so every record from this connection agrees.
func (s *SessionStore) EnsurePersisted(sessionID string, resolveWorkSession func(*SessionInfo) string) string {
	if sessionID == "" {
		return ""
	}

	s.mu.Lock()
	info, ok := s.sessions[sessionID]
	if !ok || s.storageManager == nil {
		s.mu.Unlock()
		return ""
	}

	revived := s.reopenLocked(info)
	if info.persisted {
		workSessionID := info.workSessionID
		done := info.persistDone
		s.mu.Unlock()
		if revived {
			s.reconcileStorage(sessionID)
		}
		<-done // the row may still be in flight; stats callers need it to exist
		return workSessionID
	}

	// We are the one who persists. Resolve the work session now, once, under the
	// lock, and cache it for every later record from this connection.
	info.persisted = true
	if info.workSessionID == "" && resolveWorkSession != nil {
		cp := *info
		info.workSessionID = resolveWorkSession(&cp)
	}

	record := &storage.SessionRecord{
		ID:            info.SessionID,
		ClientName:    info.ClientName,
		ClientVersion: info.ClientVersion,
		Status:        "active",
		StartTime:     info.startTime,
		LastActivity:  time.Now(),
		HasRoots:      info.hasRoots,
		HasSampling:   info.hasSampling,
		Experimental:  info.experimental,
		WorkspaceRoot: info.Workspace,
		WorkspaceName: workspaceDisplayName(info.Workspace),
		WorkSessionID: info.workSessionID,
		// The credential and the latest resolution so far.
		TokenName:     info.TokenName,
		ClientID:      info.ClientID,
		Profile:       info.Profile,
		ProfileSource: info.ProfileSource,
	}
	workSessionID := info.workSessionID
	done := info.persistDone
	mgr := s.storageManager
	s.mu.Unlock()

	err := mgr.CreateSession(record)

	s.mu.Lock()
	if err != nil {
		s.logger.Warn("failed to persist session to storage",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
		// Let a later activity retry rather than silently never persisting. The
		// waiters are released either way — a blocked caller is worse than a
		// caller that finds no row.
		if cur, ok := s.sessions[sessionID]; ok {
			cur.persisted = false
		}
	}
	select {
	case <-done:
	default:
		close(done)
	}
	s.mu.Unlock()

	return workSessionID
}

// WorkSessionID returns the work session this connection belongs to, if it has
// been resolved yet.
func (s *SessionStore) WorkSessionID(sessionID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if info, ok := s.sessions[sessionID]; ok {
		return info.workSessionID
	}
	return ""
}

// TryClaimWorkspaceFetch returns true exactly once per session, for the caller
// that should go and ask the client for its roots. Returns false if there is
// nothing to fetch (no roots capability, already known) or someone already went.
func (s *SessionStore) TryClaimWorkspaceFetch(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, ok := s.sessions[sessionID]
	if !ok || info.workspaceFetchStarted || info.Workspace != "" || !info.hasRoots {
		return false
	}
	info.workspaceFetchStarted = true
	return true
}

// GetSession returns a COPY of the session info.
//
// It must be a copy: the roots goroutine writes Workspace under the lock while
// callers (the workspace resolver, the activity path) read it, and handing out
// the live pointer is a data race.
func (s *SessionStore) GetSession(sessionID string) *SessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	info, ok := s.sessions[sessionID]
	if !ok {
		return nil
	}
	cp := *info
	return &cp
}

// signalWorkspaceReadyLocked closes the readiness channel exactly once.
// Caller must hold the write lock.
func (s *SessionStore) signalWorkspaceReadyLocked(info *SessionInfo) {
	select {
	case <-info.workspaceReady:
		// already closed
	default:
		close(info.workspaceReady)
	}
}

// WorkspaceSettled blocks until the client's roots have arrived, or we have
// given up waiting, or the deadline passes — whichever comes first.
//
// This exists so the FIRST piece of activity on a connection is not attributed
// before we know the project. Without it, the first tool call resolves a
// workspace-less work session and the second (roots having landed in between)
// resolves a workspace-keyed one: a single connection would split across two
// work sessions.
func (s *SessionStore) WorkspaceSettled(sessionID string, wait time.Duration) {
	s.mu.RLock()
	info, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return
	}

	select {
	case <-info.workspaceReady:
	case <-time.After(wait):
		// The client is not going to answer in time. Give up for good, so we do
		// not pay this wait on every subsequent call and do not later change our
		// mind about which work session this connection belongs to.
		s.mu.Lock()
		if cur, ok := s.sessions[sessionID]; ok {
			s.signalWorkspaceReadyLocked(cur)
		}
		s.mu.Unlock()
	}
}

// AbandonWorkspaceFetch marks the roots answer as never coming, unblocking
// anything waiting on it.
func (s *SessionStore) AbandonWorkspaceFetch(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if info, ok := s.sessions[sessionID]; ok {
		s.signalWorkspaceReadyLocked(info)
	}
}

// RemoveSession soft-closes a session.
//
// The entry is KEPT, marked closed: a client that re-registers on the same id
// (e.g. a GET stream ending and being re-opened) sends no new initialize, so
// dropping the entry would lose its client name, workspace, persisted flag and
// work session for good. The next activity or Reopen revives it; otherwise it is
// evicted after closedSessionTTL.
//
// Only sessions that were actually persisted are closed in storage. A session
// that never did any work has no stored record, and asking storage to
// close it would return "session not found" on every idle disconnect.
func (s *SessionStore) RemoveSession(sessionID string) {
	s.mu.Lock()
	info, ok := s.sessions[sessionID]
	wasPersisted := ok && info.persisted
	transition := ok && !info.closed
	if ok {
		info.closed = true
		if transition {
			info.closedAt = time.Now()
		}
	} else {
		delete(s.activeProfiles, sessionID)
	}
	s.evictClosedLocked()
	s.mu.Unlock()

	if transition && wasPersisted {
		s.reconcileStorage(sessionID)
	}

	s.logger.Debug("session soft-closed",
		zap.String("session_id", sessionID),
		zap.Bool("was_persisted", wasPersisted),
	)
}

func (s *SessionStore) UpdateSessionStats(sessionID string) {
	s.mu.Lock()
	persisted, revived := false, false
	if info, ok := s.sessions[sessionID]; ok {
		persisted = info.persisted
		revived = s.reopenLocked(info)
	}
	mgr := s.storageManager
	s.mu.Unlock()
	if revived {
		s.reconcileStorage(sessionID)
	}

	if !persisted || mgr == nil {
		return
	}
	if err := mgr.UpdateSessionStats(sessionID); err != nil {
		s.logger.Warn("failed to update session stats in storage",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
	}
}

// UpdateActivity updates the last activity timestamp for a session without
// incrementing stats. A never-persisted session has no row to touch — and must
// not gain one here, since merely exchanging MCP messages is not "work".
func (s *SessionStore) UpdateActivity(sessionID string) {
	s.mu.Lock()
	persisted, revived := false, false
	if info, ok := s.sessions[sessionID]; ok {
		persisted = info.persisted
		revived = s.reopenLocked(info)
	}
	mgr := s.storageManager
	s.mu.Unlock()
	if revived {
		s.reconcileStorage(sessionID)
	}

	if !persisted || mgr == nil {
		return
	}
	_ = mgr.UpdateSessionActivity(sessionID)
}

// SetActiveProfile records the active profile slug for a session
// (set_profile). An empty slug clears the selection (back to all-servers).
func (s *SessionStore) SetActiveProfile(sessionID, profileSlug string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if profileSlug == "" {
		delete(s.activeProfiles, sessionID)
		return
	}
	s.activeProfiles[sessionID] = profileSlug
}

// SetSessionIdentity records the credential that authenticated a session:
// the client credential name and its client id (empty for an admin
// session). Called once at initialize, from the request's AuthContext — the
// credential never changes mid-connection, so this is never called again for
// the same sessionID.
func (s *SessionStore) SetSessionIdentity(sessionID, tokenName, clientID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if info, ok := s.sessions[sessionID]; ok {
		info.TokenName = tokenName
		info.ClientID = clientID
	}
}

// SetSessionTokenPrefix records the display prefix of the credential that
// authenticated a session, next to SetSessionIdentity.
func (s *SessionStore) SetSessionTokenPrefix(sessionID, prefix string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if info, ok := s.sessions[sessionID]; ok {
		info.TokenPrefix = prefix
	}
}

// SessionsMatching returns every live session for which pred is true, with its
// serving instance. pred runs under the store's read
// lock and must not call back into the store.
func (s *SessionStore) SessionsMatching(pred func(*SessionInfo) bool) []sessionTarget {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []sessionTarget
	for id, info := range s.sessions {
		if !info.closed && pred(info) {
			out = append(out, sessionTarget{ID: id, Server: info.server})
		}
	}
	return out
}

// ClearSelection drops every stored set_profile selection of the named
// profile (it was deleted): those sessions fall back to their base.
func (s *SessionStore) ClearSelection(name string) {
	if name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sel := range s.activeProfiles {
		if sel == name {
			delete(s.activeProfiles, id)
		}
	}
}

// SetSessionServer records the MCP server instance serving a session (the
// session -> server-instance map). Called once at initialize.
func (s *SessionStore) SetSessionServer(sessionID string, srv *mcpserver.MCPServer) {
	if sessionID == "" || srv == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if info, ok := s.sessions[sessionID]; ok {
		info.server = srv
	}
}

// sessionTarget is one live session of a token, with the server instance that
// serves it (nil when it was not recorded).
type sessionTarget struct {
	ID     string
	Server *mcpserver.MCPServer
}

// NotifyTargets returns every live session authenticated by tokenName with its
// serving instance.
func (s *SessionStore) NotifyTargets(tokenName string) []sessionTarget {
	if tokenName == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []sessionTarget
	for id, info := range s.sessions {
		if !info.closed && info.TokenName == tokenName {
			out = append(out, sessionTarget{ID: id, Server: info.server})
		}
	}
	return out
}

func (s *SessionStore) UpdateSessionProfile(sessionID, profileName, source string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	info, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return
	}
	changed := info.Profile != profileName || info.ProfileSource != source
	info.Profile = profileName
	info.ProfileSource = source
	persisted := info.persisted
	done := info.persistDone
	mgr := s.storageManager
	s.mu.Unlock()

	if !changed || !persisted {
		return
	}
	// The row may still be in flight (EnsurePersisted flips persisted before
	// CreateSession returns); wait so the write lands on an existing row.
	<-done

	// Concurrent updates of one session must not persist an older resolution
	// after a newer one: serialize the write-throughs and persist the CURRENT
	// in-memory value at write time, so the last writer always leaves the
	// latest resolution behind.
	s.writeThroughMu.Lock()
	defer s.writeThroughMu.Unlock()
	s.mu.RLock()
	if cur, ok := s.sessions[sessionID]; ok {
		profileName, source = cur.Profile, cur.ProfileSource
	}
	s.mu.RUnlock()
	if mgr == nil {
		return
	}
	if err := mgr.SetSessionProfile(sessionID, profileName, source); err != nil {
		s.logger.Debug("failed to write session profile through to storage",
			zap.String("session_id", sessionID), zap.Error(err))
	}
}

// GetActiveProfile returns the active profile slug for a session, or "" when the
// session has no active profile selection.
func (s *SessionStore) GetActiveProfile(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeProfiles[sessionID]
}

// workspaceDisplayName is the basename of a workspace root. Only the basename is
// ever shown — the full path is a local filesystem path and stays local.
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
