package managed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/diagnostics"
	"github.com/slopster67/mcpproxy-go/internal/diagnostics/hints"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/secret"
	"github.com/slopster67/mcpproxy-go/internal/servericon"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/transport"
	"github.com/slopster67/mcpproxy-go/internal/upstream/core"
	"github.com/slopster67/mcpproxy-go/internal/upstream/dialgate"
	"github.com/slopster67/mcpproxy-go/internal/upstream/types"

	mcptransport "github.com/mark3labs/mcp-go/v2/client/transport"
	"github.com/mark3labs/mcp-go/v2/mcp"
	"go.uber.org/zap"
)

// Client wraps an upstream connection with state management and health monitoring.
type Client struct {
	// cfg holds the server configuration as an atomic pointer. SetConfig swaps it
	// (reconcile add path, off mc.mu) while many readers — including detached
	// state-change callback goroutines and Connect's unlocked phase — read it
	// concurrently. An atomic pointer makes every read/write data-race-free and
	// is lock-free, so it is safe to read whether or not mc.mu is held (the RLock
	// accessor approach would deadlock the in-lock readers). Access via
	// GetConfig() / SetConfig() only — never touch the field directly.
	//
	// The config and the commit generation it belongs to are one atomic pair, so a reader never sees a config with another commit's
	// generation (Generation, SetConfigAt).
	cfg            atomic.Pointer[stampedConfig]
	coreClient     *core.Client
	advertisedIcon atomic.Pointer[string]

	instanceID InstanceID
	// unregistered is set by upstream.Manager when it removes this instance
	// from its registry (unregisterLocked, under the manager's locks). From
	// then on Connect refuses (ErrInstanceUnregistered), so a worker, the
	// reconnect loop or a sweep that still holds the pointer cannot bring an
	// instance the manager no longer serves back online. Retire adds the
	// byte-level retirement barrier (dial gate).
	unregistered atomic.Bool
	logger       *zap.Logger
	StateManager *types.StateManager // Public field for callback access

	// globalConfig holds the proxy-wide config as an atomic pointer so a config
	// hot-reload can swap it under the running background loops (health-check
	// interval re-resolution) without a lock and without racing the readers.
	// Access via GetGlobalConfig() / SetGlobalConfig() only.
	globalConfig atomic.Pointer[config.Config]

	// Connection state protection
	mu sync.RWMutex

	// ListTools concurrency control
	listToolsMu         sync.Mutex
	listToolsInProgress bool
	listToolsCancel     context.CancelFunc
	// listToolsCur is the in-flight upstream tools/list request, or nil.
	// Waiters keep the pointer they waited on and read the result from it, so
	// a later leader replacing listToolsCur can never hand them its (nil,
	// not-yet-published) result.
	listToolsCur *listToolsCall
	// listToolsLeaderSeq numbers ListTools leaders (incremented each time
	// acquireListToolsContext grants one) so ListToolsFresh can tell an
	// in-flight upstream call that started before it from one that started
	// after it.
	listToolsLeaderSeq uint64

	// connectSeq identifies the current connect attempt. Guarded by mu. Every
	// Connect takes a new value when it starts and every Disconnect advances
	// it, so a Connect that re-acquires mu after its unlocked dial and finds
	// a different value knows a Disconnect, or a newer attempt, superseded it
	// and must not publish its result.
	connectSeq uint64

	// Connect cancellation - allows Disconnect() to cancel an in-flight Connect()
	// without waiting for mc.mu (which Connect holds during the entire OAuth flow)
	connectMu     sync.Mutex
	connectCancel context.CancelFunc

	// Background monitoring
	stopMonitoring       chan struct{}
	monitoringWG         sync.WaitGroup
	monitoringCancelFunc context.CancelFunc
	monitoringStarted    bool

	// Tool count caching to reduce upstream ListTools calls
	toolCountMu sync.RWMutex
	toolCount   int

	// Tool discovery callback for notifications/tools/list_changed handling
	toolDiscoveryCallback func(ctx context.Context, serverName string) error

	// consecutiveHealthFailures counts back-to-back transient health-check
	// failures. The state-machine only flips to Error once it reaches
	// healthCheckFailureThreshold; one success resets it. Hard failures
	// (connection refused, no such host, unreachable) bypass the counter
	// and trigger Error immediately. See recordHealthCheckFailure().
	consecutiveHealthFailures int

	// sess is the session re-init state (single-flight, baseline, counter).
	sess sessionReinit

	// ambiguousProbeInFlight gates the async liveness probe fired after an
	// ambiguous tools/call cancellation so a burst of canceled calls results
	// in at most one probe against the upstream.
	ambiguousProbeInFlight atomic.Bool

	// oauthCallRequired records that this server connected anonymously (no config
	// OAuth) but a tools/call returned "authorization required" / 401 — the
	// endpoint enforces OAuth only at call time (e.g. Google's sqladmin MCP). The
	// runtime reads it via IsOAuthCallRequired() and feeds it into the health
	// calculator so the UI shows a proactive Sign-in CTA instead of "Ready". A
	// successful call or a fresh Connect clears it.
	oauthCallRequired atomic.Bool

	// connectionEpoch is bumped on every successful connect. It identifies the
	// current connection generation so detached goroutines (the ambiguous-call
	// liveness probe) can tell whether the session they observed is still the
	// live one: coreClient is created once and never replaced, so pointer
	// identity proves nothing, and a disconnect+reconnect leaves the state
	// machine back at Ready — indistinguishable from "never left". A stale
	// verdict must not be applied to a brand-new healthy session.
	//
	// Values are drawn from the PROCESS-WIDE counter below, never restarted per
	// client: a replacement client instance installed under the same server
	// name (remove + re-add, config reload) must not restart at the epoch a
	// discovery stamp was certified against, or an epoch-pinned dispatch would
	// match the new instance's first connection and send a stale name to it.
	connectionEpoch atomic.Int64

	// epochMu serializes the probe goroutine's final epoch-check-and-SetError
	// with Connect's epoch bump. Without it a reconnect could complete between
	// the probe's staleness check and its SetError, letting a stale verdict
	// evict the new session. Lock invariant:
	// epochMu is never held across code that can run foreign callbacks —
	// TransitionTo invokes its state-change callback SYNCHRONOUSLY and so must
	// stay outside; SetError dispatches its callback in a goroutine, so the
	// probe may call it under epochMu.
	epochMu sync.Mutex

	// retired is set by the upstream manager when it replaces or removes this
	// client: its tool captures are discarded and it
	// is never current on any epoch again.
	retired atomic.Bool

	// inFlightMu guards inFlightCalls and inFlightSuppressionSince as ONE
	// coherent pair: a plain mutex, not two independent atomics, so "the last
	// call ends, count hits 0, the suppression clock resets" and "a new call
	// begins, checks the clock" can never interleave — one always fully
	// precedes the other. beginInFlightCall/end, trackInFlightSuppression and
	// resetInFlightSuppression all take this lock.
	inFlightMu sync.Mutex

	inFlightCalls int
	// inFlightSuppressionSince is the moment the first transient ping
	// failure was tolerated, in the CURRENT unbroken streak of such
	// failures, purely because a call was in flight. Nil means no
	// streak is open. Bounds trackInFlightSuppression's cap: reset to nil
	// the moment a ping succeeds or inFlightCalls reaches 0, so the cap
	// always measures one continuous busy period, never accumulated idle
	// time.
	inFlightSuppressionSince *time.Time
}

// inFlightSuppressionCap bounds how long consecutive transient ping
// failures can be tolerated solely because a tool call is in flight
// Generous relative to the default CallToolTimeout (2m) so a
// legitimately slow interactive call -- e.g. one blocked on a human consent
// prompt -- survives, but finite: a caller that keeps a call perpetually in
// flight (immediate back-to-back retries, or overlapping calls with no
// gap) must not suppress eviction of a truly dead upstream forever.
const inFlightSuppressionCap = 15 * time.Minute

// beginInFlightCall registers one in-flight callTool() invocation and
// returns the func that ends it. Call defer end() immediately -- the
// decrement-to-zero-then-reset happens atomically with respect to
// trackInFlightSuppression, all under inFlightMu.
func (mc *Client) beginInFlightCall() (end func()) {
	mc.inFlightMu.Lock()
	mc.inFlightCalls++
	mc.inFlightMu.Unlock()

	var ended bool
	return func() {
		if ended {
			return
		}
		ended = true
		mc.inFlightMu.Lock()
		mc.inFlightCalls--
		if mc.inFlightCalls == 0 {
			mc.inFlightSuppressionSince = nil
		}
		mc.inFlightMu.Unlock()
	}
}

// trackInFlightSuppression opens (on first call in a streak) or continues
// the in-flight suppression window and reports how long it has been open
// and whether that is still within inFlightSuppressionCap, alongside
// whether a call is actually in flight right now (checked under the same
// lock, so callers never act on a stale read from a separate call).
func (mc *Client) trackInFlightSuppression() (elapsed time.Duration, withinCap, inFlight bool) {
	mc.inFlightMu.Lock()
	defer mc.inFlightMu.Unlock()
	if mc.inFlightCalls == 0 {
		return 0, false, false
	}
	now := time.Now()
	if mc.inFlightSuppressionSince == nil {
		mc.inFlightSuppressionSince = &now
	}
	// time.Since (not now.Sub, though equivalent here) makes the monotonic
	// dependency explicit: elapsed must never regress on a backward
	// wall-clock adjustment.
	elapsed = time.Since(*mc.inFlightSuppressionSince)
	return elapsed, elapsed < inFlightSuppressionCap, true
}

// resetInFlightSuppression closes the current suppression window (if any),
// so the NEXT busy streak starts its own cap from zero rather than
// inheriting elapsed time from an unrelated, already-finished one. Exposed
// separately from beginInFlightCall's end() for the ping-succeeded path in
// performHealthCheck, which has no call of its own to attribute the reset to.
func (mc *Client) resetInFlightSuppression() {
	mc.inFlightMu.Lock()
	mc.inFlightSuppressionSince = nil
	mc.inFlightMu.Unlock()
}

