package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	uptransport "github.com/mark3labs/mcp-go/v2/client/transport"
	"github.com/mark3labs/mcp-go/v2/mcp"
	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/secret"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/transport"
	"github.com/slopster67/mcpproxy-go/internal/upstream/callstats"
	"github.com/slopster67/mcpproxy-go/internal/upstream/core"
	"github.com/slopster67/mcpproxy-go/internal/upstream/dialgate"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
	"github.com/slopster67/mcpproxy-go/internal/upstream/types"
)

type Manager struct {
	peerExecutor peering.Executor
	clients      map[string]*managed.Client
	desired      map[string]desiredGen
	mu           sync.RWMutex
	// captureMu serializes a bounded tool-review persistence against
	// client replacement/removal. It is deliberately separate from mu: approval
	// calculation reads manager state, and holding mu.RLock across it would
	// deadlock behind a queued AddServerConfig writer. Lock order is captureMu
	// then mu; mutation releases both before disconnecting or invoking callbacks.
	captureMu sync.RWMutex

	connectionWake atomic.Pointer[func()]
	logger         *zap.Logger
	logConfig      *config.LogConfig

	globalConfig    atomic.Pointer[config.Config]
	storage         *storage.Database
	notificationMgr *NotificationManager
	secretResolver  *secret.Resolver

	// lastSweptAt tracks each server's last discovery to avoid re-listing
	// tools before the global discovery interval has elapsed. Guarded by sweepMu.
	sweepMu     sync.Mutex
	lastSweptAt map[string]time.Time

	// callStats is the rolling per-server tool-call outcome window that feeds
	// the health calculator's failure-rate rule. In memory only.
	callStats  *callstats.Registry
	callHealth callHealthNotifier

	// tokenReconnect keeps last reconnect trigger time per server when detecting
	// newly available OAuth tokens without explicit DB events (e.g., when CLI
	// cannot write due to DB lock). Prevents rapid retrigger loops.
	tokenReconnect map[string]time.Time

	// tokenFingerprints records which persisted token each server was last
	// retried with, so the scan fires on a NEW token rather than on the mere
	// presence of the stale one it already failed with. Same goroutine
	// as tokenReconnect (the OAuth event monitor), so it needs no extra lock.
	tokenFingerprints map[string]string

	// Context for shutdown coordination
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
	shuttingDown   bool           // Flag to prevent reconnections during shutdown
	shutdownWg     sync.WaitGroup // Tracks all background goroutines for clean shutdown

	storageMgr *storage.Manager

	// committedViewCheck is the construction-time committed-view check
	// (SetCommittedViewChecker); nil skips it.
	committedViewCheck atomic.Pointer[func(name string, sg commitview.ServerGen) error]

	// Tool discovery callback for notifications/tools/list_changed handling
	toolDiscoveryCallback func(ctx context.Context, serverName string) error
}

func cloneServerConfig(cfg *config.ServerConfig) *config.ServerConfig {
	if cfg == nil {
		return nil
	}

	clone := *cfg

	if cfg.Args != nil {
		clone.Args = slices.Clone(cfg.Args)
	}

	if cfg.Env != nil {
		clone.Env = maps.Clone(cfg.Env)
	}

	if cfg.Headers != nil {
		clone.Headers = maps.Clone(cfg.Headers)
	}

	if cfg.OAuth != nil {
		o := *cfg.OAuth
		if cfg.OAuth.Scopes != nil {
			o.Scopes = slices.Clone(cfg.OAuth.Scopes)
		}
		clone.OAuth = &o
	}

	return &clone
}

// NewManager creates a new upstream manager
func NewManager(logger *zap.Logger, globalConfig *config.Config, boltStorage *storage.Database, secretResolver *secret.Resolver, storageMgr *storage.Manager) *Manager {

	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	manager := &Manager{
		clients:           make(map[string]*managed.Client),
		desired:           make(map[string]desiredGen),
		logger:            logger,
		storage:           boltStorage,
		notificationMgr:   NewNotificationManager(),
		secretResolver:    secretResolver,
		tokenReconnect:    make(map[string]time.Time),
		tokenFingerprints: make(map[string]string),
		lastSweptAt:       make(map[string]time.Time),
		callStats:         callstats.NewRegistry(),
		shutdownCtx:       shutdownCtx,
		shutdownCancel:    shutdownCancel,
		storageMgr:        storageMgr,
	}
	manager.globalConfig.Store(globalConfig)

	// Set up OAuth completion callback to trigger connection retries (in-process)
	tokenManager := oauth.GetTokenStoreManager()
	manager.installOAuthCompletionHandlers(tokenManager)

	// Surface OAuth failures that have no caller to return an error to — an
	// undeliverable callback, a state mismatch, a background token exchange
	// that failed — on the server's status.
	tokenManager.SetOAuthFailureCallback(manager.recordOAuthFailure)

	// Start database event monitor for cross-process OAuth completion notifications
	if boltStorage != nil {
		manager.shutdownWg.Add(1)
		go manager.startOAuthEventMonitor(shutdownCtx)
	}

	return manager
}

func (m *Manager) resolveConnectTimeout() time.Duration {
	timeout := 30 * time.Second
	if gc := m.globalConfig.Load(); gc != nil {
		timeout = gc.ResolveInitTimeout()
	}
	return timeout
}

// SetLogConfig sets the logging configuration for upstream server loggers
func (m *Manager) SetLogConfig(logConfig *config.LogConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logConfig = logConfig
}

func (m *Manager) SetGlobalConfig(globalConfig *config.Config) {
	m.globalConfig.Store(globalConfig)

	m.mu.RLock()
	clients := make([]*managed.Client, 0, len(m.clients))
	for _, client := range m.clients {
		if client != nil {
			clients = append(clients, client)
		}
	}
	m.mu.RUnlock()

	for _, client := range clients {
		client.SetGlobalConfig(globalConfig)
	}
}

// AddNotificationHandler adds a notification handler to receive state change notifications
func (m *Manager) AddNotificationHandler(handler NotificationHandler) {
	m.notificationMgr.AddHandler(handler)
}

