package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"strings"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/diagnostics"
	"github.com/slopster67/mcpproxy-go/internal/diagnostics/hints"
	"github.com/slopster67/mcpproxy-go/internal/runtime/configsvc"
	"github.com/slopster67/mcpproxy-go/internal/runtime/stateview"
	transportpkg "github.com/slopster67/mcpproxy-go/internal/transport"
	"github.com/slopster67/mcpproxy-go/internal/upstream"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
	"github.com/slopster67/mcpproxy-go/internal/upstream/types"
)

// classifyAndAttach converts a raw connection error into a DiagnosticError and
// stores it on the server status. Called from the supervisor's reconcile and
// event paths.
func classifyAndAttach(status *stateview.ServerStatus, err error, hints diagnostics.ClassifierHints) {
	if err == nil {
		status.Diagnostic = nil
		return
	}
	code := diagnostics.Classify(err, hints)
	if code == "" {
		// Classify always returns at least UnknownUnclassified for non-nil err,
		// so reaching here means a logic error. Defensive fallback keeps
		// the UI useful instead of silently dropping the signal.
		code = diagnostics.UnknownUnclassified
	}
	entry, _ := diagnostics.Get(code)
	msg := err.Error()
	const maxCause = 256
	if len(msg) > maxCause {
		msg = msg[:maxCause] + "..."
	}
	status.Diagnostic = &diagnostics.DiagnosticError{
		Code:       code,
		Severity:   entry.Severity,
		Cause:      msg,
		ServerID:   status.Name,
		DetectedAt: time.Now(),
	}
}

// applyRetryStopped projects a connection's permanent-park state onto the read
// model. Both stateview writers (the reconcile sweep and the event fast path)
// call it, because RetryCount is written by both and a status that carried a
// stale RetryStopped next to a fresh RetryCount would tell the user the opposite
// of the truth.
func applyRetryStopped(status *stateview.ServerStatus, ci *types.ConnectionInfo) {
	if ci == nil || !ci.Terminal || ci.RetryCount < types.PermanentFailureAttempts {
		status.RetryStopped = false
		status.RetryStoppedCode = ""
		status.RetryStoppedReason = ""
		return
	}
	status.RetryStopped = true
	status.RetryStoppedCode = ci.TerminalCode
	status.RetryStoppedReason = terminalReason(ci)
}

// terminalReason renders the user-facing cause of a permanent park. It prefers
// the diagnostics catalog message for the code that justified stopping — that is
// the text written to tell a user how to fix exactly this — and falls back to
// the raw error so the reason is never empty.
func terminalReason(ci *types.ConnectionInfo) string {
	if ci == nil {
		return ""
	}
	raw := ""
	if ci.LastError != nil {
		raw = ci.LastError.Error()
	}
	return diagnostics.PermanentFailureReason(diagnostics.Code(ci.TerminalCode), raw)
}

func (s *Supervisor) classifierHints(srv *config.ServerConfig, transport string) diagnostics.ClassifierHints {
	var global *config.Config
	if snap := s.configSvc.Current(); snap != nil {
		global = snap.Config
	}
	return hints.For(global, srv, transport)
}

// Supervisor manages the desired vs actual state reconciliation for upstream servers.
// It subscribes to config changes and emits events when server states change.
type Supervisor struct {
	logger *zap.Logger
	wake   chan struct{}

	// Config service for desired state
	configSvc *configsvc.Service

	// Upstream adapter for actual state
	upstream UpstreamInterface

	// State tracking
	snapshot atomic.Value // *ServerStateSnapshot
	version  int64
	stateMu  sync.RWMutex

	// toolTierGeneration is bumped once per publishDiscoveredTools call: any tool's effective annotations may have changed, or a
	// tool may have appeared/disappeared, whenever a server's discovered
	// tool set is republished. It is a deliberately coarse, whole-fleet
	// counter — bumped on every discovery publish, not only one that
	// actually changed a tier — rather than a diff against the previous
	// tool set: the safe direction for a cache-invalidation signal is to
	// over-invalidate (an extra read_cache miss) rather than under-invalidate
	// (a stale cached page surviving a real annotation change). Read via
	// ToolTierGeneration(); a caller stamps it on a cache entry's producer
	// authorization (internal/server/cache_authz.go) alongside the profile's
	// own PolicyFingerprint.
	toolTierGeneration atomic.Uint64

	// State view for read model
	stateView *stateview.View

	// Event publishing
	eventCh   chan Event
	listeners []chan Event
	eventMu   sync.RWMutex

	// Callback for reactive tool discovery on server connection
	onServerConnectedCallback func(serverName string)
	callbackMu                sync.RWMutex

	// errorCodeNotifier is an optional hook called whenever a DiagnosticError
	// is classified and attached to a server status. Used by the telemetry
	// layer to increment diagnostics counters. The argument
	// is a stable MCPX_* code string. Safe to leave nil; calls are no-ops.
	errorCodeNotifier func(code string)

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// actionWg tracks in-flight reconcile action goroutines (Connect/Disconnect/
	// Reconnect/Remove). Stop() drains it BEFORE disconnecting upstream clients so
	// a Connect can never overlap a Disconnect on the same client. stopping (guarded by stateMu) gates dispatch
	// so no new action is added once Stop() begins — preventing a WaitGroup
	// Add-after-Wait.
	actionWg sync.WaitGroup
	stopping bool
}

// UpstreamInterface defines the interface for upstream adapters.
//
// Every registry change and connect is bound to the
// commit generation of the snapshot the action was planned from, and a
// disconnect to the exact instance it observed. The adapter returns
// upstream.ErrStaleGeneration, and does nothing, when a newer commit (or a
// same-generation replacement) superseded the action.
type UpstreamInterface interface {
	// AddServerAt installs (or keeps) the instance serving cfg at gen.
	AddServerAt(name string, cfg *config.ServerConfig, gen int64) error
	// RemoveServerAt removes the instance; gen 0 removes a server the
	// committed configuration no longer has.
	RemoveServerAt(name string, gen int64) error
	// ConnectServerAt connects the instance serving name at gen.
	ConnectServerAt(ctx context.Context, name string, gen int64) error
	// DisconnectServerAt disconnects exactly instance inst serving name at gen.
	DisconnectServerAt(name string, gen int64, inst managed.InstanceID) error
	GetServerState(name string) (*ServerState, error)
	GetAllStates() map[string]*ServerState
	IsUserLoggedOut(name string) bool // Returns true if user explicitly logged out (prevents auto-reconnect)
	Subscribe() <-chan Event
	Unsubscribe(ch <-chan Event)
	Close()
}

// New creates a new supervisor.
func New(configSvc *configsvc.Service, upstream UpstreamInterface, logger *zap.Logger) *Supervisor {
	if logger == nil {
		logger = zap.NewNop()
	}

	ctx, cancel := context.WithCancel(context.Background())

	s := &Supervisor{
		logger:    logger,
		wake:      make(chan struct{}, 1),
		configSvc: configSvc,
		upstream:  upstream,
		version:   0,
		stateView: stateview.New(),
		eventCh:   make(chan Event, 500), // Buffered for async operations
		listeners: make([]chan Event, 0),
		ctx:       ctx,
		cancel:    cancel,
	}

	// Initialize empty snapshot
	s.snapshot.Store(&ServerStateSnapshot{
		Servers:   make(map[string]*ServerState),
		Timestamp: time.Now(),
		Version:   0,
	})

	return s
}