// guardReconnectAgainstInFlightCall defers controller-driven teardown while a
// tool call is still active, up to a bounded suppression window.
func (mc *Client) guardReconnectAgainstInFlightCall() bool {
	info := mc.StateManager.GetConnectionInfo()
	if info.IsOAuthError || !isTransientHealthCheckError(info.LastError) {
		return false
	}
	elapsed, withinCap, inFlight := mc.trackInFlightSuppression()
	if !inFlight {
		return false
	}
	if withinCap {
		mc.logger.Info("Reconnect deferred: a tool call is still in flight",
			zap.String("server", mc.GetConfig().Name),
			zap.Duration("suppressed_for", elapsed))
		return true
	}
	mc.logger.Warn("Reconnecting despite an in-flight tool call: in-flight suppression cap exceeded",
		zap.String("server", mc.GetConfig().Name),
		zap.Duration("cap", inFlightSuppressionCap))
	return false
}

// ambiguousProbeTimeout bounds the liveness probe fired after an ambiguous
// tools/call cancellation. Matches the background health-check probe budget.
const ambiguousProbeTimeout = 5 * time.Second

// healthCheckFailureThreshold is the number of consecutive transient
// health-check failures we tolerate before marking the server Error.
// With a 30-second tick this is ~90s of unreachability — long enough that a
// real outage still surfaces promptly, short enough that one slow upstream
// request doesn't paint the UI red and clear the tools list.
const healthCheckFailureThreshold = 3

// NewClientAt creates a managed client stamped, from construction, with the
// commit generation gen of serverConfig (Generation).
func NewClientAt(id string, serverConfig *config.ServerConfig, gen int64, logger *zap.Logger, logConfig *config.LogConfig, globalConfig *config.Config, storage storage.OAuthStore, secretResolver *secret.Resolver) (*Client, error) {
	// Create core client
	coreClient, err := core.NewClient(id, serverConfig, logger, logConfig, globalConfig, storage, secretResolver)
	if err != nil {
		return nil, fmt.Errorf("failed to create core client: %w", err)
	}

	coreClient.SetPeerGeneration(gen)

	// Create managed client
	mc := &Client{
		coreClient:     coreClient,
		logger:         logger.With(zap.String("component", "managed_client")),
		StateManager:   types.NewStateManager(),
		stopMonitoring: make(chan struct{}),
	}
	mc.instanceID = InstanceID(instanceIDCounter.Add(1))
	mc.cfg.Store(&stampedConfig{cfg: serverConfig, gen: gen})
	mc.globalConfig.Store(globalConfig)

	// Set up state change callback
	mc.StateManager.SetStateChangeCallback(mc.onStateChange)

	// Wire up core notification callback to forward to discovery callback
	coreClient.SetOnToolsChangedCallback(func(serverName string) {
		mc.mu.RLock()
		callback := mc.toolDiscoveryCallback
		mc.mu.RUnlock()

		if callback == nil {
			mc.logger.Debug("No tool discovery callback set - notification ignored",
				zap.String("server", serverName))
			return
		}

		// Run discovery in a goroutine with timeout to avoid blocking the notification handler
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			mc.logger.Debug("Triggering tool discovery from notification",
				zap.String("server", serverName))

			if err := callback(ctx, serverName); err != nil {
				mc.logger.Error("Tool discovery triggered by notification failed",
					zap.String("server", serverName),
					zap.Error(err))
			} else {
				mc.logger.Debug("Tool discovery from notification completed successfully",
					zap.String("server", serverName))
			}
		}()
	})

	return mc, nil
}

// ErrConnectAlreadyActive is returned by Connect when another caller's connect
// is in flight or the client is already Ready. It is a guard, not a failure.
var ErrConnectAlreadyActive = errors.New("connection already in progress or established")

// ErrInstanceUnregistered is returned by Connect on an instance the upstream
// manager has unregistered: a newer config commit, a removal or
// a same-generation replacement superseded it, so it must not connect.
var ErrInstanceUnregistered = errors.New("instance unregistered: superseded by a newer config commit or replacement")

// ErrInstanceRetired is returned by a tool call, tools/list or probe on an
// instance the upstream manager retired: the server was removed, replaced or
// disabled while the request was on its way. It is the "server unavailable"
// answer; the internal dialgate.ErrRetired never reaches a surface. It is not a connection failure: it marks
// nothing unhealthy and is not charged to the replacement instance.
var ErrInstanceRetired = errors.New("server unavailable: it was removed, replaced or disabled while the request was in flight")

// ErrConnectAborted is returned by Connect when a Disconnect, or a newer
// connect attempt, ran while the transport was being established. The dial's
// result (success or failure) is discarded; the client keeps the state the
// superseding operation left.
var ErrConnectAborted = errors.New("connect superseded by a concurrent disconnect or a newer attempt")

