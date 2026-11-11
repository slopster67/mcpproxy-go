package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/v2/mcp"
	"github.com/mark3labs/mcp-go/v2/server"
	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/dashboardauth"
	"github.com/slopster67/mcpproxy-go/internal/httpapi"
	"github.com/slopster67/mcpproxy-go/internal/management"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/runtime"
	"github.com/slopster67/mcpproxy-go/internal/secret"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/web"
)

// Status represents the current status of the server
type Status struct {
	Phase         string                 `json:"phase"`          // Starting, Ready, Error
	Message       string                 `json:"message"`        // Human readable status message
	UpstreamStats map[string]interface{} `json:"upstream_stats"` // Upstream server statistics
	ToolsIndexed  int                    `json:"tools_indexed"`  // Number of tools indexed
	LastUpdated   time.Time              `json:"last_updated"`
}

type Server struct {
	dashboardAuth         *dashboardauth.Manager
	logger                *zap.Logger
	runtime               *runtime.Runtime
	createAfterConfigRead func(name string)
	profileIndexes        profileIndexCache
	mcpProxy              *MCPProxyServer

	// Server control
	httpServer *http.Server
	running    bool
	mu         sync.RWMutex

	serverCtx    context.Context
	serverCancel context.CancelFunc
	shutdown     bool

	// serveErrCh delivers a fatal serve failure (startup bind error or
	// serve-loop death) out of the async StartServer goroutine so the
	// process can exit instead of lingering unreachable. Buffered (cap 1)
	// so the goroutine never blocks; graceful shutdown never fires it.
	serveErrCh chan error

	//: Track server start time for lifecycle events
	startTime time.Time

	//: Shutdown info for lifecycle events
	shutdownReason string
	shutdownSignal string
}

// NewServer constructs the primary from its resolved environment settings.
func NewServer(cfg *config.Config, logger *zap.Logger) (*Server, error) {
	dashboardAuth := dashboardauth.New(cfg.OIDC)
	if err := dashboardAuth.Initialize(context.Background()); err != nil {
		return nil, fmt.Errorf("invalid dashboard OIDC configuration: %w", err)
	}
	rt, err := runtime.New(cfg, logger)
	if err != nil {
		return nil, err
	}

	cfg = rt.Config()

	// Initialize management service and set it on runtime
	mgmtService := management.NewService(rt, rt, logger.Sugar())
	rt.SetManagementService(mgmtService)

	server := &Server{
		dashboardAuth: dashboardAuth,
		logger:        logger,
		runtime:       rt,
		serveErrCh:    make(chan error, 1),
	}

	// Replace the diagnostics package's placeholder fixers with the real,
	// runtime-backed ones (see diagnostics_fixers.go). builtin_fixers.go has
	// always said a higher layer registers these at startup; this is that
	// layer, and until it existed the self-heal buttons were no-ops that still
	// reported success.

	mcpProxy := NewMCPProxyServer(
		rt.StorageManager(),
		rt.IndexManager(),
		rt.UpstreamManager(),
		rt.CacheManager(),
		rt.Truncator(), // configured at startup
		logger,
		server,

		cfg,
		rt.SignatureCache(), //: the ONE Runtime-owned signature cache
	)

	server.mcpProxy = mcpProxy
	//: the one evaluator behind every guarded write (config routes,
	// connect, client bindings) and the runtime anonymous guard.
	// the profiles service asks the same proxy for effective tools,
	// try, explain and tool counts, and tells it to rewrite live sessions'
	// stored selections on a rename or delete.
	rt.SetProfileEvaluator(mcpProxy)
	rt.ProfilesService().SetSessionHook(mcpProxy)
	//: a binding change clears the stored set_profile selection of every
	// live session of that credential and sends tools/list_changed to each.
	rt.ClientsService().SetNotifier(mcpProxy)

	// Registered BEFORE StartBackgroundInitialization so the first server
	// flush cannot be missed: with an empty initial catalog a
	// missed first change would leave the direct surface blank.
	server.listenForRoutingModeRefresh()

	//: index every config snapshot before it is published —
	// the observer runs on the exact *Config about to be stored — and the
	// startup snapshot now, which NewService stored without running any
	// observer (see warmProfileIndex).
	server.installProfilePublishObserver(mcpProxy)
	server.warmProfileIndex()

	server.runtime.StartBackgroundInitialization()

	return server, nil
}

func (s *Server) installProfilePublishObserver(mcpProxy *MCPProxyServer) *profileChangeNotifier {
	svc := s.runtime.ConfigService()
	if svc == nil {
		return nil
	}
	notifier := newProfileChangeNotifier(mcpProxy, func(name, _ string) { s.runtime.ProfileChanged(name) })
	mcpProxy.profileNotifier = notifier
	notifier.start()
	svc.AddPrePublishObserver(func(cfg *config.Config) {
		// The snapshot this one replaces, read BEFORE the warm path stores the
		// new pair: the delta is computed between the two.
		var previous *config.Config
		if old := s.profileIndexes.Current(); old != nil {
			previous = old.cfg
		}
		s.profileIndexes.warmPublishing(cfg)
		if previous != nil && previous != cfg {
			notifier.enqueue(previous, cfg)
		}
	})
	return notifier
}

// mcpAuthMiddleware admits only credentials issued to saved clients.
func (s *Server) mcpAuthMiddleware(next http.Handler) http.Handler {
	next = directRequestKindMiddleware(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := httpapi.ExtractToken(r)
		if !strings.HasPrefix(token, auth.ClientTokenPrefixStr) {
			http.Error(w, `{"error":"A client token is required"}`, http.StatusUnauthorized)
			return
		}
		cfg := s.runtime.Config()
		if cfg == nil || s.runtime.StorageManager() == nil {
			http.Error(w, `{"error":"Server not ready"}`, http.StatusServiceUnavailable)
			return
		}
		key := s.runtime.StorageManager().GetDatabase().AuthenticationKey()
		record, err := s.runtime.StorageManager().ValidateClientToken(token, key)
		if err != nil {
			http.Error(w, `{"error":"Invalid client token"}`, http.StatusUnauthorized)
			return
		}
		entryHost := ""
		if peerToken := r.Header.Get(peering.Header); peerToken != "" {
			peer, ok := s.runtime.Peers().Authenticate(peerToken)
			if !ok || !s.runtime.Peers().Online(peer.ID) {
				http.Error(w, "Peer is not connected", http.StatusUnauthorized)
				return
			}
			entryHost = peer.ID
		}
		if record.HostID != entryHost {
			http.Error(w, "Client token belongs to a different host", http.StatusForbidden)
			return
		}
		go func() {
			if err := s.runtime.StorageManager().UpdateClientCredentialLastUsed(record.TokenHash); err != nil {
				s.logger.Warn("Failed to update client last-used timestamp", zap.Error(err))
			}
		}()
		caller := record.AuthContext()
		caller.ServerAvailable = func(name string) bool { return s.mcpProxy.hostAllows(caller, name) }
		next.ServeHTTP(w, r.WithContext(auth.WithAuthContext(r.Context(), caller)))
	})
}

