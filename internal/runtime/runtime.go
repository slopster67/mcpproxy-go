package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/cache"
	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/diagnostics"
	"github.com/slopster67/mcpproxy-go/internal/health"
	"github.com/slopster67/mcpproxy-go/internal/httpx"
	"github.com/slopster67/mcpproxy-go/internal/index"
	"github.com/slopster67/mcpproxy-go/internal/management"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/runtime/configsvc"
	"github.com/slopster67/mcpproxy-go/internal/runtime/stateview"
	"github.com/slopster67/mcpproxy-go/internal/runtime/supervisor"
	"github.com/slopster67/mcpproxy-go/internal/secret"
	"github.com/slopster67/mcpproxy-go/internal/servericon"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/stringutil"
	"github.com/slopster67/mcpproxy-go/internal/toolsig"
	"github.com/slopster67/mcpproxy-go/internal/truncate"
	"github.com/slopster67/mcpproxy-go/internal/upstream"
	"github.com/slopster67/mcpproxy-go/internal/upstream/core"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
)

// Status captures high-level state for API consumers.
type Status struct {
	Phase         Phase                  `json:"phase"`
	Message       string                 `json:"message"`
	UpstreamStats map[string]interface{} `json:"upstream_stats"`
	ToolsIndexed  int                    `json:"tools_indexed"`
	LastUpdated   time.Time              `json:"last_updated"`
}

// Runtime owns the non-HTTP lifecycle for the proxy process.
type Runtime struct {
	peerOAuthMu        sync.Mutex
	peerOAuthCallbacks map[[32]byte]pendingPeerOAuthCallback

	peers *peering.Hub
	cfg   *config.Config

	logger *zap.Logger

	// ConfigService provides lock-free snapshot-based config reads
	configSvc *configsvc.Service

	mu      sync.RWMutex
	running bool

	configCommitMu sync.Mutex

	// Config commit bookkeeping (commit.go).
	commitState
	// Removal-purge observers (commit_purge.go).
	purgeObserverState

	statusMu sync.RWMutex
	status   Status

	phaseMachine *phaseMachine

	eventMu   sync.RWMutex
	eventSubs map[chan Event]struct{}

	storageManager  *storage.Manager
	indexManager    *index.Manager
	upstreamManager *upstream.Manager
	cacheManager    *cache.Manager
	truncator       *truncate.Truncator
	sigCache        *toolsig.Cache // single process-wide signature cache (indexing warms, MCP reads)
	secretResolver  *secret.Resolver

	refreshManager *oauth.RefreshManager // Proactive OAuth token refresh

	// managementService is the unified lifecycle/diagnostics service. It is
	// installed after New (SetManagementService) because the service is built
	// on top of the Runtime, not because the type has to be erased: the
	// management package does not import runtime, so the field carries the
	// real interface and every consumer gets a compile-time contract.
	managementService management.Service
	activityService   *ActivityService // Activity logging service

	// workSessions derives a unit of USER WORK from the churn of transport
	// sessions underneath it.
	workSessions *WorkSessionTracker

	// changes publishes the dashboard change stream (GET /api/v1/events).
	changes *ChangeHub

	// Supervisor for state reconciliation (lock-free reads via StateView)
	supervisor *supervisor.Supervisor

	// Tool discovery deduplication: tracks servers with in-progress reactive discovery
	// Key: serverName, Value: struct{} (presence indicates discovery in progress)
	discoveryInProgress sync.Map
	// discoveryRerunPending records a discovery trigger (connect or
	// tools/list_changed) that arrived while a discovery for the same server
	// was in flight; the in-flight owner runs one follow-up for it instead of
	// the trigger being dropped. Key: serverName.
	discoveryRerunPending sync.Map

	// toolApprovalLocks serializes every read-modify-write of one server's
	// tool-approval records: discovery passes (checkToolApprovalsHeld),
	// operator approve/block/toggle, server-approval baseline promotion, the
	// quarantine flip and the scanner's baseline+blocks write. Key: server
	// name, value: *toolApprovalServerLock (the mutex plus the capture-order
	// state it guards). See lockToolApprovals.
	toolApprovalLocks sync.Map

	// reviewQueueSigs holds each server's last announced review queue
	// (blocked tool name and definition hash), so a discovery pass that finds
	// the same tools awaiting review emits no review.changed.
	reviewQueueSigs sync.Map

	// lastUsed is each server's latest tool call (contracts.Server.LastUsedAt).
	lastUsed lastUsedTimes

	// Last-good tool snapshots per server used to avoid transient tool loss during
	// global discovery races/restarts.
	lastGoodToolsMu sync.RWMutex
	lastGoodTools   map[string][]*config.ToolMetadata
	// lastCaptureAt (guarded by lastGoodToolsMu) records when each server's
	// toolset was last captured, for the review payload's fresh-capture proof.
	lastCaptureAt map[string]time.Time
	// lastGoodPurgeSeq (guarded by lastGoodToolsMu) counts the removal purges
	// of each server's last-good snapshot. A
	// local reindex reads it together with the snapshot and re-checks it under
	// the approval lock — the lock the purge runs under — so a snapshot
	// selected before a remove and re-add is never applied to the replacement.
	lastGoodPurgeSeq map[string]uint64
	// lastGoodGen (guarded by lastGoodToolsMu) is the config commit
	// generation of the instance each last-good snapshot was listed from.
	// A local reindex hands it to the final generation check, so a
	// snapshot from an instance an endpoint change superseded is never
	// reapplied; the caller re-lists the current instance instead.
	lastGoodGen map[string]int64
	// lastGoodCapture (guarded by lastGoodToolsMu) is the capture each
	// last-good snapshot came from: a snapshot is
	// replaced only by a newer-or-equal capture, and operator reconciliation
	// re-applies it only while that capture is still current.
	lastGoodCapture map[string]managed.InventoryCapture
	// captureSnapMu makes "records persisted + pruned + stamp published" one
	// unit for readers: a capture holds the write lock for all of it, a review
	// holds the read lock while it reads records and live state.
	captureSnapMu sync.RWMutex

	// Tracks the last-synced effective server set per
	// profile so a config reload can rebuild only the profiles whose membership
	// actually changed and drop profiles removed from config. Guards the
	// per-profile Bleve index reconciliation (internal/runtime/profile_index.go).
	profileIndexMu    sync.Mutex
	profileMembership map[string][]string

	// bindingWriteMu serializes binding guard-check + write for every guarded
	// path (client_binding_writes.go).
	bindingWriteMu sync.Mutex

	// clientsService is the single client-credential service.
	clientsService *ClientsService

	// profilesService is the single profiles service; the evaluator
	// and session hook are installed by the server.
	profilesService    *ProfilesService
	profileEvaluatorMu sync.RWMutex
	profileEvaluator   ProfileEvaluator
	profileSessionHook ProfileSessionHook

	appCtx    context.Context
	appCancel context.CancelFunc
	// lifetimeCtx is the runtime's original application context. Close
	// cancels it and, unlike appCtx (which Close replaces with
	// context.Background()), it is never replaced, so work scheduled or
	// started after shutdown still observes the cancellation.
	lifetimeCtx context.Context
}