// Connect establishes connection with state management.
// IMPORTANT: mc.mu is only held briefly for state checks/transitions, NOT during the
// potentially slow coreClient.Connect() call (which may involve OAuth flows taking minutes).
// This prevents blocking Disconnect, SetConfig, GetConfig, and other callers.
func (mc *Client) Connect(ctx context.Context) error {
	// Phase 1: Acquire lock, check state, prepare for connection
	mc.mu.Lock()

	// An instance the manager unregistered never connects again: whoever
	// still holds this pointer acts for a superseded config or instance.
	if mc.unregistered.Load() {
		mc.mu.Unlock()
		return ErrInstanceUnregistered
	}

	// Check if already connecting or connected
	if mc.StateManager.IsConnecting() || mc.StateManager.IsReady() {
		mc.mu.Unlock()
		return fmt.Errorf("%w (state: %s)", ErrConnectAlreadyActive, mc.StateManager.GetState().String())
	}

	// Snapshot the server name while mc.mu is held. Phase 3 below runs WITHOUT
	// mc.mu, so dereferencing mc.GetConfig() there races with SetConfig swapping the
	// pointer. Use this local for
	// any logging in the unlocked window.
	serverConfig := mc.GetConfig()
	serverName := serverConfig.Name

	mc.listToolsMu.Lock()
	listToolsInProgress := mc.listToolsInProgress
	mc.listToolsMu.Unlock()
	mc.logger.Info("Starting managed connection to upstream server",
		zap.String("server", mc.GetConfig().Name),
		zap.String("current_state", mc.StateManager.GetState().String()),
		zap.Bool("list_tools_in_progress", listToolsInProgress))

	// When reconnecting from a non-Ready state, disconnect the core client first
	// to clear a stale connected flag left by a connection that died silently
	// (e.g. HTTP server timeout); the core client otherwise rejects the connect
	// with "client already connected".
	currentState := mc.StateManager.GetState()
	if currentState == types.StateError || currentState == types.StateDisconnected || currentState == types.StatePendingAuth {
		// A failed probe must not tear down a connection still serving a call.
		if mc.guardReconnectAgainstInFlightCall() {
			mc.mu.Unlock()
			return fmt.Errorf("connect deferred: a tool call is still in flight")
		}
		mc.logger.Debug("Disconnecting core client before reconnect to clear stale state",
			zap.String("server", mc.GetConfig().Name),
			zap.String("from_state", currentState.String()))
		if err := mc.coreClient.Disconnect(); err != nil {
			mc.logger.Debug("Core client disconnect before reconnect returned",
				zap.String("server", mc.GetConfig().Name),
				zap.Error(err))
		}
	}

	// Transition to connecting state
	mc.connectSeq++
	attempt := mc.connectSeq
	mc.StateManager.TransitionTo(types.StateConnecting)

	// Release lock BEFORE the slow coreClient.Connect() call
	mc.mu.Unlock()

	// Phase 2: Create cancellable context for Disconnect() to abort in-flight Connect
	connectCtx, cancel := context.WithCancel(ctx)
	mc.connectMu.Lock()
	mc.connectCancel = cancel
	mc.connectMu.Unlock()

	defer func() {
		mc.connectMu.Lock()
		mc.connectCancel = nil
		mc.connectMu.Unlock()
	}()

	// Phase 3: Execute the actual connection (potentially slow - OAuth, MCP initialize)
	// mc.mu is NOT held here, so Disconnect/SetConfig/GetConfig won't block
	mc.logger.Debug("Invoking core client Connect for managed client",
		zap.String("server", serverName))
	connectErr := mc.coreClient.Connect(connectCtx)
	var advertisedIcon string
	if connectErr == nil {
		if info := mc.coreClient.GetServerInfo(); info != nil {
			advertisedIcon = servericon.Resolve(connectCtx, info.ServerInfo.Icons, serverConfig.URL)
		}
	}

	// Phase 4: Re-acquire lock to update state based on result
	mc.mu.Lock()
	defer mc.mu.Unlock()

	// A Disconnect, or a newer attempt, that ran while this attempt was
	// dialing (mc.mu is not held in phase 3) owns the client's state now.
	// Publishing this attempt's result would undo it: reporting Ready over
	// the transport the Disconnect closed (every call would fail with "client
	// not connected", and since the client looks connected nothing reconnects
	// it), or overwriting the reset state with Error/PendingAuth for a dial the
	// Disconnect cancelled.
	// Unregistered while dialing, before the manager's asynchronous
	// Disconnect landed: discard the result and close what this dial opened.
	if mc.connectSeq == attempt && mc.unregistered.Load() {
		mc.connectSeq++
		if connectErr == nil {
			_ = mc.coreClient.Disconnect()
		}
		mc.StateManager.Reset()
		mc.logger.Info("Connect finished on an unregistered instance; discarding its result",
			zap.String("server", serverName),
			zap.NamedError("dial_error", connectErr))
		return ErrInstanceUnregistered
	}
	if mc.connectSeq != attempt {
		// Only Disconnects ran since (a newer attempt would have left
		// Connecting, Ready or a failure state): close what this dial opened,
		// because a Disconnect that landed before connectCancel was
		// registered could not abort it. A newer attempt shares the core
		// client, so its transport is left alone.
		if connectErr == nil && mc.StateManager.GetState() == types.StateDisconnected {
			_ = mc.coreClient.Disconnect()
		}
		mc.logger.Info("Connect superseded by a concurrent disconnect or a newer attempt; discarding its result",
			zap.String("server", serverName),
			zap.NamedError("dial_error", connectErr))
		if connectErr != nil {
			return fmt.Errorf("%w: %v", ErrConnectAborted, connectErr)
		}
		return ErrConnectAborted
	}

	if connectErr != nil {
		// A rate-limited upstream (429, or 503 with a hint) told us when to come
		// back. mcp-go flattened that response into connectErr's string, so the
		// deadline comes from the recorder the transport RoundTripper fed.
		// Stamp it BEFORE the SetError/SetOAuthError branches below so the
		// ConnectionInfo their callbacks publish already carries the window.
		mc.syncRetryAfterFromTransport()

		// Check if this is a deferred OAuth requirement (pending user action)
		if core.IsOAuthPending(connectErr) {
			mc.logger.Info("⏳ OAuth authentication pending user action",
				zap.String("server", mc.GetConfig().Name))
			// Park in PendingAuth. SetPendingAuthProbe, not SetError: SetError
			// forces StateError, which would undo the park and have the
			// supervisor redial a login-blocked server on every tick.
			// A speculative park (no oauth block, no token, no 401) is flagged
			// so the reconnect loop re-probes it periodically.
			mc.StateManager.SetPendingAuthProbe(connectErr, core.IsSpeculativeOAuthPending(connectErr))
			return fmt.Errorf("OAuth authentication pending: %w", connectErr)
		}
		// A DNS/dial/timeout failure is a plain retryable error. The OAuth
		// classifiers below match a bare "401" and would misread an address or
		// ephemeral port in the net error text as an auth failure, sending the
		// server onto the 5m-24h OAuth backoff ladder.
		isNetworkErr := core.IsNetworkError(connectErr)
		// Check if this is an OAuth authorization requirement (not an error)
		if !isNetworkErr && mc.isOAuthAuthorizationRequired(connectErr) {
			// Check if this is a token refresh scenario vs full re-auth
			isRefreshScenario := mc.isTokenRefreshScenario(connectErr)
			mc.logger.Info("🎯 OAuth authorization required during MCP initialization",
				zap.String("server", mc.GetConfig().Name),
				zap.Bool("token_refresh_scenario", isRefreshScenario))
			// Don't apply backoff for OAuth authorization requirement
			mc.StateManager.SetError(connectErr)
			return fmt.Errorf("OAuth authorization during MCP init failed: %w", connectErr)
		} else if !isNetworkErr && mc.isOAuthError(connectErr) {
			// Check if this is a token refresh scenario vs full re-auth
			isRefreshScenario := mc.isTokenRefreshScenario(connectErr)
			mc.logger.Warn("OAuth authentication failed, applying extended backoff",
				zap.String("server", mc.GetConfig().Name),
				zap.Bool("token_refresh_scenario", isRefreshScenario),
				zap.Error(connectErr))
			mc.StateManager.SetOAuthError(connectErr)
		} else if code, parkable := mc.classifyConnectFailure(connectErr); parkable {
			// Deterministic, unrecoverable AND proven so by a typed signal — a
			// code we attached ourselves, or an errno from our own spawn syscall.
			// Park it so the reconcile loop stops re-spawning a guaranteed
			// failure every ladder tick. diagnostics.ParkableCode, not IsPermanent: the
			// classifier's string fallbacks match against the child's captured
			// stderr, which can say "no such file or directory" for reasons that
			// clear on their own.
			mc.logger.Warn("Connection failed permanently — automatic reconnection will stop",
				zap.String("server", mc.GetConfig().Name),
				zap.String("code", string(code)),
				zap.Error(connectErr))
			mc.StateManager.SetTerminalError(connectErr, string(code))
		} else {
			mc.StateManager.SetError(connectErr)
		}
		return fmt.Errorf("core client connection failed: %w", connectErr)
	}

	mc.logger.Debug("Core client Connect returned successfully",
		zap.String("server", mc.GetConfig().Name))

	// Open a new connection generation BEFORE exposing Ready. The bump is
	// serialized under epochMu with the ambiguous-call probe's verdict block:
	// once it lands, any stale probe (old epoch) drops its verdict, so the new
	// session can never be evicted by a probe that observed the previous one.
	// A stale verdict that wins the mutex first can only mark the still
	// pre-Ready state, which the TransitionTo below immediately overrides.
	// TransitionTo deliberately stays OUTSIDE the critical section — it invokes
	// the state-change callback synchronously (types.go), and epochMu must
	// never be held across foreign code.
	mc.epochMu.Lock()
	mc.connectionEpoch.Store(nextConnectionEpoch())
	mc.epochMu.Unlock()

	// Remember the session id this connect obtained, so a
	// transport that loses it before the first managed request can recover.
	_, _, _ = mc.sessionState()

	mc.advertisedIcon.Store(&advertisedIcon)

	// Transition to ready state only if not already ready
	if mc.StateManager.GetState() != types.StateReady {
		mc.StateManager.TransitionTo(types.StateReady)
	}

	// Wipe any consecutive-failure debt accumulated before reconnect so the
	// new session starts at zero. Without this, a server that flapped, then
	// recovered, would carry stale counts into the next health-check window.
	mc.resetHealthCheckFailures()

	// A fresh connection (e.g. a post-sign-in reconnect that now carries a token)
	// starts clean: clear any stale call-time OAuth-required flag so the Sign-in
	// CTA doesn't linger after the user has authenticated.
	mc.oauthCallRequired.Store(false)

	// Update state manager with server info
	if serverInfo := mc.coreClient.GetServerInfo(); serverInfo != nil {
		mc.StateManager.SetServerInfo(serverInfo.ServerInfo.Name, serverInfo.ServerInfo.Version)
	}

	mc.logger.Info("Successfully established managed connection",
		zap.String("server", mc.GetConfig().Name))

	// Add a small delay before starting background monitoring to let connection stabilize
	mc.logger.Debug("🔍 Adding stabilization delay before starting background monitoring",
		zap.String("server", mc.GetConfig().Name))

	// Create cancellable context for monitoring startup
	monitoringCtx, monitoringCancel := context.WithCancel(context.Background())
	mc.monitoringCancelFunc = monitoringCancel

	go func() {
		select {
		case <-time.After(2 * time.Second):
			// Check if we're still connected before starting monitoring
			mc.mu.Lock()
			if mc.monitoringCancelFunc != nil {
				mc.logger.Debug("🔍 Starting background monitoring after stabilization delay",
					zap.String("server", mc.GetConfig().Name))
				mc.startBackgroundMonitoring()
			}
			mc.mu.Unlock()
		case <-monitoringCtx.Done():
			mc.logger.Debug("🔍 Background monitoring startup cancelled",
				zap.String("server", mc.GetConfig().Name))
		}
	}()

	return nil
}

// Disconnect closes the connection and stops monitoring
func (mc *Client) Disconnect() error {
	mc.cancelInFlightListTools()
	mc.cancelInFlightConnect()

	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.logger.Info("Disconnecting managed client", zap.String("server", mc.GetConfig().Name))

	// Supersede any connect attempt still dialing (see connectSeq).
	mc.connectSeq++

	// Ensure no ListTools operations remain after acquiring the lock
	mc.cancelInFlightListTools()

	// Cancel monitoring startup if it's still pending
	if mc.monitoringCancelFunc != nil {
		mc.monitoringCancelFunc()
		mc.monitoringCancelFunc = nil
	}

	// Stop background monitoring
	mc.stopBackgroundMonitoring()

	if err := mc.coreClient.Disconnect(); err != nil {
		mc.logger.Error("Core client disconnect failed", zap.Error(err))
	}

	// Close this connection generation BEFORE resetting state, serialized with
	// the ambiguous-call probe's verdict block. An in-flight probe either
	// finishes its verdict first (its SetError is overridden by the Reset
	// below) or observes the bumped epoch and drops the verdict — it can never
	// flip the freshly Disconnected state back to Error.
	mc.epochMu.Lock()
	mc.connectionEpoch.Store(nextConnectionEpoch())
	mc.epochMu.Unlock()

	// Reset state
	mc.StateManager.Reset()

	mc.listToolsMu.Lock()
	listToolsInProgress := mc.listToolsInProgress
	mc.listToolsMu.Unlock()
	mc.logger.Debug("Managed client disconnect complete",
		zap.String("server", mc.GetConfig().Name),
		zap.Bool("list_tools_in_progress", listToolsInProgress))

	return nil
}

func (mc *Client) classifyConnectFailure(err error) (diagnostics.Code, bool) {
	cfg := mc.GetConfig()
	return diagnostics.ParkableCode(err, hints.For(mc.GetGlobalConfig(), cfg, transport.DetermineTransportType(cfg)))
}

// IsConnected returns whether the client is ready for operations
func (mc *Client) IsConnected() bool {
	return mc.StateManager.IsReady()
}

// ConnectionEpoch returns the client's connection-instance token: a
// monotonically increasing counter bumped on every successful Connect and on
// every Disconnect (see connectionEpoch), so two observations that read the
// same value were made on the SAME live connection. The runtime captures it
// before listing a server's tools and the supervisor stamps it on the
// discovery snapshot with ToolsDiscovered (stateview.ServerStatus.
// DiscoveryEpoch); every tool-identity read compares the stamp with the live
// value, so a discovery result that belongs to a previous connection can
// never certify a name on the current one when the connection events were
// dropped or lag.
func (mc *Client) ConnectionEpoch() int64 {
	return mc.connectionEpoch.Load()
}

// IsConnecting returns whether the client is in a connecting state
func (mc *Client) IsConnecting() bool {
	return mc.StateManager.IsConnecting()
}

// GetState returns the current connection state
func (mc *Client) GetState() types.ConnectionState {
	return mc.StateManager.GetState()
}

// GetConnectionInfo returns detailed connection information
func (mc *Client) GetConnectionInfo() types.ConnectionInfo {
	return mc.StateManager.GetConnectionInfo()
}

// GetConfig returns the current server configuration pointer in a thread-safe,
// lock-free manner. Safe to call whether or not mc.mu is held.
func (mc *Client) GetConfig() *config.ServerConfig {
	if p := mc.cfg.Load(); p != nil {
		return p.cfg
	}
	return nil
}

// stampedConfig pairs a server config with the commit generation it was
// committed at (0 when the caller supplied none).
type stampedConfig struct {
	cfg *config.ServerConfig
	gen int64
}