func (s *Server) TriggerOAuthLogin(serverName string) error {
	s.logger.Info("OAuth sign-in requested", zap.String("server", serverName))
	manager := s.runtime.UpstreamManager()
	if manager == nil {
		return fmt.Errorf("upstream manager not initialized")
	}
	if err := manager.StartManualOAuth(serverName, true); err != nil {
		s.logger.Error("Failed to start in-process OAuth", zap.String("server", serverName), zap.Error(err))
		return err
	}
	return nil
}

// GetServerReview forwards the scoped REST review read to the runtime.
func (s *Server) GetServerReview(ctx context.Context, serverName string) (*runtime.ServerReview, error) {
	if s.runtime == nil {
		return nil, fmt.Errorf("runtime unavailable")
	}
	return s.runtime.GetServerReview(ctx, serverName)
}

// GetManagementService returns the management service instance from runtime.
// Returns nil if service hasn't been set yet.
func (s *Server) GetManagementService() management.Service {
	if s.runtime == nil {
		return nil
	}
	return s.runtime.GetManagementService()
}

// updateStatus updates the current status and notifies subscribers
func (s *Server) updateStatus(phase runtime.Phase, message string) {
	s.runtime.UpdatePhase(phase, message)
}

// listenForRoutingModeRefresh updates tool sets when the server inventory
// changes. The hub calls the observer on its flush goroutine, so the refresh
// runs on its own goroutine behind a one-slot signal: bursts collapse into one
// refresh and the hub never blocks on it.
func (s *Server) listenForRoutingModeRefresh() {
	signal := make(chan struct{}, 1)
	s.runtime.Changes().OnServersChanged(func() {
		select {
		case signal <- struct{}{}:
		default:
		}
	})
	go func() {
		appCtx := s.runtime.AppContext()
		for {
			select {
			case <-appCtx.Done():
				return
			case <-signal:
			}
			s.warmProfileIndex()
			if s.mcpProxy != nil {
				s.mcpProxy.RefreshDirectModeTools()
				s.mcpProxy.RefreshCodeExecModeTools()
				// retrieve_tools' description names the reachable servers;
				// re-list clients when that set changes.
				s.mcpProxy.NotifyUpstreamInventoryChanged()
			}
		}
	}()
}

// Start starts the MCP proxy server
func (s *Server) Start(ctx context.Context) error {
	//: Track server start time for lifecycle events
	s.mu.Lock()
	s.startTime = time.Now()
	s.mu.Unlock()

	s.logger.Info("Starting MCP proxy server")

	// Handle graceful shutdown when context is cancelled (for full application shutdown only)
	go func() {
		<-ctx.Done()
		s.logger.Info("Main context cancelled, shutting down server")
		// First shutdown the HTTP server
		if err := s.StopServer(); err != nil {
			s.logger.Error("Error stopping server during context cancellation", zap.Error(err))
		}
		// Then shutdown the rest (only for full application shutdown, not server restarts)
		// We distinguish this by checking if the cancelled context is the application context
		runtimeCtx := s.runtime.AppContext()
		s.mu.Lock()
		alreadyShutdown := s.shutdown
		isAppContext := (ctx == runtimeCtx)
		s.mu.Unlock()

		if !alreadyShutdown && isAppContext {
			s.logger.Info("Application context cancelled, performing full shutdown")
			if err := s.Shutdown(); err != nil {
				s.logger.Error("Error during context-triggered shutdown", zap.Error(err))
			}
		} else if !isAppContext {
			s.logger.Info("Server context cancelled, server stop completed")
		}

		s.logger.Info("SERVER SHUTDOWN SEQUENCE COMPLETED")
		_ = s.logger.Sync()
	}()

	cfg := s.runtime.Config()
	listenAddr := ""
	if cfg != nil {
		listenAddr = cfg.Listen
	}

	// Determine transport mode based on listen address
	if listenAddr != "" {
		// Start the MCP server in HTTP mode (Streamable HTTP)
		s.logger.Info("Starting MCP server",
			zap.String("transport", "streamable-http"),
			zap.String("listen", listenAddr))

		// Create Streamable HTTP server with custom routing
		// Use the MCP server instance that corresponds to the configured routing_mode
		routingMode := ""
		if cfg != nil {
			routingMode = cfg.RoutingMode
		}
		streamableServer := server.NewStreamableHTTPServer(s.mcpProxy.GetMCPServerForMode(routingMode),
			clientFacingStreamableOptions()...)

		// Create custom HTTP server for handling multiple routes
		if err := s.startCustomHTTPServer(ctx, streamableServer); err != nil {
			var portErr *PortInUseError
			if errors.As(err, &portErr) {
				return err
			}
			return fmt.Errorf("MCP Streamable HTTP server error: %w", err)
		}

		s.runtime.SetRunning(true)
	} else {
		return fmt.Errorf("MCP clients must connect over HTTP using a token created in Clients")
	}

	return nil
}

// discoverAndIndexTools discovers tools from upstream servers and indexes them

