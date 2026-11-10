package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/runtime/supervisor"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/upstream"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
)

// StartBackgroundInitialization kicks off configuration sync and background loops.
func (r *Runtime) StartBackgroundInitialization() {
	// Start activity service for persisting tool call events
	if r.activityService != nil {
		// The runtime is the event emitter for sensitive data detection events.
		go r.activityService.Start(r.appCtx, r)
		r.logger.Info("Activity service started for event logging")
	}

	// Clean up orphaned OAuth tokens before starting the refresh manager
	// This removes tokens for servers that were deleted while mcpproxy was not running
	if r.storageManager != nil {
		r.mu.RLock()
		validServerNames := make([]string, 0, len(r.cfg.Servers))
		for _, server := range r.cfg.Servers {
			validServerNames = append(validServerNames, server.Name)
		}
		r.mu.RUnlock()

		if deleted, err := r.storageManager.CleanupOrphanedOAuthTokens(validServerNames); err != nil {
			r.logger.Warn("Failed to cleanup orphaned OAuth tokens", zap.Error(err))
		} else if deleted > 0 {
			r.logger.Info("Cleaned up orphaned OAuth tokens during startup",
				zap.Int("deleted", deleted))
		}
	}

	// Start proactive OAuth token refresh manager
	if r.refreshManager != nil {
		r.refreshManager.SetRuntime(r)
		r.refreshManager.SetEventEmitter(r)
		if err := r.refreshManager.Start(r.appCtx); err != nil {
			r.logger.Error("Failed to start OAuth refresh manager", zap.Error(err))
		} else {
			r.logger.Info("OAuth refresh manager started")
		}

		// Register token saved callback to wire PersistentTokenStore -> RefreshManager.
		// The binding the token was saved under goes onto the schedule, so a
		// superseded instance's reactive failure never fails it.
		oauth.GetTokenStoreManager().SetTokenSavedBoundCallback(func(serverName string, expiresAt time.Time, binding commitview.CredentialBinding) {
			if r.ServerHost(serverName) != "" {
				return
			}
			r.logger.Info("OnTokenSaved callback fired - scheduling proactive refresh",
				zap.String("server", serverName),
				zap.Time("expires_at", expiresAt),
				zap.Duration("valid_for", time.Until(expiresAt)),
				zap.String("binding", binding.String()))
			r.refreshManager.OnTokenSavedFor(serverName, expiresAt, binding)
		})
		r.logger.Info("Token saved callback registered for proactive refresh")
	}

	// Start the supervisor for state reconciliation and lock-free reads.
	if r.supervisor != nil {
		r.supervisor.Start()
		r.logger.Info("Supervisor started for state reconciliation")

		// Set up reactive tool discovery callback with deduplication
		r.supervisor.SetOnServerConnectedCallback(func(serverName string) {
			r.onServerConnectedDiscovery(serverName)
		})
		r.logger.Info("Reactive tool discovery callback registered")

		// Subscribe to supervisor events and emit servers.changed for Web UI updates
		go r.supervisorEventForwarder()
	}

	// Set up tool discovery callback on upstream manager for notifications/tools/list_changed
	// This enables reactive tool re-indexing when upstream servers change their available tools
	if r.upstreamManager != nil {
		r.upstreamManager.SetToolDiscoveryCallback(r.onUpstreamToolsListChanged)
		r.logger.Info("Tool discovery callback registered on upstream manager")

	}

	// A rolling call-failure-rate flip changes a server's
	// computed health level with no connection-state event, so nudge the
	// (coalesced) servers.changed stream. The manager already debounces
	// per server.
	if r.upstreamManager != nil {
		r.upstreamManager.SetCallHealthObserver(func(serverName string) {
			r.serversChanged(serverName)
		})
	}

	go r.backgroundInitialization()
}

func (r *Runtime) backgroundInitialization() {
	if r.CurrentPhase() == PhaseInitializing {
		r.UpdatePhase(PhaseLoading, "Loading configuration...")
	} else {
		r.UpdatePhaseMessage("Loading configuration...")
	}

	appCtx := r.AppContext()

	// Start connections from the database-backed snapshot loaded at startup.
	if err := r.bootReconcile(); err != nil {
		r.logger.Error("Failed to load configured servers", zap.Error(err))
		// Keep other runtime services available if connection startup fails.
	}

	// Mark as ready after scheduling server connections.
	switch r.CurrentPhase() {
	case PhaseInitializing, PhaseLoading, PhaseReady:
		r.UpdatePhase(PhaseReady, "Server is ready (upstream servers connecting in background)")
	default:
		r.UpdatePhaseMessage("Server is ready (upstream servers connecting in background)")
	}

	// Start tool indexing with reduced delay
	go r.backgroundToolIndexing(appCtx)

	// Start session inactivity cleanup
	go r.backgroundSessionCleanup(appCtx)
}

// toolDiscoveryDisabledRecheckInterval is how long the indexing loop sleeps
// between re-checks when the periodic sweep is disabled (resolved interval
// <= 0). It is NOT a sweep — connect-time discovery and reactive
// notifications/tools/list_changed still keep the index fresh; this only lets a
// later config hot-reload re-enable the sweep without a restart.
const toolDiscoveryDisabledRecheckInterval = 5 * time.Minute

// planToolDiscoveryCycle decides one iteration of the indexing loop: whether to
// run a periodic sweep and how long to wait first. When at least one server (or
// the global default) has a positive resolved interval the loop ticks at the
// smallest such cadence (tick) and sweeps; when every interval is disabled
// (anyEnabled=false) the loop waits the re-check window without sweeping so a
// later hot-reload can re-enable it.
func planToolDiscoveryCycle(tick time.Duration, anyEnabled bool, disabledRecheck time.Duration) (sweep bool, wait time.Duration) {
	if !anyEnabled || tick <= 0 {
		return false, disabledRecheck
	}
	return true, tick
}

func (r *Runtime) backgroundToolIndexing(ctx context.Context) {
	r.cleanupOrphanedIndexEntries()

	select {
	case <-time.After(2 * time.Second):
		_ = r.DiscoverAndIndexTools(ctx)
	case <-ctx.Done():
		r.logger.Info("Background tool indexing stopped during initial delay")
		return
	}

	// Periodic discovery uses the global cadence.
	for {
		tick := r.Config().ResolveToolDiscoveryInterval()
		anyEnabled := tick > 0
		sweep, wait := planToolDiscoveryCycle(tick, anyEnabled, toolDiscoveryDisabledRecheckInterval)
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			if sweep {
				_ = r.discoverAndIndexTools(ctx, true)
			}
		case <-ctx.Done():
			timer.Stop()
			r.logger.Info("Background tool indexing stopped due to context cancellation")
			return
		}
	}
}

// backgroundSessionCleanup periodically closes sessions that haven't had activity.
// This handles the HTTP transport limitation where OnUnregisterSession is never called.
func (r *Runtime) backgroundSessionCleanup(ctx context.Context) {
	// Session inactivity timeout: 30 minutes
	// MCP clients may have long gaps between tool calls (e.g., user reading results).
	const sessionInactivityTimeout = 30 * time.Minute

	// Check every minute for inactive sessions
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if r.storageManager != nil {
				closedCount, err := r.storageManager.CloseInactiveSessions(sessionInactivityTimeout)
				if err != nil {
					r.logger.Warn("Failed to close inactive sessions", zap.Error(err))
				} else if closedCount > 0 {
					r.logger.Debug("Closed inactive sessions",
						zap.Int("count", closedCount),
						zap.Duration("timeout", sessionInactivityTimeout))
				}
			}
		case <-ctx.Done():
			r.logger.Info("Background session cleanup stopped due to context cancellation")
			return
		}
	}
}

// DiscoverAndIndexTools discovers tools from ALL connected upstream servers and
// indexes them. Used by event-driven callers (boot, server reload, manual
// refresh) that want a full sweep regardless of per-server cadence.
func (r *Runtime) DiscoverAndIndexTools(ctx context.Context) error {
	return r.discoverAndIndexTools(ctx, false)
}