// New creates a runtime helper for the given config and prepares core managers.
func New(cfg *config.Config, logger *zap.Logger) (*Runtime, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	storageManager, err := storage.NewManager(cfg.DataDir, cfg.EncryptionKey, logger.Sugar())
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage manager: %w", err)
	}

	state, err := storageManager.LoadEntities()
	if err != nil {
		storageManager.Close()
		return nil, fmt.Errorf("load stored entities: %w", err)
	}
	cfg = cloneCommitConfig(cfg)
	cfg.Servers = make([]*config.ServerConfig, 0, len(state.Servers))
	generations := make(map[string]configsvc.ServerGen, len(state.Servers))
	for _, record := range state.Servers {
		cfg.Servers = append(cfg.Servers, config.CopyServerConfig(&record.ServerConfig))
		generations[record.Name] = configsvc.ServerGen{Gen: record.Generation, Incarnation: record.Incarnation, OAuthEpoch: record.OAuthEpoch}
	}
	cfg.Profiles = state.Profiles
	if errs := cfg.ValidateDetailed(); len(errs) > 0 {
		storageManager.Close()
		return nil, fmt.Errorf("invalid stored configuration: %s", errs[0].Error())
	}

	if err := storageManager.CloseAllActiveSessions(); err != nil {
		logger.Warn("Failed to close stale sessions on startup", zap.Error(err))
	}

	indexManager, err := index.NewManager(cfg.DataDir, logger)
	if err != nil {
		_ = storageManager.Close()
		return nil, fmt.Errorf("failed to initialize index manager: %w", err)
	}

	// Initialize secret resolver
	secretResolver := secret.NewResolver(storageManager.GetDatabase())

	upstreamManager := upstream.NewManager(logger, cfg, storageManager.GetDatabase(), secretResolver, storageManager)
	// No instance is built for a committed view the stored row has already
	// moved past.
	upstreamManager.SetCommittedViewChecker(storageManager.CheckCommittedView)
	if cfg.Logging != nil {
		upstreamManager.SetLogConfig(cfg.EffectiveLogConfig())
	}

	cacheManager, err := cache.NewManager(storageManager.GetDB(), logger)
	if err != nil {
		_ = indexManager.Close()
		_ = storageManager.Close()
		return nil, fmt.Errorf("failed to initialize cache manager: %w", err)
	}

	truncator := truncate.NewTruncator(cfg.ToolResponseLimit)

	appCtx, appCancel := context.WithCancel(context.Background())

	// Initialize ConfigService for lock-free snapshot-based reads
	configSvc := configsvc.NewService(&configsvc.Snapshot{Config: cfg, Version: state.Generation, ServerGenerations: generations}, logger)

	// Supervisor with ActorPoolSimple (delegates to UpstreamManager)
	actorPool := supervisor.NewActorPoolSimple(upstreamManager, logger)
	supervisorInstance := supervisor.New(configSvc, actorPool, logger)
	upstreamManager.SetConnectionWake(supervisorInstance.Wake)

	// Initialize OAuth refresh manager for proactive token refresh
	// Uses storageManager as the token store and global coordinator for flow coordination
	refreshManager := oauth.NewRefreshManager(
		primaryRefreshStore{db: storageManager.GetDatabase(), config: func() *config.Config { return configSvc.Current().Config }},
		oauth.GetGlobalCoordinator(),
		logger,
	)

	// Initialize activity service for logging tool calls and events
	activityService := NewActivityService(storageManager, logger)

	// Usage aggregate persistence cadence from config.
	if cfg.Observability != nil {
		if interval := cfg.Observability.UsagePersistInterval.Duration(); interval > 0 {
			activityService.usageFlushInterval = interval
		}
	}

	// Per-record size caps are not retention: they apply unconditionally,
	// outside the retention guard below.
	activityService.SetMaxResponseSize(cfg.ActivityMaxResponseSize)
	storageManager.SetToolCallLimits(cfg.ToolCallMaxResponseSize, cfg.ToolCallMaxRecordsPerServer)

	// Wire activity retention config from config file
	if cfg.ActivityRetentionDays > 0 || cfg.ActivityMaxRecords > 0 || cfg.ActivityCleanupIntervalMin > 0 || cfg.EffectiveActivityMaxSizeMB() >= 0 {
		maxAge := time.Duration(cfg.EffectiveActivityRetentionDays()) * 24 * time.Hour
		checkInterval := time.Duration(cfg.ActivityCleanupIntervalMin) * time.Minute
		// ActivityMaxSizeMB: 0 disables the size cap, so pass the explicit byte
		// value (>= 0 is applied; -1 would mean "unchanged").
		maxSizeBytes := int64(cfg.EffectiveActivityMaxSizeMB()) * 1024 * 1024
		activityService.SetRetentionConfig(maxAge, cfg.EffectiveActivityMaxRecords(), checkInterval, maxSizeBytes)
		logger.Info("Activity retention config applied",
			zap.Int("max_response_size", cfg.ActivityMaxResponseSize),
			zap.Int("retention_days", cfg.EffectiveActivityRetentionDays()),
			zap.Int("max_records", cfg.EffectiveActivityMaxRecords()),
			zap.Int("max_size_mb", cfg.EffectiveActivityMaxSizeMB()),
			zap.Int("cleanup_interval_min", cfg.ActivityCleanupIntervalMin))
	}

	rt := &Runtime{
		cfg:             cfg,
		logger:          logger,
		configSvc:       configSvc,
		storageManager:  storageManager,
		indexManager:    indexManager,
		upstreamManager: upstreamManager,
		cacheManager:    cacheManager,
		sigCache:        toolsig.NewCache(),
		secretResolver:  secretResolver,

		refreshManager:  refreshManager,
		activityService: activityService,
		supervisor:      supervisorInstance,
		appCtx:          appCtx,
		appCancel:       appCancel,
		lifetimeCtx:     appCtx,
		status: Status{
			Phase:       PhaseInitializing,
			Message:     "Runtime is initializing...",
			LastUpdated: time.Now(),
		},
		eventSubs: make(map[chan Event]struct{}),

		phaseMachine:      newPhaseMachine(PhaseInitializing),
		lastGoodTools:     make(map[string][]*config.ToolMetadata),
		profileMembership: make(map[string][]string),
	}
	rt.truncator = truncator
	rt.changes = NewChangeHub(rt.Servers, 0, logger)

	// The one clients service behind every client-credential operation. It
	// shares bindingWriteMu with the guarded config apply so a binding write
	// and a config write can never combine into a bypassable state.
	rt.clientsService = NewClientsService(ClientsServiceDeps{
		HostExists: func(id string) bool { return rt.peers != nil && rt.peers.Exists(id) },
		Store:      storageManager,
		HMACKey: func() ([]byte, error) {
			return rt.StorageManager().GetDatabase().AuthenticationKey(), nil
		},
		Config:   rt.Config,
		Activity: storageManager.SaveActivity,
		Changed:  func(id string) { rt.changes.Dirty(EntityClients, id) },
		Mu:       &rt.bindingWriteMu,
		Logger:   logger,
	})

	rt.profilesService = newProfilesService(rt)
	rt.initializePeers()

	storageManager.SetActivityObserver(func(rec *storage.ActivityRecord) {
		rt.changes.Append(EntityActivity, rec.ID, rec)
		rt.changes.Invalidate(EntityUsage, "")
		if counted, _ := storage.CountsAsCall(rec); counted && rec.ServerName != "" && rt.lastUsed.record(rec.ServerName, rec.Timestamp) {
			rt.changes.Dirty(EntityServers, rec.ServerName)
		}
	})
	storageManager.SetSessionObserver(func(sessionID, clientID string) {
		rt.changes.Dirty(EntitySessions, sessionID)
		rt.changes.Dirty(EntityClients, clientID)
	})

	return rt, nil
}