// Wake asks the connection controller to reconsider current state. It never
// dials on the notifying goroutine and coalesces bursts of equivalent events.
func (s *Supervisor) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Start begins the supervisor's reconciliation loop.
func (s *Supervisor) Start() {
	s.logger.Info("Starting supervisor")

	// Subscribe to config changes
	configUpdates := s.configSvc.Subscribe(s.ctx)

	// Subscribe to upstream events
	upstreamEvents := s.upstream.Subscribe()

	// Start event forwarding goroutine
	s.wg.Add(1)
	go s.forwardUpstreamEvents(upstreamEvents)

	// Start reconciliation loop
	s.wg.Add(1)
	go s.reconciliationLoop(configUpdates)

	// Trigger initial reconciliation to populate StateView.
	// Registered in s.wg with a ctx-aware timer: Stop() cancels s.ctx and then waits on s.wg, so it
	// either cancels this goroutine inside the 500ms warm-up window or waits
	// for the reconcile to finish. A bare time.Sleep goroutine here could wake
	// AFTER Stop() returned and write last_error_code/diagnostics via
	// reconcile()/updateStateView()/notifyErrorCode() — after the clean-
	// shutdown marker resolved, or against a closed DB.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(500 * time.Millisecond) // Give servers time to connect
		defer timer.Stop()
		select {
		case <-s.ctx.Done():
			s.logger.Debug("Supervisor stopping before initial reconciliation")
			return
		case <-timer.C:
		}
		currentConfig := s.configSvc.Current()
		if err := s.reconcile(currentConfig); err != nil {
			s.logger.Error("Initial reconciliation failed", zap.Error(err))
		} else {
			s.logger.Info("Initial reconciliation completed, StateView populated")
		}
	}()

	s.logger.Info("Supervisor started")
}

// reconciliationLoop processes config updates and reconciles state.
func (s *Supervisor) reconciliationLoop(configUpdates <-chan configsvc.Update) {
	defer s.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			s.logger.Info("Supervisor reconciliation loop stopping")
			return

		case update, ok := <-configUpdates:
			if !ok {
				s.logger.Warn("Config updates channel closed")
				return
			}

			s.logger.Info("Config update received, reconciling",
				zap.String("type", string(update.Type)),
				zap.Int64("version", update.Snapshot.Version))

			if err := s.reconcile(update.Snapshot); err != nil {
				s.logger.Error("Reconciliation failed", zap.Error(err))
				s.emitEvent(Event{
					Type:      EventReconciliationFailed,
					Timestamp: time.Now(),
					Payload: map[string]interface{}{
						"error":   err.Error(),
						"version": update.Snapshot.Version,
					},
				})
			} else {
				s.emitEvent(Event{
					Type:      EventReconciliationComplete,
					Timestamp: time.Now(),
					Payload: map[string]interface{}{
						"version": update.Snapshot.Version,
					},
				})
			}

		case <-s.wake:
			if err := s.reconcile(s.configSvc.Current()); err != nil {
				s.logger.Error("Connection reconciliation failed", zap.Error(err))
			}

		case <-ticker.C:
			// Periodic reconciliation to handle drift
			s.logger.Debug("Periodic reconciliation check")
			currentConfig := s.configSvc.Current()
			if err := s.reconcile(currentConfig); err != nil {
				s.logger.Error("Periodic reconciliation failed", zap.Error(err))
			}
		}
	}
}

// reconcile compares desired vs actual state and takes corrective actions.
// Actions run asynchronously so reconcile never blocks HTTP server startup.
func (s *Supervisor) reconcile(configSnapshot *configsvc.Snapshot) error {
	// IMPORTANT: Fetch ALL manager-dependent data BEFORE acquiring stateMu.Lock().
	// GetAllStates() and IsUserLoggedOut() call manager.GetClient() which needs manager.mu.RLock().
	// If we held stateMu.Lock() first while a concurrent goroutine holds manager.mu.Lock()
	// (e.g., during AddServerConfig), the event processing channel fills up and events are dropped,
	// causing the stateview to permanently show stale "Connecting..." status.
	actualStates := s.upstream.GetAllStates()

	// Pre-fetch user logout status for all servers
	userLoggedOut := make(map[string]bool)
	for _, srv := range configSnapshot.Config.Servers {
		if srv != nil {
			userLoggedOut[srv.Name] = s.upstream.IsUserLoggedOut(srv.Name)
		}
	}

	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	s.logger.Debug("Starting reconciliation",
		zap.Int("desired_servers", configSnapshot.ServerCount()),
		zap.Int("actual_states", len(actualStates)))

	plan := s.computeReconcilePlan(configSnapshot, actualStates, userLoggedOut)

	// Once Stop() has begun (stopping set under stateMu), do not dispatch
	// new action goroutines. This keeps all actionWg.Add calls strictly ordered
	// before Stop()'s actionWg.Wait (no Add-after-Wait) and guarantees no Connect
	// can start after we begin draining for disconnect.
	if s.stopping {
		s.logger.Debug("Supervisor stopping, skipping reconcile action dispatch")
		s.updateSnapshot(configSnapshot, actualStates)
		return nil
	}

	// Each action runs asynchronously in its own goroutine with timeout.
	actionCount := 0
	for serverName, action := range plan.Actions {
		if action == ActionNone {
			continue // Skip no-op actions
		}

		actionCount++
		s.logger.Debug("Dispatching reconcile action",
			zap.String("server", serverName),
			zap.String("action", string(action)))

		// Launch each action in a goroutine. Tracked by actionWg (Add under stateMu,
		// before the goroutine starts) so Stop() can drain in-flight actions before
		// disconnecting clients.
		s.actionWg.Add(1)
		// Bind the action to what it was planned from: the
		// server's commit generation in this snapshot and, for an action on
		// the live instance, that instance.
		planned := plannedAction{Action: action}
		if sg, ok := configSnapshot.ServerGeneration(serverName); ok {
			planned.Gen = sg.Gen
		}
		if actual, ok := actualStates[serverName]; ok && actual != nil {
			planned.Instance = actual.InstanceID
		}
		go func(name string, act plannedAction, snapshot *configsvc.Snapshot) {
			defer s.actionWg.Done()
			if err := s.executeAction(name, act, snapshot); err != nil {
				s.logger.Error("Failed to execute action",
					zap.String("server", name),
					zap.String("action", string(act.Action)),
					zap.Error(err))
			} else {
				s.logger.Debug("Action completed successfully",
					zap.String("server", name),
					zap.String("action", string(act.Action)))
			}
		}(serverName, planned, configSnapshot)
	}

	// Update state snapshot immediately using actual states from manager
	s.updateSnapshot(configSnapshot, actualStates)

	s.logger.Debug("Reconciliation dispatched",
		zap.Int("actions_dispatched", actionCount),
		zap.String("note", "actions running asynchronously"))

	return nil
}