// discoverAndIndexTools discovers tools and updates the index. When dueOnly is
// true, only servers whose global discovery interval has elapsed are
// re-listed; servers omitted from the
// sweep keep their last-good index snapshot, so the index does not shrink.
func (r *Runtime) discoverAndIndexTools(ctx context.Context, dueOnly bool) error {
	if r.upstreamManager == nil || r.indexManager == nil {
		return fmt.Errorf("runtime managers not initialized")
	}

	r.logger.Info("Discovering and indexing tools...", zap.Bool("due_only", dueOnly))

	// Capture every server's connection generation AND live connection
	// token BEFORE listing: the publish below is bound to the generation, so
	// a result whose capture straddled a reconnect is dropped rather than
	// landing on the new connection, and stamped with the token so an
	// identity read can tell whether it still describes the live connection.
	gens := r.discoveryGenerations()
	instanceGens := r.instanceGenerations()

	// Each listed server comes with the capture of its own tools/list,
	// so every server's result is applied in
	// capture order: a result older than one already applied for that server
	// (a concurrent connect or tools/list_changed pass) writes nothing.
	tools, listed, captures, err := r.upstreamManager.DiscoverToolsReportCaptured(ctx, dueOnly)
	if err != nil {
		return fmt.Errorf("failed to discover tools: %w", err)
	}

	// Group tools by server name for differential updates
	toolsByServer := make(map[string][]*config.ToolMetadata)
	for _, tool := range tools {
		toolsByServer[tool.ServerName] = append(toolsByServer[tool.ServerName], tool)
	}

	// A server whose tools/list SUCCEEDED with zero tools has completed its
	// discovery just as surely as one that listed ten: stamp it so its names
	// resolve as absent instead of lingering in the connect→discovery window.
	// Its tool set is left alone — the sweep never wipes a server on an empty
	// result — and servers the sweep skipped or that failed to list are not
	// stamped. An empty result from a superseded instance generation is
	// re-listed below like a non-empty one.
	staleEmpty := r.markZeroToolServersDiscovered(listed, toolsByServer, gens, instanceGens, captures)

	if len(tools) == 0 {
		r.logger.Warn("No tools discovered from upstream servers")
		r.relistStaleSweepServers(ctx, staleEmpty)
		return nil
	}

	// Snapshot the set of currently-known servers so we can prune entries for
	// servers that have been removed from config and avoid an unbounded map.
	knownServers := r.upstreamManager.GetAllServerNames()
	knownServerSet := make(map[string]struct{}, len(knownServers))
	for _, name := range knownServers {
		knownServerSet[name] = struct{}{}
	}

	// Prune entries for servers removed from config. Fresh snapshots are NOT
	// published here: each server's snapshot, capture stamp and approval
	// records are committed together under captureSnapMu by
	// commitInventoryAt, so a concurrent review never reads one
	// generation's records with another's stamp.
	r.lastGoodToolsMu.Lock()
	for serverName := range r.lastGoodTools {
		if _, ok := knownServerSet[serverName]; !ok {
			delete(r.lastGoodTools, serverName)
			delete(r.lastGoodGen, serverName)
			delete(r.lastCaptureAt, serverName)
			delete(r.lastGoodCapture, serverName)
		}
	}
	r.lastGoodToolsMu.Unlock()

	// Commit each discovered server: publish its snapshot and apply the
	// differential update atomically with respect to review reads. A
	// disabled server is never (re)indexed by the sweep.
	//
	// Each server's StateView publish runs inside that commit, in the same
	// approval-lock hold as its final generation check. A server whose connection
	// changed while the sweep was listing had its result dropped and is
	// collected in stale.
	processedServers := make(map[string]struct{}, len(toolsByServer))
	stale := staleEmpty
	for serverName, serverTools := range toolsByServer {
		processedServers[serverName] = struct{}{}
		// No generation captured before the list: the result cannot be bound
		// to a connection, so it is indexed but not published into the
		// StateView, and the server is re-listed.
		var capture *supervisor.DiscoveryCapture
		c, captured := gens[serverName]
		if r.supervisor != nil && captured {
			capture = &c
		}
		eligible, published, err := r.commitInventoryAt(ctx, serverName, serverTools, instanceGens[serverName], capture,
			inventoryRef{capture: captures[serverName]})
		if err != nil {
			r.logger.Error("Failed to apply differential update for server during sweep",
				zap.String("server", serverName), zap.Error(err))
		}
		if eligible && r.supervisor != nil && (!captured || !published) {
			stale = append(stale, serverName)
		}
	}

	// For connected servers that were temporarily missing from discovery results,
	// re-apply last-good snapshot to avoid transient index shrink.
	for _, serverName := range knownServers {
		if _, ok := processedServers[serverName]; ok {
			continue
		}

		if !r.serverEligibleForIndexing(serverName) {
			continue
		}

		client, ok := r.upstreamManager.GetClient(serverName)
		if !ok || client == nil || !client.IsConnected() {
			continue
		}

		r.reapplyLastGoodSnapshot(ctx, serverName)
	}

	// Invalidate tool count caches since tools may have changed
	r.upstreamManager.InvalidateAllToolCountCaches()

	// A server whose connection changed while the sweep was listing had its
	// result dropped: re-list it under its current connection so it does not
	// linger in the connect→discovery window until the next sweep tick (the
	// reactive connect discovery may have been skipped by the in-progress
	// dedup).
	r.relistStaleSweepServers(ctx, stale)

	// Reconcile per-profile indexes against the
	// current config — build new profiles, rebuild those whose membership changed
	// and drop removed ones. Unchanged profiles are left untouched.
	r.reconcileProfileIndexes()

	r.logger.Info("Successfully indexed tools", zap.Int("count", len(tools)))
	return nil
}

// DiscoverAndIndexToolsForServer discovers and indexes tools for a single server.
// This is the LENIENT entry point used by reactive tool discovery (server
// connect, notifications/tools/list_changed): a successful ListTools that
// returns zero tools is treated as a transient blip and leaves the existing
// index untouched (see the authoritative variant for the explicit-refresh path).
// Implements retry logic with exponential backoff for robustness.
func (r *Runtime) DiscoverAndIndexToolsForServer(ctx context.Context, serverName string) error {
	return r.discoverAndIndexToolsForServer(ctx, serverName, false)
}

// RefreshServerTools re-discovers and re-indexes a single server AUTHORITATIVELY:
// a successful ListTools that returns zero tools is taken as the
// truth — the server's stale index entries are removed and its last-good
// snapshot is cleared, so a later approval-driven reindex cannot resurface tools
// that no longer exist upstream. This is the explicit operator refresh/discover
// path; the reactive callbacks keep the lenient behavior above.
func (r *Runtime) RefreshServerTools(ctx context.Context, serverName string) error {
	return r.discoverAndIndexToolsForServer(ctx, serverName, true)
}

func (r *Runtime) onUpstreamToolsListChanged(ctx context.Context, serverName string) error {
	var ownErr error
	r.runCoalescedDiscovery(serverName, func(followUp bool) error {
		runCtx := ctx
		if followUp {
			// A follow-up runs after the notification's own run and may
			// outlive the notification's 30s deadline: give it a fresh
			// runtime-scoped timeout, as connect-owned discovery does, so an
			// expired notification context never turns the follow-up into a
			// silent no-op. It derives from the lifetime context
			// so a follow-up started after shutdown is already cancelled.
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(r.lifetimeContext(), discoveryRunTimeout)
			defer cancel()
		}
		r.logger.Info("Tool discovery triggered by notification",
			zap.String("server", serverName), zap.Bool("follow_up", followUp))
		runErr := r.DiscoverAndIndexToolsForServer(runCtx, serverName)
		if followUp {
			if runErr != nil {
				r.logger.Warn("Coalesced tool discovery follow-up failed",
					zap.String("server", serverName), zap.Error(runErr))
			}
			return runErr
		}
		ownErr = runErr
		return runErr
	})
	return ownErr
}

// onServerConnectedDiscovery runs connect-owned reactive tool discovery,
// deduplicated against a discovery already in flight for this server; a
// trigger arriving meanwhile is coalesced into one follow-up run rather than
// dropped.
//
// Every run derives its context from the lifetime context, not AppContext:
// Close replaces the app context with context.Background(), so a follow-up
// that starts after shutdown would otherwise run uncancelled.
func (r *Runtime) onServerConnectedDiscovery(serverName string) {
	r.runCoalescedDiscovery(serverName, func(followUp bool) error {
		ctx, cancel := context.WithTimeout(r.lifetimeContext(), discoveryRunTimeout)
		defer cancel()

		r.logger.Info("Reactive tool discovery triggered",
			zap.String("server", serverName), zap.Bool("follow_up", followUp))
		if err := r.DiscoverAndIndexToolsForServer(ctx, serverName); err != nil {
			r.logger.Error("Failed to discover tools for connected server",
				zap.String("server", serverName),
				zap.Error(err))
			return err
		}
		return nil
	})
}