func (r *Runtime) Config() *config.Config { return r.configSvc.Current().Config }

// ConfigSnapshot returns an immutable configuration snapshot.
// This is the preferred way to read configuration - it's lock-free and non-blocking.
func (r *Runtime) ConfigSnapshot() *configsvc.Snapshot { return r.configSvc.Current() }

// ConfigService returns the configuration service for advanced access patterns.
func (r *Runtime) ConfigService() *configsvc.Service {
	return r.configSvc
}

// Supervisor returns the supervisor instance for lock-free state reads via
// StateView, giving fast server status without storage queries.
func (r *Runtime) Supervisor() *supervisor.Supervisor {
	return r.supervisor
}

// SetRunning records whether the server HTTP layer is active.
func (r *Runtime) SetRunning(running bool) {
	r.mu.Lock()
	r.running = running
	r.mu.Unlock()
}

// IsRunning reports the last known running state.
func (r *Runtime) IsRunning() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.running
}

// UpdateStatus mutates the status object and notifies subscribers.
func (r *Runtime) UpdateStatus(phase Phase, message string, stats map[string]interface{}, toolsIndexed int) {
	r.statusMu.Lock()
	r.status.Phase = phase
	r.status.Message = message
	r.status.LastUpdated = time.Now()
	r.status.UpstreamStats = stats
	r.status.ToolsIndexed = toolsIndexed
	r.statusMu.Unlock()

	if r.phaseMachine != nil {
		// Ensure phase machine mirrors the externally provided phase even if this skips validation
		r.phaseMachine.Set(phase)
	}

	if r.logger != nil {
		r.logger.Info("Status updated", zap.String("phase", string(phase)), zap.String("message", message))
	}
}

// UpdatePhase gathers runtime metrics and broadcasts a status update.
func (r *Runtime) UpdatePhase(phase Phase, message string) {
	var (
		stats map[string]interface{}
		tools int
	)

	if r.upstreamManager != nil {
		stats = r.upstreamManager.GetStats()
		tools = extractToolCount(stats)
	}

	if r.phaseMachine != nil {
		if !r.phaseMachine.Transition(phase) {
			if r.logger != nil {
				current := r.phaseMachine.Current()
				r.logger.Warn("Rejected runtime phase transition",
					zap.String("from", string(current)),
					zap.String("to", string(phase)))
			}
			phase = r.phaseMachine.Current()
		}
	}

	r.UpdateStatus(phase, message, stats, tools)
}

// UpdatePhaseMessage refreshes the status message without moving to a new phase.
func (r *Runtime) UpdatePhaseMessage(message string) {
	var (
		stats map[string]interface{}
		tools int
	)

	if r.upstreamManager != nil {
		stats = r.upstreamManager.GetStats()
		tools = extractToolCount(stats)
	}

	phase := r.CurrentPhase()
	r.UpdateStatus(phase, message, stats, tools)
}

// CurrentStatus returns a copy of the underlying status struct.
func (r *Runtime) CurrentStatus() Status {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	return r.status
}

// CurrentPhase returns the current lifecycle phase.
func (r *Runtime) CurrentPhase() Phase {
	if r.phaseMachine != nil {
		return r.phaseMachine.Current()
	}

	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	return r.status.Phase
}

// Logger returns the runtime logger.
func (r *Runtime) Logger() *zap.Logger {
	return r.logger
}

// StorageManager exposes the storage manager.
func (r *Runtime) StorageManager() *storage.Manager {
	return r.storageManager
}

// IndexManager exposes the index manager.
func (r *Runtime) IndexManager() *index.Manager {
	return r.indexManager
}

// UpstreamManager exposes the upstream manager.
func (r *Runtime) UpstreamManager() *upstream.Manager {
	return r.upstreamManager
}

// CacheManager exposes the cache manager.
func (r *Runtime) CacheManager() *cache.Manager {
	return r.cacheManager
}

// Truncator exposes the tool-response truncator configured at startup.
func (r *Runtime) Truncator() *truncate.Truncator { return r.truncator }

// SignatureCache exposes the process-wide compact-signature cache. Exactly one instance exists: the indexing path warms it and the
// MCP request path (via NewMCPProxyServer) reads it. Never construct a second
// cache — warming a cache the request path does not hold is a silent no-op.
func (r *Runtime) SignatureCache() *toolsig.Cache {
	return r.sigCache
}

// AppContext returns the long-lived runtime context.
func (r *Runtime) AppContext() context.Context {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.appCtx
}

// lifetimeContext returns the runtime context that Close cancels and never
// replaces. It is set once in New.
func (r *Runtime) lifetimeContext() context.Context {
	return r.lifetimeCtx
}