// SetToolDiscoveryCallback sets the callback for triggering tool re-indexing when
// upstream servers send notifications/tools/list_changed notifications.
// This callback will be passed to all new clients created by the manager.
func (m *Manager) SetToolDiscoveryCallback(callback func(ctx context.Context, serverName string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolDiscoveryCallback = callback
	m.logger.Debug("Tool discovery callback set on manager")
}

func (m *Manager) ShutdownAll(ctx context.Context) error {
	m.logger.Info("Shutting down all upstream servers")

	if m.shutdownCancel != nil {
		m.shutdownCancel()
	}
	m.stopCallHealthTimers()

	// Wait for background goroutines to exit (with timeout)
	waitDone := make(chan struct{})
	go func() {
		m.shutdownWg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		m.logger.Info("All background goroutines exited cleanly")
	case <-time.After(3 * time.Second):
		m.logger.Warn("Background goroutines did not exit within 3 seconds, proceeding with shutdown")
	case <-ctx.Done():
		m.logger.Warn("Shutdown context cancelled while waiting for goroutines", zap.Error(ctx.Err()))
	}

	// Set shutdown flag to prevent any reconnection attempts
	m.mu.Lock()
	m.shuttingDown = true
	clientMap := make(map[string]*managed.Client, len(m.clients))
	for id, client := range m.clients {
		clientMap[id] = client
	}
	m.mu.Unlock()

	if len(clientMap) == 0 {
		m.logger.Debug("No upstream servers to shutdown")
		return nil
	}

	m.logger.Info("Disconnecting all upstream servers (in parallel)",
		zap.Int("count", len(clientMap)))

	// Disconnect all clients in parallel using goroutines
	// This ensures shutdown is fast even with many servers
	var wg sync.WaitGroup
	for id, client := range clientMap {
		if client == nil {
			continue
		}

		wg.Add(1)
		// Launch goroutine immediately without calling GetConfig() first
		// GetConfig() will be called inside goroutine but we'll use client.Disconnect()
		// which handles locking internally and won't block the shutdown loop
		go func(clientID string, c *managed.Client) {
			defer wg.Done()

			// Try to get server name, but use a fallback if it blocks
			// This prevents the entire shutdown from hanging on one stuck server
			serverName := clientID // Fallback to clientID
			if cfg := c.GetConfig(); cfg != nil {
				serverName = cfg.Name
			}

			m.logger.Debug("Disconnecting server",
				zap.String("id", clientID),
				zap.String("server", serverName))

			if err := c.Disconnect(); err != nil {
				m.logger.Warn("Error disconnecting server",
					zap.String("id", clientID),
					zap.String("server", serverName),
					zap.Error(err))
			} else {
				m.logger.Debug("Successfully disconnected server",
					zap.String("id", clientID),
					zap.String("server", serverName))
			}
		}(id, client)
	}

	// Wait for all disconnections to complete (with timeout)
	// Use a shorter timeout (5 seconds) for the disconnect phase
	// If servers are stuck in "Connecting" state, their disconnection will hang
	// In that case, we'll force-proceed to container cleanup which can handle it
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	disconnectTimeout := 5 * time.Second
	disconnectTimer := time.NewTimer(disconnectTimeout)
	defer disconnectTimer.Stop()

	select {
	case <-done:
		m.logger.Info("All upstream servers disconnected successfully")
	case <-disconnectTimer.C:
		m.logger.Warn("Disconnect phase timed out after 5 seconds, forcing cleanup")
		m.logger.Warn("Some servers may not have disconnected cleanly (likely stuck in Connecting state)")
		// Don't wait for stuck goroutines - proceed with container cleanup anyway
	case <-ctx.Done():
		m.logger.Warn("Shutdown context cancelled, forcing cleanup",
			zap.Error(ctx.Err()))
		// Don't wait for stuck goroutines - proceed with container cleanup anyway
	}

	m.logger.Info("All upstream servers shut down successfully")
	return nil
}

// GetClient returns a client by ID
func (m *Manager) GetClient(id string) (*managed.Client, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	client, exists := m.clients[id]
	return client, exists
}

// GetAllClients returns all clients
func (m *Manager) GetAllClients() map[string]*managed.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*managed.Client)
	for id, client := range m.clients {
		result[id] = client
	}
	return result
}

// GetAllServerNames returns a slice of all configured server names
func (m *Manager) GetAllServerNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}

// shouldSweepServer checks whether the global interval has elapsed since this
// server was last listed. Connection and notification discovery run independently.
func shouldSweepServer(interval time.Duration, lastSwept time.Time, hasSwept bool, now time.Time) bool {
	if interval <= 0 {
		return false
	}
	if !hasSwept {
		return true
	}
	return now.Sub(lastSwept) >= interval
}

// markSwept records that the periodic sweep just listed serverName's tools, so
// the per-server cadence gate (shouldSweepServer) can wait the server's own
// interval before listing it again.
func (m *Manager) markSwept(serverName string, now time.Time) {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()
	m.lastSweptAt[serverName] = now
}

// lastSweptFor returns the last periodic-sweep time for serverName.
func (m *Manager) lastSweptFor(serverName string) (time.Time, bool) {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()
	last, ok := m.lastSweptAt[serverName]
	return last, ok
}

// pruneSweptState drops last-swept entries for servers no longer present so the
// map can't grow unbounded as servers are added/removed (mirrors the lastGoodTools
// pruning in the runtime indexing path).
func (m *Manager) pruneSweptState(known map[string]struct{}) {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()
	for name := range m.lastSweptAt {
		if _, ok := known[name]; !ok {
			delete(m.lastSweptAt, name)
		}
	}
}

func (m *Manager) DiscoverTools(ctx context.Context) ([]*config.ToolMetadata, error) {
	tools, _, _, err := m.discoverTools(ctx, false)
	return tools, err
}

// DiscoverToolsReportCaptured is DiscoverToolsReport that also returns, per
// listed server, the capture of that server's own tools/list: the sweep
// applies each server's inventory in capture order, not
// under one ticket for the whole sweep.
func (m *Manager) DiscoverToolsReportCaptured(ctx context.Context, dueOnly bool) ([]*config.ToolMetadata, []string, map[string]managed.InventoryCapture, error) {
	return m.discoverTools(ctx, dueOnly)
}