// discoveryRunTimeout bounds one coalesced discovery run that owns its context.
const discoveryRunTimeout = 30 * time.Second

// maxCoalescedDiscoveryFollowUps bounds how many follow-up runs one owner
// performs back to back for triggers coalesced while it ran, so a server that
// signals tools/list_changed continuously cannot pin a discovery goroutine.
const maxCoalescedDiscoveryFollowUps = 4

// coalescedDiscoveryContinuationDelay is how long an owner that reached the
// follow-up limit waits before a fresh worker takes the still-pending trigger.
const coalescedDiscoveryContinuationDelay = 2 * time.Second

func (r *Runtime) runCoalescedDiscovery(serverName string, run func(followUp bool) error) {
	r.discoveryRerunPending.Store(serverName, struct{}{})
	if _, loaded := r.discoveryInProgress.LoadOrStore(serverName, struct{}{}); loaded {
		r.logger.Debug("Tool discovery already in progress for server; coalescing this trigger into a follow-up run",
			zap.String("server", serverName))
		return
	}
	r.ownCoalescedDiscovery(serverName, run, false)
}

// ownCoalescedDiscovery is the owner loop of runCoalescedDiscovery; the caller
// holds the discovery marker. continued marks a deferred continuation, whose
// every run is a follow-up.
func (r *Runtime) ownCoalescedDiscovery(serverName string, run func(followUp bool) error, continued bool) {
	for followUps := 0; ; followUps++ {
		// Clear before running: a trigger recorded from here on is either
		// covered by this run's tools/list or re-arms the flag.
		r.discoveryRerunPending.Delete(serverName)
		_ = run(continued || followUps > 0)
		r.discoveryInProgress.Delete(serverName)
		if _, pending := r.discoveryRerunPending.Load(serverName); !pending {
			return
		}
		if r.lifetimeContext().Err() != nil {
			// The runtime shut down: no further follow-ups.
			return
		}
		if followUps >= maxCoalescedDiscoveryFollowUps {
			r.logger.Warn("Tool discovery follow-up limit reached; deferring the coalesced trigger to a fresh worker",
				zap.String("server", serverName),
				zap.Duration("delay", coalescedDiscoveryContinuationDelay))
			r.scheduleCoalescedDiscoveryContinuation(serverName, run)
			return
		}
		if _, loaded := r.discoveryInProgress.LoadOrStore(serverName, struct{}{}); loaded {
			// Another trigger took ownership; it runs for the pending flag.
			return
		}
		r.logger.Info("Re-running tool discovery for a trigger that arrived while it was in flight",
			zap.String("server", serverName))
	}
}

// scheduleCoalescedDiscoveryContinuation runs a pending coalesced trigger in a
// fresh, bounded worker after coalescedContinuationDelay. If another
// owner holds the marker by then, that owner covers the pending flag.
//
// It waits on the lifetime context, not AppContext: Close replaces the app
// context with context.Background(), so an owner that reaches the cap after
// shutdown started would otherwise schedule an uncancellable continuation.

func (r *Runtime) scheduleCoalescedDiscoveryContinuation(serverName string, run func(followUp bool) error) {
	lifetime := r.lifetimeContext()
	if lifetime.Err() != nil {
		return
	}
	go func() {
		timer := time.NewTimer(coalescedDiscoveryContinuationDelay)
		defer timer.Stop()
		select {
		case <-lifetime.Done():
			return
		case <-timer.C:
		}
		if lifetime.Err() != nil {
			return
		}
		if _, pending := r.discoveryRerunPending.Load(serverName); !pending {
			return
		}
		if _, loaded := r.discoveryInProgress.LoadOrStore(serverName, struct{}{}); loaded {
			return
		}
		r.ownCoalescedDiscovery(serverName, run, true)
	}()
}

// pruneAbsentUndecidedApprovals drops pending review records for tools
// the upstream no longer lists, so a tool review capture reflects the live
// toolset rather than the union of every past capture. Operator decisions are
// kept: approved and changed (rug-pull) records and operator-blocked (Disabled) records survive.
func (r *Runtime) pruneAbsentUndecidedApprovals(serverName string, tools []*config.ToolMetadata) {
	if r.storageManager == nil {
		return
	}
	live := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		live[config.RawToolName(tool)] = struct{}{}
	}
	records, err := r.storageManager.ListToolApprovals(serverName)
	if err != nil {
		r.logger.Debug("Skipping review record prune", zap.String("server", serverName), zap.Error(err))
		return
	}
	undecidedAbsent := func(rec *storage.ToolApprovalRecord) bool {
		if _, ok := live[rec.ToolName]; ok {
			return false
		}
		return !rec.Disabled && rec.Status == storage.ToolApprovalStatusPending
	}
	for _, rec := range records {
		if !undecidedAbsent(rec) {
			continue
		}
		// Eligibility is re-checked against the stored record inside the delete
		// transaction: a decision saved after the snapshot above (block, disable,
		// approve) must survive the prune.
		if _, err := r.storageManager.DeleteToolApprovalIf(serverName, rec.ToolName, undecidedAbsent); err != nil {
			r.logger.Debug("Failed to prune review record for absent tool", zap.String("server", serverName), zap.String("tool", rec.ToolName), zap.Error(err))
		}
	}
}

// persistToolDefinitionsLocked records, prunes and publishes one review
// capture. Caller holds captureSnapMu for writing.
//
// The capture's final generation check and
// all of its writes (review records, prune, last-good snapshot, capture
// stamp) run in ONE hold of the server's approval lock, the lock a config
// commit writes the row under. A capture from an instance whose generation is
// no longer the row's (a tools/list that was in flight when a commit changed
// the endpoint or disabled the server) writes nothing and reports
// persisted=false; the commit's after-unlock discovery request replaces it.
//
// The capture is admitted in the same hold, after the
// generation check; a capture older than one already applied, or from a
// retired or reconnected client, writes nothing and reports persisted=false.
func (r *Runtime) persistToolDefinitionsLocked(serverName string, tools []*config.ToolMetadata, instanceGen int64, inv managed.InventoryCapture) (persisted bool, err error) {
	defer r.lockToolApprovals(serverName)()
	if !r.discoveryCaptureCurrentHeld(serverName, instanceGen) {
		r.logger.Info("Review capture dropped: the server's committed generation moved on",
			zap.String("server", serverName), zap.Int64("instance_generation", instanceGen))
		return false, nil
	}
	if r.admitInventoryHeld(serverName, inventoryRef{capture: inv}) != inventoryAdmitted {
		return false, nil
	}
	if _, err := r.checkToolApprovalsHeld(serverName, tools); err != nil {
		return false, fmt.Errorf("capture review definitions for %s: %w", serverName, err)
	}
	r.pruneAbsentUndecidedApprovals(serverName, tools)
	r.lastGoodToolsMu.Lock()
	r.storeLastGoodLocked(serverName, tools, inv, instanceGen)
	r.lastGoodToolsMu.Unlock()
	return true, nil
}

// noteCaptureLocked stamps a fresh capture for the review payload. Caller
// holds lastGoodToolsMu. The stamp is strictly increasing per server so a
// caller can tell a new capture from a retained one without comparing clocks.
func (r *Runtime) noteCaptureLocked(serverName string) {
	if r.lastCaptureAt == nil {
		r.lastCaptureAt = make(map[string]time.Time)
	}
	now := time.Now().UTC()
	if prev := r.lastCaptureAt[serverName]; !now.After(prev) {
		now = prev.Add(time.Nanosecond)
	}
	r.lastCaptureAt[serverName] = now
}