// Generation returns the commit generation of the config this instance holds:
// the generation of the commit that created or last restamped it, or 0 when
// the instance was built without one. Discovery captures it and drops a result
// whose generation does not match the stored row.
func (mc *Client) Generation() int64 {
	if p := mc.cfg.Load(); p != nil {
		return p.gen
	}
	return 0
}

// SetConfigAt stores cfg together with its commit generation gen as one atomic
// pair, but only when gen is newer than the generation the instance holds, so
// a late worker can never roll the config back. It reports whether it applied.
// Lock-free, like SetConfig.
func (mc *Client) SetConfigAt(cfg *config.ServerConfig, gen int64) bool {
	for {
		cur := mc.cfg.Load()
		if cur != nil && gen <= cur.gen {
			return false
		}
		if mc.cfg.CompareAndSwap(cur, &stampedConfig{cfg: cfg, gen: gen}) {
			return true
		}
	}
}

// SetConfig atomically swaps the server configuration. Lock-free; callers must
// not hold mc.mu (they don't need to — the swap is atomic).
//
// The instance keeps the commit generation it holds: SetConfig is for callers
// that have no generation.
func (mc *Client) SetConfig(config *config.ServerConfig) {
	for {
		cur := mc.cfg.Load()
		next := &stampedConfig{cfg: config}
		if cur != nil {
			next.gen = cur.gen
		}
		if mc.cfg.CompareAndSwap(cur, next) {
			return
		}
	}
}

// GetServerInfo returns server information
func (mc *Client) GetServerInfo() *mcp.InitializeResult {
	return mc.coreClient.GetServerInfo()
}

// GetLastError returns the last error from the state manager
func (mc *Client) GetLastError() error {
	info := mc.StateManager.GetConnectionInfo()
	return info.LastError
}

// SetUserLoggedOut marks that the user has explicitly logged out
// This prevents automatic reconnection until cleared (e.g., by explicit login)
func (mc *Client) SetUserLoggedOut(loggedOut bool) {
	mc.StateManager.SetUserLoggedOut(loggedOut)
}

// IsUserLoggedOut returns true if the user has explicitly logged out
func (mc *Client) IsUserLoggedOut() bool {
	return mc.StateManager.IsUserLoggedOut()
}

// ConnectedWithOAuth reports whether the live connection was established by
// the OAuth auth strategy — i.e. the stored OAuth token is what authenticated
// it. False when disconnected, when another strategy (static headers,
// anonymous) won. The runtime uses it to decide whether a stored token record
// is evidence about this server at all.
func (mc *Client) ConnectedWithOAuth() bool {
	return mc.coreClient.AuthStrategy() == core.AuthStrategyOAuth
}

// IsOAuthCallRequired reports whether a tools/call against this otherwise-
// connected server returned "authorization required" / 401, indicating the
// endpoint enforces OAuth only at call time. The runtime feeds this into the
// health calculator to surface a proactive Sign-in CTA. Cleared by a successful
// call or a fresh Connect.
func (mc *Client) IsOAuthCallRequired() bool {
	return mc.oauthCallRequired.Load()
}

// SetToolDiscoveryCallback sets the callback for triggering tool re-indexing when
// a notifications/tools/list_changed notification is received from the upstream server.
func (mc *Client) SetToolDiscoveryCallback(callback func(ctx context.Context, serverName string) error) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.toolDiscoveryCallback = callback
}

// listToolsCall is one upstream tools/list request owned by a leader. The
// leader publishes into it before release() closes done; waiters read the
// result of the exact request they waited on.
type listToolsCall struct {
	done      chan struct{}
	seq       uint64
	tools     []*config.ToolMetadata
	err       error
	published bool
	// capture is the flight's inventory capture, stamped when the leader acquires the list and before the upstream
	// request. Immutable after acquisition.
	capture InventoryCapture
}

// acquireListToolsContext claims the in-progress flag for an upstream ListTools
// call. When successful it allocates a fresh listToolsCall, so any concurrent
// ListTools waiter can block on its done channel and read the result that this
// leader publishes into it. release() must be called exactly once; it cancels
// the timeout and closes the call's done channel so coalesced waiters wake up.
func (mc *Client) acquireListToolsContext(ctx context.Context, timeout time.Duration) (context.Context, func() bool, bool) {
	mc.listToolsMu.Lock()
	if mc.listToolsInProgress {
		mc.listToolsMu.Unlock()
		return nil, nil, false
	}

	mc.listToolsInProgress = true
	mc.listToolsLeaderSeq++
	call := &listToolsCall{done: make(chan struct{}), seq: mc.listToolsLeaderSeq, capture: mc.newCapture()}
	mc.listToolsCur = call
	listCtx, cancel := context.WithTimeout(ctx, timeout)
	mc.listToolsCancel = cancel
	mc.listToolsMu.Unlock()

	release := func() bool {
		cancel()
		mc.listToolsMu.Lock()
		mc.listToolsCancel = nil
		mc.listToolsInProgress = false
		if mc.listToolsCur == call {
			mc.listToolsCur = nil
		}
		close(call.done)
		mc.listToolsMu.Unlock()
		return mc.IsConnected()
	}

	return listCtx, release, true
}

// publishListToolsResult records the outcome of the in-flight upstream
// ListTools call so that coalesced waiters in ListTools() can read it once the
// call's done channel is closed. All call sites that go through
// acquireListToolsContext (ListTools and the tool-count refresh) must publish
// their result; a waiter that finds no published result lists again rather
// than treating it as an empty tool list.
func (mc *Client) publishListToolsResult(tools []*config.ToolMetadata, err error) {
	mc.listToolsMu.Lock()
	if call := mc.listToolsCur; call != nil {
		call.tools = tools
		call.err = err
		call.published = true
	}
	mc.listToolsMu.Unlock()
}

// ListTools retrieves tools with concurrency control and coalesces concurrent
// callers onto a single in-flight upstream call.
func (mc *Client) ListTools(ctx context.Context) ([]*config.ToolMetadata, error) {
	tools, _, err := mc.listTools(ctx, false, 0)
	return tools, err
}

// listTools implements ListToolsCaptured and ListToolsFreshCaptured. With fresh set, an
// in-flight leader numbered at or below entrySeq started before the caller,
// so its result is not accepted. It returns the capture of the flight whose
// result it returns.
func (mc *Client) listTools(ctx context.Context, fresh bool, entrySeq uint64) ([]*config.ToolMetadata, InventoryCapture, error) {
	tools, capture, err := mc.listToolsUnmapped(ctx, fresh, entrySeq)
	if err != nil && mc.retired.Load() {
		// As for a tool call: the instance was retired while the list (led or joined) was in flight. Its gate
		// refused the send or closed the connection under the read, so
		// the error is dialgate.ErrRetired or a bare transport failure.
		// Either way the answer is "unavailable", never a failure that
		// says something about the server.
		mc.logger.Debug("tools/list failed on a retired instance",
			zap.String("server", mc.GetConfig().Name),
			zap.Error(err))
		return nil, InventoryCapture{}, ErrInstanceRetired
	}
	return tools, capture, err
}

func (mc *Client) listToolsUnmapped(ctx context.Context, fresh bool, entrySeq uint64) ([]*config.ToolMetadata, InventoryCapture, error) {
	mc.logger.Debug("🔍 ListTools called",
		zap.String("server", mc.GetConfig().Name),
		zap.String("state", mc.StateManager.GetState().String()),
		zap.Bool("connected", mc.IsConnected()))

	if mc.retired.Load() {
		return nil, InventoryCapture{}, ErrInstanceRetired
	}
	if !mc.IsConnected() {
		mc.logger.Debug("🔍 ListTools rejected - client not connected",
			zap.String("server", mc.GetConfig().Name),
			zap.String("state", mc.StateManager.GetState().String()))
		return nil, InventoryCapture{}, fmt.Errorf("client not connected (state: %s)", mc.StateManager.GetState().String())
	}

	for {
		listCtx, release, ok := mc.acquireListToolsContext(ctx, 30*time.Second)
		if ok {
			mc.listToolsMu.Lock()
			capture := mc.listToolsCur.capture
			mc.listToolsMu.Unlock()
			tools, err := mc.runListToolsAsLeader(listCtx, release)
			if err != nil {
				return nil, InventoryCapture{}, err
			}
			return tools, capture, nil
		}

		mc.listToolsMu.Lock()
		call := mc.listToolsCur
		inProgress := mc.listToolsInProgress
		mc.listToolsMu.Unlock()

		if !inProgress || call == nil {
			// The holder released between the failed acquire and this re-check:
			// try to become the leader again.
			continue
		}

		mc.logger.Debug("🔍 ListTools already in progress, waiting for shared result",
			zap.String("server", mc.GetConfig().Name))

		select {
		case <-ctx.Done():
			return nil, InventoryCapture{}, ctx.Err()
		case <-call.done:
			if fresh && call.seq <= entrySeq {
				// That request predates the caller: list again.
				continue
			}
			mc.listToolsMu.Lock()
			res, err, published, capture := call.tools, call.err, call.published, call.capture
			mc.listToolsMu.Unlock()
			if !published {
				// The leader ended without a result (it never reached the
				// upstream call): that is not an empty tool list. List again.
				continue
			}
			if err != nil {
				return nil, InventoryCapture{}, fmt.Errorf("ListTools failed: %w", err)
			}
			return res, capture, nil
		}
	}
}