func (m *Manager) discoverTools(ctx context.Context, dueOnly bool) ([]*config.ToolMetadata, []string, map[string]managed.InventoryCapture, error) {
	type clientSnapshot struct {
		id      string
		name    string
		enabled bool

		client *managed.Client
	}

	m.mu.RLock()
	snapshots := make([]clientSnapshot, 0, len(m.clients))
	for id, client := range m.clients {
		name := ""
		enabled := false
		var cfg *config.ServerConfig
		// Read config through the thread-safe GetConfig() accessor — the reconcile
		// add path (AddServerConfig) calls SetConfig (an atomic swap) off m.mu, so
		// a direct config-field read would race with it.
		if client != nil {
			if cfg = client.GetConfig(); cfg != nil {
				name = cfg.Name
				enabled = cfg.Enabled
			}
		}
		snapshots = append(snapshots, clientSnapshot{
			id:      id,
			name:    name,
			enabled: enabled,

			client: client,
		})
	}
	m.mu.RUnlock()

	// Resolve the global discovery cadence.
	gc := m.globalConfig.Load()
	if gc == nil {
		gc = &config.Config{}
	}
	now := time.Now()

	var allTools []*config.ToolMetadata
	var listed []string
	captures := make(map[string]managed.InventoryCapture)
	connectedCount := 0
	skippedNotDue := 0
	known := make(map[string]struct{}, len(snapshots))

	for _, snapshot := range snapshots {
		client := snapshot.client
		if client == nil {
			continue
		}
		if snapshot.name != "" {
			known[snapshot.name] = struct{}{}
		}

		if !snapshot.enabled {
			continue
		}

		if !client.IsConnected() {
			m.logger.Debug("Skipping disconnected client",
				zap.String("id", snapshot.id),
				zap.String("server", snapshot.name),
				zap.String("state", client.GetState().String()))
			continue
		}

		// Skip servers not yet due under the global discovery cadence.
		if dueOnly {
			interval := gc.ResolveToolDiscoveryInterval()
			last, hasSwept := m.lastSweptFor(snapshot.name)
			if !shouldSweepServer(interval, last, hasSwept, now) {
				m.logger.Debug("Skipping client not due for periodic tool sweep",
					zap.String("id", snapshot.id),
					zap.String("server", snapshot.name),
					zap.Duration("interval", interval))
				skippedNotDue++
				continue
			}
		}

		connectedCount++

		tools, capture, err := client.ListToolsCaptured(ctx)
		if err != nil {
			// Warn, not Error: markSwept below is deliberately skipped so the
			// next sweep retries this server, and a failure that will be
			// retried is not an error.
			m.logger.Warn("Failed to list tools from client",
				zap.String("id", snapshot.id),
				zap.String("server", snapshot.name),
				zap.Error(err))
			continue
		}

		// Record the sweep only after a successful list so a transient failure
		// retries on the next cycle rather than waiting a full interval.
		m.markSwept(snapshot.name, now)
		if snapshot.name != "" {
			listed = append(listed, snapshot.name)
			captures[snapshot.name] = capture
		}

		if tools != nil {
			allTools = append(allTools, tools...)
		}
	}

	// Keep the per-server sweep-time map bounded as servers come and go.
	m.pruneSweptState(known)

	m.logger.Info("Discovered tools from upstream servers",
		zap.Int("total_tools", len(allTools)),
		zap.Int("connected_servers", connectedCount),
		zap.Bool("due_only", dueOnly),
		zap.Int("skipped_not_due", skippedNotDue))

	return allTools, listed, captures, nil
}

// CallTool calls a tool on the appropriate upstream server
func (m *Manager) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	return m.callTool(ctx, toolName, args, nil)
}

// CallToolOnEpoch is CallTool pinned to the connection generation the caller
// certified the tool identity against: the dispatch reaches the upstream on
// exactly that generation of the server's live client
// (managed.Client.CallToolOnEpoch) or is refused with
// managed.ErrConnectionGenerationChanged and zero upstream calls. A pinned
// call never opens a new connection — a
// reconnect is a new generation by construction, whose tool set the caller
// never certified — so a client found disconnected is refused the same way.
// The refusal is returned verbatim, never enriched, so errors.Is holds.
func (m *Manager) CallToolOnEpoch(ctx context.Context, toolName string, args map[string]interface{}, expectedEpoch int64) (interface{}, error) {
	return m.callTool(ctx, toolName, args, &expectedEpoch)
}