// commitInventoryAt commits one discovered toolset for the inventory capture
// inv. The capture is admitted under the server's approval lock in the
// same hold as every write of the pass. A capture older than one already
// applied writes nothing and reports eligible and published (the newer
// capture stands; no re-list). A capture from a retired or reconnected client
// writes nothing and reports eligible but not published, so the caller
// re-lists the current client.
func (r *Runtime) commitInventoryAt(ctx context.Context, serverName string, tools []*config.ToolMetadata, instanceGen int64, capture *supervisor.DiscoveryCapture, inv inventoryRef) (eligible, published bool, err error) {
	r.inventoryBeforeApply(serverName, inv.capture)
	r.captureSnapMu.Lock()
	defer r.captureSnapMu.Unlock()
	if !r.serverEligibleForIndexing(serverName) {
		if _, perr := r.persistToolDefinitionsLocked(serverName, tools, instanceGen, inv.capture); perr != nil {
			r.logger.Warn("Failed to capture review definitions for ineligible server",
				zap.String("server", serverName), zap.Error(perr))
		}
		return false, false, nil
	}
	published = true
	held := func() {
		r.lastGoodToolsMu.Lock()
		r.storeLastGoodLocked(serverName, tools, inv.capture, instanceGen)
		r.lastGoodToolsMu.Unlock()
		if capture != nil && r.supervisor != nil {
			// The per-server variant stamps the server's discovery as
			// completed even when the result is EMPTY, so a tool-less
			// server is not left in the connect→discovery window. The
			// supervisor never calls back into the runtime, so this adds no
			// lock-order edge.
			ok, rerr := r.supervisor.RefreshServerToolsFromDiscovery(serverName, tools, *capture)
			if rerr != nil {
				r.logger.Warn("Failed to refresh tools in StateView for server",
					zap.String("server", serverName), zap.Error(rerr))
			} else {
				published = ok
			}
		}
	}
	if err := r.applyInventoryGuarded(ctx, serverName, tools, instanceGen, inv, nil, held); err != nil {
		if errors.Is(err, errDiscoveryIneligible) {
			return false, false, nil
		}
		if errors.Is(err, errInventoryStale) {
			// A newer capture of this server was already applied: it
			// stands, and nothing of this one was written.
			return true, true, nil
		}
		if errors.Is(err, errDiscoveryStaleGeneration) || errors.Is(err, errInventoryNotCurrent) {
			// Eligible but not published: the caller re-lists the
			// current instance.
			return true, false, nil
		}
		return true, published, err
	}
	return true, published, nil
}

func (r *Runtime) discoverAndIndexToolsForServer(ctx context.Context, serverName string, authoritative bool) error {
	// A result captured under a connection that changed before it was
	// published is dropped by the supervisor. Re-list under the current connection a
	// bounded number of times rather than leave the server in the
	// connect→discovery window until the next sweep — the reactive connect
	// discovery for the new connection may have been skipped by the
	// in-progress dedup while this one was still listing.
	const maxStaleRelists = 2
	for attempt := 0; ; attempt++ {
		published, err := r.discoverAndIndexToolsForServerOnce(ctx, serverName, authoritative)
		if err != nil || published || attempt >= maxStaleRelists {
			return err
		}
		r.logger.Info("Discovery result was captured under a superseded connection; re-listing the server",
			zap.String("server", serverName), zap.Int("attempt", attempt+1))
	}
}

// discoverAndIndexToolsForServerOnce is one list→index→publish attempt; it
// reports whether the result was published (false only when the server's
// connection generation moved on while it was captured).
func (r *Runtime) discoverAndIndexToolsForServerOnce(ctx context.Context, serverName string, authoritative bool) (published bool, err error) {
	if r.upstreamManager == nil || r.indexManager == nil {
		return false, fmt.Errorf("runtime managers not initialized")
	}

	if !r.serverEligibleForIndexing(serverName) {
		r.logger.Info("Skipping single-server tool discovery for ineligible server (disabled)",
			zap.String("server", serverName))
		return true, nil
	}

	r.logger.Info("Discovering and indexing tools for server", zap.String("server", serverName))

	// Get the upstream client for this server
	client, ok := r.upstreamManager.GetClient(serverName)
	if !ok {
		return false, fmt.Errorf("client not found for server %s", serverName)
	}
	// The commit generation of the config this instance runs: the publish
	// below is dropped when it no longer matches the stored row.
	instanceGen := client.Generation()

	// The connection generation and live connection token this result will
	// be published under: captured BEFORE the list so a reconnect during it
	// is detected at publish time (generation) or at identity-read time
	// (token).
	gen := r.discoveryGeneration(serverName)

	// Retry logic: Sometimes connection events fire slightly before the server is fully ready
	// We retry up to 3 times with exponential backoff (500ms, 1s, 2s)
	var tools []*config.ToolMetadata
	var inv managed.InventoryCapture
	maxRetries := 3
	baseDelay := 500 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := baseDelay * time.Duration(1<<uint(attempt-1)) // Exponential backoff
			r.logger.Debug("Retrying tool discovery after delay",
				zap.String("server", serverName),
				zap.Int("attempt", attempt+1),
				zap.Duration("delay", delay))

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return false, fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
			}
		}

		// Discover tools from this server with a fresh list:
		// this path serves connect and notifications/tools/list_changed, so
		// joining a tools/list that was already in flight when the trigger
		// fired (e.g. a tool-count refresh) could hash the contract as it was
		// BEFORE the change and keep a stale approval — which persists when
		// periodic discovery is disabled. ListToolsFresh waits for such a
		// request and lists again.
		tools, inv, err = client.ListToolsFreshCaptured(ctx)
		if err == nil {
			break // Success!
		}

		// Log the error for debugging
		r.logger.Warn("Tool discovery attempt failed",
			zap.String("server", serverName),
			zap.Int("attempt", attempt+1),
			zap.Int("max_retries", maxRetries),
			zap.Error(err))

		// Don't retry on context cancellation
		if ctx.Err() != nil {
			return false, fmt.Errorf("context cancelled during tool discovery: %w", ctx.Err())
		}
	}

	// After all retries, check if we still have an error
	if err != nil {
		return false, fmt.Errorf("failed to list tools for server %s after %d attempts: %w", serverName, maxRetries, err)
	}

	if len(tools) == 0 {
		if !authoritative {
			// Lenient path (reactive discovery): a transient empty result must
			// not wipe a server's tools. Leave the index and last-good snapshot
			// untouched; the next sweep or a real change will reconcile. The
			// discovery itself did complete, though: stamp the server so a
			// genuinely tool-less one does not stay in the connect→discovery
			// window. A server still holding a RETAINED set (the previous
			// connection's, restored on reconnect) is not stamped — this
			// connection listed none of it — and stays in the window until a
			// non-empty or authoritative pass.
			r.logger.Warn("No tools discovered from server; keeping existing index (lenient path)",
				zap.String("server", serverName))
			// A stale-generation empty result is re-listed like a
			// non-empty one.
			return r.markServerZeroToolsDiscovered(serverName, gen, instanceGen, inv), nil
		}
		// Authoritative path (explicit refresh/discover): zero tools
		// is the truth. Fall through with an empty toolset so the differential
		// update removes stale index entries and the snapshot below is cleared —
		// otherwise refresh would report success while stale tools stayed
		// searchable and a later approval reindex could resurface them.
		r.logger.Info("Refresh discovered zero tools; clearing server's index entries and snapshot",
			zap.String("server", serverName))
	}

	// Publish the last-good snapshot, capture stamp and approval records as
	// one generation (captureSnapMu), then apply the differential index
	// update. The snapshot gives the approval-driven reindex a
	// source without a network round-trip; on an authoritative empty refresh
	// it stores an empty slice so no stale set lingers.
	//
	// TOCTOU GUARD: the eligibility check at the top of this
	// function ran BEFORE the seconds-wide ListTools above, so
	// commitInventoryAt re-checks eligibility under the lock. A server
	// disabled in that window has its records captured for review (no index
	// write); a microsecond window remains before the index mutation, which is
	// deliberately not closed with a lock across the index write.
	eligible, published, err := r.commitInventoryAt(ctx, serverName, tools, instanceGen, &gen, inventoryRef{capture: inv})
	if !eligible {
		r.logger.Info("Server became ineligible during discovery (disabled); skipping index write",
			zap.String("server", serverName))
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to apply differential tool update for server %s: %w", serverName, err)
	}

	// Invalidate tool count caches since tools may have changed
	r.upstreamManager.InvalidateAllToolCountCaches()

	// The StateView publish ran inside the commit above, bound to the
	// generation captured before the list; a dropped (stale) result is
	// reported to the caller, which re-lists.
	r.logger.Debug("Refreshed tools in StateView for server",
		zap.String("server", serverName),
		zap.Int("tool_count", len(tools)),
		zap.Bool("published", published))

	r.logger.Info("Successfully indexed tools for server",
		zap.String("server", serverName),
		zap.Int("count", len(tools)))
	return published, nil
}