// computeReconcilePlan determines what actions need to be taken.
// actualStates and userLoggedOut are pre-fetched OUTSIDE stateMu.Lock() to prevent lock ordering issues.
func (s *Supervisor) computeReconcilePlan(configSnapshot *configsvc.Snapshot, actualStates map[string]*ServerState, userLoggedOut map[string]bool) *ReconcilePlan {
	plan := &ReconcilePlan{
		Actions:   make(map[string]ReconcileAction),
		Timestamp: time.Now(),
		Reason:    "config_update",
	}

	currentSnapshot := s.CurrentSnapshot()
	desiredServers := configSnapshot.Config.Servers

	// Check for servers that need to be added or updated
	for _, desiredServer := range desiredServers {
		if desiredServer == nil {
			continue
		}

		name := desiredServer.Name
		currentState, exists := currentSnapshot.Servers[name]

		// Use actual connection state from manager (more reliable than snapshot which depends on events)
		actuallyConnected := false
		if actual, ok := actualStates[name]; ok {
			actuallyConnected = actual.Connected
		}

		if !exists {
			// New server needs to be added
			if desiredServer.Enabled {
				plan.Actions[name] = ActionConnect
			} else {
				plan.Actions[name] = ActionNone
			}
		} else {
			// Existing server - check if config changed
			if s.configChanged(currentState.Config, desiredServer) {
				plan.Actions[name] = ActionReconnect
			} else if desiredServer.Enabled && !actuallyConnected {
				// Should be connected but isn't (or has inspection exemption)
				// BUT: Don't auto-reconnect if user explicitly logged out
				if userLoggedOut[name] {
					plan.Actions[name] = ActionNone
				} else if actual, ok := actualStates[name]; ok && !actual.ConnectionInfo.ShouldAutoReconnect(time.Now()) {
					// Respect the client's retry policy: exponential backoff after
					// consecutive failures, the coarse OAuth ladder, half-hourly
					// probes once the client gave up, and PendingAuth servers
					// parked waiting on user OAuth login. Without this gate the
					// periodic 30s reconciliation re-dials a dead upstream forever,
					// hammering the remote server (~3 requests per tick).
					s.logger.Debug("Skipping auto-reconnect (backoff/pending-auth/permanent)",
						zap.String("server", name),
						zap.String("state", actual.ConnectionInfo.State.String()),
						zap.Int("retry_count", actual.ConnectionInfo.RetryCount),
						zap.Bool("terminal", actual.ConnectionInfo.Terminal),
						zap.String("terminal_code", actual.ConnectionInfo.TerminalCode))
					plan.Actions[name] = ActionNone
				} else {
					plan.Actions[name] = ActionConnect
				}
			} else if (!desiredServer.Enabled) && actuallyConnected {
				// Shouldn't be connected but is (or exemption expired)
				plan.Actions[name] = ActionDisconnect
			} else {
				plan.Actions[name] = ActionNone
			}
		}
	}

	// Check for servers that need to be removed
	desiredNames := make(map[string]bool)
	for _, srv := range desiredServers {
		if srv != nil {
			desiredNames[srv.Name] = true
		}
	}

	for name := range currentSnapshot.Servers {
		if !desiredNames[name] {
			plan.Actions[name] = ActionRemove
		}
	}

	return plan
}

// configChanged checks if a server's connection configuration has changed, which
// makes the reconcile plan ActionReconnect.
//
// ActionReconnect is computed BEFORE the auto-reconnect backoff gate, so it is
// the documented bypass: a user-driven config change for this server reconnects
// it whatever the ladder says. That bypass is also the ONLY way a permanently
// parked server comes back on its own, so the comparison sees every connection
// field — including `args` and `isolation.image`, the most likely causes of a
// permanent failure.
func (s *Supervisor) configChanged(old, new *config.ServerConfig) bool {
	return !config.ConnectionEquivalent(old, new)
}

// plannedAction is a reconcile action bound to what it was planned from:
// Gen is the server's commit generation in the planning
// snapshot (0 when the snapshot carries none, or the server is absent), and
// Instance the live manager instance the plan observed (0 when none).
type plannedAction struct {
	Action   ReconcileAction
	Gen      int64
	Instance managed.InstanceID
}

// executeAction performs the specified action on a server. Every manager
// call is bound to the planned generation (and instance), so an action a
// newer commit superseded while it waited is a no-op: the adapter answers
// upstream.ErrStaleGeneration, logged at debug level.
func (s *Supervisor) executeAction(serverName string, planned plannedAction, configSnapshot *configsvc.Snapshot) error {
	action := planned.Action
	s.logger.Debug("Executing action",
		zap.String("server", serverName),
		zap.String("action", string(action)),
		zap.Int64("generation", planned.Gen))

	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	stale := func(err error) bool {
		if !errors.Is(err, upstream.ErrStaleGeneration) {
			return false
		}
		s.logger.Debug("Supervisor action superseded by a newer config commit or instance; skipping",
			zap.String("server", serverName),
			zap.String("action", string(action)),
			zap.Int64("generation", planned.Gen),
			zap.Uint64("instance", uint64(planned.Instance)))
		return true
	}

	switch action {
	case ActionNone:
		// No action needed
		return nil

	case ActionConnect, ActionReconnect:
		// ActionReconnect has deliberately no separate disconnect leg: the
		// commit that changed the connection settings already unregistered
		// the old instance (AdvanceGenerations, commit step 7), and
		// AddServerAt installs the instance for the planned generation only
		// while that generation is current. A by-name disconnect here would
		// tear down the fresh instance the commit's worker is connecting.
		serverConfig := configSnapshot.GetServer(serverName)
		if serverConfig == nil {
			return fmt.Errorf("server config not found: %s", serverName)
		}

		if err := s.upstream.AddServerAt(serverName, serverConfig, planned.Gen); err != nil {
			if stale(err) {
				return nil
			}
			return fmt.Errorf("failed to add server: %w", err)
		}

		// Enabled servers connect; individual tools are gated by their approvals.
		if serverConfig.Enabled {

			if err := s.upstream.ConnectServerAt(ctx, serverName, planned.Gen); err != nil && !stale(err) {
				s.logger.Warn("Failed to connect server (will retry)",
					zap.String("server", serverName),
					zap.Error(err))
				// Don't return error - managed client will retry
			}
		}

		return nil

	case ActionDisconnect:
		if err := s.upstream.DisconnectServerAt(serverName, planned.Gen, planned.Instance); err != nil {
			if stale(err) {
				return nil
			}
			return err
		}
		return nil

	case ActionRemove:
		// The commit that removed the server already unregistered its
		// instance; this only clears what is left, and only while the
		// committed configuration still lacks the name.
		if err := s.upstream.RemoveServerAt(serverName, 0); err != nil {
			if stale(err) {
				return nil
			}
			return err
		}
		return nil

	default:
		return fmt.Errorf("unknown action: %s", action)
	}
}