// Close releases runtime resources.
func (r *Runtime) Close() error {
	r.mu.Lock()
	if r.appCancel != nil {
		r.appCancel()
		r.appCancel = nil
		r.appCtx = context.Background()
	}
	r.mu.Unlock()
	r.changes.Close()

	var errs []error

	// Stop OAuth refresh manager first to prevent refresh attempts during shutdown
	if r.refreshManager != nil {
		r.refreshManager.Stop()
		if r.logger != nil {
			r.logger.Info("OAuth refresh manager stopped")
		}
	}

	// Stop Supervisor first to stop reconciliation
	if r.supervisor != nil {
		r.supervisor.Stop()
		if r.logger != nil {
			r.logger.Info("Supervisor stopped")
		}
	}

	if r.upstreamManager != nil {

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer shutdownCancel()

		if r.peers != nil {
			r.peers.Close()
		}
		if err := r.upstreamManager.ShutdownAll(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown upstream servers: %w", err))
			if r.logger != nil {
				r.logger.Error("Failed to shutdown upstream servers", zap.Error(err))
			}
		}

	}

	if r.cacheManager != nil {
		r.cacheManager.Close()
	}

	// Finish activity writes before closing the database.
	if r.activityService != nil {
		r.activityService.Stop()
	}

	if r.indexManager != nil {
		if err := r.indexManager.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close index manager: %w", err))
		}
	}

	if r.storageManager != nil {
		if err := r.storageManager.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close storage manager: %w", err))
		}
	}

	// Close ConfigService and its subscribers
	if r.configSvc != nil {
		r.configSvc.Close()
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func extractToolCount(stats map[string]interface{}) int {
	if stats == nil {
		return 0
	}

	if totalTools, ok := stats["total_tools"].(int); ok {
		return totalTools
	}

	servers, ok := stats["servers"].(map[string]interface{})
	if !ok {
		return 0
	}

	result := 0
	for _, value := range servers {
		serverStats, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		if count, ok := serverStats["tool_count"].(int); ok {
			result += count
		}
	}
	return result
}

// GetSecretResolver returns the secret resolver instance
func (r *Runtime) GetSecretResolver() *secret.Resolver {
	return r.secretResolver
}

// NotifySecretsChanged notifies the runtime that secrets have changed and restarts affected servers.
// This method should be called by the HTTP API when secrets are added, updated, or deleted.
func (r *Runtime) NotifySecretsChanged(ctx context.Context, operation, secretName string) error {
	r.logger.Info("Secrets changed, finding affected servers",
		zap.String("operation", operation),
		zap.String("secret_name", secretName))

	r.changes.Invalidate(EntitySecrets, "")

	// Get current config to find servers that use this secret
	cfg := r.Config()
	if cfg == nil {
		return fmt.Errorf("config not available")
	}

	// Find all servers that reference this secret in their env vars or args
	secretRef := fmt.Sprintf("${secret:%s}", secretName)
	var affectedServers []string

	for _, server := range cfg.Servers {
		if server == nil {
			continue
		}
		encoded, err := json.Marshal(server)
		if err == nil && strings.Contains(string(encoded), secretRef) {
			affectedServers = append(affectedServers, server.Name)
		}
	}

	if len(affectedServers) == 0 {
		r.logger.Info("No servers affected by secret change",
			zap.String("secret_name", secretName))
		return nil
	}

	r.logger.Info("Restarting servers affected by secret change",
		zap.String("secret_name", secretName),
		zap.Strings("servers", affectedServers))

	// Restart affected servers in the background
	go func() {
		for _, serverName := range affectedServers {
			r.logger.Info("Restarting server due to secret change",
				zap.String("server", serverName),
				zap.String("secret_name", secretName))

			if err := r.RestartServer(serverName); err != nil {
				r.logger.Error("Failed to restart server after secret change",
					zap.String("server", serverName),
					zap.String("secret_name", secretName),
					zap.Error(err))
			}
		}
	}()

	return nil
}

// GetCurrentConfig returns the current configuration. It may be nil only
// before the config is installed; New rejects a nil config outright.
func (r *Runtime) GetCurrentConfig() *config.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// GetRecentSessions returns recent MCP sessions.
//
// f.Status filters on the session status ("active" / "closed"); f.Profile,
// f.ClientID and f.TokenName are the scope filters ("-" = unattributed).
// The filters and the last-activity ordering are pushed down into storage, so
// they are applied before truncation to f.Limit.
func (r *Runtime) GetRecentSessions(f storage.SessionFilter) ([]*contracts.MCPSession, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	storageRecords, total, err := r.storageManager.GetRecentSessionsFiltered(f)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get recent sessions: %w", err)
	}

	// Convert storage records to contract types
	sessions := make([]*contracts.MCPSession, 0, len(storageRecords))
	for _, rec := range storageRecords {
		sessions = append(sessions, &contracts.MCPSession{
			ID:            rec.ID,
			ClientName:    rec.ClientName,
			ClientVersion: rec.ClientVersion,
			Status:        rec.Status,
			StartTime:     rec.StartTime,
			EndTime:       rec.EndTime,
			LastActivity:  rec.LastActivity,
			ToolCallCount: rec.ToolCallCount,

			HasRoots:      rec.HasRoots,
			HasSampling:   rec.HasSampling,
			Experimental:  rec.Experimental,
			WorkspaceName: rec.WorkspaceName,
			WorkSessionID: rec.WorkSessionID,
			ClientID:      rec.ClientID,
			TokenName:     rec.TokenName,
			Profile:       rec.Profile,
			ProfileSource: rec.ProfileSource,
		})
	}

	// Storage already returns this order before truncating; the re-sort pins
	// the tie-break to session ID rather than storage's key order.
	sort.SliceStable(sessions, func(i, j int) bool {
		if !sessions[i].LastActivity.Equal(sessions[j].LastActivity) {
			return sessions[i].LastActivity.After(sessions[j].LastActivity)
		}
		return sessions[i].ID < sessions[j].ID
	})

	return sessions, total, nil
}

// GetConfig returns a copy of the current configuration
func (r *Runtime) GetConfig() (*config.Config, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.cfg == nil {
		return nil, fmt.Errorf("config not initialized")
	}

	return config.CopyConfig(r.cfg), nil
}

// SetManagementService stores the management service instance. It is called
// after runtime initialization because the service is CONSTRUCTED on top of
// the Runtime (it takes one as its RuntimeOperations), not because of an
// import cycle — internal/management does not import internal/runtime.
func (r *Runtime) SetManagementService(svc management.Service) {
	r.managementService = svc
}

// GetManagementService returns the management service instance.
// Returns nil if service hasn't been set yet.
func (r *Runtime) GetManagementService() management.Service {
	return r.managementService
}