// instanceGenerations captures the commit generation of every registered
// instance before a sweep lists their tools.
func (r *Runtime) instanceGenerations() map[string]int64 {
	out := map[string]int64{}
	if r.upstreamManager == nil {
		return out
	}
	for _, name := range r.upstreamManager.GetAllServerNames() {
		if c, ok := r.upstreamManager.GetClient(name); ok && c != nil {
			out[name] = c.Generation()
		}
	}
	return out
}

// discoveryGenerations is supervisor.DiscoveryGenerations, nil-safe.
func (r *Runtime) discoveryGenerations() map[string]supervisor.DiscoveryCapture {
	if r.supervisor == nil {
		return nil
	}
	return r.supervisor.DiscoveryGenerations()
}

// discoveryGeneration is supervisor.DiscoveryGeneration, nil-safe.
func (r *Runtime) discoveryGeneration(serverName string) supervisor.DiscoveryCapture {
	if r.supervisor == nil {
		return supervisor.DiscoveryCapture{}
	}
	return r.supervisor.DiscoveryGeneration(serverName)
}

// markZeroToolServersDiscovered stamps ToolsDiscovered on every server in
// listed that contributed no tools to toolsByServer — a completed tools/list
// that returned nothing — without touching its tool set
// (supervisor.MarkServersToolsDiscovered also refuses to stamp a server still
// holding a previous connection's retained set). gens is the
// connection-generation capture and instanceGens the commit generations taken
// before the list.
//
// It returns the servers whose empty result came from a superseded instance
// generation, for the caller to re-list.
func (r *Runtime) markZeroToolServersDiscovered(listed []string, toolsByServer map[string][]*config.ToolMetadata, gens map[string]supervisor.DiscoveryCapture, instanceGens map[string]int64, captures map[string]managed.InventoryCapture) []string {
	if r.supervisor == nil {
		return nil
	}
	var stale []string
	for _, serverName := range listed {
		if _, hasTools := toolsByServer[serverName]; hasTools {
			continue
		}
		if !r.markServerZeroToolsDiscoveredWith(serverName, gens, instanceGens[serverName], captures[serverName]) {
			stale = append(stale, serverName)
		}
	}
	return stale
}

// relistStaleSweepServers re-lists, in name order, every server whose sweep
// result was dropped because its connection or its instance generation
// changed while the sweep was listing.
func (r *Runtime) relistStaleSweepServers(ctx context.Context, stale []string) {
	sort.Strings(stale)
	for _, serverName := range stale {
		r.logger.Info("Sweep discovery result was captured under a superseded connection; re-listing the server",
			zap.String("server", serverName))
		if err := r.discoverAndIndexToolsForServer(ctx, serverName, false); err != nil {
			r.logger.Warn("Failed to re-list server after a stale sweep result",
				zap.String("server", serverName), zap.Error(err))
		}
	}
}

// markServerZeroToolsDiscovered stamps one server's completed, empty
// discovery. Like every other discovery
// write, the stamp is made in one hold of the server's approval lock after
// the final eligibility and commit-generation check, so a stale empty
// tools/list (captured at G) cannot mark discovery complete after a
// connection-changing commit at G+1. The supervisor never calls back into the
// runtime, so stateMu under the approval lock adds no edge (the non-empty
// publish already nests it there).
//
// It reports false only when the result came from a superseded instance
// generation, so the caller re-lists the current instance.
func (r *Runtime) markServerZeroToolsDiscovered(serverName string, gen supervisor.DiscoveryCapture, instanceGen int64, inv managed.InventoryCapture) bool {
	return r.markServerZeroToolsDiscoveredWith(serverName, map[string]supervisor.DiscoveryCapture{serverName: gen}, instanceGen, inv)
}

// markServerZeroToolsDiscoveredWith is markServerZeroToolsDiscovered with the
// sweep's capture map handed to the supervisor as is (a server missing from it
// is judged by the supervisor's own rules).
//
// The empty result is admitted as an observation in
// the same hold. It advances the server's applied sequence, so an older
// non-empty capture still in flight writes nothing afterwards, and it keeps
// the retained inventory. An empty result older than one already applied is
// dropped (no re-list); one from a retired or reconnected client is re-listed.
func (r *Runtime) markServerZeroToolsDiscoveredWith(serverName string, gens map[string]supervisor.DiscoveryCapture, instanceGen int64, inv managed.InventoryCapture) bool {
	r.inventoryBeforeApply(serverName, inv)
	defer r.lockToolApprovals(serverName)()
	switch r.discoveryVerdictHeld(serverName, instanceGen) {
	case discoveryIneligible:
		r.logger.Info("Empty discovery result dropped: the server is no longer eligible at the captured generation",
			zap.String("server", serverName), zap.Int64("instance_generation", instanceGen))
		return true
	case discoveryStaleGen:
		r.logger.Info("Empty discovery result dropped: it came from a superseded instance generation",
			zap.String("server", serverName), zap.Int64("instance_generation", instanceGen))
		return false
	}
	switch r.admitInventoryHeld(serverName, inventoryRef{capture: inv, mode: inventoryObservation}) {
	case inventoryStale:
		return true
	case inventoryNotCurrent:
		return false
	}
	if r.supervisor == nil {
		return true
	}
	r.supervisor.MarkServersToolsDiscovered([]string{serverName}, gens)
	return true
}

// errDiscoveryIneligible reports a discovery result dropped by the final
// eligibility check. It is not an error for the caller's caller.
var errDiscoveryIneligible = errors.New("server not eligible for indexing at the committed generation")

// errDiscoveryStaleGeneration reports a discovery result dropped by the final
// check because it came from an instance whose commit generation no longer
// matches the stored row (a connection-changing commit ran while it listed).
// Unlike errDiscoveryIneligible it is not terminal: the caller re-lists the
// current instance, whose own discovery may have been deduped while this
// pass was in flight.
var errDiscoveryStaleGeneration = errors.New("discovery result came from a superseded instance generation")

// discoveryVerdict is the outcome of the final eligibility and generation
// check made under the server's approval lock.
type discoveryVerdict int

const (
	discoveryCurrent    discoveryVerdict = iota // eligible at the instance's generation
	discoveryIneligible                         // removed or disabled: terminal
	discoveryStaleGen                           // generation mismatch: re-list the current instance
)

func (r *Runtime) discoveryVerdictHeld(serverName string, instanceGen int64) discoveryVerdict {
	if r.storageManager == nil {
		return discoveryCurrent
	}
	row, err := r.storageManager.GetServerRecord(serverName)
	if err != nil || row == nil {
		return discoveryIneligible
	}
	if !row.Enabled {
		return discoveryIneligible
	}
	if row.Generation != instanceGen {
		return discoveryStaleGen
	}
	return discoveryCurrent
}

// discoveryCaptureCurrentHeld is the final check of a REVIEW capture (a
// discovery result for a server that is not eligible for indexing), made
// while the caller holds serverName's approval lock: the stored row exists
// and — when both are known — carries the generation of the instance the
// tools came from. Unlike discoveryVerdictHeld it accepts a
// disabled row: recording definitions for review is the point.
func (r *Runtime) discoveryCaptureCurrentHeld(serverName string, instanceGen int64) bool {
	if r.storageManager == nil {
		return true
	}
	row, err := r.storageManager.GetServerRecord(serverName)
	if err != nil || row == nil {
		return false
	}
	if row.Generation != instanceGen {
		return false
	}
	return true
}

// errLocalSnapshotPurged reports a local reindex whose last-good snapshot was
// purged by a removal after it was selected: the copy belongs
// to the removed server and must not reach a same-name replacement.
var errLocalSnapshotPurged = errors.New("last-good snapshot was purged by a removal after it was selected")

// applyInventoryGuarded applies a differential tool update for the inventory
// capture inv under the server's approval lock. Right after the discovery
// verdict and the purge