// SetShutdownInfo sets the reason and signal for shutdown.
// Call this before Shutdown() to include shutdown context in activity logs.
func (s *Server) SetShutdownInfo(reason, signal string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdownReason = reason
	s.shutdownSignal = signal
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown() error {
	s.mu.Lock()
	if s.shutdown {
		s.mu.Unlock()
		s.logger.Info("Server already shutdown, skipping")
		return nil
	}
	s.shutdown = true
	httpServer := s.httpServer
	startTime := s.startTime
	reason := s.shutdownReason
	signal := s.shutdownSignal
	s.mu.Unlock()

	//: Emit system_stop event before actual shutdown begins
	if s.runtime != nil && !startTime.IsZero() {
		uptimeSeconds := int64(time.Since(startTime).Seconds())
		if reason == "" {
			reason = "shutdown"
		}
		s.runtime.EmitActivitySystemStop(reason, signal, uptimeSeconds, "")
	}

	s.logger.Info("Shutting down MCP proxy server...")

	// Gracefully shutdown HTTP server first to stop accepting new connections
	if httpServer != nil {
		s.logger.Info("Gracefully shutting down HTTP server...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(ctx); err != nil {
			s.logger.Warn("HTTP server forced shutdown due to timeout", zap.Error(err))
			// Force close if graceful shutdown times out
			httpServer.Close()
		} else {
			s.logger.Info("HTTP server shutdown completed gracefully")
		}
	}

	if err := s.runtime.Close(); err != nil {
		s.logger.Error("Failed to close runtime", zap.Error(err))
	}

	// Close MCP proxy server (includes JavaScript runtime pool cleanup)
	if s.mcpProxy != nil {
		if err := s.mcpProxy.Close(); err != nil {
			s.logger.Error("Failed to close MCP proxy server", zap.Error(err))
		}
	}

	s.logger.Info("MCP proxy server shutdown complete")
	return nil
}

// IsRunning returns whether the server is currently running
func (s *Server) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// IsReady returns whether the server is fully initialized and ready to serve requests
func (s *Server) IsReady() bool {
	status := s.runtime.CurrentStatus()

	switch status.Phase {
	case runtime.PhaseReady:
		return true
	case runtime.PhaseRunning:
		return true
	case runtime.PhaseError,
		runtime.PhaseStopping,
		runtime.PhaseStopped,
		runtime.PhaseInitializing,
		runtime.PhaseLoading,
		runtime.PhaseStarting:
		return false
	default:
		// Future phases fall back to actual running state.
		return s.IsRunning()
	}
}

// ListServers returns the server objects (runtime.Runtime.Servers).
func (s *Server) ListServers() ([]contracts.Server, error) {
	return s.runtime.Servers()
}

// GetServer returns one server object (runtime.Runtime.Server).
func (s *Server) GetServer(name string) (*contracts.Server, error) {
	return s.runtime.Server(name)
}

func (s *Server) AddServer(ctx context.Context, serverConfig *config.ServerConfig) error {
	s.logger.Info("Adding upstream server",
		zap.String("name", serverConfig.Name),
		zap.String("protocol", serverConfig.Protocol),
		zap.Bool("enabled", serverConfig.Enabled),
		zap.Bool("quarantined", false))

	// Set creation timestamp
	serverConfig.Created = time.Now()

	// Create-only: refuses an existing name (runtime config or storage) and
	// commits it atomically. A failed add touches nothing.
	if err := s.createServer(serverConfig); err != nil {
		return err
	}

	// Notify about upstream server change
	s.OnUpstreamServerChange()

	s.logger.Info("Server added successfully",
		zap.String("name", serverConfig.Name))

	return nil
}

// applyServerUpdates applies a partial update to existing in place: the
// non-zero/non-nil fields of updates, plus Enabled (resolved against the
// existing server by the REST handler). It is the field logic UpdateServer and PatchServer share.
func applyServerUpdates(existing, updates *config.ServerConfig) {
	if updates.HostID != nil {
		host := *updates.HostID
		existing.HostID = &host
	}
	if updates.AvailableToPeers != nil {
		shared := *updates.AvailableToPeers
		existing.AvailableToPeers = &shared
	}
	if updates.Icon != nil {
		icon := *updates.Icon
		if icon == "" {
			existing.Icon = nil
		} else {
			existing.Icon = &icon
		}
	}
	if updates.Description != nil {
		if description := *updates.Description; description == "" {
			existing.Description = nil
		} else {
			existing.Description = &description
		}
	}
	if updates.URL != "" {
		existing.URL = updates.URL
	}
	if updates.Command != "" {
		existing.Command = updates.Command
	}
	if updates.Args != nil {
		existing.Args = updates.Args
	}
	if updates.Env != nil {
		existing.Env = updates.Env
	}
	if updates.Headers != nil {
		existing.Headers = updates.Headers
	}
	if updates.WorkingDir != "" {
		existing.WorkingDir = updates.WorkingDir
	}
	if updates.Protocol != "" {
		existing.Protocol = updates.Protocol
	}
	existing.OAuth = updates.OAuth

	// Enabled is resolved against the existing server by the REST handler.
	existing.Enabled = updates.Enabled

	// AutoApproveToolChanges is a tri-state *bool: nil means
	// "leave unchanged" so callers that don't touch it (e.g. config-to-secret)
	// don't reset it. A non-nil pointer (including a pointer to false) is
	// applied. The PATCH handler preserves the existing pointer when the
	// request omits the field, so this nil-guard is the second half of the
	// nil-preserve contract.

	// TrustMode is a plain string: empty means "leave unchanged"; a
	// non-empty value is applied. The PATCH handler preserves the existing value
	// when the request omits the field, so this empty-guard is the second half of
	// the leave-unchanged contract.
	if updates.TrustMode != "" {
		existing.TrustMode = updates.TrustMode
	}

}

// UpdateServer applies partial updates to an existing upstream server
// configuration.: one config commit, applied to the LIVE entry read
// inside the commit lock (so config-only fields such as Shared and AuthBroker
// survive, and a server removed concurrently is "not found").

func (s *Server) PatchServer(ctx context.Context, serverName string, resolve func(cur *config.ServerConfig) (*config.ServerConfig, error)) error {
	s.logger.Info("Updating upstream server", zap.String("name", serverName))

	_, err := s.runtime.Commit("update_server", func(tx *runtime.CommitTx) error {
		return tx.UpdateServer(serverName, func(cur *config.ServerConfig) error {
			updates, err := resolve(config.CopyServerConfig(cur))
			if err != nil {
				return err
			}
			if updates == nil {
				return nil
			}
			applyServerUpdates(cur, updates)
			// review R2.1: the concurrency overrides are validated
			// against the LIVE global defaults before anything is persisted,
			// so the commit can never write an entry the next start refuses.

			return nil
		})
	})
	if err != nil {
		if errors.Is(err, runtime.ErrServerNotFound) {
			return fmt.Errorf("server '%s' not found", serverName)
		}
		return err
	}

	// Notify about change
	s.OnUpstreamServerChange()

	s.logger.Info("Server updated successfully", zap.String("name", serverName))

	return nil
}

// RemoveServer removes an upstream server from the configuration.
// This stops the server if running and removes it from storage.
//
// : the removal is one config commit — the live config, the storage
// row and its whole purge set (PR2: tool approvals, OAuth state,
// scan results, baseline, history), the file and the search index lose the
// server together, the refresh schedule and other in-memory state are dropped,
// and the manager instance is unregistered before this returns. There is no
// cleanup after the commit for a crash or a concurrent writer to skip.
func (s *Server) RemoveServer(ctx context.Context, serverName string) error {
	s.logger.Info("Removing upstream server", zap.String("name", serverName))

	purged, err := s.removeServerFromConfig(serverName)
	if err != nil {
		if errors.Is(err, runtime.ErrServerNotFound) {
			return fmt.Errorf("server '%s' not found", serverName)
		}
		return fmt.Errorf("failed to remove server from storage: %w", err)
	}

	// Notify about upstream server change
	s.OnUpstreamServerChange()

	s.logger.Info("Server removed successfully",
		zap.String("name", serverName),
		zap.Int("purged_records", purged.Total()))

	return nil
}

// EnableServer enables/disables a server and ensures all state is synchronized.
// It acts as the entry point for changes originating from the UI or API.
func (s *Server) EnableServer(serverName string, enabled bool) error {
	return s.runtime.EnableServer(serverName, enabled)
}

// RestartServer restarts an upstream server
func (s *Server) RestartServer(serverName string) error {
	return s.runtime.RestartServer(serverName)
}

// StartServer starts the server if it's not already running
func (s *Server) StartServer(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("server is already running")
	}

	// CRITICAL: Validate data directory security BEFORE starting background goroutine
	// This ensures permission errors are returned synchronously with proper exit codes
	cfg := s.runtime.Config()
	if cfg != nil && cfg.DataDir != "" {
		if err := ValidateDataDirectory(cfg.DataDir, s.logger); err != nil {
			s.logger.Error("Data directory security validation failed",
				zap.Error(err),
				zap.String("fix", fmt.Sprintf("chmod 0700 %s", cfg.DataDir)))
			return &PermissionError{Path: cfg.DataDir, Err: err}
		}
	}

	// Cancel the old context before creating a new one to avoid race conditions
	if s.serverCancel != nil {
		s.serverCancel()
	}

	s.serverCtx, s.serverCancel = context.WithCancel(ctx)

	go func() {
		var serverError error

		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			s.runtime.SetRunning(false)

			// Only send "Stopped" status if there was no error
			// If there was an error, the error status should remain.
			// errors.Is: graceful cancellation may arrive wrapped
			// (e.g. "MCP Streamable HTTP server error: context canceled").
			if serverError == nil || errors.Is(serverError, context.Canceled) {
				s.updateStatus(runtime.PhaseStopped, "Server has stopped")
			}
		}()

		s.mu.Lock()
		s.running = true
		s.mu.Unlock()
		s.runtime.SetRunning(true)

		// Notify about server start
		s.updateStatus(runtime.PhaseStarting, "Server is starting...")

		serverError = s.Start(s.serverCtx)
		if serverError != nil && !errors.Is(serverError, context.Canceled) {
			s.logger.Error("Server error during background start", zap.Error(serverError))
			s.updateStatus(runtime.PhaseError, fmt.Sprintf("Server error: %v", serverError))
			// Deliver the fatal error to ServeErr() so the process can
			// exit (e.g. exit code 2 on port conflict) instead of
			// lingering with no listeners. Non-blocking: buffered cap 1
			// tolerates restarts when nobody is draining the channel.
			select {
			case s.serveErrCh <- serverError:
			default:
			}
		}
	}()

	return nil
}