// updateSnapshot updates the current state snapshot.
// actualStates is pre-fetched from the manager OUTSIDE stateMu.Lock() to prevent lock ordering issues
// and to ensure we always have the latest connection state (not dependent on event delivery).
func (s *Supervisor) updateSnapshot(configSnapshot *configsvc.Snapshot, actualStates map[string]*ServerState) {
	s.version++

	// Get existing snapshot for data that's only available from events (tools, last seen, etc.)
	currentSnapshot := s.CurrentSnapshot()
	existingStates := make(map[string]*ServerState)
	if currentSnapshot != nil {
		for name, state := range currentSnapshot.Servers {
			existingStates[name] = state
		}
	}

	// Merge desired and actual state
	newSnapshot := &ServerStateSnapshot{
		Servers:   make(map[string]*ServerState),
		Timestamp: time.Now(),
		Version:   s.version,
	}
	// Servers whose live connection token moved unobserved;
	// their reactive discovery is kicked once the snapshot is stored.
	var rediscover []string

	// Add all configured servers
	for _, srv := range configSnapshot.Config.Servers {
		if srv == nil {
			continue
		}

		state := &ServerState{
			Name:    srv.Name,
			Config:  srv,
			Enabled: srv.Enabled,

			DesiredVersion: configSnapshot.Version,
			LastReconcile:  time.Now(),
		}
		if g, ok := configSnapshot.ServerGeneration(srv.Name); ok {
			state.Incarnation = g.Incarnation
		}

		// Use ACTUAL state from manager (authoritative, not dependent on events)
		if actual, ok := actualStates[srv.Name]; ok {
			state.Connected = actual.Connected
			state.ConnectionInfo = actual.ConnectionInfo
			state.ToolCount = actual.ToolCount
			state.ConnectionEpoch = actual.ConnectionEpoch
		}

		// Merge with existing snapshot for data only available from events.
		//
		// The retained state is keyed
		// by NAME, and a same-name replacement is a different server. Its
		// incarnation differs from the retained one, so nothing discovered
		// for the removed server (tools, discovery stamp) is carried into
		// it; the connection generation moves, so a discovery result
		// captured before the replacement is dropped at publish time, and a
		// connected replacement's discovery is kicked again (its own result
		// may have been captured under the old generation). Incarnation zero
		// means the snapshot carried no generations (the snapshot the config
		// service was constructed with): that is the same server, not a
		// replacement.
		existing, ok := existingStates[srv.Name]
		if ok && existing.Incarnation != 0 && state.Incarnation != 0 && existing.Incarnation != state.Incarnation {
			state.ConnectionGeneration = existing.ConnectionGeneration + 1
			ok = false
			if state.Connected {
				rediscover = append(rediscover, srv.Name)
			}
		}
		if ok {
			state.LastSeen = existing.LastSeen
			state.Tools = existing.Tools // Tools come from background discovery
			state.ConnectionGeneration = existing.ConnectionGeneration
			// Reconcile is authoritative for Connected, so it is authoritative
			// for the per-connection discovery marker too: a
			// disconnect the manager reports but whose event was dropped
			// (actor_pool.emitEvent drops on a full channel) must not carry
			// the previous connection's stamp — or its generation — forward.
			state.ToolsDiscovered = existing.ToolsDiscovered && state.Connected
			state.DiscoveryEpoch = existing.DiscoveryEpoch
			if existing.Connected && !state.Connected {
				state.ConnectionGeneration++
			}
			// The live client's connection token is the edge detector the
			// events are not: a disconnect+reconnect whose BOTH
			// events were dropped leaves the manager reporting "connected"
			// on both observations, so the marker, tools and generation of
			// the previous connection would survive until the next sweep
			// (default 5 min). A token that moved since the last observation
			// is a missed edge, and a stamp captured under a token other
			// than the live one is the previous connection's: clear the
			// marker, move the generation (a result captured on the old
			// connection is dropped at publish time) and kick the reactive
			// discovery the dropped connect event would have kicked.
			if state.Connected && state.ConnectionEpoch != 0 {
				missedEdge := existing.ConnectionEpoch != 0 && existing.ConnectionEpoch != state.ConnectionEpoch
				staleStamp := state.ToolsDiscovered && state.DiscoveryEpoch != state.ConnectionEpoch
				if missedEdge || staleStamp {
					s.logger.Info("Reconcile observed a connection the events did not report; invalidating the server's discovery stamp and re-listing",
						zap.String("server", srv.Name),
						zap.Int64("observed_epoch", existing.ConnectionEpoch),
						zap.Int64("discovery_epoch", state.DiscoveryEpoch),
						zap.Int64("live_epoch", state.ConnectionEpoch))
					state.ToolsDiscovered = false
					state.ConnectionGeneration++
					rediscover = append(rediscover, srv.Name)
				}
			}
			if !state.ToolsDiscovered {
				state.DiscoveryEpoch = 0
			}
			// If actual state didn't have tool count but existing does, keep it
			if state.ToolCount == 0 && existing.ToolCount > 0 {
				state.ToolCount = existing.ToolCount
			}
		}

		newSnapshot.Servers[srv.Name] = state

		// Update stateview
		s.updateStateView(srv.Name, state)
	}

	s.snapshot.Store(newSnapshot)

	if len(rediscover) > 0 {
		s.callbackMu.RLock()
		callback := s.onServerConnectedCallback
		s.callbackMu.RUnlock()
		if callback != nil {
			for _, name := range rediscover {
				// Asynchronous, exactly as the connect event dispatches it:
				// the caller holds stateMu.
				go callback(name)
			}
		}
	}

	// Remove servers from stateview that are no longer in config
	currentView := s.stateView.Snapshot()
	for name := range currentView.Servers {
		if _, exists := newSnapshot.Servers[name]; !exists {
			s.stateView.RemoveServer(name)
		}
	}
}

// updateStateView updates the stateview with current server state.
func (s *Supervisor) updateStateView(name string, state *ServerState) {
	var classifiedCode string
	s.stateView.UpdateServer(name, func(status *stateview.ServerStatus) {
		// Edge-trigger basis. View.UpdateServer deep-clones the snapshot and
		// hands us the PREVIOUS status, so these two reads (taken before any
		// mutation below) are the state as of the last pass. See
		// shouldNotifyErrorCode for why they gate the telemetry notification.
		prevCode, prevRetryCount, prevErrorTime := errorCodeEdgeBasis(status)

		oldState := status.State
		status.Config = state.Config
		status.Enabled = state.Enabled

		status.Connected = state.Connected
		status.ToolCount = state.ToolCount
		status.Incarnation = state.Incarnation

		// Convert ToolMetadata to ToolInfo and cache in StateView
		status.Tools = toolInfosFromMetadata(state.Tools)
		status.ToolsDiscovered = state.ToolsDiscovered
		status.DiscoveryEpoch = state.DiscoveryEpoch

		// Map connection state to string
		// Use detailed state from ConnectionInfo when available to avoid mislabeling disconnected servers as "connecting"
		if state.ConnectionInfo != nil {
			status.State = strings.ToLower(state.ConnectionInfo.State.String())
		} else if state.Connected {
			status.State = "connected"
		} else if state.Enabled {
			status.State = "connecting"
		} else if state.Enabled {
			status.State = "disconnected"
		} else {
			status.State = "idle"
		}

		// Update connection time if connected
		if state.Connected && !state.LastSeen.IsZero() {
			t := state.LastSeen
			status.ConnectedAt = &t
		}

		// CRITICAL: Clear error when connected, even if ConnectionInfo is unavailable
		// This ensures stale OAuth/connection errors don't persist after successful reconnection
		if state.Connected {
			status.LastError = ""
			status.LastErrorTime = nil
			status.Diagnostic = nil
			applyRetryStopped(status, nil)
		}

		// Update connection info if available
		if state.ConnectionInfo != nil {
			// Extract LastError from ConnectionInfo and convert to string with limit
			if state.ConnectionInfo.LastError != nil {
				errorStr := state.ConnectionInfo.LastError.Error()
				// Limit error string to 500 characters to prevent UI hangs
				const maxErrorLen = 500
				if len(errorStr) > maxErrorLen {
					errorStr = errorStr[:maxErrorLen] + "... (truncated)"
				}
				status.LastError = errorStr

				// Classify raw error into stable diagnostic code.
				// Use the RESOLVED transport, not the raw Config.Protocol: an
				// auto-detected stdio server has Protocol=="" + Command!="" and
				// would otherwise miss the stdio-gated classifier rules.
				transport := ""
				if state.Config != nil {
					transport = transportpkg.DetermineTransportType(state.Config)
				}
				classifyAndAttach(status, state.ConnectionInfo.LastError, s.classifierHints(state.Config, transport))
				// Capture the classified code; delivered synchronously after
				// the stateview lock is released (see notifyErrorCode). Only
				// on an EDGE: reconcile() runs this for every configured
				// server every 30s and ConnectionInfo.LastError is sticky (see
				// shouldNotifyErrorCode).
				if status.Diagnostic != nil {
					code := string(status.Diagnostic.Code)
					if shouldNotifyErrorCode(prevCode, code, prevRetryCount, state.ConnectionInfo.RetryCount, prevErrorTime, state.ConnectionInfo.LastRetryTime) {
						classifiedCode = code
					}
				}

				// Set last error time if available
				if !state.ConnectionInfo.LastRetryTime.IsZero() {
					t := state.ConnectionInfo.LastRetryTime
					status.LastErrorTime = &t
				}
			}
			// Note: error already cleared above if connected=true
			// Only set error from ConnectionInfo if it has one

			// Copy retry count
			status.RetryCount = state.ConnectionInfo.RetryCount

			// Surface a permanently parked server explicitly. Without
			// this it is indistinguishable from a server still working through
			// its backoff, and the user waits for a retry that never comes.
			applyRetryStopped(status, state.ConnectionInfo)

			// Store full connection info in metadata for debugging
			if status.Metadata == nil {
				status.Metadata = make(map[string]interface{})
			}
			status.Metadata["connection_info"] = state.ConnectionInfo
		}

		// Debug: log state transitions during reconcile
		if oldState != status.State {
			s.logger.Debug("🔄 StateView updated via reconcile",
				zap.String("server", name),
				zap.String("old_state", oldState),
				zap.String("new_state", status.State),
				zap.Bool("connected", state.Connected),
				zap.Bool("has_conn_info", state.ConnectionInfo != nil))
		}
	})
	s.notifyErrorCode(classifiedCode)
}