// check, still before held and any write, the capture is admitted in capture
// order (admitInventoryHeld): a refused capture writes nothing and returns
// errInventoryStale or errInventoryNotCurrent.
func (r *Runtime) applyInventoryGuarded(ctx context.Context, serverName string, newTools []*config.ToolMetadata, instanceGen int64, inv inventoryRef, stale func() bool, held func()) error {
	unlock := r.lockToolApprovals(serverName)
	locked := true
	release := func() {
		if locked {
			locked = false
			unlock()
		}
	}
	defer release()

	switch r.discoveryVerdictHeld(serverName, instanceGen) {
	case discoveryIneligible:
		r.logger.Info("Discovery result dropped: the server is no longer eligible at the captured generation",
			zap.String("server", serverName), zap.Int64("instance_generation", instanceGen))
		return errDiscoveryIneligible
	case discoveryStaleGen:
		r.logger.Info("Discovery result dropped: it came from a superseded instance generation; the current instance is re-listed",
			zap.String("server", serverName), zap.Int64("instance_generation", instanceGen))
		return errDiscoveryStaleGeneration
	}
	if stale != nil && stale() {
		r.logger.Info("Local reindex dropped: the server was removed after its last-good snapshot was selected",
			zap.String("server", serverName))
		return errLocalSnapshotPurged
	}
	if err := inventoryVerdictErr(r.admitInventoryHeld(serverName, inv)); err != nil {
		return err
	}
	if held != nil {
		held()
	}

	// Check tool-level quarantine approvals before indexing
	approvalResult, err := r.checkToolApprovalsResolved(serverName, newTools)
	if err != nil {
		return fmt.Errorf("check tool approvals for %s: %w", serverName, err)
	}

	// Query existing tools from the index
	existingTools, err := r.indexManager.GetToolsByServer(serverName)
	if err != nil {
		r.logger.Warn("Failed to query existing tools, performing full re-index",
			zap.String("server", serverName),
			zap.Error(err))
		// Filter out blocked tools before full batch index
		allowedTools := filterBlockedTools(newTools, approvalResult.BlockedTools)
		if err := r.indexManager.BatchIndexTools(allowedTools); err != nil {
			return err
		}
		r.warmSignatureCache(allowedTools)
		release()
		// The shared index changed for this server; refresh dependent profiles.
		r.reindexAffectedProfiles(serverName)
		r.reconcileSignatureCache()
		return nil
	}

	// Build maps for efficient lookup, keyed by the tool's RAW upstream name.
	// The index hands back canonical "<server>:<raw>" ids and discovery hands
	// in raw names; config.RawToolName maps both onto the same exact key, so
	// "erase" and "ns:erase" stay two entries on both sides and a rediscovery
	// of an unchanged set diffs to nothing. Any lossy key here would report a
	// tool as removed and delete its approval record in step 1 below.
	oldToolsMap := make(map[string]*config.ToolMetadata)
	for _, tool := range existingTools {
		oldToolsMap[config.RawToolName(tool)] = tool
	}

	newToolsMap := make(map[string]*config.ToolMetadata)
	for _, tool := range newTools {
		newToolsMap[config.RawToolName(tool)] = tool
	}

	// Detect changes
	var addedTools []*config.ToolMetadata
	var modifiedTools []*config.ToolMetadata
	var removedTools []string
	// Annotations are deliberately excluded from Hash (see
	// calculateToolApprovalHash: they are unstable across reconnections), so
	// a tool whose Hash is unchanged never reaches the modifiedTools branch
	// and its indexed annotations_json would never refresh. Annotation-only
	// changes are tracked separately so they never touch Hash, approval state
	// or the "Tool schema changed" log line: a silent, best-effort metadata
	// sync, not change detection.
	var annotationsOnlyTools []*config.ToolMetadata

	// Find added and modified tools
	for toolName, newTool := range newToolsMap {
		oldTool, exists := oldToolsMap[toolName]
		if !exists {
			// Tool is new
			addedTools = append(addedTools, newTool)
		} else if oldTool.Hash != newTool.Hash {
			// Tool exists but has changed (different hash)
			modifiedTools = append(modifiedTools, newTool)
		} else if !reflect.DeepEqual(oldTool.Annotations, newTool.Annotations) {
			annotationsOnlyTools = append(annotationsOnlyTools, newTool)
		}
		// else: tool unchanged, no action needed
	}

	// Find removed tools
	for toolName := range oldToolsMap {
		if _, exists := newToolsMap[toolName]; !exists {
			removedTools = append(removedTools, toolName)
		}
	}

	// Log the changes
	if len(addedTools) > 0 || len(modifiedTools) > 0 || len(removedTools) > 0 {
		r.logger.Info("Tool changes detected for server",
			zap.String("server", serverName),
			zap.Int("added", len(addedTools)),
			zap.Int("modified", len(modifiedTools)),
			zap.Int("removed", len(removedTools)))
	} else {
		r.logger.Debug("No tool changes detected for server",
			zap.String("server", serverName),
			zap.Int("tool_count", len(newTools)))
	}

	// Apply changes

	// 1. Delete removed tools
	for _, toolName := range removedTools {
		r.logger.Info("Removing tool from index",
			zap.String("server", serverName),
			zap.String("tool", toolName))

		if err := r.indexManager.DeleteTool(serverName, toolName); err != nil {
			r.logger.Error("Failed to delete tool from index",
				zap.String("server", serverName),
				zap.String("tool", toolName),
				zap.Error(err))
		}

		if r.storageManager != nil {
			if err := r.storageManager.DeleteToolApproval(serverName, toolName); err != nil {
				r.logger.Debug("Failed to delete tool approval for removed tool",
					zap.String("tool", fmt.Sprintf("%s:%s", serverName, toolName)), zap.Error(err))
			}
		}
	}

	// 2. Remove blocked tools from the index if they were indexed
	for blockedToolName := range approvalResult.BlockedTools {
		if _, wasIndexed := oldToolsMap[blockedToolName]; wasIndexed {
			r.logger.Info("Removing blocked tool from index (quarantine)",
				zap.String("server", serverName),
				zap.String("tool", blockedToolName))
			if err := r.indexManager.DeleteTool(serverName, blockedToolName); err != nil {
				r.logger.Error("Failed to remove blocked tool from index",
					zap.String("server", serverName),
					zap.String("tool", blockedToolName),
					zap.Error(err))
			}
		}
	}

	// 3. Index added tools (excluding blocked)
	allowedAddedTools := filterBlockedTools(addedTools, approvalResult.BlockedTools)
	if len(allowedAddedTools) > 0 {
		r.logger.Info("Indexing new tools",
			zap.String("server", serverName),
			zap.Int("count", len(allowedAddedTools)),
			zap.Int("blocked", len(addedTools)-len(allowedAddedTools)))

		if err := r.indexManager.BatchIndexTools(allowedAddedTools); err != nil {
			return fmt.Errorf("failed to index added tools: %w", err)
		}
	}

	// 4. Re-index modified tools (excluding blocked)
	allowedModifiedTools := filterBlockedTools(modifiedTools, approvalResult.BlockedTools)
	if len(allowedModifiedTools) > 0 {
		r.logger.Info("Re-indexing modified tools",
			zap.String("server", serverName),
			zap.Int("count", len(allowedModifiedTools)),
			zap.Int("blocked", len(modifiedTools)-len(allowedModifiedTools)))

		for _, tool := range allowedModifiedTools {
			r.logger.Debug("Tool schema changed",
				zap.String("server", serverName),
				zap.String("tool", tool.Name),
				zap.String("old_hash", oldToolsMap[config.RawToolName(tool)].Hash),
				zap.String("new_hash", tool.Hash))
		}

		if err := r.indexManager.BatchIndexTools(allowedModifiedTools); err != nil {
			return fmt.Errorf("failed to re-index modified tools: %w", err)
		}
	}

	// 4b. Silently refresh annotations_json for otherwise-unchanged
	// tools. Deliberately not logged at Info and
	// not described as a "change" — see the comment on annotationsOnlyTools
	// above — this is metadata hygiene, not a tool-contract update.
	allowedAnnotationsOnlyTools := filterBlockedTools(annotationsOnlyTools, approvalResult.BlockedTools)
	if len(allowedAnnotationsOnlyTools) > 0 {
		r.logger.Debug("Refreshing stored annotations for unchanged tools",
			zap.String("server", serverName),
			zap.Int("count", len(allowedAnnotationsOnlyTools)))

		if err := r.indexManager.BatchIndexTools(allowedAnnotationsOnlyTools); err != nil {
			return fmt.Errorf("failed to refresh tool annotations: %w", err)
		}
	}

	// 5. Warm the signature cache for every tool this server still serves —
	// deliberately the WHOLE allowed set, not just what steps 3 and 4 touched.
	// On a restart against an existing index the differential update finds
	// nothing to do, and the deferred direct listing reads through Peek, which
	// never compiles: without warming unchanged tools every entry would lose
	// its compact signature.
	//
	// Idempotent and cheap: Warm returns on the first cache hit, so the steady
	// state is one map lookup per tool per discovery.
	r.warmSignatureCache(filterBlockedTools(newTools, approvalResult.BlockedTools))

	// Index and approval writes are done; the per-profile rebuild and the
	// events below must not run under the approval lock.
	release()

	// If the shared index changed for this server, refresh the per-profile indexes
	// that include it. Profiles without this server are
	// untouched. Skipped when nothing changed to avoid churn on idle sweeps.
	changed := len(addedTools) > 0 || len(modifiedTools) > 0 || len(removedTools) > 0 ||
		len(annotationsOnlyTools) > 0 || len(approvalResult.BlockedTools) > 0
	if changed {
		r.reindexAffectedProfiles(serverName)
		// Evict signature-cache entries orphaned by removed/redefined tools —
		// warming above only ever ADDS entries.
		r.reconcileSignatureCache()

		// Tell the routing-mode surfaces that a tool DEFINITION moved, not just
		// that the server list did. The direct surface is first built when the
		// server connects, before indexing warms the signature cache, and a
		// rug-pull changes a schema without changing the server list; the
		// rebuild re-renders from the freshly indexed definitions with a warm
		// cache, so deferred entries carry compact signatures and pre-dispatch
		// validation and describe_tool see the current schema. Guarded by
		// `changed`, so an idle discovery sweep emits nothing.
		r.toolsChanged(serverName)
	}

	return nil
}