// runListToolsAsLeader performs the upstream ListTools call as the elected
// leader and publishes the result before release() closes the wait channel,
// so coalesced waiters always see a consistent result.
func (mc *Client) runListToolsAsLeader(listCtx context.Context, release func() bool) ([]*config.ToolMetadata, error) {
	defer func() {
		if release() {
			mc.logger.Debug("🔍 ListTools operation completed, flag reset",
				zap.String("server", mc.GetConfig().Name))
		} else {
			mc.logger.Debug("🔍 ListTools operation completed while disconnected",
				zap.String("server", mc.GetConfig().Name))
		}
	}()

	listEpoch := mc.connectionEpoch.Load()
	tools, err := mc.listToolsUpstream(listCtx)
	mc.publishListToolsResult(tools, err)

	if err != nil {
		// Debug for the same reason as the core layer: this is the middle of
		// three logs of one failure. A ListTools miss that matters is either
		// re-logged by the caller or promoted by the isConnectionError branch
		// just below, which raises a Warn AND moves the state machine.
		mc.logger.Debug("ListTools operation failed",
			zap.String("server", mc.GetConfig().Name),
			zap.Error(err))

		// A retired instance's failure says nothing about the server: it
		// moves no state machine.
		if !mc.retired.Load() && mc.isConnectionError(err) {
			mc.logger.Warn("Connection error detected during ListTools, updating server state",
				zap.String("server", mc.GetConfig().Name),
				zap.Error(err))
			// Guarded like the call and health paths: only the connection
			// that produced the failure may be marked, once.
			mc.setErrorIfCurrentConnection(err, listEpoch)
		}
		return nil, fmt.Errorf("ListTools failed: %w", err)
	}

	mc.setToolCountCache(len(tools))
	return tools, nil
}

// ErrConnectionGenerationChanged is CallToolOnEpoch's refusal: the client's
// connection generation is no longer the one the caller certified the tool
// identity against, so the call was NOT sent. It is returned verbatim (never
// wrapped with server context) so the dispatch paths can map it onto their
// own unresolved-identity refusal with errors.Is.
var ErrConnectionGenerationChanged = errors.New("connection generation changed since the tool identity was resolved")

// CallTool executes a tool with error handling
func (mc *Client) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	return mc.callTool(ctx, toolName, args, nil)
}

func (mc *Client) CallToolOnEpoch(ctx context.Context, toolName string, args map[string]interface{}, expectedEpoch int64) (*mcp.CallToolResult, error) {
	return mc.callTool(ctx, toolName, args, &expectedEpoch)
}

// generationIs reports whether the client is connected on exactly the
// expected generation, read under epochMu so it cannot interleave with
// Connect's or Disconnect's bump.
func (mc *Client) generationIs(expectedEpoch int64) bool {
	mc.epochMu.Lock()
	defer mc.epochMu.Unlock()
	return mc.IsConnected() && mc.connectionEpoch.Load() == expectedEpoch
}

// callTool is the shared body of CallTool and CallToolOnEpoch; expectedEpoch
// nil means unpinned.
func (mc *Client) callTool(ctx context.Context, toolName string, args map[string]interface{}, expectedEpoch *int64) (*mcp.CallToolResult, error) {
	// A pinned call on a moved generation is refused before the connection
	// check: a Disconnect bumps the epoch, so a dropped client fails here
	// too, with the generation verdict rather than the not-connected one.
	if expectedEpoch != nil && !mc.generationIs(*expectedEpoch) {
		return nil, ErrConnectionGenerationChanged
	}
	if mc.retired.Load() {
		return nil, ErrInstanceRetired
	}
	if !mc.IsConnected() {
		return nil, fmt.Errorf("client not connected (state: %s)", mc.StateManager.GetState().String())
	}

	endInFlightCall := mc.beginInFlightCall()
	defer endInFlightCall()

	// Publish-then-revalidate. The FIRST connectivity check
	// above and this registration are two separate reads with a gap between
	// them, so a reconnect could read the counter as 0 in that exact gap and
	// proceed to disconnect before the registration lands. Re-checking here,
	// immediately after registering, closes it: either a reconnect's guard
	// runs AFTER this registration (sees this call, defers per
	// guardReconnectAgainstInFlightCall) or it ran BEFORE (this re-check then
	// observes the state it already set, e.g. StateError -- the controller
	// never runs while still Ready, so the transition always precedes it).
	// No mutex is held across Disconnect() or the transport to get this.
	if expectedEpoch != nil {
		if !mc.generationIs(*expectedEpoch) {
			return nil, ErrConnectionGenerationChanged
		}
	} else if !mc.IsConnected() {
		return nil, fmt.Errorf("client not connected (state: %s)", mc.StateManager.GetState().String())
	}

	// Charge transport failures to the connection generation that dispatched the call.
	callEpoch := mc.connectionEpoch.Load()
	result, err := mc.callToolWithSession(core.WithConnectionGeneration(ctx, callEpoch), toolName, args, expectedEpoch)
	if err != nil && mc.retired.Load() {
		// The instance was retired while the call was in flight; its gate closed the connection or refused the
		// send. Unavailable, and nothing to classify: a retired instance's
		// failure says nothing about the server's health.
		mc.logger.Debug("Tool call failed on a retired instance",
			zap.String("server", mc.GetConfig().Name),
			zap.String("tool", toolName),
			zap.Error(err))
		return nil, ErrInstanceRetired
	}
	if err != nil {
		if errors.Is(err, ErrSessionReestablished) || errors.Is(err, ErrConnectionGenerationChanged) {
			// The session was re-established and the call was deliberately
			// not repeated. Not a connection failure.
			mc.logger.Warn("Tool call not repeated after upstream session re-initialize",
				zap.String("server", mc.GetConfig().Name),
				zap.String("tool", toolName),
				zap.Error(err))
			return nil, err
		}
		mc.recordCallToolOAuthSignal(toolName, err)
		// A 429 answered to a tools/call is the same instruction as one answered
		// to connect. Recording it here does NOT mark the server
		// unhealthy — the classification below owns that — it only makes sure
		// the hint survives into the state machine, where the reconnect gates
		// can honour it if this upstream later needs redialing.
		mc.syncRetryAfterFromTransport()
		// A canceled or timed-out CALL is not a dead SERVER. SetError
		// flips the whole upstream to Error and burns a retry, evicting it for
		// every other client — so only hard evidence of a broken transport may
		// take that path. Classify, most-specific first.
		switch {
		case ctx.Err() != nil:
			// The caller itself went away (HTTP client disconnect, per-request
			// deadline). Call-scoped by definition — never touch server state.
			mc.logger.Warn("Tool call canceled/deadline by caller; not marking server unhealthy",
				zap.String("server", mc.GetConfig().Name),
				zap.String("tool", toolName),
				zap.Error(ctx.Err()))

		case !mc.isConnectionError(err):
			// Log non-connection errors at error level
			mc.logger.Error("Tool call failed",
				zap.String("server", mc.GetConfig().Name),
				zap.String("tool", toolName),
				zap.Error(err))

		case isAmbiguousCancellationError(err):
			// Cancellation surfaced from inside the transport while our caller
			// context is still live (mcp-go internals, an HTTP client timeout,
			// or remote error text). It could be a dead server or just a dropped
			// request — probe asynchronously instead of evicting on a guess.
			mc.logger.Warn("Tool call failed with an ambiguous cancellation; probing server liveness before marking it unhealthy",
				zap.String("server", mc.GetConfig().Name),
				zap.String("tool", toolName),
				zap.Error(err))
			mc.probeAfterAmbiguousCallError(err)

		default:
			// Hard evidence (connection refused/reset, broken pipe, dial i/o
			// timeout…): the transport is genuinely broken.
			if mc.isNormalReconnectionError(err) {
				mc.logger.Warn("Tool call failed due to connection loss, will attempt reconnection",
					zap.String("server", mc.GetConfig().Name),
					zap.String("tool", toolName),
					zap.String("error_type", "normal_reconnection"),
					zap.Error(err))
			} else {
				mc.logger.Error("Tool call failed with connection error",
					zap.String("server", mc.GetConfig().Name),
					zap.String("tool", toolName),
					zap.Error(err))
			}
			mc.recordDeadConnection(toolName, err, callEpoch)
		}
		return nil, err
	}

	// A successful call means OAuth (if it was ever required at call time) is now
	// satisfied — clear the Sign-in CTA flag.
	if mc.oauthCallRequired.CompareAndSwap(true, false) {
		mc.logger.Info("🔓 Tool call succeeded; clearing OAuth Sign-in CTA flag",
			zap.String("server", mc.GetConfig().Name))
	}

	return result, nil
}

func (mc *Client) recordDeadConnection(toolName string, err error, callEpoch int64) {
	if !mc.setErrorIfCurrentConnection(err, callEpoch) {
		mc.logger.Debug("Tool call connection failure belongs to a connection that is already gone; not re-marking",
			zap.String("server", mc.GetConfig().Name),
			zap.String("tool", toolName),
			zap.Error(err))
	}
}

// setErrorIfCurrentConnection applies SetError(err) only while the client is
// still Ready on connection generation epoch, serialized with Connect and
// Disconnect through epochMu. It reports whether the error was recorded.
//
// Both the tools/call path and the health loop use it: Disconnect closes the
// transport (every in-flight request then fails with "transport closed")
// BEFORE it bumps the epoch and Resets the state, so a verdict computed from
// that failure must not land on the freshly Disconnected client.
func (mc *Client) setErrorIfCurrentConnection(err error, epoch int64) bool {
	mc.epochMu.Lock()
	defer mc.epochMu.Unlock()
	// A retired instance's failure says nothing about the server: retirement
	// kills its transport under any
	// request in flight, and the epoch does not move until a teardown runs.
	if mc.retired.Load() || !mc.IsConnected() || mc.connectionEpoch.Load() != epoch {
		return false
	}
	mc.StateManager.SetError(err)
	return true
}

// recordCallToolOAuthSignal inspects a failed tools/call error and, when it is an
// "authorization required" / 401 from an otherwise-connected server (NOT a
// connection error), flags the server as needing OAuth sign-in. This covers
// endpoints that connect + list tools anonymously but enforce OAuth only at
// call time (e.g. Google's sqladmin MCP). The flag drives a proactive Sign-in
// CTA via the health calculator.
func (mc *Client) recordCallToolOAuthSignal(toolName string, err error) {
	if err == nil || mc.isConnectionError(err) {
		return
	}
	if !mc.isOAuthAuthorizationRequired(err) && !mc.isOAuthError(err) {
		return
	}
	if mc.oauthCallRequired.CompareAndSwap(false, true) {
		mc.logger.Info("🔐 Tool call requires OAuth sign-in; flagging server for Sign-in CTA",
			zap.String("server", mc.GetConfig().Name),
			zap.String("tool", toolName))
	}
}