// callTool is the shared body of CallTool and CallToolOnEpoch; expectedEpoch
// nil means the call does not require a particular connection generation.
func (m *Manager) callTool(ctx context.Context, toolName string, args map[string]interface{}, expectedEpoch *int64) (interface{}, error) {
	m.logger.Debug("CallTool: starting",
		zap.String("tool_name", toolName),
		zap.Any("args", args))

	// Parse tool name to extract server and tool components
	parts := strings.SplitN(toolName, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid tool name format: %s (expected server:tool)", toolName)
	}

	serverName := parts[0]
	actualToolName := parts[1]

	m.logger.Debug("CallTool: parsed tool name",
		zap.String("server_name", serverName),
		zap.String("actual_tool_name", actualToolName))

	m.mu.RLock()
	clientCount := len(m.clients)

	// Find the client for this server
	var targetClient *managed.Client
	for _, client := range m.clients {
		if client.GetConfig().Name == serverName {
			targetClient = client
			break
		}
	}
	m.mu.RUnlock()

	m.logger.Debug("CallTool: resolved client under read lock",
		zap.String("server_name", serverName),
		zap.Int("total_clients", clientCount))

	if targetClient == nil {
		m.logger.Error("CallTool: no client found",
			zap.String("server_name", serverName))
		return nil, fmt.Errorf("no client found for server: %s", serverName)
	}

	m.logger.Debug("CallTool: client found",
		zap.String("server_name", serverName),
		zap.Bool("enabled", targetClient.GetConfig().Enabled),
		zap.Bool("connected", targetClient.IsConnected()),
		zap.String("state", targetClient.GetState().String()))

	if !targetClient.GetConfig().Enabled {
		return nil, fmt.Errorf("client for server %s is disabled", serverName)
	}

	// Check connection status and provide detailed error information
	if !targetClient.IsConnected() {
		state := targetClient.GetState()
		if targetClient.IsConnecting() {
			return nil, fmt.Errorf("server '%s' is currently connecting - please wait for connection to complete (state: %s)", serverName, state.String())
		}

		// A pinned dispatch never reconnects: the generation the caller
		// certified is closed (Disconnect bumped it), and a reconnect would
		// open one the caller never certified.
		if expectedEpoch != nil {
			return nil, managed.ErrConnectionGenerationChanged
		}

		// Include last error if available with enhanced context
		if lastError := targetClient.GetLastError(); lastError != nil {
			// Enrich OAuth-related errors at source
			lastErrStr := lastError.Error()
			if strings.Contains(lastErrStr, "OAuth authentication failed") ||
				strings.Contains(lastErrStr, "Dynamic Client Registration") ||
				strings.Contains(lastErrStr, "authorization required") {
				return nil, fmt.Errorf("server '%s' requires OAuth authentication but is not properly configured. OAuth setup failed: %s. Please configure OAuth credentials manually or use a Personal Access Token - check mcpproxy logs for detailed setup instructions", serverName, lastError.Error())
			}

			if strings.Contains(lastErrStr, "OAuth metadata unavailable") {
				return nil, fmt.Errorf("server '%s' does not provide valid OAuth configuration endpoints. This server may not support OAuth or requires manual authentication setup: %s", serverName, lastError.Error())
			}

			return nil, fmt.Errorf("server '%s' is not connected (state: %s) - connection failed with error: %s", serverName, state.String(), lastError.Error())
		}

		return nil, fmt.Errorf("server '%s' is not connected (state: %s) - use 'upstream_servers' tool to check server configuration", serverName, state.String())
	}

	m.logger.Debug("CallTool: calling client.CallTool",
		zap.String("server_name", serverName),
		zap.String("actual_tool_name", actualToolName))

	// Call the tool on the upstream server with enhanced error handling
	dispatch := targetClient.CallTool
	if expectedEpoch != nil {
		pinned := *expectedEpoch
		dispatch = func(ctx context.Context, toolName string, args map[string]interface{}) (*mcp.CallToolResult, error) {
			return targetClient.CallToolOnEpoch(ctx, toolName, args, pinned)
		}
	}
	result, err := dispatch(ctx, actualToolName, args)
	// Record the raw dispatch outcome before any error
	// enrichment below. Pre-dispatch refusals above never reach this line.
	m.RecordClientCallOutcome(ctx, targetClient, result, err)

	m.logger.Debug("CallTool: client.CallTool returned",
		zap.String("server_name", serverName),
		zap.String("actual_tool_name", actualToolName),
		zap.Error(err),
		zap.Bool("has_result", result != nil))
	if err != nil {

		// The generation refusal is a typed
		// identity the dispatch paths map onto their own unresolved-identity
		// body; return it verbatim so errors.Is survives.
		if errors.Is(err, managed.ErrConnectionGenerationChanged) {
			return nil, err
		}
		// The instance was retired (removed,
		// replaced or disabled) while the call was on its way. Unavailable,
		// with no connection or authentication advice.
		if errors.Is(err, managed.ErrInstanceRetired) {
			return nil, fmt.Errorf("server '%s' is unavailable: it was removed, replaced or disabled while tool '%s' was being called: %w", serverName, actualToolName, err)
		}

		// Enrich errors at source with server context
		errStr := err.Error()

		// OAuth-related errors
		if strings.Contains(errStr, "OAuth authentication failed") ||
			strings.Contains(errStr, "authorization required") ||
			strings.Contains(errStr, "invalid_token") ||
			strings.Contains(errStr, "Unauthorized") {
			return nil, fmt.Errorf("server '%s' authentication failed for tool '%s'. OAuth/token authentication required but not properly configured. Check server authentication settings and ensure valid credentials are available: %w", serverName, actualToolName, err)
		}

		// Permission/scope errors
		if strings.Contains(errStr, "insufficient_scope") || strings.Contains(errStr, "access_denied") {
			return nil, fmt.Errorf("server '%s' denied access to tool '%s' due to insufficient permissions or scopes. Check OAuth scopes configuration or token permissions: %w", serverName, actualToolName, err)
		}

		// Rate limiting
		if strings.Contains(errStr, "429") || strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "too many requests") {
			return nil, fmt.Errorf("server '%s' rate limit exceeded for tool '%s'. Please wait before making more requests or check API quotas: %w", serverName, actualToolName, err)
		}

		// Connection issues
		if strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "no such host") {
			return nil, fmt.Errorf("server '%s' connection failed for tool '%s'. Check if the server URL is correct and the server is running: %w", serverName, actualToolName, err)
		}

		// Tool-specific errors
		if strings.Contains(errStr, "tool not found") || strings.Contains(errStr, "unknown tool") {
			return nil, fmt.Errorf("tool '%s' not found on server '%s'. Use 'retrieve_tools' to see available tools: %w", actualToolName, serverName, err)
		}

		// Generic error with helpful context
		return nil, fmt.Errorf("tool '%s' on server '%s' failed: %w. Check server configuration, authentication, and tool parameters", actualToolName, serverName, err)
	}

	return result, nil
}

// RecordClientCallOutcome is RecordCallOutcome for a dispatch that went through
// client. It is the single entry point shared by Manager.callTool and the
// code_execution dispatch, which calls the managed client directly and
// bypasses the manager. The outcome is dropped unless client is still the
// registered client for its server: RemoveServer deletes the client and then
// drops the window, so a call still in flight at removal (its transport closes
// and it fails) must not recreate the history a same-named replacement would
// inherit. The check and the Record happen under the manager read lock, which
// RemoveServer's delete takes for writing, so either the record lands before
// the drop (and is wiped by it) or the check fails.
func (m *Manager) RecordClientCallOutcome(ctx context.Context, client *managed.Client, result *mcp.CallToolResult, err error) {
	if client == nil {
		return
	}
	counted, failed, kind := callstats.Classify(ctx, result, err)
	if !counted {
		return
	}
	name := client.GetConfig().Name
	m.mu.RLock()
	registered := false
	for _, c := range m.clients {
		if c == client {
			registered = true
			break
		}
	}
	if registered {
		m.callStats.Record(name, counted, failed, kind)
	}
	m.mu.RUnlock()
	if registered {
		m.noteCallHealth(name)
	}
}

// CallStats returns the counted calls and failures for server inside the
// rolling window, plus a short label for the dominant failure kind.
func (m *Manager) CallStats(server string) (calls, failures int, dominantKind string) {
	calls, failures, kind := m.callStats.Snapshot(server)
	return calls, failures, kind.String()
}

func (m *Manager) GetStats() map[string]interface{} {
	// Copy client references while holding the lock briefly.
	m.mu.RLock()
	clientsCopy := make(map[string]*managed.Client, len(m.clients))
	for id, client := range m.clients {
		clientsCopy[id] = client
	}
	totalCount := len(m.clients)
	m.mu.RUnlock()

	// Now process clients without holding lock to avoid deadlock
	connectedCount := 0
	connectingCount := 0
	serverStatus := make(map[string]interface{})

	for id, client := range clientsCopy {
		// Skip nil clients (can happen during shutdown)
		if client == nil {
			continue
		}

		// Get detailed connection info from state manager
		connectionInfo := client.GetConnectionInfo()

		// Read config through the thread-safe accessor to avoid racing with
		// SetConfig on the reconcile add path.
		name, url, protocol := "", "", ""
		enabled := false
		if cfg := client.GetConfig(); cfg != nil {
			name, url, protocol = cfg.Name, cfg.URL, cfg.Protocol
			enabled = cfg.Enabled

		}

		status := map[string]interface{}{
			"enabled":      enabled,
			"state":        connectionInfo.State.String(),
			"connected":    connectionInfo.State == types.StateReady,
			"connecting":   client.IsConnecting(),
			"retry_count":  connectionInfo.RetryCount,
			"should_retry": connectionInfo.ShouldAutoReconnect(time.Now()),
			"name":         name,
			"url":          url,
			"protocol":     protocol,
		}

		if connectionInfo.State == types.StateReady {
			connectedCount++
		}

		if client.IsConnecting() {
			connectingCount++
		}

		if !connectionInfo.LastRetryTime.IsZero() {
			status["last_retry_time"] = connectionInfo.LastRetryTime
		}

		if connectionInfo.LastError != nil {
			status["last_error"] = connectionInfo.LastError.Error()
		}

		if connectionInfo.ServerName != "" {
			status["server_name"] = connectionInfo.ServerName
		}

		if connectionInfo.ServerVersion != "" {
			status["server_version"] = connectionInfo.ServerVersion
		}

		// Only call GetServerInfo() for connected clients.
		// GetServerInfo() acquires core.Client.mu.RLock() which blocks if Connect() holds
		// core.Client.mu.Lock() during slow OAuth flows — preventing HTTP server startup.
		if connectionInfo.State == types.StateReady {
			if serverInfo := client.GetServerInfo(); serverInfo != nil && serverInfo.ProtocolVersion != "" {
				status["protocol_version"] = serverInfo.ProtocolVersion
			}
		}

		serverStatus[id] = status
	}

	// Call GetTotalToolCount without holding manager lock
	totalTools := m.GetTotalToolCount()

	return map[string]interface{}{
		"connected_servers":  connectedCount,
		"connecting_servers": connectingCount,

		"total_servers": totalCount,
		"servers":       serverStatus,
		"total_tools":   totalTools,
	}
}