// Servers builds the server object for every configured server, sorted by
// name. It is the one builder behind REST list/get, the servers.changed event
// embed and the MCP management tools. The result carries stored config values
// as written; callers that must not disclose them mask the copy they own.
func (r *Runtime) Servers() ([]contracts.Server, error) {
	supervisor := r.Supervisor()
	if supervisor == nil {
		return nil, fmt.Errorf("server state is unavailable")
	}
	stateView := supervisor.StateView()
	if stateView == nil {
		return nil, fmt.Errorf("server state is unavailable")
	}

	// The committed snapshot supplies BOTH
	// the displayed configuration and the generation/incarnation of each
	// server. The StateView lags commits, so pairing its config with the
	// snapshot's incarnation could show the old server's command or URL with
	// a same-name replacement's incarnation, which the client echoes back as
	// expected_incarnation. Read it before the StateView: the view then never
	// shows a server newer than the identity it carries.
	committed := r.committedSnapshot()
	snapshot := stateView.Snapshot()

	var oauthExpiryWarningHours float64
	if cfg, err := r.GetConfig(); err == nil && cfg != nil {
		// Read here rather than via r.cfg in the loop below: the servers.changed
		// coalescer calls this from its own goroutine, and r.cfg is swapped by
		// applyConfigLocked under r.mu (data race with a concurrent apply).
		oauthExpiryWarningHours = cfg.OAuthExpiryWarningHours
	}

	result := make([]contracts.Server, 0, len(snapshot.Servers))
	snapGens := committed.gens()
	seen := make(map[string]struct{}, len(snapshot.Servers))
	for _, serverStatus := range snapshot.Servers {
		// A server the committed snapshot does not configure is a removed server the view has not caught up
		// with; listing it would offer unbound rows (no incarnation).
		if serverStatus == nil || committed.absent(serverStatus.Name) {
			continue
		}
		serverStatus = committed.pairStatus(serverStatus)
		seen[serverStatus.Name] = struct{}{}
		result = append(result, r.buildServer(serverStatus, snapGens, oauthExpiryWarningHours))
	}
	// A create is committed before the supervisor reconciles it into the
	// StateView. create/list/get all read this list, so a server that exists
	// only in the snapshot must still be returned — not connected, no tools —
	// instead of "server not found".
	for _, serverStatus := range committed.unreconciled(seen) {
		result = append(result, r.buildServer(serverStatus, snapGens, oauthExpiryWarningHours))
	}

	sort.SliceStable(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// ServerStats aggregates a server list into its header counters.
func ServerStats(servers []contracts.Server) contracts.ServerStats {
	out := contracts.ServerStats{TotalServers: len(servers)}
	for i := range servers {
		if servers[i].Connected {
			out.ConnectedServers++
		}
		if config.ServerContributesTools(servers[i].Enabled) {
			out.TotalTools += servers[i].ToolCount
		}
	}
	return out
}

// Server returns the server object for one configured server, or an error
// wrapping contracts.ErrServerNotFound.
func (r *Runtime) Server(name string) (*contracts.Server, error) {
	servers, err := r.Servers()
	if err != nil {
		return nil, err
	}
	for i := range servers {
		if servers[i].Name == name {
			return &servers[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s", contracts.ErrServerNotFound, name)
}

func (r *Runtime) buildServer(serverStatus *stateview.ServerStatus, gens map[string]configsvc.ServerGen, oauthExpiryWarningHours float64) contracts.Server {
	srv := contracts.Server{ID: serverStatus.Name}
	if cfg := config.CopyServerConfig(serverStatus.Config); cfg != nil {
		srv.ServerConfig = *cfg
	}
	srv.Name = serverStatus.Name
	srv.Enabled = serverStatus.Enabled
	if at, ok := r.lastUsed.get(serverStatus.Name, r.storageManager); ok {
		srv.LastUsedAt = &at
	}

	connected := serverStatus.Connected
	connecting := strings.EqualFold(serverStatus.State, "connecting")
	status := serverStatus.State
	if status == "" {
		switch {
		case !serverStatus.Enabled:
			status = "disabled"
		case connecting:
			status = "connecting"
		case connected:
			status = "ready"
		default:
			status = "disconnected"
		}
	}

	mode := config.OAuthModeAuto
	if serverStatus.Config != nil {
		mode = serverStatus.Config.OAuthMode()
	}
	// OAuth mode "on" declares OAuth; auto relies on the evidence below.
	oauthActive := mode == config.OAuthModeOn
	var authenticated, hasRefreshToken bool
	var oauthStatus string // "authenticated", "expired", "error", "none"
	var tokenExpiresAt time.Time
	url := srv.URL
	// A stored token record is not evidence about a server that
	// authenticates with a static Authorization header — see
	// StoredOAuthTokenInPlay.
	if serverStatus.Config != nil && url != "" && r.storageManager != nil && mode != config.OAuthModeOff &&
		r.StoredOAuthTokenInPlay(serverStatus.Name, serverStatus.Config) {
		// PersistentTokenStore keys tokens by name + URL hash ("name_hash16").
		hash := sha256.Sum256([]byte(fmt.Sprintf("%s|%s", serverStatus.Name, url)))
		serverKey := fmt.Sprintf("%s_%s", serverStatus.Name, hex.EncodeToString(hash[:])[:16])
		if token, err := r.storageManager.GetOAuthToken(serverKey); err == nil && token != nil {
			authenticated = true
			oauthActive = true
			tokenExpiresAt = token.ExpiresAt
			hasRefreshToken = token.RefreshToken != ""
			if token.ExpiresAt.IsZero() || time.Now().Before(token.ExpiresAt) {
				oauthStatus = string(oauth.OAuthStatusAuthenticated)
			} else {
				oauthStatus = string(oauth.OAuthStatusExpired)
			}
		} else if oauthActive {
			oauthStatus = string(oauth.OAuthStatusNone)
		}
	}
	// An OAuth error in last_error means discovery found an OAuth-required
	// server that has no usable token (the user needs to sign in).
	if mode != config.OAuthModeOff && oauthStatus != string(oauth.OAuthStatusExpired) &&
		serverStatus.LastError != "" && oauth.IsOAuthError(serverStatus.LastError) {
		if oauthActive {
			oauthStatus = string(oauth.OAuthStatusError)
		} else {
			oauthActive = true
			oauthStatus = string(oauth.OAuthStatusNone)
		}
	}

	lastError := stringutil.CollapseRepeatedErrorWrappers(serverStatus.LastError)
	srv.Connected = connected
	srv.Connecting = connecting
	srv.Status = status
	srv.LastError = lastError
	srv.ToolCount = serverStatus.ToolCount
	srv.ReconnectCount = serverStatus.RetryCount
	srv.OAuthActive = oauthActive
	srv.Authenticated = authenticated
	srv.OAuthStatus = oauthStatus
	if url != "" {
		srv.OAuthMode = mode
	}
	if !tokenExpiresAt.IsZero() {
		expires := tokenExpiresAt
		srv.TokenExpiresAt = &expires
	}
	if g, ok := gens[serverStatus.Name]; ok {
		srv.Generation, srv.Incarnation = g.Gen, g.Incarnation
	}
	if icon := servericon.Effective(srv.Icon, r.upstreamManager.GetServerIcon(serverStatus.Name)); icon != "" {
		srv.IconURL = servericon.Path(icon)
	}
	srv.IconUploaded = srv.Icon != nil && *srv.Icon != ""

	// Structured diagnostic error when available.
	if d := serverStatus.Diagnostic; d != nil {
		diag := &contracts.Diagnostic{
			Code:     string(d.Code),
			Severity: string(d.Severity),
			// mcp-go wraps a send failure at two nesting levels;
			// collapse it as last_error is.
			Cause: stringutil.CollapseRepeatedErrorWrappers(d.Cause),
		}
		if !d.DetectedAt.IsZero() {
			detected := d.DetectedAt
			diag.DetectedAt = &detected
		}
		if entry, ok := diagnostics.Get(d.Code); ok {
			// Prefer the runtime-aware remediation when present.
			diag.UserMessage = entry.UserMessage
			if d.Remediation != "" {
				diag.UserMessage = d.Remediation
			}
		}
		srv.Diagnostic = diag
		srv.ErrorCode = string(d.Code)
	}

	// Call-time OAuth requirement — set when an anonymously-connected
	// server rejected a tools/call with "authorization required".
	var callTimeOAuthRequired bool
	if r.upstreamManager != nil {
		if client, exists := r.upstreamManager.GetClient(serverStatus.Name); exists && client != nil {
			srv.UserLoggedOut = client.IsUserLoggedOut()
			callTimeOAuthRequired = client.IsOAuthCallRequired()
		}
	}

	healthConfig := health.DefaultHealthConfig()
	if oauthExpiryWarningHours > 0 {
		healthConfig.ExpiryWarningDuration = time.Duration(oauthExpiryWarningHours * float64(time.Hour))
	}
	healthInput := health.HealthCalculatorInput{
		Name:      serverStatus.Name,
		Enabled:   serverStatus.Enabled,
		State:     serverStatus.State,
		Connected: connected,
		LastError: lastError,
		// Only a URL-addressed server has an endpoint the user can edit; a
		// stdio server's own network failures must not offer "Edit URL".
		HasEndpointURL:        url != "",
		OAuthRequired:         oauthActive,
		OAuthStatus:           oauthStatus,
		HasRefreshToken:       hasRefreshToken,
		UserLoggedOut:         srv.UserLoggedOut,
		CallTimeOAuthRequired: callTimeOAuthRequired,
		ToolCount:             serverStatus.ToolCount,
		MissingSecret:         health.ExtractMissingSecret(lastError),
		OAuthConfigErr:        health.ExtractOAuthConfigError(lastError),
		// A permanently parked server must not render as a
		// generic "Connection error" that looks like it is still retrying.
		RetryStopped:       serverStatus.RetryStopped,
		RetryStoppedCode:   serverStatus.RetryStoppedCode,
		RetryStoppedReason: serverStatus.RetryStoppedReason,
		RetryCount:         serverStatus.RetryCount,
	}
	// Rolling tool-call failure rate.
	if r.upstreamManager != nil {
		healthInput.CallsInWindow, healthInput.CallFailuresInWindow, healthInput.DominantCallFailureKind =
			r.upstreamManager.CallStats(serverStatus.Name)
	}
	if !tokenExpiresAt.IsZero() {
		healthInput.TokenExpiresAt = &tokenExpiresAt
	}
	// OAuth refresh state.
	if refreshState := r.HealthRefreshState(serverStatus.Name, serverStatus.Config); refreshState != nil {
		healthInput.RefreshState = health.RefreshState(refreshState.State)
		healthInput.RefreshRetryCount = refreshState.RetryCount
		healthInput.RefreshLastError = refreshState.LastError
		healthInput.RefreshNextAttempt = refreshState.NextAttempt
	}
	srv.Health = health.CalculateHealth(healthInput, healthConfig)

	// Agents and the CLI read these directly rather than parsing
	// the health summary.
	if serverStatus.RetryStopped {
		srv.RetryStopped = true
		srv.RetryStoppedCode = serverStatus.RetryStoppedCode
		srv.RetryStoppedReason = serverStatus.RetryStoppedReason
	}

	srv.Quarantine = r.serverQuarantineStats(serverStatus.Name)
	r.projectServerHost(&srv)
	return srv
}

// serverQuarantineStats counts a server's pending, changed and blocked tools;
// nil when all three are zero.
func (r *Runtime) serverQuarantineStats(name string) *contracts.QuarantineStats {
	if r.storageManager == nil {
		return nil
	}
	records, err := r.storageManager.ListToolApprovals(name)
	if err != nil {
		r.logger.Debug("Failed to load tool approvals for server", zap.String("server", name), zap.Error(err))
		return nil
	}
	var stats contracts.QuarantineStats
	for _, rec := range records {
		if rec.Disabled {
			stats.BlockedCount++
		}
		switch rec.Status {
		case storage.ToolApprovalStatusPending:
			stats.PendingCount++
		case storage.ToolApprovalStatusChanged:
			stats.ChangedCount++
		}
	}
	if stats == (contracts.QuarantineStats{}) {
		return nil
	}
	return &stats
}

// committedView is one committed configuration snapshot that a server list
// reads both the displayed configuration and the incarnation from.
type committedView struct{ snap *configsvc.Snapshot }

func (r *Runtime) committedSnapshot() committedView {
	return committedView{snap: r.configSvc.Current()}
}

func (v committedView) gens() map[string]configsvc.ServerGen {
	if v.snap == nil {
		return nil
	}
	return v.snap.ServerGenerations
}

// absent reports whether the committed snapshot authoritatively has no server
// named name: a snapshot a commit published (it carries generations) that does
// not configure it. Between a removal and a same-name re-add the StateView can
// still hold the removed server's row and tools, and there is no incarnation
// to bind a decision on them to, so readers serve nothing for an absent
// server.
func (v committedView) absent(name string) bool {
	return v.snap != nil && v.snap.Config != nil && v.snap.GetServer(name) == nil
}

// pairStatus returns a copy of st whose configuration (and the enabled and
// quarantined flags projected from it) is the committed one, so the row's
// command, URL and quarantine state belong to the incarnation it carries.
func (v committedView) pairStatus(st *stateview.ServerStatus) *stateview.ServerStatus {
	if v.snap == nil || st == nil {
		return st
	}
	cs := v.snap.GetServer(st.Name)
	if cs == nil {
		return st
	}
	paired := *st
	paired.Config = cs
	paired.Enabled = cs.Enabled

	return &paired
}

// unreconciled returns a status for every committed server the StateView does
// not already list. seen holds those names; names returned here are added to
// it so a duplicate config entry is listed once. The status carries the
// committed config and incarnation and no tools.
func (v committedView) unreconciled(seen map[string]struct{}) []*stateview.ServerStatus {
	if v.snap == nil || v.snap.Config == nil {
		return nil
	}
	var out []*stateview.ServerStatus
	for _, server := range v.snap.Config.Servers {
		if server == nil || server.Name == "" {
			continue
		}
		if _, ok := seen[server.Name]; ok {
			continue
		}
		cs := config.CopyServerConfig(server)
		if cs == nil {
			continue
		}
		status := &stateview.ServerStatus{
			Name:    cs.Name,
			Config:  cs,
			Enabled: cs.Enabled,
		}
		if g, ok := v.snap.ServerGeneration(cs.Name); ok {
			status.Incarnation = g.Incarnation
		}
		// Reconcile publishes "connecting" for an enabled server it has no
		// connection info for yet. Match that so the row does not flash
		// "disconnected" in the gap.
		if cs.Enabled {
			status.State = "connecting"
		}
		seen[cs.Name] = struct{}{}
		out = append(out, status)
	}
	return out
}

func (r *Runtime) GetServerTools(serverName string) ([]map[string]interface{}, error) {
	r.logger.Debug("Runtime.GetServerTools called", zap.String("server", serverName))

	// Use Supervisor's StateView for lock-free, instant reads
	if r.supervisor == nil {
		return nil, fmt.Errorf("supervisor not available")
	}

	stateView := r.supervisor.StateView()
	if stateView == nil {
		return nil, fmt.Errorf("StateView not available")
	}

	// Read the committed snapshot before the view. A server it does not configure was removed (and not yet re-added):
	// the view may still hold its tools, but they have no incarnation a
	// decision could bind to, so the read answers not found rather than
	// serving them unbound.
	if r.committedSnapshot().absent(serverName) {
		return nil, fmt.Errorf("server not found: %s", serverName)
	}

	// Get snapshot - this is lock-free and instant
	snapshot := stateView.Snapshot()
	serverStatus, exists := snapshot.Servers[serverName]
	if !exists {
		return nil, fmt.Errorf("server not found: %s", serverName)
	}

	// The REST listings stamp these
	// tools with the server's COMMITTED incarnation, read before this call.
	// The StateView lags the commit, and a remove + same-name re-add leaves
	// it holding the removed server's definitions until reconcile catches up.
	// Its tools are served only when the view was reconciled from the
	// committed incarnation; otherwise they belong to a removed server, and the listing falls through to the index below, which the
	// removal commit purged and which holds only the replacement's tools.
	viewTools := serverStatus.Tools
	if committedInc, ok := r.ServerIncarnation(serverName); ok && serverStatus.Incarnation != committedInc {
		r.logger.Debug("GetServerTools: StateView lags the committed incarnation; not serving its tools",
			zap.String("server", serverName),
			zap.Int64("view_incarnation", serverStatus.Incarnation),
			zap.Int64("committed_incarnation", committedInc))
		viewTools = nil
	}

	// Convert []stateview.ToolInfo to []map[string]interface{}
	tools := make([]map[string]interface{}, 0, len(viewTools))
	for _, tool := range viewTools {
		toolMap := map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"server_name": serverName,
		}
		if tool.InputSchema != nil {
			toolMap["inputSchema"] = tool.InputSchema
		}
		if len(tool.OutputSchemaJSON) > 0 {
			toolMap["outputSchema"] = string(tool.OutputSchemaJSON)
		}
		if tool.Annotations != nil {
			toolMap["annotations"] = tool.Annotations
		}
		tools = append(tools, toolMap)
	}

	if len(tools) == 0 && r.indexManager != nil {
		if indexed, err := r.indexManager.GetToolsByServer(serverName); err == nil && len(indexed) > 0 {
			tools = make([]map[string]interface{}, 0, len(indexed))
			for _, tool := range indexed {
				// Index stores the full tool name; normalize to the bare tool name
				// (no "server:" prefix) to match the StateView/approval-record
				// convention used by the enrichment layer.
				name := tool.Name
				if idx := strings.Index(name, ":"); idx != -1 {
					name = name[idx+1:]
				}
				toolMap := map[string]interface{}{
					"name":        name,
					"description": tool.Description,
					"server_name": serverName,
				}
				if tool.ParamsJSON != "" {
					var inputSchema map[string]interface{}
					if err := json.Unmarshal([]byte(tool.ParamsJSON), &inputSchema); err == nil {
						toolMap["inputSchema"] = inputSchema
					}
				}
				if tool.OutputSchemaJSON != "" {
					toolMap["outputSchema"] = tool.OutputSchemaJSON
				}
				if tool.Annotations != nil {
					toolMap["annotations"] = tool.Annotations
				}
				tools = append(tools, toolMap)
			}
			r.logger.Debug("GetServerTools: StateView empty, served tools from search index fallback",
				zap.String("server", serverName),
				zap.Int("tool_count", len(tools)))
		}
	}

	return tools, nil
}

// TriggerOAuthLogin implements RuntimeOperations interface for management service.
// Initiates OAuth 2.x authentication flow for a specific server.
func (r *Runtime) TriggerOAuthLogin(serverName string) error {
	if err := r.oauthAllowed(serverName); err != nil {
		return err
	}
	if host := r.ServerHost(serverName); host != "" {
		return r.peerOAuthLogin(serverName)
	}
	r.logger.Debug("Runtime.TriggerOAuthLogin called", zap.String("server", serverName))

	// Delegate to upstream manager to start manual OAuth flow
	if r.upstreamManager == nil {
		return fmt.Errorf("upstream manager not available")
	}

	// Clear the user logged out flag to allow connection after successful OAuth
	if err := r.upstreamManager.SetUserLoggedOut(serverName, false); err != nil {
		r.logger.Warn("Failed to clear user logged out state",
			zap.String("server", serverName),
			zap.Error(err))
		// Continue - this is not a fatal error
	}

	// StartManualOAuth launches browser and starts callback server
	if err := r.upstreamManager.StartManualOAuth(serverName, true); err != nil {
		return fmt.Errorf("failed to start OAuth flow: %w", err)
	}

	return nil
}

// TriggerOAuthLoginQuick implements RuntimeOperations interface for management service.
// Returns OAuthStartResult with actual browser status, auth URL, and any errors.
// This is the synchronous version that provides immediate feedback about browser opening.
func (r *Runtime) TriggerOAuthLoginQuick(ctx context.Context, serverName string) (*core.OAuthStartResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.oauthAllowed(serverName); err != nil {
		return nil, err
	}
	if host := r.ServerHost(serverName); host != "" {
		var result core.OAuthStartResult
		callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := r.peers.Call(callCtx, host, "oauth/login", serverName, map[string]string{"origin": httpx.PublicOriginFrom(ctx)}, &result)
		if err == nil && result.AuthURL != "" {
			err = r.registerPeerOAuthCallback(serverName, host, result.AuthURL)
		}
		if err == nil {
			_ = r.upstreamManager.SetUserLoggedOut(serverName, false)
		}
		return &result, err
	}
	r.logger.Debug("Runtime.TriggerOAuthLoginQuick called", zap.String("server", serverName))

	if r.upstreamManager == nil {
		return nil, fmt.Errorf("upstream manager not available")
	}

	// Clear the user logged out flag to allow connection after successful OAuth
	if err := r.upstreamManager.SetUserLoggedOut(serverName, false); err != nil {
		r.logger.Warn("Failed to clear user logged out state",
			zap.String("server", serverName),
			zap.Error(err))
		// Continue - this is not a fatal error
	}

	// StartManualOAuthQuick returns immediately with browser status
	result, err := r.upstreamManager.StartManualOAuthQuick(ctx, serverName)
	if err != nil {
		return result, fmt.Errorf("failed to start OAuth flow: %w", err)
	}

	return result, nil
}

// StoredOAuthTokenInPlay reports whether a record in the oauth_tokens bucket
// (and the RefreshManager schedule built from it) is evidence about the named
// server's authentication state.
//
// It is not when the server has no `oauth` block and carries a static
// Authorization header: the headers-auth strategy runs first and OAuth is
// never attempted while it works, and OAuth would populate that very header,
// so a token from an earlier OAuth login (autodiscovery servers have no
// `oauth` block whose removal could have cleared it) is stale and would
// misreport a connected upstream as expired. The record is left alone
// (logout still removes it); it is simply not consulted.
//
// The live connection has the final say: if the configured header was
// rejected and the OAuth strategy rescued the connection with that very
// token, the token IS in play and is reported as for any other OAuth server.
// A nil config (server known only to the state view) keeps the historical
// behaviour of consulting the record.
func (r *Runtime) StoredOAuthTokenInPlay(name string, cfg *config.ServerConfig) bool {
	if cfg != nil && cfg.OAuthMode() == config.OAuthModeOff {
		return false
	}
	if cfg == nil || cfg.OAuthMode() == config.OAuthModeOn || !cfg.HasStaticAuthorizationHeader() {
		return true
	}
	return r.serverConnectedWithOAuth(name)
}

func (r *Runtime) HealthRefreshState(name string, cfg *config.ServerConfig) *oauth.RefreshStateInfo {
	if r.refreshManager == nil || !r.StoredOAuthTokenInPlay(name, cfg) {
		return nil
	}
	return r.refreshManager.GetRefreshState(name)
}

// serverConnectedWithOAuth reports whether the named server's live connection
// was authenticated by the OAuth strategy. False when the upstream
// manager or the client is absent, or the client is not connected that way.
func (r *Runtime) serverConnectedWithOAuth(name string) bool {
	if r.upstreamManager == nil {
		return false
	}
	client, exists := r.upstreamManager.GetClient(name)
	return exists && client != nil && client.ConnectedWithOAuth()
}

// TriggerOAuthLogout implements RuntimeOperations interface for management service.
// Clears OAuth token and disconnects a specific server.
func (r *Runtime) TriggerOAuthLogout(serverName string) error {
	if host := r.ServerHost(serverName); host != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		binding, ok := r.CurrentCredentialBinding(serverName)
		if !ok {
			return fmt.Errorf("server not configured")
		}
		snapshot, err := r.storageManager.GetDatabase().RevokePeerOAuth(serverName, binding)
		if err != nil {
			return err
		}
		if err := r.peers.Call(ctx, host, "oauth/logout", serverName, snapshot, nil); err != nil && !errors.Is(err, peering.ErrOffline) {
			return err
		}
		if client, ok := r.upstreamManager.GetClient(serverName); ok {
			client.SetUserLoggedOut(true)
			_ = client.Disconnect()
		}
		r.serversChanged(serverName)
		return nil
	}
	r.logger.Debug("Runtime.TriggerOAuthLogout called", zap.String("server", serverName))

	if r.upstreamManager == nil {
		return fmt.Errorf("upstream manager not available")
	}

	// Resolve the instance ONCE and run the logged-out
	// flag and the disconnect against that instance, bound to its
	// (generation, instance). A replacement installed in between (an
	// OAuth-config change, a remove and re-add, a restart) is not touched by
	// a logout aimed at the instance it replaced.
	client, hasClient := r.upstreamManager.GetClient(serverName)
	var gen int64
	var inst managed.InstanceID
	if hasClient && client != nil {
		gen, inst = client.Generation(), client.InstanceID()
		// IMPORTANT: Set user logged out flag FIRST before any other
		// operations. This prevents race conditions where reconnection
		// logic kicks in during ClearOAuthToken or the disconnect.
		client.SetUserLoggedOut(true)
	} else {
		r.logger.Warn("Failed to set user logged out state",
			zap.String("server", serverName),
			zap.Error(fmt.Errorf("server not found: %s", serverName)))
		// Continue - still try to clear token
	}

	// Clear OAuth token from persistent storage
	if err := r.upstreamManager.ClearOAuthToken(serverName); err != nil {
		return fmt.Errorf("failed to clear OAuth token: %w", err)
	}

	// The refresh schedule rides on the token that was just removed; a
	// leftover schedule would report the logged-out server as "Refresh token
	// expired" instead of "Logged out".
	if r.refreshManager != nil {
		r.refreshManager.OnTokenCleared(serverName)
	}

	// Disconnect the instance logged out above to force re-authentication.
	if hasClient && client != nil {
		if err := r.upstreamManager.DisconnectServerAt(serverName, gen, inst); errors.Is(err, upstream.ErrStaleGeneration) {
			r.logger.Debug("OAuth logout: the instance was replaced meanwhile; not disconnecting its replacement",
				zap.String("server", serverName))
		} else if err != nil {
			r.logger.Warn("Failed to disconnect server after OAuth logout",
				zap.String("server", serverName),
				zap.Error(err))
			// Continue - token was cleared which is the primary goal
		}
	}

	return nil
}

// CurrentCredentialBinding reports the credential binding of the instance
// that serves serverName now. It implements
// oauth.RefreshBindingResolver: the RefreshManager ignores the outcome of a
// refresh flight that ran under another binding.
func (r *Runtime) CurrentCredentialBinding(serverName string) (commitview.CredentialBinding, bool) {
	if r.upstreamManager == nil {
		return commitview.CredentialBinding{}, false
	}
	return r.upstreamManager.CurrentCredentialBinding(serverName)
}

// RefreshOAuthToken implements RuntimeOperations interface for management service.
// Triggers token refresh for a specific server.
func (r *Runtime) RefreshOAuthToken(serverName string) error {
	if r.ServerHost(serverName) != "" {
		return fmt.Errorf("OAuth refresh is owned by the execution host")
	}
	r.logger.Debug("Runtime.RefreshOAuthToken called", zap.String("server", serverName))

	if r.upstreamManager == nil {
		return fmt.Errorf("upstream manager not available")
	}

	// Delegate to upstream manager to refresh the token
	if err := r.upstreamManager.RefreshOAuthToken(serverName); err != nil {
		// Connect workers install instances asynchronously (at boot, and on
		// every reconfiguration), so a configured, enabled server can briefly
		// have no instance. That is not a removed server; retry later.
		if errors.Is(err, upstream.ErrNoActiveInstance) && r.serverEnabled(serverName) {
			return fmt.Errorf("%w: %s has no active connection yet", oauth.ErrTokenRefreshTransient, serverName)
		}
		wrapped := fmt.Errorf("failed to refresh OAuth token: %w", err)
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "expired") || strings.Contains(msg, "no refresh token"):
			return diagnostics.WrapOAuthRefreshExpired(wrapped)
		case strings.Contains(msg, "403") || strings.Contains(msg, "invalid_grant"):
			return diagnostics.WrapOAuthRefresh403(wrapped)
		}
		return wrapped
	}

	return nil
}

// SetSessionClientResolver wires the session -> MCP client lookup that stamps
// client_name / client_version onto every activity record at write time.
// No-op if the activity service is not wired.
func (r *Runtime) SetSessionClientResolver(resolver SessionClientResolver) {
	if r.activityService == nil {
		return
	}
	r.activityService.SetSessionClientResolver(resolver)
}

// SetWorkSessionResolver wires the session -> work-session lookup used to stamp
// every activity record.
//
// The resolver reads the id CACHED on the connection rather than re-deriving it.
// Re-deriving per record would let one connection's records disagree: the first
// resolves before the client's project has arrived, the second after.
func (r *Runtime) SetWorkSessionResolver(resolver SessionWorkSessionResolver) {
	if r.activityService == nil {
		return
	}
	r.activityService.SetWorkSessionResolver(resolver)
	r.activityService.SetWorkSessionReaper(r.ReapWorkSessions)
}

// ResolveWorkSession derives the work session for an identity, opening a new one
// when it has been idle past the window. Called once per connection.
func (r *Runtime) ResolveWorkSession(id WorkSessionIdentity) string {
	if r.workSessions == nil {
		r.workSessions = NewWorkSessionTracker(DefaultWorkSessionIdleWindow)
	}
	return r.workSessions.Resolve(id)
}

// ReapWorkSessions drops work sessions idle past maxIdle, so a long-lived daemon
// does not accumulate one map entry per identity forever.
func (r *Runtime) ReapWorkSessions(maxIdle time.Duration) int {
	if r.workSessions == nil {
		return 0
	}
	return r.workSessions.Reap(maxIdle)
}

// Activity logging methods (RFC-003)

// ListActivities returns activity records matching the filter.
func (r *Runtime) ListActivities(filter storage.ActivityFilter) ([]*storage.ActivityRecord, int, error) {
	if r.storageManager == nil {
		return nil, 0, nil
	}
	return r.storageManager.ListActivities(filter)
}

// GetActivity returns a single activity record by ID.
func (r *Runtime) GetActivity(id string) (*storage.ActivityRecord, error) {
	if r.storageManager == nil {
		return nil, nil
	}
	return r.storageManager.GetActivity(id)
}

// AggregateToolUsage rolls up tool_call activity per (server,tool) since the
// given time (spec 050). Returns an empty map when storage is unavailable.
func (r *Runtime) AggregateToolUsage(since time.Time) (map[string]storage.ToolUsageStat, error) {
	if r.storageManager == nil {
		return map[string]storage.ToolUsageStat{}, nil
	}
	return r.storageManager.AggregateToolUsage(since)
}

// UsageSnapshot returns the actor-owned in-memory usage aggregate snapshot
// (spec 069 A2/A3). Reads are lock-free; the returned value is immutable and
// must be treated as read-only. Returns nil when the activity service is
// unavailable.
func (r *Runtime) UsageSnapshot() *UsageAggregate {
	if r.activityService == nil {
		return nil
	}
	return r.activityService.UsageSnapshot()
}

// StreamActivities returns a channel that yields activity records matching the filter.
func (r *Runtime) StreamActivities(filter storage.ActivityFilter) <-chan *storage.ActivityRecord {
	if r.storageManager == nil {
		ch := make(chan *storage.ActivityRecord)
		close(ch)
		return ch
	}
	return r.storageManager.StreamActivities(filter)
}

// ListToolApprovals returns all tool approval records for a server.
func (r *Runtime) ListToolApprovals(serverName string) ([]*storage.ToolApprovalRecord, error) {
	if r.storageManager == nil {
		return nil, nil
	}
	return r.storageManager.ListToolApprovals(serverName)
}

// GetToolApproval returns a single tool approval record.
func (r *Runtime) GetToolApproval(serverName, toolName string) (*storage.ToolApprovalRecord, error) {
	if r.storageManager == nil {
		return nil, fmt.Errorf("storage not available")
	}
	return r.storageManager.GetToolApproval(serverName, toolName)
}