// ServeErr delivers a fatal serve failure: a startup bind error (e.g.
// *PortInUseError) or a serve-loop death after start. Graceful shutdown
// (context cancellation, http.ErrServerClosed) never fires it.
func (s *Server) ServeErr() <-chan error {
	return s.serveErrCh
}

// StopServer stops the server if it's running
func (s *Server) StopServer() error {
	s.logger.Info("STOPSERVER CALLED - STARTING SHUTDOWN SEQUENCE")
	_ = s.logger.Sync()

	s.mu.Lock()
	// Check if Shutdown() has already been called - prevent duplicate shutdown
	if s.shutdown {
		s.mu.Unlock()
		s.logger.Debug("Server shutdown already in progress via Shutdown(), skipping StopServer")
		return nil
	}
	if !s.running {
		s.mu.Unlock()
		// Return nil instead of error to prevent race condition logs
		s.logger.Debug("Server stop requested but server is not running")
		return nil
	}

	// Capture httpServer reference while holding the lock
	httpServer := s.httpServer
	s.mu.Unlock()

	// Notify about server stopping
	s.logger.Info("STOPSERVER - Server is running, proceeding with stop")
	_ = s.logger.Sync()

	s.updateStatus(runtime.PhaseStopping, "Server is stopping...")

	// STEP 1: Gracefully shutdown HTTP server FIRST to stop accepting new connections
	// This must happen before we disconnect upstream servers to prevent new requests
	if httpServer != nil {
		s.logger.Info("STOPSERVER - Shutting down HTTP server gracefully")
		_ = s.logger.Sync()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			s.logger.Warn("STOPSERVER - HTTP server forced shutdown due to timeout", zap.Error(err))
			// Force close if graceful shutdown times out
			if closeErr := httpServer.Close(); closeErr != nil {
				s.logger.Error("STOPSERVER - Failed to force close HTTP server", zap.Error(closeErr))
			}
		} else {
			s.logger.Info("STOPSERVER - HTTP server shutdown completed gracefully")
		}
		_ = s.logger.Sync()
	}

	// STEP 2: Disconnect upstream servers AFTER HTTP server is shut down
	// This ensures no new requests can come in while we're disconnecting
	// Use a FRESH context (not the cancelled server context) for cleanup
	s.logger.Info("STOPSERVER - Disconnecting upstream servers with parallel cleanup")
	_ = s.logger.Sync()

	// NEW: Create dedicated cleanup context with generous timeout (45 seconds)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cleanupCancel()

	// NEW: Use ShutdownAll for parallel cleanup with proper container verification
	if err := s.runtime.UpstreamManager().ShutdownAll(cleanupCtx); err != nil {
		s.logger.Error("STOPSERVER - Failed to shutdown upstream servers", zap.Error(err))
		_ = s.logger.Sync()
	} else {
		s.logger.Info("STOPSERVER - Successfully shutdown all upstream servers")
		_ = s.logger.Sync()
	}

	// STEP 3: Cancel the server context to signal other components
	s.logger.Info("STOPSERVER - Cancelling server context")
	_ = s.logger.Sync()
	s.mu.Lock()
	if s.serverCancel != nil {
		s.serverCancel()
	}

	// Set running to false immediately after server is shut down
	s.running = false
	s.mu.Unlock()
	s.runtime.SetRunning(false)

	// Notify about server stopped with explicit status update
	s.updateStatus(runtime.PhaseStopped, "Server has been stopped")

	s.logger.Info("STOPSERVER - All operations completed successfully")
	_ = s.logger.Sync() // Final log flush

	return nil
}