// GetTotalToolCount returns the total number of tools across all servers.
// Uses cached counts to avoid excessive network calls (2-minute cache per server).
// The manager lock is held only to copy client references.
func (m *Manager) GetTotalToolCount() int {
	m.mu.RLock()
	clientsCopy := make([]*managed.Client, 0, len(m.clients))
	for _, client := range m.clients {
		clientsCopy = append(clientsCopy, client)
	}
	m.mu.RUnlock()

	// Now process clients without holding lock
	totalTools := 0
	for _, client := range clientsCopy {
		if client == nil {
			continue
		}
		cfg := client.GetConfig()
		if cfg == nil || !cfg.ContributesTools() || !client.IsConnected() {
			continue
		}

		// Use non-blocking cached count to avoid stalling API handlers
		totalTools += client.GetCachedToolCountNonBlocking()
	}
	return totalTools
}

// ListServers returns information about all registered servers
func (m *Manager) ListServers() map[string]*config.ServerConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	servers := make(map[string]*config.ServerConfig)
	for id, client := range m.clients {
		servers[id] = client.GetConfig()
	}
	return servers
}

func (m *Manager) recordOAuthFailure(serverName string, cause error) {
	if cause == nil {
		return
	}

	m.mu.RLock()
	client, exists := m.clients[serverName]
	m.mu.RUnlock()
	if !exists {
		m.logger.Warn("OAuth failure reported for unknown server",
			zap.String("server", serverName),
			zap.Error(cause))
		return
	}

	if client.StateManager.IsReady() {
		m.logger.Warn("Ignoring OAuth failure for a server that is already connected",
			zap.String("server", serverName),
			zap.Error(cause))
		return
	}

	m.logger.Warn("Recording OAuth failure on server status",
		zap.String("server", serverName),
		zap.Error(cause))
	client.StateManager.SetOAuthError(cause)
}

// installOAuthCompletionHandlers wires the in-process OAuth completion
// callback: it is binding-checked, so the login
// completion of an instance a commit removed, replaced or re-bound never
// retries (disconnects and reconnects) the instance that serves the name now.
func (m *Manager) installOAuthCompletionHandlers(tm *oauth.TokenStoreManager) {
	tm.SetCompletionBindingResolver(m.CurrentCredentialBinding)
	tm.SetOAuthCompletionBoundCallback(m.onOAuthCompleted)
}

// onOAuthCompleted retries the connection of serverName after an OAuth login
// completed, only when the completing instance's binding (zero: unbound, e.g.
// the CLI process) is still the registered instance's. The binding check and
// the selection of the retry target are one registry critical section, and
// the retry acts on that captured instance: a removal and
// same-name re-add landing after the check cannot redirect the old
// instance's completion onto the replacement.
func (m *Manager) onOAuthCompleted(serverName string, binding commitview.CredentialBinding) {
	m.logger.Info("OAuth completion callback triggered, attempting connection retry",
		zap.String("server", serverName))
	if err := m.retryConnection(serverName, binding, !binding.IsZero()); err != nil {
		m.logger.Warn("Failed to trigger connection retry after OAuth completion",
			zap.String("server", serverName),
			zap.Error(err))
	}
}

// RetryConnection notifies the connection controller after new credentials
// become available. It does not dial from the notifying goroutine.
func (m *Manager) RetryConnection(serverName string) error {
	return m.retryConnection(serverName, commitview.CredentialBinding{}, false)
}

// retryConnection is RetryConnection with an optional binding check: when
// checkBinding is set, the instance registered under serverName is selected
// and its binding compared to want under the same m.mu read lock, and a
// mismatch (removed, replaced or re-bound) is a no-op. Everything after acts
// on the captured instance only, never on a later lookup of the name; an
// instance unregistered or retired after the check never connects again.
func (m *Manager) retryConnection(serverName string, want commitview.CredentialBinding, checkBinding bool) error {
	m.mu.RLock()
	// Check if we're shutting down - prevent reconnections during shutdown
	if m.shuttingDown {
		m.mu.RUnlock()
		m.logger.Debug("Skipping RetryConnection - manager is shutting down",
			zap.String("server", serverName))
		return nil
	}
	client, exists := m.clients[serverName]
	if exists && client != nil && checkBinding && client.CredentialBinding() != want {
		m.mu.RUnlock()
		m.logger.Debug("Ignoring the OAuth completion of a superseded server instance",
			zap.String("server", serverName),
			zap.String("binding", want.String()))
		return nil
	}
	m.mu.RUnlock()

	if !exists || client == nil {
		if checkBinding {
			m.logger.Debug("Ignoring the OAuth completion of a removed server instance",
				zap.String("server", serverName),
				zap.String("binding", want.String()))
			return nil
		}
		return fmt.Errorf("server not found: %s", serverName)
	}

	// New credentials let the controller reconsider a parked connection. The
	// client atomically rejects requests against an active or retired instance.
	if client.PrepareConnectionRetry(false) {
		m.wakeConnections()
	}

	return nil
}

// startOAuthEventMonitor monitors the database for OAuth completion events from CLI processes
func (m *Manager) startOAuthEventMonitor(ctx context.Context) {
	defer m.shutdownWg.Done()
	m.logger.Info("Starting OAuth event monitor for cross-process notifications")

	ticker := time.NewTicker(5 * time.Second) // Check every 5 seconds
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("OAuth event monitor stopped due to context cancellation")
			return
		case <-ticker.C:
			// Check if context was cancelled before processing
			select {
			case <-ctx.Done():
				return
			default:
			}

			if err := m.processOAuthEvents(); err != nil {
				m.logger.Warn("Failed to process OAuth events", zap.Error(err))
			}

			// Also scan for newly available tokens to handle cases where the CLI
			// could not write a DB event due to a lock. If we see a persisted
			// token for an errored OAuth server, trigger a reconnect once.
			m.scanForNewTokens()
		}
	}
}

