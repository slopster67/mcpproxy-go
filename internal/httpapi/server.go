package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/slopster67/mcpproxy-go/internal/dashboardauth"
	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/httpx"
	"github.com/slopster67/mcpproxy-go/internal/logs"
	"github.com/slopster67/mcpproxy-go/internal/management"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/preflight"
	"github.com/slopster67/mcpproxy-go/internal/reqcontext"
	internalRuntime "github.com/slopster67/mcpproxy-go/internal/runtime"
	"github.com/slopster67/mcpproxy-go/internal/secret"
	"github.com/slopster67/mcpproxy-go/internal/servericon"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

const (
	secretTypeStored = "secret"

	// defaultAPIRequestTimeout bounds an ordinary /api/v1 request.
	defaultAPIRequestTimeout = 60 * time.Second
)

// ServerController defines the interface for core server functionality
type ServerController interface {
	incarnationApprovalController
	reviewedIncarnationController
	serverIncarnationReader
	GetToolClassification(server, tool string) (string, error)
	SetToolClassificationAt(server, tool, tier, hash string, incarnation int64) error

	IsReady() bool

	// Changes is the dashboard change stream behind GET /api/v1/events.
	Changes() *internalRuntime.ChangeHub

	// Server management. ListServers and GetServer return the stored config
	// as written plus runtime state (runtime.Runtime.Servers).
	ListServers() ([]contracts.Server, error)
	GetServer(name string) (*contracts.Server, error)
	AddServer(ctx context.Context, serverConfig *config.ServerConfig) error // Add server
	RemoveServer(ctx context.Context, serverName string) error              // Remove server
	PatchServer(ctx context.Context, serverName string, resolve func(cur *config.ServerConfig) (*config.ServerConfig, error)) error
	EnableServer(serverName string, enabled bool) error

	RestartServer(serverName string) error

	// GetManagementService returns the unified lifecycle/diagnostics service.
	// Typed, not interface{}: handlers must get a compile-time contract rather
	// than re-deriving a method set with ad-hoc assertions that fail at
	// runtime (ARC-04). May be nil before the service is installed.
	GetManagementService() management.Service

	// Tools and search
	GetServerTools(serverName string) ([]map[string]interface{}, error)
	SearchTools(query string, limit int) ([]map[string]interface{}, error)

	// Logs
	GetServerLogs(serverName string, tail int) ([]contracts.LogEntry, error)

	// Secrets management
	GetSecretResolver() *secret.Resolver
	// GetCurrentConfig returns the live config snapshot, or nil when none is
	// installed. Typed so dashboard session middleware cannot be handed a value it
	// fails to recognise and then wave through unauthenticated (SEC-02).
	GetCurrentConfig() *config.Config
	GetServerIcon(name string) string
	GetServerOAuthSettings(ctx context.Context, name string) (*oauth.Settings, error)
	GetPeerHub() *peering.Hub
	NotifySecretsChanged(ctx context.Context, operation, secretName string) error

	// Session management. status filters on session status ("active" /
	// "closed"); an empty string means no filter.
	GetRecentSessions(f storage.SessionFilter) ([]*contracts.MCPSession, int, error)

	GetConfig() (*config.Config, error)

	// Activity logging (RFC-003)
	ListActivities(filter storage.ActivityFilter) ([]*storage.ActivityRecord, int, error)
	GetActivity(id string) (*storage.ActivityRecord, error)
	StreamActivities(filter storage.ActivityFilter) <-chan *storage.ActivityRecord
	// AggregateToolUsage rolls up tool_call activity per (server,tool) since the
	// given time. Backs the global tools page usage columns.
	AggregateToolUsage(since time.Time) (map[string]storage.ToolUsageStat, error)
	// UsageSnapshot returns the actor-owned in-memory usage aggregate snapshot.
	// The /api/v1/activity/usage endpoint reads it without a
	// full-log scan. May be nil before the activity service is ready.
	UsageSnapshot() *internalRuntime.UsageAggregate

	// Tool-level quarantine
	ListToolApprovals(serverName string) ([]*storage.ToolApprovalRecord, error)

	GetToolApproval(serverName, toolName string) (*storage.ToolApprovalRecord, error)
}

// Server provides HTTP API endpoints with chi router
type Server struct {
	dashboardAuth *dashboardauth.Manager
	controller    ServerController
	logger        *zap.SugaredLogger
	httpLogger    *zap.Logger // Separate logger for HTTP requests
	router        *chi.Mux

	clientsService *internalRuntime.ClientsService // Client credentials and bindings

	profilesService ProfilesAPI // Profiles service

	// usageCache is the short-TTL read cache for GET /api/v1/activity/usage.
	// Keyed by the request's query params; entries expire
	// after the configured usage_cache_ttl so wide-window reads are cheap and
	// staleness is bounded.
	usageCacheMu sync.Mutex
	usageCache   map[string]usageCacheEntry
}

// usageCacheEntry is one cached usage response with the time it was stored.
//
// It records when the entry was WRITTEN rather than when it expires, because
// usage_cache_ttl is hot-reloadable and the TTL is the documented bound on how
// far the Usage figures may lag the Activity Log. An absolute
// expiry banked from the old TTL would outlive a lowered one — the operator
// would tighten the bound and the served answer would ignore it.
type usageCacheEntry struct {
	resp   *contracts.UsageAggregateResponse
	stored time.Time
}

// usageCacheMaxEntries bounds the usage cache; on overflow it is cleared
// wholesale (entries are short-lived and the working set is tiny in practice).
const usageCacheMaxEntries = 64