// errorCodeEdgeBasis extracts the previous diagnostic code and retry count
// from a ServerStatus. It must be called at the very top of a
// View.UpdateServer callback, before any mutation: View.UpdateServer clones
// the snapshot and passes the PREVIOUS status in, so these are last-pass
// values only until the callback starts writing.
func errorCodeEdgeBasis(status *stateview.ServerStatus) (code string, retryCount int, errorTime time.Time) {
	if status == nil {
		return "", 0, time.Time{}
	}
	if status.Diagnostic != nil {
		code = string(status.Diagnostic.Code)
	}
	if status.LastErrorTime != nil {
		errorTime = *status.LastErrorTime
	}
	return code, status.RetryCount, errorTime
}

// shouldNotifyErrorCode reports whether a freshly classified diagnostic is a
// new EVENT rather than the same standing condition re-observed.
//
// reconcile() calls updateStateView for EVERY configured server on a 30s
// ticker, and ConnectionInfo.LastError is sticky (cleared only on a transition
// to Ready, see upstream/types.SetState). A level-triggered notification would
// count a server parked awaiting OAuth login ~2880 times a day while it makes
// zero connection attempts, so the telemetry counter behind
// diagnostics.error_code_counts_24h is edge-triggered.
//
// The edge is any of:
//   - a different classified code — a new failure, or the first failure after
//     a recovery that cleared the standing diagnostic;
//   - a changed ConnectionInfo.RetryCount — the count advances on each failed
//     attempt, and the state machine resets it to 0 on a transition to Ready,
//     so a DECREASE marks a new failure episode after a recovery that no
//     stateview pass happened to observe. Either direction is a real event,
//     which is why this compares != rather than >.
//   - a changed ConnectionInfo.LastRetryTime — the timestamp every failure
//     setter stamps with time.Now() (upstream/types SetError,
//     SetTerminalError, SetPendingAuth, SetOAuthError) and that nothing else
//     advances, so a fresh value is a fresh ATTEMPT. It is required for OAuth
//     failures: SetOAuthError increments oauthRetryCount and NOT retryCount,
//     so without it a server failing OAuth repeatedly would hold (same code,
//     same RetryCount) and edge only once. It also closes two ABA
//     routes that need no dropped event — a Reset()+Connect() that returns to
//     the same (code, RetryCount), and a recover-then-refail observed through
//     a state fetched after the queued connected event.
//     Re-observing a STICKY error does not advance it, which is the whole
//     point; reconcile never writes it.
//
// The standing condition itself is not lost: classifyAndAttach still runs on
// every pass (so the UI, REST API and CLI keep rendering the diagnostic), and
// Supervisor.CurrentErrorCodes reports the standing set for telemetry.
//
// Known and accepted imprecision — this is a SAMPLED predicate over two
// observations, not an event log tapped at the failure site, so it can alias:
//   - Coalescing (under-count): several attempts failing between two
//     observations collapse into one notification, and a transient different
//     code that has already reverted is not seen at all. Bounded by the
//     observation cadence.
//   - ABA (under-count): a recovery followed by a new failure that returns to
//     the identical (code, RetryCount, LastRetryTime) triple with no
//     observation in between. Needs all three to match, so in practice it
//     needs a clock that did not move.
//   - Double-stamping (over-count): one attempt routed through two failure
//     setters (SetError then SetPendingAuth, say) stamps LastRetryTime twice
//     and can edge twice. Bounded at a small constant per attempt.
//   - Stale-observation replay (over-count): reconcile pre-fetches upstream
//     states BEFORE taking stateMu (a deliberate lock-ordering choice, see
//     reconcile), so it can publish a state older than one the event writer
//     already applied, clearing the diagnostic and letting the same standing
//     failure edge a second time.
//
// All are bounded by the observation cadence or by a small constant. Closing
// them would require giving failures a monotonic identity at the source rather
// than diffing sampled projections. Treat the counter as "roughly how often
// something newly broke", and read CurrentErrorCodes for an exact right-now
// number.
func shouldNotifyErrorCode(prevCode, code string, prevRetryCount, retryCount int, prevErrorTime, errorTime time.Time) bool {
	if code == "" {
		return false
	}
	if code != prevCode {
		return true
	}
	if retryCount != prevRetryCount {
		return true
	}
	// Zero means the state manager never stamped an attempt time (a synthesised
	// ConnectionInfo, or one built before the failure setters ran); fall back to
	// the two counter terms rather than inventing an edge.
	return !errorTime.IsZero() && !errorTime.Equal(prevErrorTime)
}

// notifyErrorCode delivers a freshly classified MCPX_* code to the registered
// error-code notifier, synchronously, outside the stateview lock. The pre-churn last_error_code write must complete at the
// classification site — a crash right after classification is exactly the
// moment the field exists for, and an async hand-off could lose the final
// pre-crash code. Locking: callbackMu is released before invoking the
// callback; callers (reconcile / the event loop) may hold stateMu, which is
// safe because the telemetry callback only performs encrypted writes and never
// re-enters the supervisor — no lock cycle, and the write is sub-ms.
func (s *Supervisor) notifyErrorCode(code string) {
	if code == "" {
		return
	}
	s.callbackMu.RLock()
	notifier := s.errorCodeNotifier
	s.callbackMu.RUnlock()
	if notifier != nil {
		notifier(code)
	}
}

// SetOnServerConnectedCallback sets a callback to be invoked when a server connects.
// This allows for reactive tool discovery instead of relying on periodic polling.
func (s *Supervisor) SetOnServerConnectedCallback(callback func(serverName string)) {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	s.onServerConnectedCallback = callback
}