func resolveDisplayAddress(actual, requested string) string {
	host, port, err := net.SplitHostPort(actual)
	if err != nil {
		return actual
	}

	if host == "" || host == "::" || host == "0.0.0.0" {
		if reqHost, _, reqErr := net.SplitHostPort(requested); reqErr == nil {
			if reqHost != "" && reqHost != "::" && reqHost != "0.0.0.0" {
				host = reqHost
			} else {
				host = "127.0.0.1"
			}
		} else {
			host = "127.0.0.1"
		}
	}

	return net.JoinHostPort(host, port)
}

func (s *Server) warmProfileIndex() {
	if s.runtime == nil {
		return
	}
	s.profileIndexes.warmCurrent(s.runtime.Config())
}

func (s *Server) registerHTTPHandlers(mux *http.ServeMux, httpAPIServer http.Handler) {
	mux.Handle("/server-icons/", httpAPIServer)
	mux.Handle("/peer/", s.streamingNoDeadline(httpAPIServer))
	mux.Handle("/api/", httpAPIServer)
	// The change stream is long-lived: it must escape http.Server.WriteTimeout
	// (and, being a GET, ReadTimeout) or the connection dies mid-stream.
	mux.Handle("/api/v1/events", s.streamingNoDeadline(httpAPIServer))
	mux.Handle("/auth/", httpAPIServer)

	// Mount health endpoints directly on main mux at root level
	healthEndpoints := []string{"/healthz", "/readyz"}
	for _, endpoint := range healthEndpoints {
		mux.Handle(endpoint, httpAPIServer)
	}

	s.logger.Info("Registered REST API endpoints", zap.Strings("api_endpoints", []string{"/api/v1/*"}))
	s.logger.Info("Registered health endpoints", zap.Strings("health_endpoints", healthEndpoints))

}

// httpServerTimeouts resolves the Read/Write/Idle deadlines applied to the
// main http.Server. Extracted as a pure function so the policy is
// unit-testable without binding a socket. A nil config yields the built-in
// defaults (120s read / 120s write / 180s idle). The write deadline is a
// wall-clock cap on a whole response, so the streaming routes opt out of it
// per-request via streamingNoDeadline rather than the process disabling it
// globally. ReadHeaderTimeout is NOT configurable and stays at 60s: it is the
// deadline that actually defends against slowloris, and nothing legitimate
// needs it relaxed.
func httpServerTimeouts(cfg *config.Config) (read, write, idle time.Duration) {
	if cfg == nil {
		cfg = &config.Config{} // all-nil pointers → the built-in defaults
	}
	return cfg.ResolveHTTPReadTimeout(), cfg.ResolveHTTPWriteTimeout(), cfg.ResolveHTTPIdleTimeout()
}