// getUsageCache returns a cached response for key that is still within ttl, or
// nil. Freshness is judged against the ttl passed in — the one in force NOW —
// so a hot-reloaded usage_cache_ttl takes effect on the entries already held.
func (s *Server) getUsageCache(key string, ttl time.Duration) *contracts.UsageAggregateResponse {
	if ttl <= 0 {
		return nil
	}
	s.usageCacheMu.Lock()
	defer s.usageCacheMu.Unlock()
	entry, ok := s.usageCache[key]
	if !ok || time.Since(entry.stored) >= ttl {
		return nil
	}
	return entry.resp
}

// putUsageCache stores resp under key. ttl only gates whether caching happens
// at all; how long the entry stays fresh is decided at read time.
func (s *Server) putUsageCache(key string, resp *contracts.UsageAggregateResponse, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	s.usageCacheMu.Lock()
	defer s.usageCacheMu.Unlock()
	if s.usageCache == nil || len(s.usageCache) >= usageCacheMaxEntries {
		s.usageCache = make(map[string]usageCacheEntry)
	}
	s.usageCache[key] = usageCacheEntry{resp: resp, stored: time.Now()}
}

// NewServer creates a new HTTP API server
func NewServer(controller ServerController, logger *zap.SugaredLogger, logConfig *config.LogConfig, dashboardAuth *dashboardauth.Manager) *Server {
	// Create HTTP logger for API request logging
	httpLogger, err := logs.CreateHTTPLogger(logConfig)
	if err != nil {
		logger.Warnf("Failed to create HTTP logger: %v", err)
		httpLogger = zap.NewNop() // Use no-op logger as fallback
	}

	s := &Server{
		controller: controller,
		logger:     logger,
		httpLogger: httpLogger,
		router:     chi.NewRouter(),
	}

	s.dashboardAuth = dashboardAuth
	s.setupRoutes()
	return s
}

// SetClientsService configures the clients service behind the client
// binding route and the connect credential path.
func (s *Server) SetClientsService(svc *internalRuntime.ClientsService) {
	s.clientsService = svc
}

// ExtractToken extracts the authentication token from the request.
// It reads the Authorization: Bearer header.
// Returns an empty string if no token is found.
func ExtractToken(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if strings.HasPrefix(value, "Bearer ") {
		return strings.TrimPrefix(value, "Bearer ")
	}
	return ""
}

func (s *Server) correlationIDMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Generate or retrieve correlation ID
			correlationID := r.Header.Get("X-Correlation-ID")
			if correlationID == "" {
				correlationID = reqcontext.GenerateCorrelationID()
			}

			// Inject correlation ID and request source into context
			ctx := reqcontext.WithCorrelationID(r.Context(), correlationID)
			ctx = reqcontext.WithRequestSource(ctx, reqcontext.SourceRESTAPI)

			// Add correlation ID to response headers for client tracking
			w.Header().Set("X-Correlation-ID", correlationID)

			// Continue with enriched context
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ServeHTTP implements http.Handler
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// corsMiddleware echoes the request Origin when that origin is loopback or
// the same host and scheme the client used to reach this request. It emits
// nothing otherwise, including when there is no Origin header, which is every
// non-browser client. The embedded Web UI is same-origin under /ui/.
//
// The OPTIONS short-circuit stays ahead of dashboard session middleware: browsers
// never send credentials on a preflight, so gating it behind the API key would
// break legitimate cross-origin use.
func (s *Server) corsMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Add, never Set: another layer may already have written a Vary
			// and clobbering it would poison shared caches.
			w.Header().Add("Vary", "Origin")

			origin := r.Header.Get("Origin")
			if origin != "" && httpx.OriginMatchesRequest(origin, r) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// setupRoutes configures all API routes