// warmSignatureCache pre-compiles compact signatures for freshly indexed
// tools (signatures are compiled at index time into the ONE Runtime-owned
// cache, keyed by the tool hash), so a
// later compact retrieve_tools is a pure cache read — never a per-request
// compile. Hashless tools are skipped: warming them would memoize distinct
// schemas under one "" key (indexed tools always carry a hash).
func (r *Runtime) warmSignatureCache(tools []*config.ToolMetadata) {
	if r.sigCache == nil {
		return
	}
	for _, tool := range tools {
		if tool == nil || tool.Hash == "" {
			continue
		}
		r.sigCache.Warm(tool.Hash, tool.ParamsJSON, tool.Description)
	}
}

// reconcileSignatureCache evicts signature-cache entries whose hash no longer
// backs any indexed tool. warmSignatureCache only
// ever ADDS entries, so after tool removals/redefinitions the dead hashes
// would otherwise accumulate for the life of the process. Called after index
// rebuilds and differential updates; enumerates the SHARED index only —
// per-profile indexes hold subsets of the same tools, hence the same hashes.
func (r *Runtime) reconcileSignatureCache() {
	if r.sigCache == nil || r.indexManager == nil {
		return
	}
	serverNames, err := r.indexManager.GetAllIndexedServerNames()
	if err != nil {
		r.logger.Debug("signature cache reconcile skipped: cannot enumerate indexed servers", zap.Error(err))
		return
	}
	live := make(map[string]struct{})
	for _, serverName := range serverNames {
		tools, err := r.indexManager.GetToolsByServer(serverName)
		if err != nil {
			// Fail open (skip the sweep) rather than evict entries we could not
			// enumerate: a lingering stale entry is harmless memory, an evicted
			// live one costs a recompile on the next compact retrieve.
			r.logger.Debug("signature cache reconcile skipped: cannot enumerate server tools",
				zap.String("server", serverName), zap.Error(err))
			return
		}
		for _, tool := range tools {
			if tool != nil && tool.Hash != "" {
				live[tool.Hash] = struct{}{}
			}
		}
	}
	if evicted := r.sigCache.RetainHashes(live); evicted > 0 {
		r.logger.Debug("evicted stale signature cache entries",
			zap.Int("evicted", evicted),
			zap.Int("live", len(live)))
	}
}

// filterBlockedTools removes tools that are blocked by quarantine from the list.
func filterBlockedTools(tools []*config.ToolMetadata, blocked map[string]bool) []*config.ToolMetadata {
	if len(blocked) == 0 {
		return tools
	}
	var allowed []*config.ToolMetadata
	for _, tool := range tools {
		// BlockedTools is keyed by raw name (checkToolApprovalsHeld), so the
		// lookup must use the same exact identity.
		if !blocked[config.RawToolName(tool)] {
			allowed = append(allowed, tool)
		}
	}
	return allowed
}

// bootReconcile starts workers for the database-backed server snapshot.
func (r *Runtime) bootReconcile() error {
	if r.configSvc == nil || r.storageManager == nil || r.upstreamManager == nil || r.indexManager == nil {
		return fmt.Errorf("runtime managers not initialized")
	}
	r.logger.Info("Starting configured servers")
	_, err := r.Commit(commitReasonBoot, func(tx *CommitTx) error {
		tx.Bootstrap()
		return nil
	})
	return err
}

// EnableServer enables or disables a server and persists the change.
//
// The flag commits to the database before becoming visible. The commit purges
// a disabled server's tools from the index, and
// the connect/disconnect runs after the commit lock is released.
func (r *Runtime) EnableServer(serverName string, enabled bool) error {
	r.logger.Info("Request to change server enabled state",
		zap.String("server", serverName),
		zap.Bool("enabled", enabled))

	if _, err := r.Commit("enable_toggle", func(tx *CommitTx) error {
		// The commit sends the one servers.changed of this toggle.
		tx.EventExtra(map[string]any{"server": serverName, "enabled": enabled})
		return tx.SetEnabled(serverName, enabled)
	}); err != nil {
		r.logger.Error("Failed to update server enabled state", zap.Error(err))
		return fmt.Errorf("failed to update server '%s': %w", serverName, err)
	}

	// Emit config change activity for the audit trail.
	action := "server_disabled"
	if enabled {
		action = "server_enabled"
	}
	r.EmitActivityConfigChange(action, serverName, "api", []string{"enabled"}, map[string]interface{}{"enabled": !enabled}, map[string]interface{}{"enabled": enabled})

	// Reindexing and reconnect checks are slow; run them off the caller's
	// goroutine so the HTTP handler returns right after the commit.
	go r.HandleUpstreamServerChange(r.AppContext())

	return nil
}

// RestartServer restarts an upstream server by disconnecting and reconnecting it.
// Validation and disconnect are synchronous; reconnection and reindexing happen
// asynchronously so the caller (HTTP handler) returns immediately.
func (r *Runtime) RestartServer(serverName string) error {
	r.logger.Info("Request to restart server", zap.String("server", serverName))

	// Restart the current database-backed server definition.
	serverConfig := r.ConfigSnapshot().GetServer(serverName)
	if serverConfig == nil {
		return &ServerNotFoundError{Name: serverName}
	}

	// If server is not enabled, enable it first (EnableServer is already async-safe)
	if !serverConfig.Enabled {
		r.logger.Info("Server is disabled, enabling it",
			zap.String("server", serverName))
		return r.EnableServer(serverName, true)
	}

	// The restart is bound to the
	// server's committed generation, read together with its config from one
	// snapshot. The old instance is replaced at that SAME generation by
	// ReplaceInstanceAt, which acts only while that generation is current and
	// the instance is the one observed here. The controller connects the
	// current committed instance; a later change or removal always wins.
	for attempt := 0; ; attempt++ {
		snap := r.configSvc.Current()
		cfg := snap.GetServer(serverName)
		sg, ok := snap.ServerGeneration(serverName)
		if cfg == nil || !ok {
			return fmt.Errorf("server '%s' not found in configuration", serverName)
		}
		if !cfg.Enabled {
			return r.EnableServer(serverName, true)
		}
		client, exists := r.upstreamManager.GetClient(serverName)
		if !exists {
			// Enabled but no instance (its commit's worker has not run yet,
			// or construction failed): install one at the committed
			// generation.
			r.logger.Info("Server client not found, attempting to create and connect",
				zap.String("server", serverName))
			go r.restartInstallWorker(serverName, cfg, sg.Gen)
			return nil
		}

		// Recreate the client to pick up new secrets: simply reconnecting
		// reuses the old client with old (unresolved) secrets.
		r.logger.Info("Replacing the server's client to recreate it with fresh secret resolution",
			zap.String("server", serverName))
		_, err := r.upstreamManager.ReplaceInstanceAt(serverName, sg.Gen, client, cfg)
		if errors.Is(err, upstream.ErrStaleGeneration) {
			if attempt < 3 {
				continue
			}
			return fmt.Errorf("server '%s' changed while it was being restarted; try again", serverName)
		}
		if err != nil {
			return fmt.Errorf("failed to restart server '%s': %w", serverName, err)
		}
		r.supervisor.Wake()
		break
	}

	r.logger.Info("Server restart initiated asynchronously", zap.String("server", serverName))
	return nil
}