// isAmbiguousCancellationError reports whether a tools/call error is a
// cancellation/deadline signal rather than hard evidence that the transport is
// broken.
//
// These arrive with a live caller context — mcp-go cancelling internally, the
// HTTP client's own timeout firing, or the remote echoing cancellation text —
// so they say nothing definitive about the server's health. isConnectionError
// also matches them by substring; ListTools, the health loop and reconnect rely
// on that broader matching.
func isAmbiguousCancellationError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	errStr := err.Error()
	// "cancelled" (British) is how mcp-go's sse.go spells it; the plain-text
	// forms cover errors whose chain was stripped before reaching us.
	for _, marker := range []string{"context canceled", "context cancelled", "context deadline exceeded"} {
		if strings.Contains(errStr, marker) {
			return true
		}
	}
	return false
}

// probeAfterAmbiguousCallError fires one bounded liveness probe after an
// ambiguous tools/call cancellation and classifies the outcome:
//
//   - healthy probe → the call was merely canceled; the server is left untouched.
//   - transient probe failure (deadline exceeded on the ping, momentary
//     overload, or a non-connection error such as an upstream without `ping`)
//     → nothing happens here. A busy upstream is precisely what produces slow
//     calls and ambiguous cancellations, so a single missed 5s ping is not
//     eviction-grade evidence; the background health loop owns transient
//     failures and its healthCheckFailureThreshold consecutive-miss budget.
//   - hard probe failure (connection refused/reset, broken pipe, no such
//     host…) on the SAME connection generation → Error.
//
// Runs asynchronously so the tool call returns immediately, and is gated to a
// single in-flight probe so a burst of canceled calls cannot stampede the
// upstream. The connection epoch captured before launching guards against a
// disconnect+reconnect completing mid-probe: a verdict about a superseded
// session is dropped rather than applied to the fresh one.
func (mc *Client) probeAfterAmbiguousCallError(cause error) {
	if mc.retired.Load() {
		return
	}
	if !mc.ambiguousProbeInFlight.CompareAndSwap(false, true) {
		return
	}

	epoch := mc.connectionEpoch.Load()

	go func() {
		defer mc.ambiguousProbeInFlight.Store(false)

		if !mc.IsConnected() || mc.connectionEpoch.Load() != epoch {
			return
		}

		// Derived from Background: the caller context that produced the
		// ambiguous error is very likely already dead.
		ctx, cancel := context.WithTimeout(context.Background(), ambiguousProbeTimeout)
		defer cancel()

		err := mc.coreClient.Ping(ctx)
		if err == nil {
			mc.logger.Debug("Liveness probe after canceled tool call succeeded; server left untouched",
				zap.String("server", mc.GetConfig().Name),
				zap.NamedError("call_error", cause))
			return
		}

		// Only hard transport evidence evicts. Transient failures are the
		// background health loop's business — it counts consecutive misses
		// instead of acting on one. consecutiveHealthFailures is deliberately
		// NOT touched here: it is only synchronized for the monitor goroutine.
		if !mc.isConnectionError(err) || isTransientHealthCheckError(err) {
			mc.logger.Info("Liveness probe after canceled tool call failed transiently; leaving state to the background health loop",
				zap.String("server", mc.GetConfig().Name),
				zap.NamedError("call_error", cause),
				zap.Error(err))
			return
		}

		// The state may have moved on while the probe was in flight (disconnect,
		// or a full reconnect that opened a new connection generation) — this
		// verdict describes a session nobody is using any more. epochMu pairs
		// this check-and-SetError with Connect's bump-and-Ready so a reconnect
		// cannot complete between the check and the verdict.
		mc.epochMu.Lock()
		defer mc.epochMu.Unlock()
		// A retirement while the probe was in flight closed its transport:
		// no verdict.
		if mc.retired.Load() || !mc.IsConnected() || mc.connectionEpoch.Load() != epoch {
			mc.logger.Debug("Liveness probe after canceled tool call failed, but the connection it observed is gone; skipping",
				zap.String("server", mc.GetConfig().Name),
				zap.Error(err))
			return
		}

		mc.logger.Warn("Liveness probe after canceled tool call failed with hard connection evidence; marking server unhealthy",
			zap.String("server", mc.GetConfig().Name),
			zap.NamedError("call_error", cause),
			zap.Error(err))
		mc.StateManager.SetError(err)
	}()
}

func (mc *Client) cancelInFlightListTools() {
	mc.listToolsMu.Lock()
	cancel := mc.listToolsCancel
	inProgress := mc.listToolsInProgress
	mc.listToolsMu.Unlock()

	if !inProgress || cancel == nil {
		return
	}

	mc.logger.Debug("Cancelling in-flight ListTools operation",
		zap.String("server", mc.GetConfig().Name))

	cancel()

	deadline := time.Now().Add(500 * time.Millisecond)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		<-ticker.C
		mc.listToolsMu.Lock()
		done := !mc.listToolsInProgress
		mc.listToolsMu.Unlock()
		if done {
			return
		}
	}

	mc.logger.Debug("Timed out waiting for ListTools operation to cancel",
		zap.String("server", mc.GetConfig().Name))
}

// cancelInFlightConnect cancels any in-flight Connect() operation.
// This is called from Disconnect() BEFORE acquiring mc.mu, so Disconnect()
// doesn't block on a Connect() that holds mc.mu during slow OAuth flows.
func (mc *Client) cancelInFlightConnect() {
	mc.connectMu.Lock()
	cancel := mc.connectCancel
	mc.connectMu.Unlock()

	if cancel == nil {
		return
	}

	mc.logger.Debug("Cancelling in-flight Connect operation",
		zap.String("server", mc.GetConfig().Name))
	cancel()
}

// logSafeURL renders the configured upstream URL for a LOG FIELD, with its
// credential-bearing query parameters and any userinfo password masked.
//
// Server logs are readable by MCP callers through `upstream_servers tail_log`,
// so a URL carrying `?token=…` must never be logged verbatim. Scheme, host,
// path and non-sensitive parameters survive, so the "your URL needs updating"
// advice stays actionable.
func (mc *Client) logSafeURL() string {
	cfg := mc.GetConfig()
	if cfg == nil {
		return ""
	}
	return oauth.LogSafeURL(cfg.URL)
}

// onStateChange handles state transition events
func (mc *Client) onStateChange(oldState, newState types.ConnectionState, info *types.ConnectionInfo) {
	mc.logger.Info("State transition",
		zap.String("from", oldState.String()),
		zap.String("to", newState.String()),
		zap.String("server", mc.GetConfig().Name))

	// Handle error states with appropriate log levels
	if newState == types.StateError && info.LastError != nil {
		// Check for deprecated endpoint errors first - these require URL changes, not reconnection
		if mc.isDeprecatedEndpointError(info.LastError) {
			mc.logger.Error("⚠️ ENDPOINT DEPRECATED: Server URL needs to be updated",
				zap.String("server", mc.GetConfig().Name),
				zap.String("current_url", mc.logSafeURL()),
				zap.String("error_type", "endpoint_deprecated"),
				zap.String("action", "Update the server URL in your configuration"),
				zap.String("hint", "The server may have migrated from /sse to /mcp - check the server's documentation"),
				zap.Error(info.LastError))
			return // Don't log as normal reconnection error
		}

		if mc.isNormalReconnectionError(info.LastError) {
			mc.logger.Warn("Connection error, will attempt automatic reconnection",
				zap.String("server", mc.GetConfig().Name),
				zap.String("error_type", "normal_reconnection"),
				zap.Error(info.LastError),
				zap.Int("retry_count", info.RetryCount))
		} else {
			mc.logger.Error("Connection error",
				zap.String("server", mc.GetConfig().Name),
				zap.Error(info.LastError),
				zap.Int("retry_count", info.RetryCount))
		}
	}
}

// startBackgroundMonitoring starts monitoring the connection health.
//
// Reconnecting the core transport must not start a second health loop.
func (mc *Client) startBackgroundMonitoring() {
	if mc.monitoringStarted {
		return
	}

	// Mark that monitoring has been started
	mc.monitoringStarted = true
	mc.monitoringWG.Add(1)
	go func() {
		defer mc.monitoringWG.Done()
		mc.backgroundHealthCheck()
	}()
}

// stopBackgroundMonitoring stops the background monitoring
func (mc *Client) stopBackgroundMonitoring() {
	// Only proceed if monitoring was actually started
	if !mc.monitoringStarted {
		mc.logger.Debug("Background monitoring was never started, skipping stop",
			zap.String("server", mc.GetConfig().Name))
		return
	}

	close(mc.stopMonitoring)

	// Use a timeout for the wait to prevent hanging during shutdown
	done := make(chan struct{})
	go func() {
		mc.monitoringWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		mc.logger.Debug("Background monitoring stopped successfully",
			zap.String("server", mc.GetConfig().Name))
	case <-time.After(1 * time.Second):
		mc.logger.Warn("Background monitoring stop timed out after 1s, forcing shutdown",
			zap.String("server", mc.GetConfig().Name))
	}

	mc.monitoringStarted = false

	// Recreate the channel for potential reuse
	mc.stopMonitoring = make(chan struct{})
}

// healthCheckDisabledRecheckInterval is how long the health loop sleeps between
// re-checks when probing is disabled (resolved interval <= 0). It is NOT a
// probe — it just lets a later config hot-reload re-enable the loop without a
// restart.
const healthCheckDisabledRecheckInterval = 30 * time.Second

// GetGlobalConfig returns the current proxy-wide config snapshot (may be nil).
// Lock-free; safe to call whether or not mc.mu
// is held.
func (mc *Client) GetGlobalConfig() *config.Config {
	return mc.globalConfig.Load()
}

// SetGlobalConfig swaps the proxy-wide config the background loops re-resolve
// against. Called on a config hot-reload (via Manager.SetGlobalConfig) so the
// resettable health-check timer picks up a new global interval without a restart
// Lock-free atomic swap.
func (mc *Client) SetGlobalConfig(cfg *config.Config) {
	mc.globalConfig.Store(cfg)
}

// resolveHealthCheckInterval resolves this server's effective health-check
// interval (global → built-in default). A nil globalConfig falls back to the
// built-in default.
func (mc *Client) resolveHealthCheckInterval() time.Duration {
	cfg := mc.globalConfig.Load()
	if cfg == nil {
		cfg = &config.Config{}
	}
	return cfg.ResolveHealthCheckInterval()
}