func (s *Server) setupRoutes() {
	s.logger.Debug("Setting up HTTP API routes")

	// Observability middleware (if available)

	// Core middleware
	// Request ID middleware MUST be first to ensure all responses have X-Request-Id header
	s.router.Use(RequestIDMiddleware)
	s.router.Use(RequestIDLoggerMiddleware(s.logger)) // Add request_id to logger context
	s.router.Use(s.httpLoggingMiddleware())           // Custom HTTP API logging
	s.router.Use(s.recoverRequests)
	s.router.Use(s.correlationIDMiddleware()) // Correlation ID and request source tracking
	s.logger.Debug("Core middleware configured (request ID, logging, recovery, correlation ID)")

	// CORS headers for browser access (SEC-04). Registered with Use so it
	// covers every route on this router, /events included.
	s.router.Use(s.corsMiddleware())

	livenessHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
	readinessHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.controller.IsReady() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ready":true}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ready":false}`))
	}

	// Controller-backed health and readiness endpoints.
	s.router.Get("/healthz", livenessHandler)
	s.router.Get("/readyz", readinessHandler)

	s.router.Get("/auth/login", s.dashboardAuth.Login)
	s.router.Get("/auth/callback", s.dashboardAuth.Callback)
	s.router.Get("/auth/session", s.dashboardAuth.Session)
	s.router.Post("/auth/logout", s.dashboardAuth.Logout)

	// API v1 routes with timeout and authentication middleware
	s.router.Get("/server-icons/{asset}", s.handleServerIcon)
	s.router.Handle("/peer/control", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.controller.GetPeerHub().ServeHTTP(w, r) }))

	s.router.Route("/api/v1", func(r chi.Router) {
		r.Use(s.dashboardAuth.Middleware)

		// The change stream is long-lived: no request deadline (the outer mux
		// also clears the write deadline for this path).
		r.Get("/events", s.handleEvents)

		r.Group(func(r chi.Router) {
			r.Use(requestTimeout(defaultAPIRequestTimeout))

			s.registerClientRoutes(r)
			s.registerPeerRoutes(r)

			r.Get("/profiles", s.handleListProfiles)
			r.Post("/profiles", s.handleCreateProfile)
			r.Get("/profiles/{id}", s.handleGetProfile)
			r.Patch("/profiles/{id}", s.handlePatchProfile)
			r.Delete("/profiles/{id}", s.handleDeleteProfile)
			r.Get("/profiles/{id}/effective-tools", s.handleProfileEffectiveTools)
			r.Get("/access/explain", s.handleAccessExplain)

			// Servers: one object per server (stored config + runtime state).
			r.Get("/servers", s.handleGetServers)
			r.Post("/servers", s.handleAddServer)
			r.Route("/servers/{id}", func(r chi.Router) {
				// chi routes on RawPath, so the {id} param arrives percent-encoded.
				// Official modelcontextprotocol/registry v0.1 ids are namespace/name,
				// so the slash reaches handlers as %2F. Decode it once here so every
				// /servers/{id}/* sub-resource handler does its exact-match server
				// lookup against the real name.
				r.Use(decodeServerIDParam)
				r.Get("/", s.handleGetServer)
				r.Patch("/", s.handlePatchServer)
				r.Delete("/", s.handleRemoveServer)
				// Move a header / env value into the OS secret store.
				r.Post("/config-to-secret", s.handleConvertConfigToSecret)
				r.Post("/restart", s.handleRestartServer)
				r.Post("/login", s.handleServerLogin)
				r.Post("/logout", s.handleServerLogout)
				r.Get("/logs", s.handleGetServerLogs)
				r.Get("/oauth/discovered", s.handleGetServerOAuthDiscovered)

				// Hash-checked review actions. Atomic block = approve+disable
				//, server-side so the pair is all-or-nothing.
				r.Post("/tools/approve", s.handleApproveTools)
				r.Post("/tools/block", s.handleBlockTools)
			})

			// Tools: id is "server:tool" (URL-encoded).
			r.Get("/tools", s.handleGetGlobalTools)
			r.Get("/tools/{id}", s.handleGetTool)
			r.Patch("/tools/{id}", s.handlePatchTool)
			r.Get("/index/search", s.handleSearchTools)

			r.Get("/secrets", s.handleListSecrets)
			r.Post("/secrets", s.handleSetSecret)
			r.Patch("/secrets/{name}", s.handlePatchSecret)
			r.Delete("/secrets/{name}", s.handleDeleteSecret)

			r.Get("/sessions", s.handleGetSessions)
			r.Get("/sessions/{id}", s.handleGetSession)

			r.Get("/activity", s.handleListActivity)
			r.Get("/activity/{id}", s.handleGetActivityDetail)
			r.Get("/usage", s.handleActivityUsage)
		})
	})

	// Note: Swagger UI is mounted directly on the main mux (not via HTTP API server)
	// See internal/server/server.go for swagger handler registration

	s.logger.Debugw("HTTP API routes setup completed",
		"api_routes", "/api/v1/*",
		"health_routes", "/healthz,/readyz")
}

// httpLoggingMiddleware creates custom HTTP request logging middleware
func (s *Server) httpLoggingMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Create a response writer wrapper to capture status code
			ww := &responseWriter{ResponseWriter: w, statusCode: 200}

			// Process request
			next.ServeHTTP(ww, r)

			duration := time.Since(start)

			s.httpLogger.Info("HTTP API Request",
				zap.String("method", r.Method),
				zap.String("path", oauth.LogSafeRequestPath(r.URL.Path)),
				zap.String("query", oauth.LogSafeQueryString(r.URL.RawQuery)),
				zap.String("remote_addr", r.RemoteAddr),
				zap.String("user_agent", r.UserAgent()),
				zap.Int("status", ww.statusCode),
				zap.Duration("duration", duration),
				zap.String("referer", oauth.LogSafeRequestURL(r.Referer())),
				zap.Int64("content_length", r.ContentLength),
			)
		})
	}
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher interface by delegating to the underlying ResponseWriter
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

//nolint:unused // swagger documentation stub

//nolint:unused // swagger documentation stub

// JSON response helpers

func (s *Server) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Errorw("Failed to encode JSON response", "error", err)
	}
}

// writeError writes an error response including request_id from the request context
// Updated signature to include request for request_id extraction
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, message string) {
	requestID := reqcontext.GetRequestID(r.Context())
	s.writeJSON(w, status, contracts.NewErrorResponseWithRequestID(message, requestID))
}

// getRequestLogger returns a logger with request_id attached, or falls back to the server logger
// Helper for request-scoped logging
func (s *Server) getRequestLogger(r *http.Request) *zap.SugaredLogger {
	if r == nil {
		return s.logger
	}
	if logger := GetLogger(r.Context()); logger != nil {
		return logger
	}
	return s.logger
}

func (s *Server) writeSuccess(w http.ResponseWriter, data interface{}) {
	s.writeJSON(w, http.StatusOK, contracts.NewSuccessResponse(data))
}

// API v1 handlers

// buildVersion is set during build using -ldflags
var buildVersion = "development"

// GetBuildVersion returns the build version from build-time variables.
// This should be set during build using -ldflags.
func GetBuildVersion() string {
	return buildVersion
}

func (s *Server) handleGetServers(w http.ResponseWriter, r *http.Request) {
	// GET /servers honours profile (rows limited to the
	// profile's effective servers, tool_count = the tools visible under it);
	// client and token are not server filters; search stays client-side.
	if !rejectUnsupportedScopeFilters(w, r, "profile") {
		return
	}
	viewAs, ok := s.parseViewAs(w, r)
	if !ok {
		return
	}
	var viewAsEval ViewAsEvaluator
	if viewAs != nil {
		if viewAsEval, ok = s.resolveViewAs(w, r, viewAs); !ok {
			return
		}
	}
	servers, err := s.controller.ListServers()
	if err != nil {
		s.logger.Errorw("Failed to list servers", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "Failed to get servers")
		return
	}
	if viewAsEval != nil {
		servers = s.limitServersToProfile(r.Context(), servers, viewAs, viewAsEval)
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeSuccess(w, contracts.GetServersResponse{Servers: servers, Stats: internalRuntime.ServerStats(servers)})
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	server, err := s.controller.GetServer(chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, contracts.ErrServerNotFound) {
			s.writeError(w, r, http.StatusNotFound, "Server not found")
			return
		}
		s.logger.Errorw("Failed to get server", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "Failed to get server")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeSuccess(w, server)
}

// flattenNullableMap turns a request-side `map[string]*string` (which uses
// nil values to signal "delete" under JSON Merge Patch semantics) into the
// `map[string]string` shape config.ServerConfig stores. Nil entries are
// dropped — they have no meaning on POST (add) and are handled separately
// by the deep-merge loop on PATCH.
func flattenNullableMap(m map[string]*string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, vp := range m {
		if vp != nil {
			out[k] = *vp
		}
	}
	return out
}

type AddServerRequest struct {
	OAuth            config.OAuthPatch  `json:"oauth,omitempty"`
	HostID           *string            `json:"host_id,omitempty"`
	AvailableToPeers *bool              `json:"available_to_peers,omitempty"`
	Icon             *string            `json:"icon,omitempty"`
	Description      *string            `json:"description,omitempty"`
	Name             string             `json:"name"`
	URL              string             `json:"url,omitempty"`
	Command          string             `json:"command,omitempty"`
	Args             []string           `json:"args,omitempty"`
	Env              map[string]*string `json:"env,omitempty"`
	Headers          map[string]*string `json:"headers,omitempty"`
	WorkingDir       string             `json:"working_dir,omitempty"`
	Protocol         string             `json:"protocol,omitempty"`
	Enabled          *bool              `json:"enabled,omitempty"`

	TrustMode string `json:"trust_mode,omitempty"`
}

func invalidTrustModeMessage(mode string) string {
	return fmt.Sprintf("invalid trust_mode %q: must be one of: %s (values are case-sensitive; omit the field to leave it unchanged)",
		mode, strings.Join(config.ValidTrustModes(), ", "))
}

func (s *Server) handleAddServer(w http.ResponseWriter, r *http.Request) {
	var req AddServerRequest
	if err := decodeManagementBody(r, &req); err != nil {
		s.badProfileBody(w, r, err)
		return
	}
	data, err := s.createServer(r.Context(), req, false)
	if err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	s.writeSuccess(w, data)
}

func (s *Server) handleRemoveServer(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "id")
	if serverID == "" {
		s.writeError(w, r, http.StatusBadRequest, "Server ID required")
		return
	}

	logger := s.getRequestLogger(r) // Use request-scoped logger

	// Remove server via controller
	if err := s.controller.RemoveServer(r.Context(), serverID); err != nil {
		// Check if it's a not found error
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, r, http.StatusNotFound, err.Error())
			return
		}
		logger.Errorw("Failed to remove server", "server", serverID, "error", err)
		s.writeError(w, r, http.StatusInternalServerError, fmt.Sprintf("Failed to remove server: %v", err))
		return
	}

	logger.Infow("Server removed successfully", "server", serverID)
	s.writeSuccess(w, map[string]string{"deleted": serverID})
}

// writeServerObject answers an action with the server object after it.
func (s *Server) writeServerObject(w http.ResponseWriter, r *http.Request, name string) {
	server, err := s.controller.GetServer(name)
	if err != nil {
		if errors.Is(err, contracts.ErrServerNotFound) {
			s.writeError(w, r, http.StatusNotFound, "Server not found")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, "Failed to get server")
		return
	}
	s.writeSuccess(w, server)
}

// patchError is a PATCH /api/v1/servers/{id} refusal produced while the
// update is resolved against the live entry (inside the config commit).
type patchError struct {
	status int
	msg    string
}

func (e *patchError) Error() string { return e.msg }

// resolveServerPatch turns a PATCH body into the partial update to apply to
// existingSrv (the live entry; nil when unknown). Omitted fields, map merges,
// and validation are resolved against it, so
// running it inside the commit means a PATCH can never revert a newer write
// with values it read earlier.
//
// masked is true for a caller that reads servers masked (the MCP management
// tools): echoed masks are then reverted to the stored values where they can
// be bound to a key, and refused otherwise. The dashboard reads raw values and
// writes them back verbatim.
func resolveServerPatch(serverName string, req *AddServerRequest, existingSrv *config.ServerConfig, masked bool) (*config.ServerConfig, error) {
	// Build partial update config - only set fields that were provided
	updates := config.CopyServerConfig(existingSrv)
	if updates == nil {
		updates = &config.ServerConfig{Name: serverName}
	}
	updates.HostID = req.HostID
	updates.AvailableToPeers = req.AvailableToPeers
	hasUpdates := req.HostID != nil || req.AvailableToPeers != nil
	if req.Icon != nil {
		normalized, err := servericon.Normalize(*req.Icon)
		if err != nil {
			return nil, &patchError{status: http.StatusBadRequest, msg: err.Error()}
		}
		updates.Icon = &normalized
		hasUpdates = true
	}

	if req.Description != nil {
		normalized, err := config.NormalizeServerDescription(*req.Description)
		if err != nil {
			return nil, &patchError{status: http.StatusBadRequest, msg: err.Error()}
		}
		updates.Description = &normalized
		hasUpdates = true
	}

	if req.URL != "" {
		updates.URL = req.URL
		if masked {
			var storedURL string
			if existingSrv != nil {
				storedURL = existingSrv.URL
			}
			unmaskedURL, err := oauth.UnmaskLiveURL(req.URL, storedURL)
			if err != nil {
				return nil, &patchError{status: http.StatusBadRequest, msg: err.Error()}
			}
			updates.URL = unmaskedURL
		}
		hasUpdates = true
	}
	if req.Command != "" {
		updates.Command = req.Command
		hasUpdates = true
	}
	if req.Args != nil {
		// `args` REPLACES the vector, and the masked read path
		// masks credential-shaped argv tokens. An argv slot has no key to bind
		// a stored secret to, so an echoed mask is refused, not reverted.
		if masked {
			var storedArgs []string
			if existingSrv != nil {
				storedArgs = existingSrv.Args
			}
			if err := oauth.CheckArgvMaskEcho("args", req.Args, storedArgs); err != nil {
				return nil, &patchError{status: http.StatusBadRequest, msg: err.Error()}
			}
		}
		updates.Args = req.Args
		hasUpdates = true
	}
	if req.Env != nil {
		merged := map[string]string{}
		if existingSrv != nil {
			for k, v := range existingSrv.Env {
				merged[k] = v
			}
		}
		for k, vp := range req.Env {
			if vp == nil {
				delete(merged, k)
			} else {
				merged[k] = *vp
			}
		}
		if masked && existingSrv != nil {
			merged = oauth.UnmaskEnvValues(oauth.UnmaskLiveEnvValues(merged, existingSrv.Env), existingSrv.Env)
		}
		updates.Env = merged
		hasUpdates = true
	}
	if req.Headers != nil {
		merged := map[string]string{}
		if existingSrv != nil {
			for k, v := range existingSrv.Headers {
				merged[k] = v
			}
		}
		for k, vp := range req.Headers {
			if vp == nil {
				delete(merged, k)
			} else {
				merged[k] = *vp
			}
		}
		// revert any masked header value echoed back
		// (same two-step, same key binding as env).
		if masked && existingSrv != nil {
			merged = oauth.UnmaskHeaders(oauth.UnmaskLiveHeaders(merged, existingSrv.Headers), existingSrv.Headers)
		}
		updates.Headers = merged
		hasUpdates = true
	}
	if req.WorkingDir != "" {
		updates.WorkingDir = req.WorkingDir
		hasUpdates = true
	}
	if req.Protocol != "" {
		updates.Protocol = req.Protocol
		hasUpdates = true
	}
	if req.Enabled != nil {
		updates.Enabled = *req.Enabled
		hasUpdates = true
	} else if existingSrv != nil {
		updates.Enabled = existingSrv.Enabled
	}
	// trust_mode is a plain string — empty means "leave unchanged", so
	// preserve the existing value when the request omits it (a bare PATCH of an
	// unrelated field must not reset the trust tier).
	if req.TrustMode != "" {
		updates.TrustMode = req.TrustMode
		hasUpdates = true
	} else if existingSrv != nil {
		updates.TrustMode = existingSrv.TrustMode
	}

	if req.OAuth != nil {
		var previous *config.OAuthConfig
		if existingSrv != nil {
			previous = existingSrv.OAuth
		}
		merged, err := config.PatchOAuth(previous, req.OAuth)
		if err != nil {
			return nil, &patchError{status: 400, msg: err.Error()}
		}
		updates.OAuth = merged
		hasUpdates = true
	} else if existingSrv != nil {
		updates.OAuth = existingSrv.OAuth
	}

	if !hasUpdates {
		return nil, &patchError{status: http.StatusBadRequest, msg: "No fields to update"}
	}

	// the fail-closed net for masked callers. The key-bound
	// reverts above restored every mask this proxy can bind back to the value
	// it was read from; anything still carrying one is refused rather than
	// persisted over a live credential.
	if masked {
		if err := oauth.CheckServerWriteMasks("server.", updates); err != nil {
			return nil, &patchError{status: http.StatusBadRequest, msg: err.Error()}
		}
	}

	return updates, nil
}

func (s *Server) handlePatchServer(w http.ResponseWriter, r *http.Request) {
	var req AddServerRequest
	if err := decodeManagementBody(r, &req); err != nil {
		s.badProfileBody(w, r, err)
		return
	}
	data, err := s.patchServer(r.Context(), chi.URLParam(r, "id"), req, false)
	if err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	s.writeSuccess(w, data)
}

func (s *Server) handleConvertConfigToSecret(w http.ResponseWriter, r *http.Request) {
	var req ConvertSecretRequest
	if err := decodeManagementBody(r, &req); err != nil {
		s.writeError(w, r, 400, err.Error())
		return
	}
	data, err := s.convertConfigToSecret(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	s.writeSuccess(w, data)
}

// handleRestartServer restarts a server and answers with its object. A
// restart that ends in "OAuth required" succeeded: the object says so.
func (s *Server) handleRestartServer(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "id")
	if serverID == "" {
		s.writeError(w, r, http.StatusBadRequest, "Server ID required")
		return
	}
	var err error
	if mgmtSvc := s.controller.GetManagementService(); mgmtSvc != nil {
		err = mgmtSvc.RestartServer(r.Context(), serverID)
	} else {
		err = s.controller.RestartServer(serverID)
	}
	if err != nil && !isOAuthRequiredError(err) {
		s.logger.Errorw("Failed to restart server", "server", serverID, "error", err)
		s.writeError(w, r, http.StatusInternalServerError, fmt.Sprintf("Failed to restart server: %v", err))
		return
	}
	s.writeServerObject(w, r, serverID)
}

func isOAuthRequiredError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "OAuth authorization") || strings.Contains(msg, "oauth") ||
		strings.Contains(msg, "authorization required") || strings.Contains(msg, "no valid token")
}

func (s *Server) handleServerLogin(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "id")
	if serverID == "" {
		s.writeError(w, r, http.StatusBadRequest, "Server ID required")
		return
	}

	// TriggerOAuthLoginQuick returns the actual browser status
	mgmtSvc := s.controller.GetManagementService()
	if mgmtSvc == nil {
		s.logger.Error("Management service not available")
		s.writeError(w, r, http.StatusInternalServerError, "Management service not available")
		return
	}

	result, err := mgmtSvc.TriggerOAuthLoginQuick(r.Context(), serverID)
	if err != nil {
		s.logger.Errorw("Failed to trigger OAuth login", "server", serverID, "error", err)

		// Structured OAuth errors are returned directly
		var oauthFlowErr *contracts.OAuthFlowError
		if errors.As(err, &oauthFlowErr) {
			// Add request ID from context for correlation
			oauthFlowErr.RequestID = reqcontext.GetRequestID(r.Context())
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			if encErr := json.NewEncoder(w).Encode(oauthFlowErr); encErr != nil {
				s.logger.Errorw("Failed to encode OAuth flow error response", "error", encErr)
			}
			return
		}

		// Map errors to HTTP status codes
		if strings.Contains(err.Error(), "management disabled") {
			s.writeError(w, r, http.StatusForbidden, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, r, http.StatusNotFound, fmt.Sprintf("Server not found: %s", serverID))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, fmt.Sprintf("Failed to trigger login: %v", err))
		return
	}

	// Phase 3: Return OAuthStartResponse with actual browser status and auth_url
	correlationID := reqcontext.GetCorrelationID(r.Context())
	if correlationID == "" {
		correlationID = reqcontext.GetRequestID(r.Context())
	}

	// Use actual result from StartManualOAuthQuick
	browserOpened := result != nil && result.BrowserOpened
	authURL := ""
	browserError := ""
	if result != nil {
		authURL = result.AuthURL
		browserError = result.BrowserError
	}

	// Determine appropriate message based on browser status
	message := fmt.Sprintf("OAuth authentication started for server '%s'. Please complete authentication in browser.", serverID)
	if !browserOpened && authURL != "" {
		message = fmt.Sprintf("Could not open browser automatically. Please open this URL manually: %s", authURL)
	}

	response := contracts.OAuthStartResponse{
		Success:       true,
		ServerName:    serverID,
		CorrelationID: correlationID,
		BrowserOpened: browserOpened,
		AuthURL:       authURL,
		BrowserError:  browserError,
		Message:       message,
	}

	s.writeSuccess(w, response)
}

func (s *Server) handleServerLogout(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "id")
	if serverID == "" {
		s.writeError(w, r, http.StatusBadRequest, "Server ID required")
		return
	}

	// Call management service TriggerOAuthLogout
	mgmtSvc := s.controller.GetManagementService()
	if mgmtSvc == nil {
		s.logger.Error("Management service not available")
		s.writeError(w, r, http.StatusInternalServerError, "Management service not available")
		return
	}

	if err := mgmtSvc.TriggerOAuthLogout(r.Context(), serverID); err != nil {
		s.logger.Errorw("Failed to trigger OAuth logout", "server", serverID, "error", err)

		// Map errors to HTTP status codes
		if strings.Contains(err.Error(), "management disabled") {
			s.writeError(w, r, http.StatusForbidden, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, r, http.StatusNotFound, fmt.Sprintf("Server not found: %s", serverID))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, fmt.Sprintf("Failed to trigger logout: %v", err))
		return
	}

	s.writeServerObject(w, r, serverID)
}

// enrichServerTools converts generic upstream tools to typed tools and enriches
// each with approval status, per-tool disabled state.
// Shared by the per-server tools endpoint and the global tools endpoint.
// ServerName is forced to serverID so the global merge can attribute
// every tool to its server even if the upstream payload omits server_name. The
// approval record's current hash is published as a preflight pin.
func (s *Server) enrichServerTools(serverID string, tools []map[string]interface{}) []contracts.Tool {
	typedTools := contracts.ConvertGenericToolsToTyped(tools)

	enrichedCount := 0
	var firstErr error
	for i := range typedTools {
		typedTools[i].ServerName = serverID
		typedTools[i].ID = serverID + ":" + typedTools[i].Name
		record, err := s.controller.GetToolApproval(serverID, typedTools[i].Name)
		if err == nil && record != nil {
			typedTools[i].ApprovalStatus = record.Status
			typedTools[i].Disabled = record.Disabled
			if tier, err := s.controller.GetToolClassification(serverID, typedTools[i].Name); err == nil && tier != "" && typedTools[i].Tier == contracts.TierUnannotated {
				typedTools[i].Classification = tier
				typedTools[i].Tier = contracts.Tier(tier)
			}
			typedTools[i].ClassificationHash = record.CurrentHash

			if typedTools[i].OutputSchema == nil && record.CurrentOutputSchema != "" {
				_ = json.Unmarshal([]byte(record.CurrentOutputSchema), &typedTools[i].OutputSchema)
			}
			// Scan-gate hold evidence — empty for tools that
			// are not held by trust_mode: scan and for pre-existing records.
			// Hash-pin authoring surface, only when a hash
			// is actually stored — otherwise the field stays absent instead of
			// rendering a "sha256/v0:" placeholder.
			if record.CurrentHash != "" {
				typedTools[i].Hash = preflight.FormatPin(record.HashSchemaVersion, record.CurrentHash)
			}
			enrichedCount++
		} else if i == 0 {
			firstErr = err
		}
	}
	if firstErr != nil {
		s.logger.Debugw("Tool approval enrichment partial", "server", serverID, "enriched", enrichedCount, "total", len(typedTools), "error", firstErr)
	}
	return typedTools
}

// globalToolsUsageWindow is the fixed look-back window for the usage columns on
// the global tools page. Not user-configurable in v1.
const globalToolsUsageWindow = 30 * 24 * time.Hour

func (s *Server) handleGetGlobalTools(w http.ResponseWriter, r *http.Request) {
	// GET /tools honours profile and client (view-as); the
	// other filters (`tool`, `status` ...) stay client-side per
	// url-filter-contract.md, and token is not a tools filter.
	if !rejectUnsupportedScopeFilters(w, r, "profile", "client") {
		return
	}
	viewAs, ok := s.parseViewAs(w, r)
	if !ok {
		return
	}
	serverFilter := r.URL.Query().Get("server")
	var viewAsEval ViewAsEvaluator
	if viewAs != nil {
		if viewAsEval, ok = s.resolveViewAs(w, r, viewAs); !ok {
			return
		}
	}
	result, err := s.globalToolsData(r.Context(), serverFilter)
	if err != nil {
		s.writeError(w, r, 500, "Failed to enumerate tools")
		return
	}
	resp := *result

	// view-as stamps each row with the subject's verdict.
	if viewAsEval != nil {
		applyViewAs(viewAsEval, resp.Tools)
	}

	updateGlobalToolsStats(&resp)

	s.writeSuccess(w, resp)
}

func (s *Server) handleGetServerLogs(w http.ResponseWriter, r *http.Request) {
	// The {id} param is percent-decoded by decodeServerIDParam middleware on the
	// /servers/{id} subtree, so a namespace/name server id such as
	// io.github.evidai/polymarket-guard reaches us already unescaped.
	serverID := chi.URLParam(r, "id")
	if serverID == "" {
		s.writeError(w, r, http.StatusBadRequest, "Server ID required")
		return
	}

	tailStr := r.URL.Query().Get("tail")
	tail := 100 // default
	if tailStr != "" {
		if parsed, err := strconv.Atoi(tailStr); err == nil && parsed > 0 {
			tail = parsed
		}
	}

	logEntries, err := s.controller.GetServerLogs(serverID, tail)
	if err != nil {
		if errors.Is(err, contracts.ErrServerNotFound) {
			s.writeError(w, r, http.StatusNotFound, fmt.Sprintf("Server not found: %s", serverID))
			return
		}
		s.logger.Errorw("Failed to get server logs", "server", serverID, "error", err)
		s.writeError(w, r, http.StatusInternalServerError, fmt.Sprintf("Failed to get logs: %v", err))
		return
	}

	response := contracts.GetServerLogsResponse{
		ServerName: serverID,
		Logs:       logEntries,
		Count:      len(logEntries),
	}

	s.writeSuccess(w, response)
}

func (s *Server) handleSearchTools(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		s.writeError(w, r, http.StatusBadRequest, "Query parameter 'q' required")
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 10 // default
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	var results []map[string]interface{}
	var err error
	profileSearchHandled := false
	if profileController, ok := s.controller.(profileToolVisibilityController); ok {
		results, profileSearchHandled, err = profileController.SearchToolsForProfile(r.Context(), query, limit)
	}
	if !profileSearchHandled {
		results, err = s.controller.SearchTools(query, limit)
	}
	if err != nil {
		s.logger.Errorw("Failed to search tools", "query", query, "error", err)
		s.writeError(w, r, http.StatusInternalServerError, fmt.Sprintf("Search failed: %v", err))
		return
	}

	// Convert to typed search results
	typedResults := contracts.ConvertGenericSearchResultsToTyped(results)

	// Restore the bare-tool-name contract on this REST surface. The index
	// read seams canonicalize the name to "server:tool" for the MCP discovery
	// path, but external consumers of /api/v1/index/search assemble the id
	// themselves from server_name + name, so a prefixed name would
	// double-prefix. The REST envelope carries the bare name plus a separate
	// server_name.
	for i := range typedResults {
		typedResults[i].Tool.Name = stripServerPrefix(typedResults[i].Tool.ServerName, typedResults[i].Tool.Name)
	}

	response := contracts.SearchToolsResponse{
		Query:   query,
		Results: typedResults,
		Total:   len(typedResults),
		Took:    "0ms", // TODO: Add timing measurement
	}

	s.writeSuccess(w, response)
}

// stripServerPrefix reverses index.CanonicalToolName for the REST index-search
// surface: given serverName "github" and name "github:create_issue" it returns
// "create_issue". Names without the "<serverName>:" prefix (already bare, or a
// missing server name) pass through unchanged.
func stripServerPrefix(serverName, name string) string {
	if serverName == "" {
		return name
	}
	return strings.TrimPrefix(name, serverName+":")
}

// Secrets management handlers

func decodePathParam(raw string) string {
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

// decodeServerIDParam percent-decodes the {id} path param of the /servers/{id}
// route subtree in place. chi matches the parent /servers/{id} segment (and thus
// populates "id") before mounting this subrouter, so the value is available to
// this middleware. Slash-name servers (io.github.owner/repo) arrive as
// io.github.owner%2Frepo; decoding here makes every sub-resource handler's
// exact-match server lookup work without each one having to call
// decodePathParam.
func decodeServerIDParam(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			for i, k := range rctx.URLParams.Keys {
				if k == "id" {
					rctx.URLParams.Values[i] = decodePathParam(rctx.URLParams.Values[i])
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Session management handlers

func (s *Server) handleGetSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// /sessions honours profile, client and token. client_name is advisory and not filterable here.
	if !rejectUnsupportedScopeFilters(w, r, "profile", "client", "token") {
		return
	}
	if !s.rejectClientNameParam(w, r) {
		return
	}

	// Parse query parameters
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 10 // default for sessions
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = min(parsed, 100)
		}
	}

	offset := 0
	if offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// Status filter. The domain is closed ("active" / "closed"), so an unknown
	// value is a client bug — rejecting it is honest, where silently ignoring it
	// would return unfiltered sessions that the caller believes are filtered.
	status := r.URL.Query().Get("status")
	if status != "" && status != "active" && status != "closed" {
		s.writeError(w, r, http.StatusBadRequest, "Invalid status. Use 'active' or 'closed'")
		return
	}

	sessionFilter := sessionFilterFromQuery(r.URL.Query(), limit, status)

	// Get recent sessions from controller
	sessions, total, err := s.controller.GetRecentSessions(sessionFilter)
	if err != nil {
		s.logger.Errorw("Failed to get sessions", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "Failed to get sessions")
		return
	}

	// Convert to non-pointer slice
	sessionList := make([]contracts.MCPSession, 0, len(sessions))
	for _, session := range sessions {
		if session != nil {
			sessionList = append(sessionList, *session)
		}
	}

	response := contracts.GetSessionsResponse{
		Sessions: sessionList,
		Total:    total,
		Limit:    limit,
		Offset:   offset,
	}

	s.writeSuccess(w, response)
}

// approveToolsRequest is the body of POST /api/v1/servers/{id}/tools/approve.
type approveToolsRequest struct {
	Tools          []string          `json:"tools"`
	ApproveAll     bool              `json:"approve_all"`
	ExpectedHashes map[string]string `json:"expected_hashes"`
	// ExpectedIncarnation binds the approval to the server the client
	// reviewed; 409 server_replaced on a mismatch. It is
	// independent of ExpectedHashes and the two may be sent together.
	ExpectedIncarnation int64 `json:"expected_incarnation,omitempty"`
}

// approveAllReviewed / approveToolsReviewed run the review-bound writers,
// carrying the expected incarnation when the controller supports it.
func (s *Server) approveAllReviewed(serverID string, expected map[string]string, inc int64) (int, error) {
	return s.controller.ApproveAllToolsReviewedAt(serverID, expected, "api", inc)
}
func (s *Server) approveToolsReviewed(serverID string, tools []string, expected map[string]string, inc int64) error {
	_, err := s.controller.ApproveToolsReviewedAt(serverID, tools, expected, "api", inc)
	return err
}

func (s *Server) handleApproveTools(w http.ResponseWriter, r *http.Request) {
	var req approveToolsRequest
	if err := decodeManagementBody(r, &req); err != nil {
		s.writeError(w, r, 400, err.Error())
		return
	}
	data, err := s.approveTools(chi.URLParam(r, "id"), req)
	if err != nil {
		if !s.writeStaleReview(w, r, err) {
			s.writeApprovalWriteError(w, r, err, "Failed to approve tools")
		}
		return
	}
	s.writeSuccess(w, data)
}

func (s *Server) handleBlockTools(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "id")
	if serverID == "" {
		s.writeError(w, r, http.StatusBadRequest, "Server ID required")
		return
	}

	var req struct {
		Tools    []string `json:"tools"`
		BlockAll bool     `json:"block_all"`
		// ExpectedIncarnation: see handleApproveTools.
		ExpectedIncarnation int64 `json:"expected_incarnation,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	if req.BlockAll {
		var (
			count int
			err   error
		)
		count, err = s.controller.BlockAllToolsAt(serverID, "api", req.ExpectedIncarnation)

		if err != nil {
			s.writeApprovalWriteError(w, r, err, "Failed to block tools")
			return
		}
		s.writeSuccess(w, map[string]interface{}{
			"blocked": count,
			"message": fmt.Sprintf("Blocked %d tools for server %s", count, serverID),
		})
		return
	}

	if len(req.Tools) == 0 {
		s.writeError(w, r, http.StatusBadRequest, "Either 'tools' array or 'block_all: true' required")
		return
	}

	var (
		count int
		err   error
	)
	count, err = s.controller.BlockToolsAt(serverID, req.Tools, "api", req.ExpectedIncarnation)

	if err != nil {
		s.writeApprovalWriteError(w, r, err, "Failed to block tools")
		return
	}

	s.writeSuccess(w, map[string]interface{}{
		"blocked": count,
		"tools":   req.Tools,
		"message": fmt.Sprintf("Blocked %d tools for server %s", count, serverID),
	})
}

// uniqueStrings returns in without repeats, in first-seen order.
func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func (s *Server) Close() {
	if s.dashboardAuth != nil {
		s.dashboardAuth.Close()
	}
}

func (s *Server) ProtectUI(next http.Handler) http.Handler { return s.dashboardAuth.ProtectUI(next) }

// sessionObject returns the session GET /sessions/{id} serves, or nil.
func (s *Server) sessionObject(id string) (*contracts.MCPSession, error) {
	sessions, _, err := s.controller.GetRecentSessions(storage.SessionFilter{Limit: 1000})
	if err != nil {
		return nil, err
	}
	for _, session := range sessions {
		if session != nil && session.ID == id {
			return session, nil
		}
	}
	return nil, nil
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.sessionObject(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Failed to get session")
		return
	}
	if session == nil {
		s.writeError(w, r, http.StatusNotFound, "session not found")
		return
	}
	s.writeSuccess(w, session)
}