// toolInfosFromMetadata converts cached upstream tool metadata into the
// StateView ToolInfo representation. Shared by reconcile, background-discovery
// refresh, and reconnect repopulation so all three paths produce an identical
// StateView tool set.
func toolInfosFromMetadata(tools []*config.ToolMetadata) []stateview.ToolInfo {
	if tools == nil {
		return nil
	}
	infos := make([]stateview.ToolInfo, len(tools))
	for i, tool := range tools {
		// Parse ParamsJSON into InputSchema. StateView is served verbatim by the
		// REST/CLI tool listings, so a malformed schema must stay nil (field
		// omitted downstream) rather than become a misleading empty object.
		var inputSchema map[string]interface{}
		if tool.ParamsJSON != "" {
			if err := json.Unmarshal([]byte(tool.ParamsJSON), &inputSchema); err != nil {
				inputSchema = nil
			}
		}

		infos[i] = stateview.ToolInfo{
			Name:             tool.Name,
			Description:      tool.Description,
			InputSchema:      inputSchema,
			Annotations:      tool.Annotations,
			OutputSchemaJSON: tool.OutputSchemaJSON,
		}
	}
	return infos
}

// DiscoveryGenerations returns every known server's current
// DiscoveryCapture — the Supervisor's ConnectionGeneration and the live
// client's connection token. A discovery caller captures it BEFORE listing
// tools and hands it back to the publish call, which drops any server whose
// generation has moved on and stamps the token on the result.
func (s *Supervisor) DiscoveryGenerations() map[string]DiscoveryCapture {
	// The live tokens come from the adapter (manager lock) and are read
	// BEFORE stateMu, per the lock-ordering rule reconcile documents.
	actual := s.upstream.GetAllStates()

	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	snapshot := s.CurrentSnapshot()
	gens := make(map[string]DiscoveryCapture, len(snapshot.Servers))
	for name, state := range snapshot.Servers {
		if state == nil {
			continue
		}
		capture := DiscoveryCapture{Generation: state.ConnectionGeneration}
		if live, ok := actual[name]; ok && live != nil {
			capture.Epoch = live.ConnectionEpoch
		}
		gens[name] = capture
	}
	return gens
}

// DiscoveryGeneration returns one server's current DiscoveryCapture (a zero
// Generation for a server the snapshot does not hold — the live token is
// still captured, so the StateView write such a server gets carries it); see
// DiscoveryGenerations.
func (s *Supervisor) DiscoveryGeneration(serverName string) DiscoveryCapture {
	var epoch int64
	if live, err := s.upstream.GetServerState(serverName); err == nil && live != nil {
		epoch = live.ConnectionEpoch
	}

	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if state, ok := s.CurrentSnapshot().Servers[serverName]; ok && state != nil {
		return DiscoveryCapture{Generation: state.ConnectionGeneration, Epoch: epoch}
	}
	return DiscoveryCapture{Epoch: epoch}
}

// RefreshServerToolsFromDiscovery publishes ONE server's completed discovery
// result — tools may be empty — into the Supervisor snapshot and the
// StateView, stamping ToolsDiscovered for it.
// This is the per-server discovery path (connect, tools/list_changed, the
// operator's refresh), where an empty result is authoritative: the server
// really lists no tools, so every name on it must resolve as undiscovered
// rather than fall through to the connect→discovery window's fallback.
//
// gen is the server's DiscoveryGeneration() captured before the tools were
// listed; published reports whether the result landed (false: the connection
// generation moved on, the result was dropped and the caller should re-list).
func (s *Supervisor) RefreshServerToolsFromDiscovery(serverName string, tools []*config.ToolMetadata, gen DiscoveryCapture) (published bool, err error) {
	if serverName == "" {
		return false, nil
	}
	serverTools := make([]*config.ToolMetadata, 0, len(tools))
	for _, tool := range tools {
		if tool != nil && tool.ServerName == serverName {
			serverTools = append(serverTools, tool)
		}
	}
	stale := s.publishDiscoveredTools(map[string][]*config.ToolMetadata{serverName: serverTools}, map[string]DiscoveryCapture{serverName: gen})
	s.logger.Debug("Refreshed tools in Supervisor snapshot and StateView from server discovery",
		zap.String("server", serverName),
		zap.Int("total_tools", len(serverTools)),
		zap.Bool("published", len(stale) == 0))
	return len(stale) == 0, nil
}