// planHealthCheckCycle decides one iteration of the health loop: whether to
// probe and how long to wait first. A positive interval probes on that cadence;
// a non-positive interval disables probing and waits the re-check window.
func planHealthCheckCycle(interval time.Duration) (probe bool, wait time.Duration) {
	if interval <= 0 {
		return false, healthCheckDisabledRecheckInterval
	}
	return true, interval
}

// backgroundHealthCheck performs periodic health checks. The interval is
// re-resolved every cycle from config, so a hot-reload changes the cadence (or
// disables the loop entirely) without restarting the server.
func (mc *Client) backgroundHealthCheck() {
	for {
		probe, wait := planHealthCheckCycle(mc.resolveHealthCheckInterval())
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			if probe {
				mc.performHealthCheck()
			}
		case <-mc.stopMonitoring:
			timer.Stop()
			mc.logger.Debug("Background health monitoring stopped",
				zap.String("server", mc.GetConfig().Name))
			return
		}
	}
}

// performHealthCheck checks if the connection is still healthy and attempts reconnection if needed
func (mc *Client) performHealthCheck() {
	// A retired instance has no health to report: its probes are refused by its gate, and their failure must not drive
	// a reconnect or an Error state.
	if mc.retired.Load() {
		return
	}
	// Skip all health/reconnect work when user explicitly logged out
	if mc.IsUserLoggedOut() {
		mc.logger.Debug("Health check skipped - user explicitly logged out",
			zap.String("server", mc.GetConfig().Name))
		return
	}

	// Skip health checks if not connected
	if !mc.IsConnected() {
		return
	}

	// Create a short timeout for health check
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Probe liveness with the MCP-standard lightweight `ping` rather than
	// re-listing every tool, so health checks add no background `tools/list`
	// traffic.
	// The generation this ping runs on: a Disconnect racing the ping closes
	// the transport (the ping fails with "transport closed") and only then
	// bumps the epoch, so the verdict below must land on this generation only.
	pingEpoch := mc.connectionEpoch.Load()
	err := mc.probeLiveness(ctx)

	// Retired while the ping was in flight: its failure (the retirement closed the transport) is no
	// health evidence, and neither is a success. No accounting either way.
	if mc.retired.Load() {
		return
	}

	if err != nil {
		// Pick up any rate-limit hint this ping's response carried, BEFORE the
		// classification below. A 429 does not read as a connection
		// error — isConnectionError matches transport-level failures, not HTTP
		// statuses — so gating the sync on that branch would make it
		// unreachable for exactly the case it exists for. Recording the window
		// does not change the health verdict; it only stops the automatic
		// reconnect paths from returning before the upstream said we may.
		mc.syncRetryAfterFromTransport()

		// Only mark as error if it's a real connection issue, not timeout during high activity
		if mc.isConnectionError(err) {
			// A transient ping failure while a tool call is in flight
			// is fully explained by the upstream serving one JSON-RPC
			// exchange at a time on the same stdio process — it is not
			// evidence the connection is dead. Don't let it accumulate
			// toward the eviction threshold; the in-flight call's own
			// CallToolTimeout already bounds how long this can mask a truly
			// dead upstream. Hard failures (connection refused/reset, broken
			// pipe) still evict immediately below, in-flight call or not —
			// that IS real evidence.
			if isTransientHealthCheckError(err) {
				elapsed, withinCap, inFlight := mc.trackInFlightSuppression()
				switch {
				case inFlight && withinCap:
					mc.logger.Info("Health check ping failed while a tool call is in flight, tolerating",
						zap.String("server", mc.GetConfig().Name),
						zap.Duration("suppressed_for", elapsed),
						zap.Error(err))
					return
				case inFlight:
					mc.logger.Warn("In-flight suppression cap exceeded; resuming normal eviction accounting despite in-flight call",
						zap.String("server", mc.GetConfig().Name),
						zap.Duration("suppressed_for", elapsed),
						zap.Duration("cap", inFlightSuppressionCap),
						zap.Error(err))
					// Deliberately fall through to the normal accounting
					// below WITHOUT resetting the suppression clock -- a
					// still-busy upstream must not re-enter tolerance
					// mid-streak once the cap trips (that would defeat the
					// cap). The clock only resets once inFlightCalls reaches
					// 0 (beginInFlightCall's end(), or below on success).
				}
			} else {
				mc.resetInFlightSuppression()
			}
			if mc.recordHealthCheckFailure(err) {
				if mc.setErrorIfCurrentConnection(err, pingEpoch) {
					mc.logger.Warn("Health check failed repeatedly, marking as error",
						zap.String("server", mc.GetConfig().Name),
						zap.Int("consecutive_failures", mc.consecutiveHealthFailures),
						zap.Error(err))
				} else {
					mc.logger.Debug("Health check failure belongs to a connection that is already gone; not marking",
						zap.String("server", mc.GetConfig().Name),
						zap.Error(err))
				}
			} else {
				mc.logger.Info("Health check failed transiently, tolerating below threshold",
					zap.String("server", mc.GetConfig().Name),
					zap.Int("consecutive_failures", mc.consecutiveHealthFailures),
					zap.Int("threshold", healthCheckFailureThreshold),
					zap.Error(err))
			}
		} else {
			mc.logger.Debug("Health check failed with timeout (high activity), ignoring",
				zap.String("server", mc.GetConfig().Name),
				zap.Error(err))
		}
		return
	}

	mc.resetInFlightSuppression()
	mc.recordHealthCheckSuccess()
	mc.logger.Debug("Health check passed successfully",
		zap.String("server", mc.GetConfig().Name))
}

// syncRetryAfterFromTransport copies any rate-limit hint the transport
// RoundTripper recorded for this upstream into the state machine, where both
// reconnect gates can see it.
//
// It is called from every path that turns an upstream failure into Error state,
// not just connect: a 429 answered to a tools/call or to the health-check ping
// is just as much a "come back later" as one answered to initialize, and the
// recorder is the only place that hint exists — mcp-go has already flattened
// the response into a string by the time the error reaches us.
func (mc *Client) syncRetryAfterFromTransport() {
	deadline := mc.coreClient.RetryAfterDeadline()
	if deadline.IsZero() {
		return
	}
	mc.logger.Warn("Upstream rate-limited us; parking reconnects until it says we may return",
		zap.String("server", mc.GetConfig().Name),
		zap.Time("retry_not_before", deadline),
		zap.Duration("retry_after", time.Until(deadline)))
	mc.StateManager.SetRetryAfter(deadline)
}

// recordHealthCheckFailure increments the consecutive-failure counter and
// returns whether the caller should now flip the state machine to Error.
//
// Transient errors (timeout, deadline exceeded, context canceled) need
// healthCheckFailureThreshold consecutive misses before they're considered a
// real outage — slow upstreams under load routinely miss a single 5-second
// health-check window without actually being down. Hard failures (connection
// refused, host unreachable, DNS gone) trigger Error immediately because
// waiting buys nothing.
func (mc *Client) recordHealthCheckFailure(err error) bool {
	mc.consecutiveHealthFailures++
	if !isTransientHealthCheckError(err) {
		return true
	}
	return mc.consecutiveHealthFailures >= healthCheckFailureThreshold
}

// recordHealthCheckSuccess resets the consecutive-failure counter. One good
// check is enough to wipe the slate — we're not trying to track flap
// frequency, just preventing single misses from looking like outages.
func (mc *Client) recordHealthCheckSuccess() {
	mc.consecutiveHealthFailures = 0
}

// resetHealthCheckFailures clears the counter. Called from the connect
// success path so a successful reconnect doesn't carry stale failure debt
// from before the disconnect.
func (mc *Client) resetHealthCheckFailures() {
	mc.consecutiveHealthFailures = 0
}