// streamingNoDeadline clears the per-request write deadline (and, for
// body-less GET/HEAD streams, the read deadline) inherited from
// http.Server.WriteTimeout/ReadTimeout, so long tool calls and long-lived SSE
// streams are never truncated mid-response.
//
// Only the streaming routes are wrapped — the MCP endpoints and /events. Every
// other route (REST, Web UI, health) keeps the configured write
// deadline, which is what protects a non-loopback deployment from slow readers.
// The read deadline is cleared for GET/HEAD only: those have no request body,
// whereas a POST must keep its read deadline so a slow-body upload stays
// bounded.
//
// This must be the OUTERMOST wrapper on a route: http.NewResponseController
// unwraps only ResponseWriters that implement Unwrap() http.ResponseWriter, so
// running it before any logging/auth wrappers guarantees it reaches the real
// connection. A controller error (e.g. http.ErrNotSupported behind a
// non-cooperating ResponseWriter) is logged at Debug and otherwise ignored:
// failing to relax a deadline must never fail the request.
func (s *Server) streamingNoDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Time{}); err != nil {
			s.logger.Debug("Could not clear the write deadline for a streaming route",
				zap.String("path", oauth.LogSafeRequestPath(r.URL.Path)),
				zap.Error(err))
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			if err := rc.SetReadDeadline(time.Time{}); err != nil {
				s.logger.Debug("Could not clear the read deadline for a streaming route",
					zap.String("path", oauth.LogSafeRequestPath(r.URL.Path)),
					zap.Error(err))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func clientFacingStreamableOptions() []server.StreamableHTTPOption {
	return []server.StreamableHTTPOption{
		server.WithDisableLocalhostProtection(true),
		server.WithStreamableHTTPProtocolVersions(mcp.LegacyProtocolVersions()...),
	}
}

// registerMCPHandler exposes only the configured MCP surface. Removed paths
// return 404 instead of falling through to the web UI or another routing mode.
func registerMCPHandler(mux *http.ServeMux, handler http.Handler) {
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", http.NotFoundHandler())
}

func (s *Server) startCustomHTTPServer(ctx context.Context, streamableServer *server.StreamableHTTPServer) error {
	cfg := s.runtime.Config()
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}

	// CRITICAL: Validate data directory security before starting
	if err := ValidateDataDirectory(cfg.DataDir, s.logger); err != nil {
		s.logger.Error("Data directory security validation failed",
			zap.Error(err),
			zap.String("fix", fmt.Sprintf("chmod 0700 %s", cfg.DataDir)))
		return &PermissionError{Path: cfg.DataDir, Err: err}
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		if isAddrInUseError(err) {
			return &PortInUseError{Address: cfg.Listen, Err: err}
		}
		return fmt.Errorf("failed to listen: %w", err)
	}
	defer listener.Close()

	mux := http.NewServeMux()

	// Create a logging wrapper for debugging client connections
	loggingHandler := s.mcpLoggingHandler

	mcpHandler := s.streamingNoDeadline(s.mcpAuthMiddleware(loggingHandler(streamableServer)))
	registerMCPHandler(mux, mcpHandler)

	s.logger.Info("Registered MCP endpoint",
		zap.String("path", "/mcp"),
		zap.String("routing_mode", cfg.RoutingMode))

	// API v1 endpoints with chi router for REST API and SSE.
	// Apply optional HTTP tracing middleware.
	httpAPIServer := httpapi.NewServer(s, s.logger.Sugar(), s.runtime.Config().EffectiveLogConfig(), s.dashboardAuth)
	// the profiles service behind /profiles and /access/explain
	// (both editions: profiles are admin-owned config in either).
	httpAPIServer.SetProfilesService(s.runtime.ProfilesService())
	httpAPIServer.RegisterChangeHydrators(s.runtime.Changes())
	// the `profiles` admin MCP tool reads the same admin views the
	// REST routes do (one implementation,).
	s.mcpProxy.SetAdminViews(httpAPIServer)
	// Wire the sensitive-data masker so an activity record the detector flagged
	// never serves the credential it flagged. Same config as the
	// detector the activity service scans with, so the two can never disagree
	// about which categories count.
	//
	// Installed unconditionally, including when detection is currently off:
	// records flagged while it was on are still in the log, and turning the
	// feature off must not start serving their credentials in cleartext.
	if cfg := s.runtime.Config(); cfg != nil {
	}
	// Wire the clients (client-binding credential) service.
	if cfg := s.runtime.Config(); cfg != nil {
		if clients := s.runtime.ClientsService(); clients != nil {
			httpAPIServer.SetClientsService(clients)

		}

	}

	// Forward REST API, events and health onto the outer
	// mux. Extracted so the routing is unit-testable.
	s.registerHTTPHandlers(mux, httpAPIServer)

	// Web UI endpoints (serves embedded Vue.js frontend) ; API routes require an OIDC session.
	// Explicit HTTPS reverse-proxy callbacks reuse OAuth's state/PKCE flow.
	// The upstream OAuth callback dispatcher requires a
	// live, single-use state and never initiates a sign-in itself.
	mux.HandleFunc(config.OAuthRemoteCallbackPrefix, s.oauthCallback)

	webUIHandler := web.NewHandler(s.logger.Sugar())
	selectiveProtectedWebUIHandler := httpAPIServer.ProtectUI(http.StripPrefix("/ui", webUIHandler))
	mux.Handle("/ui/", selectiveProtectedWebUIHandler)
	// Redirect root to web UI
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui/", http.StatusFound)
			return
		}

		http.NotFound(w, r)
	})
	s.logger.Info("Registered Web UI endpoints", zap.Strings("ui_endpoints", []string{"/ui/", "/"}))

	actualAddr := listener.Addr().String()
	displayAddr := resolveDisplayAddress(actualAddr, cfg.Listen)
	s.logger.Info("HTTP listener created", zap.String("address", displayAddr))

	// the request deadlines are configurable. The write deadline keeps
	// its 120s default for ordinary endpoints; the streaming routes (MCP,
	// /events) clear it per-request via streamingNoDeadline so slow tool calls
	// and SSE streams survive without weakening the rest of the surface.
	readTimeout, writeTimeout, idleTimeout := httpServerTimeouts(cfg)

	s.mu.Lock()
	s.httpServer = &http.Server{
		Addr:              cfg.Listen,
		Handler:           publicOriginHandler(mux),
		ReadHeaderTimeout: 60 * time.Second, // Slowloris guard — deliberately not configurable
		ReadTimeout:       readTimeout,      // Full request read timeout (http_read_timeout)
		WriteTimeout:      writeTimeout,     // Response write timeout (http_write_timeout; 0 = none; streaming routes opt out)
		IdleTimeout:       idleTimeout,      // Keep-alive timeout for persistent connections (http_idle_timeout)
		MaxHeaderBytes:    1 << 20,          // 1MB max header size
		// Enable connection state tracking for better debugging
		ConnState: s.logConnectionState,
	}
	s.httpServer.RegisterOnShutdown(httpAPIServer.Close)
	s.running = true
	s.runtime.SetRunning(true)
	s.mu.Unlock()

	// Broadcast running status with resolved listen address so readiness checks succeed immediately.
	s.updateStatus(runtime.PhaseRunning, fmt.Sprintf("Server is running on %s", displayAddr))

	//: Emit system_start activity event
	startupDurationMs := time.Since(s.startTime).Milliseconds()
	s.runtime.EmitActivitySystemStart(
		httpapi.GetBuildVersion(),
		displayAddr,
		startupDurationMs,
	)

	// List all registered endpoints for visibility
	allEndpoints := []string{
		"/mcp",      // MCP protocol endpoint
		"/api/v1/*", // REST API (change stream at /api/v1/events)
		"/ui/", "/", // Web UI endpoints
		"/healthz", "/readyz", // Health endpoints (at root level)
	}

	s.logger.Info("Starting MCP HTTP server",
		zap.String("protocol", "HTTP"),
		zap.String("address", actualAddr),
		zap.String("requested_address", cfg.Listen),
		zap.Strings("endpoints", allEndpoints),
		zap.Duration("read_timeout", readTimeout),
		zap.Duration("write_timeout", writeTimeout),
		zap.Duration("idle_timeout", idleTimeout),
		zap.String("features", "connection_tracking,graceful_shutdown,enhanced_logging"),
	)

	// Setup error channel for server communication
	serverErrCh := make(chan error, 1)

	go func() {
		err := s.httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.mu.Lock()
		if err != nil {
			s.running = false
		}
		s.mu.Unlock()
		if err != nil {
			s.logger.Error("HTTP server failed", zap.Error(err))
			s.runtime.SetRunning(false)
			s.updateStatus(runtime.PhaseError, fmt.Sprintf("HTTP server failed: %v", err))
		} else {
			s.logger.Info("HTTP server stopped")
		}
		serverErrCh <- err
	}()

	// Wait for either context cancellation or server error
	select {
	case <-ctx.Done():
		s.logger.Info("Server context cancelled, shutdown will be handled by StopServer")
		// HTTP server shutdown is now handled synchronously in StopServer()
		// to avoid race conditions during graceful shutdown
		return ctx.Err()
	case err := <-serverErrCh:
		return err
	}
}