// MarkServersToolsDiscovered stamps ToolsDiscovered on the named servers
// WITHOUT touching their tool sets. It is the
// marker for a discovery pass that completed with zero tools on a path that
// deliberately keeps whatever tool set the server already has — the lenient
// reactive connect path and the sweep, which must not wipe a server's tools on
// a transient empty result. A server that has never published a tool set is
// thereby "discovered, nothing served": every name on it resolves as absent
// (refused) rather than lingering in the connect→discovery window.
//
// A server that still HOLDS a tool set is deliberately NOT stamped: the
// disconnect clears the marker but keeps the retained Tools (restored on
// reconnect for counts and listings), so an unstamped server with
// tools is carrying the PREVIOUS connection's discovery result — and this
// connection listed none of it. Stamping would certify names this connection
// never served; wiping the set would break the lenient path's "never wipe on
// a transient empty result" contract. It stays in the discovery window
// (refusal says "retry") until a non-empty or authoritative pass replaces the
// set. gens is the DiscoveryGenerations() capture taken before the list; a
// server whose generation moved on is skipped.
func (s *Supervisor) MarkServersToolsDiscovered(serverNames []string, gens map[string]DiscoveryCapture) {
	if len(serverNames) == 0 {
		return
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	// The StateView is what identity resolution reads, and both sides clear
	// the marker on every connection edge, so a server needs stamping when
	// EITHER side lacks the marker. Both reads and both writes happen under
	// stateMu so a connection event cannot interleave between them.
	view := s.stateView.Snapshot()
	currentSnapshot := s.snapshot.Load().(*ServerStateSnapshot)
	newServers := make(map[string]*ServerState, len(currentSnapshot.Servers))
	for name, state := range currentSnapshot.Servers {
		newState := *state
		newServers[name] = &newState
	}
	snapshotChanged := false
	stampView := make([]string, 0, len(serverNames))
	var skipped []string
	for _, name := range serverNames {
		// The generation and retained-set rules are decided on the retained
		// Supervisor state; a server the snapshot does not hold is judged on
		// the StateView alone, as
		// publishDiscoveredTools does.
		if state, exists := newServers[name]; exists {
			if gen, ok := gens[name]; !ok || gen.Generation != state.ConnectionGeneration {
				skipped = append(skipped, name)
				continue
			}
			if len(state.Tools) > 0 {
				// Retained set from a previous connection: see the doc comment.
				skipped = append(skipped, name)
				continue
			}
			if !state.ToolsDiscovered || state.DiscoveryEpoch != gens[name].Epoch {
				state.ToolsDiscovered = true
				state.DiscoveryEpoch = gens[name].Epoch
				snapshotChanged = true
			}
		}
		if status, ok := view.Servers[name]; ok && status != nil && len(status.Tools) == 0 &&
			(!status.ToolsDiscovered || status.DiscoveryEpoch != gens[name].Epoch) {
			stampView = append(stampView, name)
		}
	}
	if snapshotChanged {
		s.snapshot.Store(&ServerStateSnapshot{
			Servers:   newServers,
			Timestamp: time.Now(),
			Version:   currentSnapshot.Version + 1,
		})
		s.version++
	}
	for _, name := range stampView {
		epoch := gens[name].Epoch
		s.stateView.UpdateServer(name, func(status *stateview.ServerStatus) {
			status.ToolsDiscovered = true
			status.DiscoveryEpoch = epoch
		})
	}
	if snapshotChanged || len(stampView) > 0 || len(skipped) > 0 {
		s.logger.Debug("Stamped discovery completed for servers that listed no tools",
			zap.Strings("servers", stampView), zap.Bool("snapshot_changed", snapshotChanged),
			zap.Strings("skipped_retained_or_stale", skipped))
	}
}

// publishDiscoveredTools writes a completed discovery result per server into
// the Supervisor snapshot (source of truth) and the StateView, stamping
// ToolsDiscovered on both so identity resolution can tell "discovery has not
// run" from "discovery found nothing". Publication is bound to the connection
// generation the result was captured under (gens): a server whose generation
// has moved on since is skipped and returned — the result is the previous
// connection's and must not land on the new one. Both the
// snapshot and the StateView are written under stateMu, the same lock
// updateSnapshotFromEvent holds across its own two-sided write, so a
// disconnect cannot interleave between the two halves and resurrect a marker
// the snapshot side just cleared.
func (s *Supervisor) publishDiscoveredTools(toolsByServer map[string][]*config.ToolMetadata, gens map[string]DiscoveryCapture) (stale []string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	currentSnapshot := s.snapshot.Load().(*ServerStateSnapshot)

	// Clone the snapshot
	newServers := make(map[string]*ServerState)
	for name, state := range currentSnapshot.Servers {
		// Shallow copy of ServerState
		newState := *state
		newServers[name] = &newState
	}

	// Update tool counts and tools for servers with discovered tools
	accepted := make(map[string][]*config.ToolMetadata, len(toolsByServer))
	for serverName, serverTools := range toolsByServer {
		captured := gens[serverName]
		state, exists := newServers[serverName]
		if !exists {
			// A server the snapshot does not hold (removed from config): the
			// StateView is still written so listings stay consistent.
			accepted[serverName] = serverTools
			continue
		}
		if _, ok := gens[serverName]; !ok || captured.Generation != state.ConnectionGeneration {
			s.logger.Debug("Dropping stale discovery result: the server's connection changed while it was captured",
				zap.String("server", serverName),
				zap.Uint64("captured_generation", captured.Generation),
				zap.Uint64("current_generation", state.ConnectionGeneration),
				zap.Bool("generation_supplied", ok))
			stale = append(stale, serverName)
			continue
		}
		state.ToolCount = len(serverTools)
		state.Tools = serverTools
		state.ToolsDiscovered = true
		state.DiscoveryEpoch = captured.Epoch
		accepted[serverName] = serverTools
	}

	newSnapshot := &ServerStateSnapshot{
		Servers:   newServers,
		Timestamp: time.Now(),
		Version:   currentSnapshot.Version + 1,
	}

	s.snapshot.Store(newSnapshot)
	s.version++
	s.toolTierGeneration.Add(1)

	// Update StateView for each accepted server
	for serverName, serverTools := range accepted {
		epoch := gens[serverName].Epoch
		s.stateView.UpdateServer(serverName, func(status *stateview.ServerStatus) {
			// StateView mirrors the Supervisor snapshot (updated unconditionally
			// above for every accepted server), so apply discovery results as
			// last-writer-wins: a server that legitimately dropped tools must
			// not keep a stale higher count that diverges from the snapshot
			// and the bleve index.
			status.ToolCount = len(serverTools)
			status.Tools = toolInfosFromMetadata(serverTools)
			status.ToolsDiscovered = true
			status.DiscoveryEpoch = epoch
		})
	}
	return stale
}

// forwardUpstreamEvents forwards upstream events to supervisor listeners.
func (s *Supervisor) forwardUpstreamEvents(upstreamEvents <-chan Event) {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return

		case event, ok := <-upstreamEvents:
			if !ok {
				return
			}

			s.logger.Debug("📨 Upstream event received",
				zap.String("server", event.ServerName),
				zap.String("type", string(event.Type)),
				zap.Any("connected", event.Payload["connected"]),
				zap.Any("title", event.Payload["title"]))

			// Forward to supervisor listeners
			s.emitEvent(event)

			// Update snapshot on state changes
			if event.Type == EventServerStateChanged || event.Type == EventServerConnected || event.Type == EventServerDisconnected {
				s.updateSnapshotFromEvent(event)
				s.Wake()
			}
		}
	}
}