// isTransientHealthCheckError identifies failure modes that warrant
// flap-resistance — slow upstream / momentary timeout — vs. hard failures
// that should surface to the user immediately.
func isTransientHealthCheckError(err error) bool {
	if err == nil {
		return false
	}
	// A dead transport (the stdio child exited, its pipes closed) never
	// recovers by waiting: every later request fails the same way until the
	// process is respawned.
	if isDeadTransportError(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Hard failures: short-circuit to "not transient" so the caller flips
	// Error on the first miss. Order matters — check these BEFORE the
	// generic timeout heuristics below.
	switch {
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no such host"),
		strings.Contains(msg, "network is unreachable"),
		strings.Contains(msg, "no route to host"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "broken pipe"),
		strings.Contains(msg, "econnrefused"):
		return false
	}
	// Soft failures: short-window misses we want to tolerate.
	switch {
	case strings.Contains(msg, "deadline exceeded"),
		strings.Contains(msg, "timeout"),
		strings.Contains(msg, "context canceled"):
		return true
	}
	return false
}

// RefreshOAuthTokenDirect forces an OAuth token refresh without reconnecting.
// This delegates to the core client's direct refresh implementation.
// Used by the RefreshManager for proactive token refresh before expiration.
func (mc *Client) RefreshOAuthTokenDirect(ctx context.Context) error {
	if mc == nil {
		return fmt.Errorf("client not initialized")
	}
	// A retired instance runs no refresh.
	if mc.retired.Load() || mc.unregistered.Load() {
		return ErrInstanceRetired
	}
	return mc.coreClient.RefreshOAuthTokenDirect(ctx)
}

// SetCredentialBinding binds the instance's OAuth state to the committed
// credential binding b of the view it was built for. The upstream manager
// calls it before the instance becomes reachable.
func (mc *Client) SetCredentialBinding(b commitview.CredentialBinding) {
	if mc == nil {
		return
	}
	mc.coreClient.SetCredentialBinding(b)
}

// CredentialBinding returns the instance's credential binding (zero when
// unbound).
func (mc *Client) CredentialBinding() commitview.CredentialBinding {
	if mc == nil {
		return commitview.CredentialBinding{}
	}
	return mc.coreClient.CredentialBinding()
}

// Gate returns the instance's retirement barrier. The upstream manager shares
// it with the transient clients of a manual OAuth flow.
func (mc *Client) Gate() *dialgate.Gate {
	if mc == nil {
		return nil
	}
	return mc.coreClient.Gate()
}

// isDeadTransportError reports whether err proves the transport itself is
// closed, as opposed to one request failing on a live connection. When a stdio
// upstream's child process dies, mcp-go's stdio reader hits EOF and closes the
// transport, and every later request returns transport.ErrTransportClosed
// ("transport closed"); it must count as a connection error so the server
// leaves Ready.
//
// The classifier is shared by every protocol, so it only accepts evidence that
// the LOCAL end of the transport is closed:
//   - io.EOF / io.ErrUnexpectedEOF are deliberately NOT matched. net/http wraps
//     them for a single truncated or reset response on a live HTTP/SSE
//     upstream (LB idle timeout, keep-alive race), which must stay subject to
//     the normal flap tolerance. A stdio child's death never surfaces as them:
//     mcp-go's stdio reader turns its EOF into ErrTransportClosed.
//   - The text fallbacks (for chains flattened before they reach us) only
//     count after mcp-go's "transport error: " prefix, i.e. text produced by
//     transport.Error — never a live server's JSON-RPC error message, which
//     mcp-go returns unwrapped and which may echo errno text such as
//     "file already closed".
func isDeadTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, mcptransport.ErrTransportClosed) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, os.ErrClosed) {
		return true
	}
	// Text fallback, for chains that were flattened into a string: only the
	// payload of mcp-go's own transport wrapper counts — either the typed
	// *transport.Error, or a message that STARTS with its prefix. A server's
	// JSON-RPC error is surfaced unwrapped (errors.New(message)), so text it
	// supplies can only match if it is the entire message, not by containing
	// the phrase somewhere.
	var transportMsg string
	var tErr *mcptransport.Error
	if errors.As(err, &tErr) && tErr.Err != nil {
		transportMsg = tErr.Err.Error()
	} else {
		const transportPrefix = "transport error: "
		msg := err.Error()
		if !strings.HasPrefix(msg, transportPrefix) {
			return false
		}
		transportMsg = strings.TrimPrefix(msg, transportPrefix)
	}
	for _, marker := range []string{
		mcptransport.ErrTransportClosed.Error(), // "transport closed"
		os.ErrClosed.Error(),                    // "file already closed"
		io.ErrClosedPipe.Error(),                // "io: read/write on closed pipe"
	} {
		if strings.Contains(transportMsg, marker) {
			return true
		}
	}
	return false
}

// isConnectionError checks if an error indicates a connection problem
func (mc *Client) isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if isDeadTransportError(err) {
		return true
	}

	errStr := err.Error()
	connectionErrors := []string{
		"connection refused",
		"no such host",
		"connection reset",
		"broken pipe",
		"network is unreachable",
		"timeout",
		"deadline exceeded",
		"context canceled",
		// SSE and HTTP transport specific errors
		"terminated",
		"fetch failed",
		"TypeError",
		"ECONNREFUSED",
		"SSE stream disconnected",
		"stream disconnected",
		"Failed to reconnect SSE stream",
		"Maximum reconnection attempts",
		"connect ECONNREFUSED",
	}

	for _, connErr := range connectionErrors {
		if strings.Contains(errStr, connErr) {
			return true
		}
	}

	return false
}

// isOAuthAuthorizationRequired checks if OAuth authorization is needed (not an error)
func (mc *Client) isOAuthAuthorizationRequired(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	authRequiredErrors := []string{
		"OAuth authorization during MCP init failed",
		"OAuth authorization not implemented",
		"OAuth authorization required",
		"authorization required",
	}

	for _, authErr := range authRequiredErrors {
		if strings.Contains(errStr, authErr) {
			return true
		}
	}

	return false
}

// isTokenRefreshScenario checks if we're in a token refresh scenario vs full re-auth.
// Returns true if we have a refresh token available but need new access token.
func (mc *Client) isTokenRefreshScenario(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	// Token refresh scenarios typically involve expired access tokens
	// but still having a valid refresh token
	tokenRefreshIndicators := []string{
		"token expired",
		"access_token expired",
		"token refresh",
		"refresh_token",
		"automatic refresh",
	}

	for _, indicator := range tokenRefreshIndicators {
		if strings.Contains(errStr, indicator) {
			mc.logger.Debug("🔄 Detected token refresh scenario",
				zap.String("server", mc.GetConfig().Name),
				zap.String("indicator", indicator))
			return true
		}
	}

	return false
}

// isOAuthError checks if the error is OAuth-related (actual authentication failure)
func (mc *Client) isOAuthError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	oauthErrors := []string{
		"invalid_token",
		"invalid_grant",
		"access_denied",
		"unauthorized",
		"401", // HTTP 401 Unauthorized
		"Missing or invalid access token",
		"OAuth authentication failed",
		"oauth timeout",
		"oauth error",
	}

	for _, oauthErr := range oauthErrors {
		if strings.Contains(errStr, oauthErr) {
			return true
		}
	}

	return false
}

// isNormalReconnectionError checks if error is part of normal reconnection flow
func (mc *Client) isNormalReconnectionError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	normalReconnectionErrors := []string{
		"SSE stream disconnected",
		"stream disconnected",
		"terminated",
		"fetch failed",
		"Failed to reconnect SSE stream",
		"Maximum reconnection attempts",
		"TypeError: terminated",
		"OAuth authorization required",
		"authentication strategies failed",
	}

	for _, reconnErr := range normalReconnectionErrors {
		if strings.Contains(errStr, reconnErr) {
			return true
		}
	}

	return false
}

// isDeprecatedEndpointError checks if error indicates a deprecated/removed endpoint (HTTP 410 Gone)
// This helps detect when an MCP server has migrated to a new endpoint URL
func (mc *Client) isDeprecatedEndpointError(err error) bool {
	if err == nil {
		return false
	}

	// Check for transport.ErrEndpointDeprecated type first
	if transport.IsEndpointDeprecatedError(err) {
		return true
	}

	errStr := strings.ToLower(err.Error())
	deprecationIndicators := []string{
		"410",                            // HTTP 410 Gone
		"gone",                           // Status text
		"deprecated",                     // Common migration message
		"removed",                        // Endpoint removed
		"no longer supported",            // Common deprecation message
		"use the http transport",         // Sentry-specific migration hint
		"sse transport has been removed", // Sentry-specific error
		"endpoint deprecated",            // Our custom error message
	}

	for _, indicator := range deprecationIndicators {
		if strings.Contains(errStr, indicator) {
			return true
		}
	}

	return false
}

// GetCachedToolCountNonBlocking returns the cached tool count without any blocking calls
// Returns 0 if cache is not populated yet. Safe to call from SSE/API handlers.
func (mc *Client) GetCachedToolCountNonBlocking() int {
	mc.toolCountMu.RLock()
	count := mc.toolCount
	mc.toolCountMu.RUnlock()
	return count
}

// InvalidateToolCountCache clears the tool count cache
// Should be called when tools are known to have changed
func (mc *Client) InvalidateToolCountCache() {
	mc.toolCountMu.Lock()
	mc.toolCount = 0
	mc.toolCountMu.Unlock()

	mc.logger.Debug("🔍 Tool count cache invalidated",
		zap.String("server", mc.GetConfig().Name))
}

// setToolCountCache records the latest tool count for non-blocking consumers.
func (mc *Client) setToolCountCache(count int) {
	mc.toolCountMu.Lock()
	mc.toolCount = count
	mc.toolCountMu.Unlock()
}

// InstanceID identifies one managed client instance. Zero is never assigned
// by NewClientAt.
type InstanceID uint64

// instanceIDCounter hands out instance ids to every managed client in the
// process; an id is never reused.
var instanceIDCounter atomic.Uint64

// InstanceID returns this instance's process-unique id.
func (mc *Client) InstanceID() InstanceID { return mc.instanceID }

// MarkUnregistered records that the upstream manager removed this instance
// from its registry. Called only by upstream.Manager, under its registry
// locks; from then on Connect refuses with ErrInstanceUnregistered.
func (mc *Client) MarkUnregistered() { mc.unregistered.Store(true) }

// Unregistered reports whether the upstream manager removed this instance
// from its registry.
func (mc *Client) Unregistered() bool { return mc.unregistered.Load() }

// connectionEpochCounter hands out connection epochs to every managed client
// in the process. It is process-wide so an epoch never repeats across client
// instances that share a server name (see Client.connectionEpoch).
var connectionEpochCounter atomic.Int64

// nextConnectionEpoch returns a fresh, strictly increasing epoch.
func nextConnectionEpoch() int64 { return connectionEpochCounter.Add(1) }

// GetServerIcon returns validated artwork from the latest successful handshake.
// Reading it never waits for an upstream connection or an image download.
func (mc *Client) GetServerIcon() string {
	if icon := mc.advertisedIcon.Load(); icon != nil {
		return *icon
	}
	return ""
}

func (mc *Client) SetPeerExecutor(executor peering.Executor) { mc.coreClient.SetPeerExecutor(executor) }
func (mc *Client) PeerRPC(ctx context.Context, request mcptransport.JSONRPCRequest) (*mcptransport.JSONRPCResponse, error) {
	return mc.coreClient.PeerRPC(ctx, request)
}
func (mc *Client) PeerNotify(ctx context.Context, notification mcp.JSONRPCNotification) error {
	return mc.coreClient.PeerNotify(ctx, notification)
}
func (mc *Client) OnPeerNotification(fn func(mcp.JSONRPCNotification)) {
	mc.coreClient.OnPeerNotification(fn)
}

func (mc *Client) SuppressBrowser() { mc.coreClient.SuppressBrowser() }

// PrepareConnectionRetry records a recovery request without dialing. The same
// lock as Connect admission prevents a notification from resetting a connection
// already starting. Only changed peer state invalidates an otherwise ready link.
func (mc *Client) PrepareConnectionRetry(invalidate bool) bool {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if mc.unregistered.Load() || mc.retired.Load() || mc.IsUserLoggedOut() ||
		!mc.GetConfig().Enabled || mc.IsConnecting() || (!invalidate && mc.IsConnected()) {
		return false
	}
	mc.StateManager.ResetForCredentials()
	return true
}