// restartInstallWorker is RestartServer's worker for a server with no
// instance: it installs and connects one at generation gen, and is inert once
// a newer commit superseded gen.
func (r *Runtime) restartInstallWorker(serverName string, cfg *config.ServerConfig, gen int64) {

	if err := r.upstreamManager.AddServerConfigAt(serverName, cfg, gen); err != nil {
		if errors.Is(err, upstream.ErrStaleGeneration) {
			r.logger.Debug("Restart worker superseded by a newer config commit; nothing to do",
				zap.String("server", serverName), zap.Int64("generation", gen))
			return
		}
		r.logger.Error("Failed to add server during restart",
			zap.String("server", serverName),
			zap.Error(err))
		return
	}
	r.logger.Info("Successfully added server", zap.String("server", serverName))
	r.serversChanged(serverName)
	r.supervisor.Wake()
}

// HandleUpstreamServerChange should be called when upstream servers change.
func (r *Runtime) HandleUpstreamServerChange(ctx context.Context) {
	if ctx == nil {
		ctx = r.AppContext()
	}

	r.logger.Info("Upstream server configuration changed, triggering comprehensive update")

	phase := r.CurrentStatus().Phase
	r.UpdatePhase(phase, "Upstream servers updated")

	// Tool discovery runs in a background goroutine, and the SSE event is
	// emitted AFTER discovery completes so the frontend reads tool counts from
	// an updated StateView.
	go func() {
		if err := r.DiscoverAndIndexTools(ctx); err != nil {
			r.logger.Error("Failed to update tool index after upstream change", zap.Error(err))
		}
		r.cleanupOrphanedIndexEntries()

		// Emit event AFTER tool discovery completes so frontend gets fresh stats
		r.serversChanged()
		r.changes.Invalidate(EntityTools, "")
	}()
}

func (r *Runtime) cleanupOrphanedIndexEntries() {
	if r.indexManager == nil || r.upstreamManager == nil {
		return
	}

	r.logger.Debug("Checking for orphaned index entries")

	orphans, activeCount, indexedCount, err := findOrphanedIndexServers(
		r.indexManager.GetAllIndexedServerNames, r.upstreamManager.GetAllServerNames)
	if err != nil {
		r.logger.Warn("Failed to retrieve indexed server names for orphan cleanup", zap.Error(err))
		return
	}

	var removedCount int
	for _, indexedServer := range orphans {
		r.logger.Info("Removing orphaned index entries for server no longer in config",
			zap.String("server", indexedServer))
		if err := r.indexManager.DeleteServerTools(indexedServer); err != nil {
			r.logger.Warn("Failed to delete orphaned index entries",
				zap.String("server", indexedServer),
				zap.Error(err))
		} else {
			removedCount++
		}
	}

	r.logger.Debug("Orphaned index cleanup completed",
		zap.Int("active_servers", activeCount),
		zap.Int("indexed_servers", indexedCount),
		zap.Int("orphans_removed", removedCount))

	if removedCount > 0 {
		// Removed servers' tools leave the index; their signatures leave too.
		r.reconcileSignatureCache()
	}
}

// findOrphanedIndexServers returns the indexed servers that are not active,
// plus the sizes of both lists for logging.
func findOrphanedIndexServers(
	listIndexed func() ([]string, error),
	listActive func() []string,
) (orphans []string, activeCount, indexedCount int, err error) {
	// Indexed first: a server is registered before its tools are indexed, so a
	// server added while this runs is either missing from the indexed list or
	// already present in the active list read after it. This covers servers newly
	// added during cleanup; index entries persisted from a previous run can still
	// be pruned if their server's async re-registration has not finished yet, and
	// are re-indexed once discovery runs for it.
	indexedServers, err := listIndexed()
	if err != nil {
		return nil, 0, 0, err
	}

	activeServers := listActive()
	active := make(map[string]bool, len(activeServers))
	for _, name := range activeServers {
		active[name] = true
	}

	for _, name := range indexedServers {
		if !active[name] {
			orphans = append(orphans, name)
		}
	}
	return orphans, len(activeServers), len(indexedServers), nil
}

// supervisorEventForwarder subscribes to supervisor events and emits runtime events
// to notify Web UI via SSE when server connection state changes.
func (r *Runtime) supervisorEventForwarder() {
	eventCh := r.supervisor.Subscribe()
	defer r.supervisor.Unsubscribe(eventCh)

	r.logger.Info("Supervisor event forwarder started - will emit servers.changed on connection state changes")

	// Get app context once with proper locking
	appCtx := r.AppContext()

	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				r.logger.Info("Supervisor event channel closed, stopping event forwarder")
				return
			}

			// Emit servers.changed event for connection state changes
			// This triggers Web UI to refresh server list via SSE
			switch event.Type {
			case supervisor.EventServerConnected:
				r.logger.Info("Server connected - emitting servers.changed event",
					zap.String("server", event.ServerName))
				r.serversChanged(event.ServerName)

			case supervisor.EventServerDisconnected:
				r.logger.Info("Server disconnected - emitting servers.changed event",
					zap.String("server", event.ServerName))
				r.serversChanged(event.ServerName)

			case supervisor.EventServerStateChanged:
				r.logger.Debug("Server state changed - emitting servers.changed event",
					zap.String("server", event.ServerName))
				r.serversChanged(event.ServerName)
			}

		case <-appCtx.Done():
			r.logger.Info("App context cancelled, stopping supervisor event forwarder")
			return
		}
	}
}

// globalSettingsEqual excludes database entities from deployment settings comparison.
func globalSettingsEqual(a, b *config.Config) bool {
	left, right := *a, *b
	left.Servers, left.Profiles = nil, nil
	right.Servers, right.Profiles = nil, nil
	return reflect.DeepEqual(left, right)
}
func configsEquivalent(a, b *config.Config) bool { return reflect.DeepEqual(a, b) }

// reapplyLastGoodSnapshot re-applies a server's last-good snapshot to the index
// (the sweep fallback for a connected server missing from discovery). The
// snapshot is selected and applied under one captureSnapMu hold: a snapshot
// read outside the lock could be superseded by a concurrent capture that marks
// a tool changed, and applying the stale one afterwards would look like a
// genuine revert and clear that hold.
func (r *Runtime) reapplyLastGoodSnapshot(ctx context.Context, serverName string) {
	r.captureSnapMu.Lock()
	defer r.captureSnapMu.Unlock()

	snapshot, inv, seq, snapshotGen := r.lastGoodSnapshotWithCaptureGen(serverName)
	if len(snapshot) == 0 {
		return
	}

	// Logged at Info: this can fire repeatedly during reconnect storms and
	// is benign self-healing, not a warning condition.
	r.logger.Info("Server missing from discovery result; reusing last-good tool snapshot",
		zap.String("server", serverName),
		zap.Int("snapshot_tools", len(snapshot)))

	// Re-checks eligibility again immediately before the write, and applies
	// only while the snapshot's capture is still

	// current (same client and connection, no newer definitions); otherwise
	// the server's connect discovery owns it and the fallback skips it.
	r.applyServerDiffIfEligibleGuarded(ctx, serverName, snapshot, snapshotGen, inventoryRef{capture: inv, mode: inventoryReconcile}, func() bool {
		return r.lastGoodPurgedSince(serverName, seq)
	})
}