// updateSnapshotFromEvent updates the snapshot based on an upstream event.
func (s *Supervisor) updateSnapshotFromEvent(event Event) {
	// IMPORTANT: Fetch server state BEFORE acquiring stateMu to prevent lock ordering issues.
	// GetServerState() needs manager.mu.RLock(), and if we hold stateMu.Lock() first while
	// a concurrent goroutine holds manager.mu.Lock() (e.g. during AddServerConfig/reconciliation),
	// we create a blocking chain that permanently stalls the reconciliation loop.
	var toolCount int
	var connInfo *types.ConnectionInfo
	var liveEpoch int64
	if actualState, err := s.upstream.GetServerState(event.ServerName); err == nil && actualState != nil {
		toolCount = actualState.ToolCount
		connInfo = actualState.ConnectionInfo
		liveEpoch = actualState.ConnectionEpoch
	} else if err != nil {
		s.logger.Warn("Failed to get server state for tool count",
			zap.String("server", event.ServerName),
			zap.Error(err))
	}

	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	current := s.CurrentSnapshot()
	if state, ok := current.Servers[event.ServerName]; ok {
		// Update connection status
		if connected, ok := event.Payload["connected"].(bool); ok {
			state.Connected = connected
			state.LastSeen = event.Timestamp
			// The discovery-completed marker is per connection: the next
			// connection needs its own discovery pass. It is cleared on BOTH
			// edges — on the
			// disconnect, and again on the connect in case the disconnect
			// event was dropped (actor_pool.emitEvent drops on a full
			// channel): a connection has just been established, and the
			// previous connection's result is not this one's. Discovery for
			// this connection is kicked off from this very event
			// (onServerConnectedCallback) and re-stamps it. It is cleared on
			// the retained Supervisor state as well as on the StateView below
			// — reconcile copies the retained state back into the StateView,
			// and the reconnect branch restores the retained tool set from
			// it, so a stale stamp here would resurrect "discovery completed"
			// for a connection that has not discovered anything yet. The
			// retained Tools themselves are kept. The connection generation
			// moves on every edge, so a discovery result captured under the
			// previous connection is dropped at publish time
			// (publishDiscoveredTools).
			state.ToolsDiscovered = false
			state.DiscoveryEpoch = 0
			state.ConnectionGeneration++
			// The live token this observation was made under:
			// reconcile compares its own observation against it to detect
			// an edge whose events were dropped.
			if liveEpoch != 0 {
				state.ConnectionEpoch = liveEpoch
			}

			// Update ConnectionInfo from pre-fetched server state
			if connInfo != nil {
				state.ConnectionInfo = connInfo
			}

			// Update stateview
			var classifiedCode string
			s.stateView.UpdateServer(event.ServerName, func(status *stateview.ServerStatus) {
				// Edge-trigger basis, read before any mutation — same
				// contract as updateStateView above.
				prevCode, prevRetryCount, prevErrorTime := errorCodeEdgeBasis(status)

				oldState := status.State
				status.Connected = connected

				// Use detailed state from ConnectionInfo if available
				// Normalize to lowercase to match health calculator expectations
				if connInfo != nil && connInfo.State != types.StateDisconnected {
					status.State = strings.ToLower(connInfo.State.String())
				} else if connected {
					status.State = "connected"
				} else {
					status.State = "disconnected"
				}

				s.logger.Debug("📡 StateView updated via event",
					zap.String("server", event.ServerName),
					zap.String("old_state", oldState),
					zap.String("new_state", status.State),
					zap.Bool("connected", connected),
					zap.Bool("has_conn_info", connInfo != nil))

				if connected {
					t := event.Timestamp
					status.ConnectedAt = &t
					// A new connection has no completed discovery pass yet,
					// whatever the StateView held (a dropped disconnect event
					// leaves the previous stamp and tools in place). The retained tools stay for counts and listings;
					// only the marker is dropped.
					status.ToolsDiscovered = false
					status.DiscoveryEpoch = 0
					if len(status.Tools) == 0 {
						if len(state.Tools) > 0 {
							status.Tools = toolInfosFromMetadata(state.Tools)
							status.ToolCount = len(state.Tools)
							// The restored set is the PREVIOUS connection's
							// discovery result, kept for counts and listings
							// until discovery re-runs; the discovery-completed
							// marker is not restored with it — it was cleared
							// on both sides above, and this connection has not
							// completed a pass yet (an unlisted name reads as
							// "discovery not completed, retry", never as
							// "stale name").
						} else {
							status.ToolCount = toolCount
						}
					}
					// If tools are already populated, keep the existing set/count

					// CRITICAL: Clear error when connected, even if connInfo is unavailable
					// This ensures stale OAuth/connection errors don't persist after successful reconnection
					status.LastError = ""
					status.LastErrorTime = nil
					status.Diagnostic = nil
				} else {
					t := event.Timestamp
					status.DisconnectedAt = &t
					status.Tools = nil // Clear tools on disconnect
					status.ToolCount = 0
					status.ToolsDiscovered = false // the next connection needs its own discovery pass
					status.DiscoveryEpoch = 0
				}

				// Update ConnectionInfo for immediate error propagation to UI
				if connInfo != nil {
					if connInfo.LastError != nil {
						errorStr := connInfo.LastError.Error()
						const maxErrorLen = 500
						if len(errorStr) > maxErrorLen {
							errorStr = errorStr[:maxErrorLen] + "... (truncated)"
						}
						status.LastError = errorStr

						// Classify raw error into stable diagnostic code.
						// Resolved transport (not raw Config.Protocol) so auto-detected
						// stdio servers (Protocol=="" + Command!="") hit the stdio
						// classifier rules.
						transport := ""
						if status.Config != nil {
							transport = transportpkg.DetermineTransportType(status.Config)
						}
						classifyAndAttach(status, connInfo.LastError, s.classifierHints(status.Config, transport))
						// Capture the classified code; delivered synchronously
						// after the stateview lock is released (see
						// notifyErrorCode). Edge-triggered, see
						// shouldNotifyErrorCode.
						if status.Diagnostic != nil {
							code := string(status.Diagnostic.Code)
							if shouldNotifyErrorCode(prevCode, code, prevRetryCount, connInfo.RetryCount, prevErrorTime, connInfo.LastRetryTime) {
								classifiedCode = code
							}
						}

						if !connInfo.LastRetryTime.IsZero() {
							t := connInfo.LastRetryTime
							status.LastErrorTime = &t
						}
					}
					// Note: We already cleared error above when connected=true
					// Only set error from connInfo if it has one
					status.RetryCount = connInfo.RetryCount
					applyRetryStopped(status, connInfo)
				}
			})
			s.notifyErrorCode(classifiedCode)

			// Trigger reactive tool discovery when server connects
			if connected {
				s.callbackMu.RLock()
				callback := s.onServerConnectedCallback
				s.callbackMu.RUnlock()

				if callback != nil {
					// Run callback asynchronously to avoid blocking supervisor
					go callback(event.ServerName)
				}
			}
		}
	}
}

// CurrentSnapshot returns the current state snapshot (lock-free read).
func (s *Supervisor) CurrentSnapshot() *ServerStateSnapshot {
	return s.snapshot.Load().(*ServerStateSnapshot)
}

// StateView returns the read-only state view.
// This provides a lock-free view of server statuses for API consumers.
func (s *Supervisor) StateView() *stateview.View {
	return s.stateView
}

// ToolTierGeneration returns the counter bumped once per
// publishDiscoveredTools call — a coarse, whole-fleet signal that some
// server's discovered tool set (and therefore some tool's effective
// annotations) may have changed since a caller last read it. Lock-free.
func (s *Supervisor) ToolTierGeneration() uint64 {
	return s.toolTierGeneration.Load()
}

// Subscribe returns a channel that receives supervisor events.
func (s *Supervisor) Subscribe() <-chan Event {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()

	ch := make(chan Event, 200) // Buffered for async reconciliation
	s.listeners = append(s.listeners, ch)
	return ch
}

// Unsubscribe removes a subscriber.
func (s *Supervisor) Unsubscribe(ch <-chan Event) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()

	for i, listener := range s.listeners {
		if listener == ch {
			s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
			close(listener)
			break
		}
	}
}

// emitEvent sends an event to all subscribers.
func (s *Supervisor) emitEvent(event Event) {
	s.eventMu.RLock()
	defer s.eventMu.RUnlock()

	for _, ch := range s.listeners {
		select {
		case ch <- event:
		default:
			s.logger.Warn("Supervisor event channel full, dropping event",
				zap.String("event_type", string(event.Type)))
		}
	}
}

// actionDrainTimeout bounds how long Stop() waits for in-flight reconcile action
// goroutines to finish before disconnecting clients. It exceeds the per-action
// context timeout (executeAction, 30s) so a well-behaved action that observes the
// cancelled context returns first; the timeout is only a backstop against a wedged
// Connect so shutdown can't hang forever.
const actionDrainTimeout = 35 * time.Second

// Stop gracefully stops the supervisor.
func (s *Supervisor) Stop() {
	s.logger.Info("Stopping supervisor")

	// Mark stopping under stateMu so reconcile() dispatches no further
	// action goroutines. Serializing on stateMu (the same lock reconcile holds while
	// dispatching) ensures every actionWg.Add has happened before the drain below.
	s.stateMu.Lock()
	s.stopping = true
	s.stateMu.Unlock()

	s.cancel()
	s.wg.Wait()

	// Drain in-flight reconcile actions (Connect/Disconnect/...) BEFORE disconnecting
	// upstream clients, so ShutdownAll -> Disconnect never overlaps an in-flight
	// Connect on the same client.
	s.drainActions()

	// Close upstream adapter
	s.upstream.Close()

	// Close event channels
	s.eventMu.Lock()
	for _, ch := range s.listeners {
		close(ch)
	}
	s.listeners = nil
	s.eventMu.Unlock()

	s.logger.Info("Supervisor stopped")
}

// drainActions waits for in-flight reconcile action goroutines to finish, bounded
// by actionDrainTimeout. Called from Stop() before disconnecting clients so a
// Connect can never overlap a Disconnect on the same client.
func (s *Supervisor) drainActions() {
	done := make(chan struct{})
	go func() {
		s.actionWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Debug("Drained in-flight reconcile actions before disconnect")
	case <-time.After(actionDrainTimeout):
		s.logger.Warn("Timed out draining in-flight reconcile actions before disconnect; "+
			"proceeding to disconnect (a Connect may still be in flight)",
			zap.Duration("timeout", actionDrainTimeout))
	}
}