// responseWriter wraps http.ResponseWriter to capture the status code
type responseWriter struct {
	http.ResponseWriter
	statusCode    int
	headerWritten bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.headerWritten {
		rw.statusCode = code
		rw.headerWritten = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

// Flush delegates to the underlying ResponseWriter so that SSE keeps working
// through this wrapper.
//
// Without it, wrapping the writer silently strips http.Flusher, and the MCP
// transport's CanStream() check fails: GET /mcp — the listening stream the
// protocol uses for every SERVER-TO-CLIENT message — is rejected with
// "405 Streaming unsupported". That kills roots, sampling and elicitation over
// HTTP, since the server has no channel on which to ask the client anything.
//
// Found while implementing: the roots request went out and timed out
// every time, because the client was never able to receive it.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logConnectionState logs HTTP connection state changes for debugging client issues
func (s *Server) logConnectionState(conn net.Conn, state http.ConnState) {
	// Handle cases where conn or RemoteAddr might be nil
	remoteAddr := "unknown"
	if conn != nil {
		if addr := conn.RemoteAddr(); addr != nil {
			remoteAddr = addr.String()
		}
	}

	switch state {
	case http.StateNew:
		s.logger.Debug("New client connection established",
			zap.String("remote_addr", remoteAddr),
			zap.String("state", "new"))
	// StateActive and StateIdle removed - too noisy with keep-alive connections and SSE streams
	// case http.StateActive:
	// 	s.logger.Debug("Client connection active",
	// 		zap.String("remote_addr", conn.RemoteAddr().String()),
	// 		zap.String("state", "active"))
	// case http.StateIdle:
	// 	s.logger.Debug("Client connection idle",
	// 		zap.String("remote_addr", conn.RemoteAddr().String()),
	// 		zap.String("state", "idle"))
	case http.StateHijacked:
		s.logger.Debug("Client connection hijacked (likely for upgrade)",
			zap.String("remote_addr", remoteAddr),
			zap.String("state", "hijacked"))
	case http.StateClosed:
		s.logger.Debug("Client connection closed",
			zap.String("remote_addr", remoteAddr),
			zap.String("state", "closed"))
	}
}

// OnUpstreamServerChange should be called when upstream servers are modified
func (s *Server) OnUpstreamServerChange() {
	s.runtime.HandleUpstreamServerChange(s.serverCtx)
}

// Configuration management methods

// GetConfig returns the current configuration
func (s *Server) GetConfig() (*config.Config, error) {
	return s.runtime.GetConfig()
}

// GetServerTools returns cached tool definitions, including input/output schemas.
func (s *Server) GetServerTools(serverName string) ([]map[string]interface{}, error) {
	s.logger.Debug("GetServerTools called (Phase 7.1: using StateView)", zap.String("server", serverName))

	// Phase 7.1: Use StateView for lock-free cached tool reads
	supervisor := s.runtime.Supervisor()
	if supervisor == nil {
		return nil, fmt.Errorf("supervisor not available")
	}

	snapshot := supervisor.StateView().Snapshot()
	serverStatus, exists := snapshot.Servers[serverName]
	if !exists {
		return nil, fmt.Errorf("server not found: %s", serverName)
	}

	if !serverStatus.Connected {
		return nil, fmt.Errorf("server %s is not connected", serverName)
	}

	// Convert cached tools to API response format
	result := make([]map[string]interface{}, len(serverStatus.Tools))
	for i, tool := range serverStatus.Tools {
		toolMap := map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": tool.InputSchema,
			"server_name": serverName,
		}
		if len(tool.OutputSchemaJSON) > 0 {
			toolMap["outputSchema"] = string(tool.OutputSchemaJSON)
		}

		// Include annotations if present
		if tool.Annotations != nil {
			annotations := map[string]interface{}{}
			if tool.Annotations.Title != "" {
				annotations["title"] = tool.Annotations.Title
			}
			if tool.Annotations.ReadOnlyHint != nil {
				annotations["readOnlyHint"] = *tool.Annotations.ReadOnlyHint
			}
			if tool.Annotations.DestructiveHint != nil {
				annotations["destructiveHint"] = *tool.Annotations.DestructiveHint
			}
			if tool.Annotations.IdempotentHint != nil {
				annotations["idempotentHint"] = *tool.Annotations.IdempotentHint
			}
			if tool.Annotations.OpenWorldHint != nil {
				annotations["openWorldHint"] = *tool.Annotations.OpenWorldHint
			}
			if len(annotations) > 0 {
				toolMap["annotations"] = annotations
			}
		}

		result[i] = toolMap
	}

	s.logger.Debug("Retrieved server tools from cache",
		zap.String("server", serverName),
		zap.Int("count", len(result)))
	return result, nil
}

// SearchTools searches for tools using the index
func (s *Server) SearchTools(query string, limit int) ([]map[string]interface{}, error) {
	s.logger.Debug("SearchTools called", zap.String("query", query), zap.Int("limit", limit))

	if s.runtime.IndexManager() == nil {
		return nil, fmt.Errorf("index manager not initialized")
	}

	// Search tools in the index
	results, err := s.runtime.IndexManager().SearchTools(query, limit)
	if err != nil {
		s.logger.Error("Failed to search tools", zap.String("query", query), zap.Error(err))
		return nil, err
	}

	resultMaps := s.searchResultsToMaps(results)
	s.logger.Debug("Search completed", zap.String("query", query), zap.Int("results", len(resultMaps)))
	return resultMaps, nil
}

func (s *Server) searchResultsToMaps(results []*config.SearchResult) []map[string]interface{} {
	withheld := s.unresolvedServerFilter()

	// Convert to map format for API
	var resultMaps []map[string]interface{}
	for _, result := range results {
		if result.Tool != nil {
			serverName := result.Tool.ServerName
			if serverName == "" {
				// Fallback: recover the server from a "server:tool" index name so
				// the quarantine gate still applies when ServerName is unset.
				if parts := strings.SplitN(result.Tool.Name, ":", 2); len(parts) == 2 {
					serverName = parts[0]
				}
			}
			if withheld(serverName) {
				continue
			}
			toolData := map[string]interface{}{
				"name":        result.Tool.Name,
				"description": result.Tool.Description,
				"server_name": result.Tool.ServerName,
			}
			// without this, contracts'
			// AnnotationTier(nil) always resolved to TierUnannotated for
			// every search hit, regardless of the tool's real annotations,
			// so the same tool's tier badge disagreed between the normal
			// Tools list and the search box.
			if result.Tool.Annotations != nil {
				toolData["annotations"] = result.Tool.Annotations
			}
			// Parse params JSON as input schema if available
			if result.Tool.ParamsJSON != "" {
				var inputSchema map[string]interface{}
				if err := json.Unmarshal([]byte(result.Tool.ParamsJSON), &inputSchema); err == nil {
					toolData["input_schema"] = inputSchema
				}
			}

			// Wrap in search result format with nested tool
			resultMap := map[string]interface{}{
				"tool":  toolData,
				"score": result.Score,
			}
			resultMaps = append(resultMaps, resultMap)
		}
	}
	return resultMaps
}

func (s *Server) unresolvedServerFilter() func(serverName string) bool {
	cache := make(map[string]bool)
	storageMgr := s.runtime.StorageManager()
	return func(serverName string) bool {
		if serverName == "" || storageMgr == nil {
			return true
		}
		if withheld, ok := cache[serverName]; ok {
			return withheld
		}
		withheld := true
		if cfg, err := storageMgr.GetUpstreamServer(serverName); err == nil && cfg != nil {
			withheld = false
		}
		cache[serverName] = withheld
		return withheld
	}
}

// GetSecretResolver returns the secret resolver instance
func (s *Server) GetSecretResolver() *secret.Resolver {
	return s.runtime.GetSecretResolver()
}

// NotifySecretsChanged notifies the runtime that secrets have changed
func (s *Server) NotifySecretsChanged(ctx context.Context, operation, secretName string) error {
	return s.runtime.NotifySecretsChanged(ctx, operation, secretName)
}

// GetCurrentConfig returns the current configuration
func (s *Server) GetCurrentConfig() *config.Config {
	if s.runtime == nil {
		return nil
	}
	return s.runtime.GetCurrentConfig()
}

// Changes returns the dashboard change stream.
func (s *Server) Changes() *runtime.ChangeHub { return s.runtime.Changes() }

// GetRecentSessions retrieves recent MCP sessions, optionally filtered by
// status ("active" / "closed"; empty means no filter).
func (s *Server) GetRecentSessions(f storage.SessionFilter) ([]*contracts.MCPSession, int, error) {
	return s.runtime.GetRecentSessions(f)
}

// Activity logging methods (RFC-003)

// ListActivities returns activity records matching the filter.
func (s *Server) ListActivities(filter storage.ActivityFilter) ([]*storage.ActivityRecord, int, error) {
	return s.runtime.ListActivities(filter)
}

// GetActivity returns a single activity record by ID.
func (s *Server) GetActivity(id string) (*storage.ActivityRecord, error) {
	return s.runtime.GetActivity(id)
}

// StreamActivities returns a channel that yields activity records matching the filter.
func (s *Server) StreamActivities(filter storage.ActivityFilter) <-chan *storage.ActivityRecord {
	return s.runtime.StreamActivities(filter)
}

// AggregateToolUsage rolls up tool_call activity per (server,tool) since the
// given time.
func (s *Server) AggregateToolUsage(since time.Time) (map[string]storage.ToolUsageStat, error) {
	return s.runtime.AggregateToolUsage(since)
}

// UsageSnapshot returns the actor-owned usage aggregate snapshot.
func (s *Server) UsageSnapshot() *runtime.UsageAggregate {
	return s.runtime.UsageSnapshot()
}

// ListToolApprovals returns tool approval records for a server.
func (s *Server) ListToolApprovals(serverName string) ([]*storage.ToolApprovalRecord, error) {
	return s.runtime.ListToolApprovals(serverName)
}

// The *At variants bind an approval to the incarnation the caller reviewed
// (the REST expected_incarnation; 0 binds to the incarnation
// current when the call starts). A mismatch returns runtime.ErrServerReplaced
// and writes nothing.

// BlockToolsAt is BlockTools bound to expectedIncarnation.
func (s *Server) BlockToolsAt(serverName string, toolNames []string, blockedBy string, expectedIncarnation int64) (int, error) {
	return s.runtime.BlockToolsAt(serverName, toolNames, blockedBy, expectedIncarnation)
}

// BlockAllToolsAt is BlockAllTools bound to expectedIncarnation.
func (s *Server) BlockAllToolsAt(serverName string, blockedBy string, expectedIncarnation int64) (int, error) {
	return s.runtime.BlockAllToolsAt(serverName, blockedBy, expectedIncarnation)
}

// SetToolEnabledAt is SetToolEnabled bound to expectedIncarnation.
func (s *Server) SetToolEnabledAt(serverName, toolName string, enabled bool, updatedBy string, expectedIncarnation int64) error {
	return s.runtime.SetToolEnabledAt(serverName, toolName, enabled, updatedBy, expectedIncarnation)
}

// ApproveToolsReviewed approves the named tools bound to the reviewed
// fingerprints; all requested definitions must match.
// ApproveAllToolsReviewed approves every pending/changed tool bound to the
// reviewed fingerprints; all requested definitions must match.
// ApproveToolsReviewedAt is ApproveToolsReviewed also bound to the reviewed
// server incarnation.
func (s *Server) ApproveToolsReviewedAt(serverName string, toolNames []string, expected map[string]string, approvedBy string, expectedIncarnation int64) (int, error) {
	return s.runtime.ApproveToolsReviewedAt(serverName, toolNames, expected, approvedBy, expectedIncarnation)
}

// ApproveAllToolsReviewedAt is ApproveAllToolsReviewed also bound to the
// reviewed server incarnation.
func (s *Server) ApproveAllToolsReviewedAt(serverName string, expected map[string]string, approvedBy string, expectedIncarnation int64) (int, error) {
	return s.runtime.ApproveAllToolsReviewedAt(serverName, expected, approvedBy, expectedIncarnation)
}

// GetToolApproval returns the approval record for a specific tool.
func (s *Server) GetToolApproval(serverName, toolName string) (*storage.ToolApprovalRecord, error) {
	return s.runtime.GetToolApproval(serverName, toolName)
}

// mcpLoggingHandler wraps an MCP route with the request/response debug lines
// that every /mcp mount shares. Extracted from startCustomHTTPServer so the
// log fields it writes are reachable from a test without binding a listener.
func (s *Server) mcpLoggingHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Extract connection source from context
		source := "tcp"

		safePath := oauth.LogSafeRequestPath(r.URL.Path)

		// Log incoming request with connection details
		s.logger.Debug("MCP client request received",
			zap.String("method", r.Method),
			zap.String("path", safePath),
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("source", string(source)),
			zap.String("user_agent", r.UserAgent()),
			zap.String("content_type", r.Header.Get("Content-Type")),
			zap.String("connection", r.Header.Get("Connection")),
			zap.Int64("content_length", r.ContentLength),
		)

		// Create response writer wrapper to capture status and errors
		wrappedWriter := &responseWriter{ResponseWriter: w, statusCode: 200}

		// Handle the request
		handler.ServeHTTP(wrappedWriter, r)

		duration := time.Since(start)

		// Log response with timing and status
		if wrappedWriter.statusCode >= 400 {
			s.logger.Warn("MCP client request completed with error",
				zap.String("method", r.Method),
				zap.String("path", safePath),
				zap.String("remote_addr", r.RemoteAddr),
				zap.String("source", string(source)),
				zap.Int("status_code", wrappedWriter.statusCode),
				zap.Duration("duration", duration),
			)
		} else {
			s.logger.Debug("MCP client request completed successfully",
				zap.String("method", r.Method),
				zap.String("path", safePath),
				zap.String("remote_addr", r.RemoteAddr),
				zap.String("source", string(source)),
				zap.Int("status_code", wrappedWriter.statusCode),
				zap.Duration("duration", duration),
			)
		}
	})
}

// ServerIncarnation returns serverName's committed incarnation
// ); the REST tool listings stamp it on their rows.
func (s *Server) ServerIncarnation(serverName string) (int64, bool) {
	if s.runtime == nil {
		return 0, false
	}
	return s.runtime.ServerIncarnation(serverName)
}

func (s *Server) GetPeerHub() *peering.Hub { return s.runtime.Peers() }

func (s *Server) GetServerOAuthSettings(ctx context.Context, name string) (*oauth.Settings, error) {
	return s.runtime.GetServerOAuthSettings(ctx, name)
}