// processOAuthEvents checks for and processes unprocessed OAuth completion events
func (m *Manager) processOAuthEvents() error {
	if m.storage == nil {
		m.logger.Debug("processOAuthEvents: no storage available, skipping")
		return nil
	}

	m.logger.Debug("processOAuthEvents: checking for OAuth completion events...")
	events, err := m.storage.GetUnprocessedOAuthCompletionEvents()
	if err != nil {
		m.logger.Error("processOAuthEvents: failed to get events", zap.Error(err))
		return fmt.Errorf("failed to get OAuth completion events: %w", err)
	}

	if len(events) == 0 {
		m.logger.Debug("processOAuthEvents: no unprocessed events found")
		return nil
	}

	m.logger.Info("processOAuthEvents: found unprocessed OAuth completion events", zap.Int("count", len(events)))

	for _, event := range events {
		m.logger.Info("Processing OAuth completion event from database",
			zap.String("server", event.ServerName),
			zap.Time("completed_at", event.CompletedAt))

		// Skip retry if client is already connected/connecting to avoid flapping
		m.mu.RLock()
		c, exists := m.clients[event.ServerName]
		m.mu.RUnlock()
		// An event a bound instance wrote names its
		// credential binding. One that is not the registered instance's
		// came from an instance a commit removed, replaced or re-bound: its
		// login says nothing about the server that holds the name now.
		stale := exists && c != nil && (event.Incarnation != 0 || event.OAuthEpoch != 0) &&
			(commitview.CredentialBinding{Incarnation: event.Incarnation, OAuthEpoch: event.OAuthEpoch}) != c.CredentialBinding()
		if stale {
			m.logger.Debug("Dropping an OAuth completion event of a superseded server instance",
				zap.String("server", event.ServerName))
		} else if exists && (c.IsConnected() || c.IsConnecting()) {
			m.logger.Info("Skipping retry for OAuth event: client already connected/connecting",
				zap.String("server", event.ServerName),
				zap.String("state", c.GetState().String()))
		} else {
			// Trigger connection retry
			if err := m.RetryConnection(event.ServerName); err != nil {
				m.logger.Warn("Failed to retry connection for OAuth completion event",
					zap.String("server", event.ServerName),
					zap.Error(err))
			} else {
				m.logger.Info("Successfully triggered connection retry for OAuth completion event",
					zap.String("server", event.ServerName))
			}
		}

		// Mark event as processed
		if err := m.storage.MarkOAuthCompletionEventProcessed(event.ServerName, event.CompletedAt); err != nil {
			m.logger.Error("Failed to mark OAuth completion event as processed",
				zap.String("server", event.ServerName),
				zap.Error(err))
		}

		// Clean up old events periodically (when processing events)
		if err := m.storage.CleanupOldOAuthCompletionEvents(); err != nil {
			m.logger.Warn("Failed to cleanup old OAuth completion events", zap.Error(err))
		}
	}

	return nil
}

// scanForNewTokens checks persistent token store for each client in Error state
// and triggers a reconnect if a token is present. This complements DB-based
// events and handles DB lock scenarios.
func (m *Manager) scanForNewTokens() {
	if m.storage == nil {
		return
	}

	m.mu.RLock()
	clients := make(map[string]*managed.Client, len(m.clients))
	for id, c := range m.clients {
		clients[id] = c
	}
	m.mu.RUnlock()

	now := time.Now()
	for id, c := range clients {
		// Get config in a thread-safe manner to avoid race conditions
		cfg := c.GetConfig()

		// Only consider enabled, HTTP/SSE servers not currently connected
		if !cfg.Enabled || c.IsConnected() {
			continue
		}

		state := c.GetState()
		// Focus on states that a freshly persisted token can unblock: a plain
		// Error (likely OAuth/authorization) and PendingAuth, where the client is
		// parked waiting for exactly this token.
		if state != types.StateError && state != types.StatePendingAuth {
			continue
		}

		// Rate-limit triggers per server
		if last, ok := m.tokenReconnect[id]; ok && now.Sub(last) < 10*time.Second {
			continue
		}

		// Check for a persisted token
		ts := oauth.NewBoundPersistentTokenStore(cfg.Name, cfg.URL, m.storage, c.CredentialBinding())
		tok, err := ts.GetToken(context.Background())
		if err != nil || tok == nil {
			continue
		}

		// Only a token we have NOT already retried with is news. The failing
		// server usually still has its (expired/revoked/rejected) token in the
		// store; triggering on mere presence would redial it every scan and
		// defeat the PendingAuth park. The fingerprint changes exactly when a
		// login/refresh writes a new token, which is the event this scan exists
		// to catch.
		//
		// The write timestamp is part of the identity, not just the token
		// content: a provider re-issuing a byte-identical token would otherwise
		// leave the fingerprint unchanged and permanently suppress this wake —
		// and for a CLI login that could not persist its completion event, this
		// scan is the only one left.
		var writtenAt time.Time
		if rec, recErr := m.storage.GetOAuthTokenBound(oauth.GenerateServerKey(cfg.Name, cfg.URL), c.CredentialBinding()); recErr == nil && rec != nil {
			writtenAt = rec.Updated
		}
		fingerprint := tokenFingerprint(tok, writtenAt)
		if seen, ok := m.tokenFingerprints[id]; ok && seen == fingerprint {
			continue
		}

		m.logger.Info("Detected new persisted OAuth token; triggering reconnect",
			zap.String("server", cfg.Name),
			zap.Time("token_expires_at", tok.ExpiresAt))

		// Remember trigger time + which token we tried, then retry connection
		m.tokenReconnect[id] = now
		m.tokenFingerprints[id] = fingerprint
		_ = m.RetryConnection(cfg.Name)
	}
}

// tokenFingerprint identifies a persisted OAuth token without retaining it: a
// truncated SHA-256 over the access token, refresh token, expiry and the time
// the record was last written, so any re-issue compares different while the
// same untouched token compares equal. Never log or persist the raw token to
// make this comparison.
func tokenFingerprint(tok *uptransport.Token, writtenAt time.Time) string {
	if tok == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		tok.AccessToken,
		tok.RefreshToken,
		tok.ExpiresAt.UTC().Format(time.RFC3339Nano),
		writtenAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// StartManualOAuth performs an in-process OAuth flow for the given server.
// This avoids cross-process DB locking by using the daemon's storage directly.
func (m *Manager) StartManualOAuth(serverName string, force bool) error {
	m.mu.RLock()
	client, exists := m.clients[serverName]
	m.mu.RUnlock()
	if !exists || client.Retired() {
		return fmt.Errorf("server not found: %s", serverName)
	}

	cfg := client.GetConfig()
	m.logger.Info("Starting in-process manual OAuth",
		zap.String("server", cfg.Name),
		zap.Bool("force", force))

	if !oauth.ShouldUseOAuth(cfg) && !force {
		m.logger.Info("OAuth not applicable based on config (no headers, protocol)", zap.String("server", cfg.Name))
		return fmt.Errorf("OAuth is not supported or not required for server '%s'", cfg.Name)
	}

	// Create a transient core client that uses the daemon's storage
	coreClient, err := core.NewClient(cfg.Name, cfg, m.logger, m.logConfig, m.globalConfig.Load(), m.storage, m.secretResolver)
	if err != nil {
		return fmt.Errorf("failed to create core client for OAuth: %w", err)
	}
	// The login runs behind the registered instance's gate, so retiring the
	// instance (removal, replacement, disable) stops it: no browser, no
	// registration, no code exchange, no token request.
	coreClient.SetGate(client.Gate())
	coreClient.SetCredentialBinding(client.CredentialBinding())

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		if force {
			coreClient.ClearOAuthState()
		}

		// Preflight no-auth check: try a quick connect without OAuth to
		// determine if authorization is actually required. If initialize
		// succeeds, inform and return early.
		if !force {
			cpy := *cfg
			cpy.Headers = cfg.Headers // preserve headers
			// Try HTTP/SSE path with no OAuth
			noAuthTransport := transport.DetermineTransportType(&cpy)
			if noAuthTransport == "http" || noAuthTransport == "streamable-http" || noAuthTransport == "sse" {
				m.logger.Info("Running preflight no-auth initialize to check OAuth requirement", zap.String("server", cfg.Name))
				testClient, err2 := core.NewClient(cfg.Name, &cpy, m.logger, m.logConfig, m.globalConfig.Load(), m.storage, m.secretResolver)
				if err2 == nil {
					testClient.SetGate(client.Gate())
					testClient.SetCredentialBinding(client.CredentialBinding())
					tctx, tcancel := context.WithTimeout(ctx, 10*time.Second)
					_ = testClient.Connect(tctx)
					tcancel()
					if testClient.GetServerInfo() != nil {
						m.logger.Info("Preflight succeeded without OAuth; skipping OAuth flow", zap.String("server", cfg.Name))
						return
					}
				}
			}
		}

		m.logger.Info("Triggering OAuth flow (in-process)", zap.String("server", cfg.Name))
		if err := coreClient.ForceOAuthFlow(ctx); err != nil {
			m.logger.Warn("In-process OAuth flow failed",
				zap.String("server", cfg.Name),
				zap.Error(err))
			return
		}
		m.logger.Info("In-process OAuth flow completed successfully",
			zap.String("server", cfg.Name))
		// Immediately attempt reconnect with new tokens
		if err := m.RetryConnection(cfg.Name); err != nil {
			m.logger.Warn("Failed to trigger reconnect after in-process OAuth",
				zap.String("server", cfg.Name),
				zap.Error(err))
		}
	}()

	return nil
}

// StartManualOAuthQuick starts OAuth and returns browser status immediately.
// Unlike StartManualOAuth (fully async, no result) or StartManualOAuthWithInfo (fully sync, blocks),
// this returns browser status quickly but continues the OAuth callback handling in background.
//
// This is the recommended method for API endpoints that need to return browser_opened status.
func (m *Manager) StartManualOAuthQuick(serverName string) (*core.OAuthStartResult, error) {
	m.mu.RLock()
	client, exists := m.clients[serverName]
	m.mu.RUnlock()
	if !exists || client.Retired() {
		return nil, fmt.Errorf("server not found: %s", serverName)
	}

	cfg := client.GetConfig()
	m.logger.Info("Starting quick OAuth flow (returns browser status immediately)",
		zap.String("server", cfg.Name))

	// Preflight: if server does not appear to require OAuth, return error
	if !oauth.ShouldUseOAuth(cfg) {
		m.logger.Info("OAuth not applicable based on config", zap.String("server", cfg.Name))
		return nil, fmt.Errorf("OAuth is not supported or not required for server '%s'", cfg.Name)
	}

	// Create a transient core client that uses the daemon's storage
	coreClient, err := core.NewClient(cfg.Name, cfg, m.logger, m.logConfig, m.globalConfig.Load(), m.storage, m.secretResolver)
	if err != nil {
		return nil, fmt.Errorf("failed to create core client for OAuth: %w", err)
	}
	// The login runs behind the registered instance's gate.
	coreClient.SetGate(client.Gate())
	coreClient.SetCredentialBinding(client.CredentialBinding())

	// Use a long-running context for the OAuth callback (30 minutes)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)

	// Clear OAuth state for fresh flow
	coreClient.ClearOAuthState()

	// Snapshot BEFORE the flow starts so a completion can never precede it
	// (the watcher below detects completion as a change from this value).
	before := m.storedAccessToken(cfg, client.CredentialBinding())

	// Start the quick OAuth flow - this returns immediately with browser status
	result, err := coreClient.StartOAuthFlowQuick(ctx)
	if client.Retired() || dialgate.IsRetired(err) {
		// Retired (removed, replaced, disabled) while the login was
		// starting. The gate
		// refused its requests and browser launch; the caller sees the
		// unavailable server, never the internal retirement error.
		cancel()
		return nil, fmt.Errorf("server not found: %s", serverName)
	}
	if err != nil {
		cancel()
		return result, err
	}

	// Set up reconnection after OAuth completes (in background).
	//
	// The watcher owns the login context: its deferred cancel ends the
	// callback wait. It therefore must only finish when THIS login is over —
	// a new token landed, or the 30-minute deadline passed — or the browser
	// callback would find nobody waiting. Completion is detected as a CHANGE
	// of the stored access token, snapshotted before the flow, so an earlier
	// sign-in's token does not count; the watcher also ends with the manager
	// so it never outlives a shutdown.
	go func() {
		defer cancel()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.shutdownCtx.Done():
				return
			case <-ticker.C:
			}
			if tok := m.storedAccessToken(cfg, client.CredentialBinding()); tok != "" && tok != before {
				m.logger.Info("OAuth token obtained, triggering reconnect",
					zap.String("server", cfg.Name))
				if err := m.RetryConnection(cfg.Name); err != nil {
					m.logger.Warn("Failed to trigger reconnect after OAuth",
						zap.String("server", cfg.Name),
						zap.Error(err))
				}
				return
			}
		}
	}()

	return result, nil
}

// storedAccessToken returns the access token persisted for the server, or ""
// when there is none (or no storage).
func (m *Manager) storedAccessToken(cfg *config.ServerConfig, b commitview.CredentialBinding) string {
	if m.storage == nil {
		return ""
	}
	record, err := m.storage.GetOAuthTokenBound(oauth.GenerateServerKey(cfg.Name, cfg.URL), b)
	if err != nil || record == nil {
		return ""
	}
	return record.AccessToken
}

// InvalidateAllToolCountCaches invalidates tool count caches for all clients
// This should be called when tools are known to have changed (e.g., after indexing)
func (m *Manager) InvalidateAllToolCountCaches() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, client := range m.clients {
		client.InvalidateToolCountCache()
	}

	m.logger.Debug("Invalidated tool count caches for all clients",
		zap.Int("client_count", len(m.clients)))
}

// ClearOAuthToken clears the OAuth token for a specific server.
// This is called by the OAuth logout flow to remove stored credentials.
// Works for both explicit OAuth config and discovered OAuth (server-announced OAuth).
func (m *Manager) ClearOAuthToken(serverName string) error {
	m.mu.RLock()
	client, exists := m.clients[serverName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("server not found: %s", serverName)
	}

	// Get the server config for logging (but don't require explicit OAuth config)
	serverConfig := client.GetConfig()
	hasExplicitOAuth := serverConfig.OAuthMode() == config.OAuthModeOn

	// Clear token from persistent storage
	// This works for both explicit OAuth config and discovered OAuth (server-announced)
	if m.storageMgr != nil {
		if err := m.storageMgr.ClearOAuthState(serverName); err != nil {
			m.logger.Warn("Failed to clear OAuth state from storage",
				zap.String("server", serverName),
				zap.Bool("has_explicit_oauth", hasExplicitOAuth),
				zap.Error(err))
			// Continue - storage might not have the token
		}
	}
	// A logout clears the credential whatever binding wrote it (an operator
	// action on the current state): drop the refresh coordinator's latch,
	// cooldown and re-registration state of every binding of the key, so the
	// next sign-in starts clean.
	if serverConfig != nil {
		oauth.DefaultRefreshCoordinator().ForgetServer(oauth.GenerateServerKey(serverName, serverConfig.URL))
	}

	// Note: We don't need to clear in-memory OAuth state on the managed client
	// because the managed client will pick up the cleared state from storage
	// on the next connection attempt via DisconnectServer + reconnection flow.

	m.logger.Info("OAuth token cleared",
		zap.String("server", serverName),
		zap.Bool("has_explicit_oauth", hasExplicitOAuth))

	return nil
}

// SetUserLoggedOut marks a server as explicitly logged out by the user.
// This prevents automatic reconnection until the user explicitly logs in again.
func (m *Manager) SetUserLoggedOut(serverName string, loggedOut bool) error {
	m.mu.RLock()
	client, exists := m.clients[serverName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("server not found: %s", serverName)
	}

	client.SetUserLoggedOut(loggedOut)

	m.logger.Info("Server user logged out state changed",
		zap.String("server", serverName),
		zap.Bool("logged_out", loggedOut))

	return nil
}

// ErrNoActiveInstance means the manager has no live client for a server: it
// was removed, or its instance is still being installed or replaced. Callers
// that know the configuration tell those apart.
var ErrNoActiveInstance = errors.New("server not found")

// RefreshOAuthToken triggers a token refresh for a specific server.
// This is called by the RefreshManager for proactive token refresh.
//
// OAuth detection checks both:
// 1. Static config (serverConfig.OAuth) - for servers with pre-configured OAuth
// 2. Database tokens - for servers using dynamic OAuth discovery (Protected Resource Metadata)
func (m *Manager) RefreshOAuthToken(serverName string) error {
	m.mu.RLock()
	client, exists := m.clients[serverName]
	m.mu.RUnlock()

	// An unregistered or retired instance runs no
	// refresh; the gate refuses its token request in any case.
	if !exists || client.Retired() {
		return fmt.Errorf("%w: %s", ErrNoActiveInstance, serverName)
	}

	// Check if server uses OAuth via either static config or dynamic discovery
	serverConfig := client.GetConfig()
	if serverConfig.OAuthMode() == config.OAuthModeOff {
		return fmt.Errorf("OAuth is off for %s", serverName)
	}
	hasStaticOAuth := serverConfig.OAuthMode() == config.OAuthModeOn

	// Also check for OAuth tokens in the database (dynamic OAuth discovery)
	// Servers like atlassian-remote, slack discover OAuth requirements at runtime
	// via Protected Resource Metadata and store tokens without OAuth in static config
	hasStoredTokens := false
	if m.storage != nil && serverConfig != nil {
		// IMPORTANT: Use GenerateServerKey to match how PersistentTokenStore stores tokens.
		// Tokens are stored with key = SHA256(serverName|serverURL)[:16], not just serverName.
		serverKey := oauth.GenerateServerKey(serverName, serverConfig.URL)
		token, err := m.storage.GetOAuthTokenBound(serverKey, client.CredentialBinding())
		if err == nil && token != nil && token.RefreshToken != "" {
			hasStoredTokens = true
			m.logger.Debug("Found OAuth token in database for dynamic OAuth server",
				zap.String("server", serverName),
				zap.String("server_key", serverKey),
				zap.Bool("has_refresh_token", token.RefreshToken != ""))
		}
	}

	if !hasStaticOAuth && !hasStoredTokens {
		return fmt.Errorf("server does not use OAuth: %s", serverName)
	}

	// Refresh tokens without tearing down a healthy connection.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := client.RefreshOAuthTokenDirect(ctx)
	if err != nil && client.Retired() {
		// Retired while the refresh was on its way: nothing to reconnect.
		return fmt.Errorf("%w: %s", ErrNoActiveInstance, serverName)
	}
	if err != nil {
		m.logger.Warn("Direct OAuth refresh failed, falling back to reconnect",
			zap.String("server", serverName),
			zap.Error(err))
		// Fall back to reconnect for cases where direct refresh isn't available
		// (e.g., server not connected, no OAuth handler)
		_ = m.RetryConnection(serverName)
		return err
	}

	m.logger.Info("OAuth token refresh completed successfully",
		zap.String("server", serverName),
		zap.Bool("has_static_oauth", hasStaticOAuth),
		zap.Bool("has_stored_tokens", hasStoredTokens))

	return nil
}

// GetServerIcon reads cached upstream artwork without accessing transport locks.
func (m *Manager) GetServerIcon(name string) string {
	if client, ok := m.GetClient(name); ok {
		return client.GetServerIcon()
	}
	return ""
}

func (m *Manager) SetPeerExecutor(executor peering.Executor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peerExecutor = executor
	for _, client := range m.clients {
		client.SetPeerExecutor(executor)
	}
}
