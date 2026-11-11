package server

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/cache"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/index"
	"github.com/slopster67/mcpproxy-go/internal/jsruntime"
	"github.com/slopster67/mcpproxy-go/internal/outputvalidation"
	"github.com/slopster67/mcpproxy-go/internal/preflight"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	"github.com/slopster67/mcpproxy-go/internal/reqcontext"
	"github.com/slopster67/mcpproxy-go/internal/runtime/stateview"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/toolsig"
	"github.com/slopster67/mcpproxy-go/internal/truncate"
	"github.com/slopster67/mcpproxy-go/internal/upstream"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"

	"errors"

	"github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"go.uber.org/zap"
)

const (
	// defaultInstructions is returned in the MCP initialize response when no
	// custom instructions are configured. It guides AI agents on the correct
	// workflow for discovering and calling tools through the proxy.
	defaultInstructions = instrIntro + instrNotListed + instrDiscovery + instrCLIFallback + instrRetry +
		instrCalling + instrCodeExecution + instrDirect + instrUpstreamList + instrAbout
)

// resolveInstructions returns custom instructions if set, otherwise the built-in default.
func resolveInstructions(custom string) string {
	if custom != "" {
		return custom
	}
	return defaultInstructions
}

// proxyVersion holds the build version for MCP server identification.
// Set via SetMCPServerVersion during startup.
var proxyVersion = "1.0.0"

// SetMCPServerVersion sets the version reported by MCP server instances.
func SetMCPServerVersion(v string) {
	if v != "" {
		proxyVersion = v
	}
}

func mcpServerVersion() string {
	return proxyVersion
}

// MCPProxyServer implements an MCP server that acts as a proxy
type MCPProxyServer struct {
	managementIndexOnce sync.Once
	managementIndex     *index.Manager
	managementIndexErr  error

	// inventoryMu guards lastInventoryKey, the reachable-server set last
	// announced via tools/list_changed (NotifyUpstreamInventoryChanged).
	inventoryMu      sync.Mutex
	lastInventoryKey string
	// reachableMu guards the cached reachable-server set (mcp_agent_access.go).
	reachableMu sync.Mutex
	reachable   []string
	reachableAt time.Time

	server          *mcpserver.MCPServer
	storage         *storage.Manager
	index           *index.Manager
	upstreamManager *upstream.Manager
	cacheManager    *cache.Manager
	truncator       *truncate.Truncator
	outputValidator *outputvalidation.Validator // output-schema validation (nil when disabled)
	inputValidator  *inputValidator             // pre-dispatch argument validation (never nil)
	logger          *zap.Logger
	mainServer      *Server        // Reference to main server for config persistence
	config          *config.Config // Add config reference for security checks

	// sigCache memoizes compact tool signatures keyed by tool hash. It is the
	// SAME instance the Runtime owns and the indexing path warms — never
	// construct a second one here, or the warm-up becomes a silent no-op and
	// every request recompiles.
	sigCache *toolsig.Cache

	// profilesViews is the REST server's admin views behind the `profiles`
	// tool, installed by SetAdminViews. While nil every operation answers 503.
	profilesViewsMu sync.RWMutex
	profilesViews   profilesAdminViews

	// preflightStateSource overrides the connection-state snapshot the
	// preflight glue reads. When nil, preflightSnapshot resolves it from the
	// supervisor's StateView.
	preflightStateSource func() (preflight.StateReader, func(serverName, toolName string) *config.ToolAnnotations, error)

	// Routing mode MCP server instances. Each instance has different tools
	// registered for its routing mode.
	directServer   *mcpserver.MCPServer // Direct mode: upstream tools with serverName__toolName naming
	codeExecServer *mcpserver.MCPServer // Code execution mode: code_execution + retrieve_tools
	callToolServer *mcpserver.MCPServer // Call tool mode: retrieve_tools + call_tool_read/write/destructive

	// JavaScript runtime pool for code execution
	jsPool *jsruntime.Pool

	// MCP session tracking
	sessionStore *SessionStore

	// profileNotifier delivers profile-edit notifications and the
	// profiles.changed event after a snapshot is published.
	profileNotifier *profileChangeNotifier

	// toolCounts memoizes an administrator's per-profile tool counts for one
	// published (profile index, tool tier generation) pair.
	toolCounts profileToolCountsCache

	// Hooks shared across all routing mode servers
	hooks *mcpserver.Hooks

	// directCatalogPtr holds the immutable directCatalog snapshot: the single
	// source for direct-surface listing, describe_tool resolution, signature
	// lookup and pre-dispatch validation. A nil pointer means "not built yet",
	// distinct from a built, empty catalog.
	//
	// The snapshot is written only by the routing-refresh goroutine, read on
	// every direct tools/list, describe_tool and dispatch, and never mutated
	// after publication, so the read path takes no lock.
	directCatalogPtr atomic.Pointer[directCatalog]

	// Surface fingerprints: the content last actually registered on each tool
	// surface, so a rebuild that would change nothing can be skipped instead of
	// notifying every client. See mcp_surface_fingerprint.go.
	//
	// The direct pair is guarded by directRefreshMu, which every caller of
	// refreshDirectModeToolsLocked already holds; the code-exec and call-tool
	// surfaces have their own mutexes.
	directSurfaceToolsFP   string
	directSurfaceRoutingFP string

	codeExecRefreshMu sync.Mutex
	codeExecSurfaceFP string

	// The call-tool surface is rebuilt on every config.reloaded; the
	// fingerprint keeps an unrelated edit from telling every client the tool
	// set changed.
	callToolSurfaceFP string

	directCatalogGeneration atomic.Uint64

	// directRefreshMu serializes whole rebuilds so SetTools and the matching
	// publishDirectCatalog cannot interleave with another rebuild's pair.
	directRefreshMu sync.Mutex
}

// NewMCPProxyServer creates a new MCP proxy server
func NewMCPProxyServer(
	storage *storage.Manager,
	index *index.Manager,
	upstreamManager *upstream.Manager,
	cacheManager *cache.Manager,
	truncator *truncate.Truncator,
	logger *zap.Logger,
	mainServer *Server,

	config *config.Config,
	sigCache *toolsig.Cache,
) *MCPProxyServer {
	// Initialize session store first (needed for hooks)
	sessionStore := NewSessionStore(logger)
	// Wire up storage manager for session persistence
	sessionStore.SetStorageManager(storage)

	// Let the activity log name the MCP client on every record it writes.
	// Activity is retained for 90 days but only the 100 most recent SESSIONS are
	// kept — and an IDE that reconnects every few minutes burns through 100
	// sessions in about a day. So the client name must be stamped on the record
	// at write time; resolving it later against the session store works for a
	// day and then decays back to a bare session id.
	if mainServer != nil && mainServer.runtime != nil {
		mainServer.runtime.SetSessionClientResolver(func(sessionID string) (name, version string) {
			if info := sessionStore.GetSession(sessionID); info != nil {
				return info.ClientName, info.ClientVersion
			}
			return "", ""
		})

		// Which WORK session a record belongs to — one client, one
		// project, across reconnects. Reads the id cached on the connection, so
		// every record from that connection agrees.
		mainServer.runtime.SetWorkSessionResolver(func(sessionID string) string {
			return sessionStore.WorkSessionID(sessionID)
		})
	}

	// Forward handles to the MCP server and the proxy, both constructed below but
	// needed by the hooks above them (to send a roots request back to the client,
	// and to attribute work to a session). Atomic because the hooks run on client
	// goroutines.
	var mcpSrvRef atomic.Pointer[mcpserver.MCPServer]
	var proxyRef atomic.Pointer[MCPProxyServer]

	// Create hooks to capture session information
	hooks := &mcpserver.Hooks{}
	// Update session activity on every MCP message to prevent premature cleanup.
	// Without this, sessions that only do retrieve_tools (not tool calls) would
	// appear inactive and get closed by the background cleanup.
	hooks.AddBeforeAny(func(ctx context.Context, id any, method mcp.MCPMethod, message any) {
		session := mcpserver.ClientSessionFromContext(ctx)
		if session == nil {
			return
		}
		sessionStore.UpdateActivity(session.SessionID())

		// Ask the client which project it is working in — once, lazily,
		// on the first real request after the handshake.
		//
		// The timing is the whole trick, and two obvious options are both wrong:
		//   - AddAfterInitialize runs BEFORE the initialize response is written,
		//     so asking there DEADLOCKS: the client cannot answer until it has
		//     the initialize result it is still waiting for.
		//   - notifications/initialized never reaches this hook at all — mcp-go
		//     dispatches notifications before beforeAny is invoked
		//     (request_handler.go: `if baseMessage.ID == nil { ... return }`).
		//
		// The first request with an id (tools/list, tools/call, ...) is the
		// earliest point that both fires this hook and finds the client able to
		// answer. Fetching is async and best-effort: a client that will not
		// answer simply has no workspace.
		if method != mcp.MethodInitialize && sessionStore.TryClaimWorkspaceFetch(session.SessionID()) {
			go fetchWorkspaceRoot(ctx, mcpSrvRef.Load(), sessionStore, logger)
		}

		// Attribute this request to a work session before the handler
		// runs, so every activity record the handler writes carries it. Kicked off
		// AFTER the roots fetch above, which it may wait on briefly — the fetch is
		// already in flight by then.
		if proxy := proxyRef.Load(); proxy != nil {
			proxy.markWorkIfToolCall(ctx, method)
		}
	})

	hooks.AddOnRegisterSession(func(ctx context.Context, sess mcpserver.ClientSession) {
		sessionID := sess.SessionID()

		// Client info and capabilities are set by OnAfterInitialize. A transport
		// can re-register an id that was unregistered without a new initialize
		// (a GET stream ending and being re-opened), so revive the soft-closed
		// entry to keep its attribution.
		sessionStore.Reopen(sessionID)
		logger.Info("MCP session registered",
			zap.String("session_id", sessionID),
		)
	})

	// Add hook to capture client capabilities after initialize completes
	// This hook is called for ALL transports (including HTTP POST), and receives the
	// InitializeRequest with client info and capabilities.
	hooks.AddAfterInitialize(func(ctx context.Context, id any, request *mcp.InitializeRequest, result *mcp.InitializeResult) {
		// Get session from context
		session := mcpserver.ClientSessionFromContext(ctx)
		if session == nil {
			return
		}

		sessionID := session.SessionID()

		// Extract client info and capabilities directly from the initialize request
		// This works for all transports, including ephemeral streamable HTTP sessions
		clientName := request.Params.ClientInfo.Name
		clientVersion := request.Params.ClientInfo.Version

		capabilities := request.Params.Capabilities
		hasRoots := capabilities.Roots != nil
		hasSampling := capabilities.Sampling != nil

		var experimental []string
		if len(capabilities.Experimental) > 0 {
			experimental = make([]string, 0, len(capabilities.Experimental))
			for key := range capabilities.Experimental {
				experimental = append(experimental, key)
			}
		}

		// Store/update session information with capabilities
		sessionStore.SetSession(sessionID, clientName, clientVersion, hasRoots, hasSampling, experimental)

		// Record which credential authenticated
		// this session and which server instance serves it, so a binding
		// change can find the session and notify it on the right instance.
		// The credential never changes mid-connection, so this is once.
		if ac := auth.AuthContextFromContext(ctx); ac != nil && ac.AgentName != "" {
			sessionStore.SetSessionIdentity(sessionID, ac.AgentName, ac.ClientID)
			sessionStore.SetSessionTokenPrefix(sessionID, ac.TokenPrefix)
		}
		sessionStore.SetSessionServer(sessionID, mcpserver.ServerFromContext(ctx))

		logger.Info("MCP client initialized with capabilities",
			zap.String("session_id", sessionID),
			zap.String("client_name", clientName),
			zap.String("client_version", clientVersion),
			zap.Bool("has_roots", hasRoots),
			zap.Bool("has_sampling", hasSampling),
			zap.Strings("experimental", experimental),
		)
	})

	// Per-caller instructions: profile, token scope and limits (mcp_agent_access.go).
	installCallerInstructionsHooks(hooks, proxyRef.Load)

	// Add hook to clean up session on disconnect
	// NOTE: for Streamable HTTP this hook fires whenever a GET stream ends, not
	// only on a real disconnect, and the client may keep using the same session id
	// afterwards. So it only soft-closes (SessionStore.RemoveSession): attribution
	// is kept and revived by the next activity or register, and closed entries are
	// evicted after a TTL. Persisted records are also closed by inactivity timeout
	// (see runtime.backgroundSessionCleanup).
	hooks.AddOnUnregisterSession(func(ctx context.Context, sess mcpserver.ClientSession) {
		sessionID := sess.SessionID()

		logger.Debug("OnUnregisterSession hook called - transport supports disconnect detection",
			zap.String("session_id", sessionID),
		)

		// Soft-close the session (closes in storage, keeps attribution)
		sessionStore.RemoveSession(sessionID)

		logger.Info("MCP session unregistered",
			zap.String("session_id", sessionID),
		)
	})

	// Annotate every tool in tools/list responses with
	// `_meta.anthropic/maxResultSizeChars`. Claude Code and other clients
	// that honor this annotation raise their inline-response ceiling from
	// the default 50k chars up to the declared value (max 500k), so large
	// upstream tool responses flow through inline instead of being spilled
	// to disk as a 2KB preview. No-op when config is 0.
	registerMaxResultSizeHook(hooks, config.EffectiveMaxResultSizeChars())

	// Create MCP server with capabilities and hooks
	capabilities := []mcpserver.ServerOption{
		mcpserver.WithToolCapabilities(true),
		mcpserver.WithRecovery(),
		mcpserver.WithHooks(hooks),
		mcpserver.WithIcons(mcpproxyIcons()...),
	}

	// Explicit, private, short-lived cache hints.
	capabilities = append(capabilities, cacheHintServerOptions()...)

	// Add server instructions for AI agent guidance
	capabilities = append(capabilities, mcpserver.WithInstructions(resolveInstructions(config.Instructions)))

	mcpServer := mcpserver.NewMCPServer(
		"mcpproxy-go",
		mcpServerVersion(),
		capabilities...,
	)

	// Publish the server to the hooks, which need it to ask the client for its
	// workspace roots. Set before the server ever serves a request.
	mcpSrvRef.Store(mcpServer)

	jsPool, err := jsruntime.NewPool(config.CodeExecutionPoolSize)
	if err != nil {
		logger.Error("failed to create JavaScript runtime pool", zap.Error(err))
	}

	// Construct the output-schema validator when validation is enabled
	// (mode != "off"). A nil validator means "no validation" on the hot path.
	var outputValidator *outputvalidation.Validator
	if config.OutputValidation.IsEnabled() {
		outputValidator = outputvalidation.New(
			config.OutputValidation.EffectiveMaxBytes(),
			config.OutputValidation.EffectiveMaxDepth(),
			logger,
		)
	}

	proxy := &MCPProxyServer{
		server:          mcpServer,
		storage:         storage,
		index:           index,
		upstreamManager: upstreamManager,
		cacheManager:    cacheManager,
		truncator:       truncator,
		outputValidator: outputValidator,
		inputValidator:  newInputValidator(logger),
		logger:          logger,
		mainServer:      mainServer,
		config:          config,
		sigCache:        sigCache,
		jsPool:          jsPool,
		sessionStore:    sessionStore,
		hooks:           hooks,
	}

	// Let the hooks (registered before the proxy existed) reach it.
	proxyRef.Store(proxy)

	// Build the stored-script index now that the scripts directory is known,
	// so no scoped request ever lists it.
	scriptsDir := proxy.scriptsDir()
	proxy.warmStoredScripts(scriptsDir)

	// Register proxy tools for the default (retrieve_tools) server
	proxy.registerTools()

	mcpserver.WithToolFilter(proxy.filterProfileV3Tools)(mcpServer)
	mcpserver.WithToolFilter(proxy.filterAdvertiseServersInRetrieveTools)(mcpServer)

	// Initialize routing mode server instances
	proxy.initRoutingModeServers()
	proxy.seedInventoryKey()

	return proxy
}

// Close gracefully shuts down the MCP proxy server and releases resources
func (p *MCPProxyServer) Close() error {
	if p.managementIndex != nil {
		defer p.managementIndex.Close()
	}

	if p.profileNotifier != nil {
		p.profileNotifier.close()
	}
	if p.jsPool != nil {
		if err := p.jsPool.Close(); err != nil {
			p.logger.Warn("failed to close JavaScript runtime pool", zap.Error(err))
			return err
		}
		p.logger.Info("JavaScript runtime pool closed successfully")
	}
	return nil
}

// getAuthMetadata extracts auth identity metadata from context for activity logging.
// Returns nil if no auth context is present.
func getAuthMetadata(ctx context.Context) map[string]string {
	authCtx := auth.AuthContextFromContext(ctx)
	if authCtx == nil {
		return nil
	}
	meta := map[string]string{
		"auth_type": authCtx.Type,
	}
	if authCtx.Type == auth.AuthTypeClient {
		meta["agent_name"] = authCtx.AgentName
		meta["token_prefix"] = authCtx.TokenPrefix
	}
	return meta
}

// injectAuthMetadata creates a shallow copy of args with auth identity metadata added.
// Uses "_auth_" prefix to clearly separate auth metadata from tool arguments.
// Returns a new map — the original args map is never mutated, so it remains
// safe to forward to upstream servers without leaking internal metadata.
func injectAuthMetadata(ctx context.Context, args map[string]interface{}) map[string]interface{} {
	authMeta := getAuthMetadata(ctx)
	if authMeta == nil {
		return args
	}
	enriched := make(map[string]interface{}, len(args)+len(authMeta))
	for k, v := range args {
		enriched[k] = v
	}
	for k, v := range authMeta {
		enriched["_auth_"+k] = v
	}
	return enriched
}

func (p *MCPProxyServer) emitActivityToolCallStarted(ctx context.Context, serverName, toolName, sessionID, requestID, source string, args map[string]any) {
	if p.mainServer != nil && p.mainServer.runtime != nil {
		p.mainServer.runtime.EmitActivityToolCallStarted(serverName, toolName, sessionID, requestID, source, args)
	}
}

func (p *MCPProxyServer) emitActivityToolCallCompleted(ctx context.Context, serverName, toolName, sessionID, requestID, source, status, errorMsg string, durationMs int64, arguments map[string]interface{}, response string, responseTruncated bool, toolVariant string, intent map[string]interface{}, contentTrust, profile string, requestBytes, responseBytes int, parentID string) {
	p.emitActivityToolCallCompletedWithBlockReason(ctx, serverName, toolName, sessionID, requestID, source, status, errorMsg, durationMs, arguments, response, responseTruncated, toolVariant, intent, contentTrust, profile, requestBytes, responseBytes, parentID, "")
}

// emitActivityToolCallCompletedWithBlockReason is emitActivityToolCallCompleted
// plus blockReason: the profile.BlockReason of a pre-dispatch profile
// tool-policy refusal (the code_execution sandbox's nested gate);
// empty for everything else. It mirrors the emitActivityPolicyDecision /
// emitActivityPolicyDecisionWithBlockReason pair, and keeps status at index 6.
func (p *MCPProxyServer) emitActivityToolCallCompletedWithBlockReason(ctx context.Context, serverName, toolName, sessionID, requestID, source, status, errorMsg string, durationMs int64, arguments map[string]interface{}, response string, responseTruncated bool, toolVariant string, intent map[string]interface{}, contentTrust, profile string, requestBytes, responseBytes int, parentID, blockReason string) {
	if p.mainServer != nil && p.mainServer.runtime != nil {
		p.mainServer.runtime.EmitActivityToolCallCompletedAttributed(serverName, toolName, sessionID, requestID, source, status, errorMsg, durationMs, arguments, response, responseTruncated, toolVariant, intent, contentTrust, profile, requestBytes, responseBytes, parentID, blockReason, p.activityAttribution(ctx, sessionID))
	}
}

func (p *MCPProxyServer) emitActivityPolicyDecision(ctx context.Context, serverName, toolName, sessionID, requestID, decision, reason string) {
	p.emitActivityPolicyDecisionWithBlockReason(ctx, serverName, toolName, sessionID, requestID, decision, reason, "")
}

func (p *MCPProxyServer) emitActivityPolicyDecisionWithBlockReason(ctx context.Context, serverName, toolName, sessionID, requestID, decision, reason, blockReason string) {
	if p.mainServer != nil && p.mainServer.runtime != nil {
		p.mainServer.runtime.EmitActivityPolicyDecisionAttributed(serverName, toolName, sessionID, requestID, decision, reason, blockReason, p.activityAttribution(ctx, sessionID))
	}
}

var activityRequestIDSeq atomic.Uint64

// mintCorrelationIDAt builds "<nanos>-<part>-…-<seq>". It is the only place in
// the package that turns the clock into an identifier.
//
// The instant is a parameter because some records are keyed by when the work
// started, not by when the id was needed.
func mintCorrelationIDAt(at time.Time, parts ...string) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(at.UnixNano(), 10))
	for _, part := range parts {
		b.WriteByte('-')
		b.WriteString(part)
	}
	b.WriteByte('-')
	b.WriteString(strconv.FormatUint(activityRequestIDSeq.Add(1), 10))
	return b.String()
}

// mintCorrelationID is mintCorrelationIDAt for work starting now.
func mintCorrelationID(parts ...string) string {
	return mintCorrelationIDAt(time.Now(), parts...)
}

// mintActivityRequestID produces the correlation id shared by every activity
// event of one dispatch. The counter suffix is what guarantees uniqueness.
//
// Callers mint ONCE, above the earliest policy gate they can reach, and reuse
// the value: minting per emit site would make two gates in one dispatch look
// like two unrelated requests.
func mintActivityRequestID(serverName, toolName string) string {
	return mintCorrelationID(serverName, toolName)
}

// emitActivityInternalToolCall safely emits an internal tool call completion event.
// internalToolName is the name of the internal tool (retrieve_tools, call_tool_read, etc.)
// targetServer and targetTool are used for call_tool_* handlers
// arguments contains the input parameters, response contains the output
// intent is the intent declaration metadata
//
// This is the whole-response form: the built-in returned to the agent exactly
// the text it recorded. Handlers that SHAPE their text before returning it —
// retrieve_tools, which is subject to tool_response_limit — must use
// emitActivityInternalToolCallTruncated instead, so the record says so.
func (p *MCPProxyServer) emitActivityInternalToolCall(ctx context.Context, internalToolName, targetServer, targetTool, toolVariant, sessionID, requestID, status, errorMsg string, durationMs int64, arguments map[string]interface{}, response interface{}, intent map[string]interface{}, contentTrust string) {
	p.emitActivityInternalToolCallTruncated(ctx, internalToolName, targetServer, targetTool, toolVariant, sessionID, requestID, status, errorMsg, durationMs, arguments, response, intent, contentTrust, false)
}

func (p *MCPProxyServer) emitActivityInternalToolCallTruncated(ctx context.Context, internalToolName, targetServer, targetTool, toolVariant, sessionID, requestID, status, errorMsg string, durationMs int64, arguments map[string]interface{}, response interface{}, intent map[string]interface{}, contentTrust string, responseTruncated bool) {
	if p.mainServer != nil && p.mainServer.runtime != nil {
		// The attribution comes from the request context like
		// the call this record wraps (REST /tools/call included); only the fields
		// the context does not carry fall back to the session's latest resolution.
		p.mainServer.runtime.EmitActivityInternalToolCallAttributed(internalToolName, targetServer, targetTool, toolVariant, sessionID, requestID, status, errorMsg, durationMs, arguments, response, intent, contentTrust, responseTruncated, p.activityAttribution(ctx, sessionID))
	}
}

// buildCallToolVariantTool constructs the mcp.Tool definition for a single
// call_tool_* variant (read/write/destructive).
func buildCallToolVariantTool(variant string) mcp.Tool {
	var description, title, nameExample, sensitivityDesc, reasonDesc string
	var opts []mcp.ToolOption

	switch variant {
	case contracts.ToolVariantRead:
		description = "Execute a READ-ONLY tool. WORKFLOW: 1) Call retrieve_tools first to find tools, 2) Use the exact 'name' field from results, 3) Build args from the 'sig' signature ('*'=required; if lossy '~', call describe_tool first). DECISION RULE: Use this when the tool name contains: search, query, list, get, fetch, find, check, view, read, show, describe, lookup, retrieve, browse, explore, discover, scan, inspect, analyze, examine, validate, verify. Examples: search_files, get_user, list_repositories, query_database, find_issues, check_status. This is the DEFAULT choice when unsure - most tools are read-only."
		title = "Call Tool (Read)"
		nameExample = "github:get_user"
		sensitivityDesc = "Classify data being accessed: public, internal, private, or unknown. Helps track sensitive data access patterns."
		reasonDesc = "Why is this tool being called? Provide context like 'User asked to check status' or 'Gathering data for report'."
		opts = []mcp.ToolOption{
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithOpenWorldHintAnnotation(true),
		}
	case contracts.ToolVariantWrite:
		description = "Execute a STATE-MODIFYING tool. WORKFLOW: 1) Call retrieve_tools first to find tools, 2) Use the exact 'name' field from results, 3) Build args from the 'sig' signature ('*'=required; if lossy '~', call describe_tool first). DECISION RULE: Use this when the tool name contains: create, update, modify, add, set, send, edit, change, write, post, put, patch, insert, upload, submit, assign, configure, enable, register, subscribe, publish, move, copy, rename, merge. Examples: create_issue, update_file, send_message, add_comment, set_status, edit_page. Use only when explicitly modifying state."
		title = "Call Tool (Write)"
		nameExample = "github:create_issue"
		sensitivityDesc = "Classify data being modified: public, internal, private, or unknown. Helps track sensitive data changes."
		reasonDesc = "Why is this modification needed? Provide context like 'User requested update' or 'Fixing reported issue'."
		opts = []mcp.ToolOption{
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithOpenWorldHintAnnotation(true),
		}
	case contracts.ToolVariantDestructive:
		description = "Execute a DESTRUCTIVE tool. WORKFLOW: 1) Call retrieve_tools first to find tools, 2) Use the exact 'name' field from results, 3) Build args from the 'sig' signature ('*'=required; if lossy '~', call describe_tool first). DECISION RULE: Use this when the tool name contains: delete, remove, drop, revoke, disable, destroy, purge, reset, clear, unsubscribe, cancel, terminate, close, archive, ban, block, disconnect, kill, wipe, truncate, force, hard. Examples: delete_repo, remove_user, drop_table, revoke_access, clear_cache, terminate_session. Use for irreversible or high-impact operations."
		title = "Call Tool (Destructive)"
		nameExample = "github:delete_repo"
		sensitivityDesc = "Classify data being deleted: public, internal, private, or unknown. Important for tracking destructive operations on sensitive data."
		reasonDesc = "Why is this deletion needed? Provide justification like 'User confirmed cleanup' or 'Removing obsolete data'."
		opts = []mcp.ToolOption{
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(true),
		}
	default:
		description = "Execute a tool."
		title = "Call Tool"
		nameExample = "server:tool"
		sensitivityDesc = "Classify data sensitivity."
		reasonDesc = "Why is this tool being called?"
	}

	allOpts := []mcp.ToolOption{
		mcp.WithDescription(description),
		mcp.WithTitleAnnotation(title),
		mcp.WithToolTitle(title),
	}
	allOpts = append(allOpts, opts...)
	allOpts = append(allOpts,
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description(fmt.Sprintf("Tool name in format 'server:tool' (e.g., '%s'). CRITICAL: You MUST use exact names from retrieve_tools results - do NOT guess or invent server names. Unknown servers will fail.", nameExample)),
		),
		mcp.WithObject("args",
			openObject(),
			mcp.Description("Arguments to pass to the upstream tool as a native JSON object. Build arguments from the tool's compact signature ('sig') in retrieve_tools results — '*' marks required parameters, '~' marks a lossy signature (call describe_tool for the full JSON Schema before calling). Example: {\"path\": \"src/index.ts\", \"limit\": 20}."),
		),
		mcp.WithString("intent_data_sensitivity",
			mcp.Description(sensitivityDesc),
		),
		mcp.WithString("intent_reason",
			mcp.Description(reasonDesc),
		),
	)

	return newProxyTool(variant, allOpts...)
}

// retrieveToolsDetailOption returns the per-call serialization override
// parameter shared by every retrieve_tools registration —
// the default server and both routing-mode builders — so the schema never
// drifts between surfaces. Enum {compact, full}, deliberately NO default:
// an unset detail means "use the configured tool_response_mode".
func retrieveToolsDetailOption() mcp.ToolOption {
	return mcp.WithString("detail",
		mcp.Description("Per-call response serialization override: 'compact' returns one-line signatures (sig/desc/lossy) instead of full schemas; 'full' returns complete inputSchema entries. Unset: the server's configured tool_response_mode applies."),
		mcp.Enum(config.ToolResponseModeCompact, config.ToolResponseModeFull),
	)
}

// retrieveToolsAnnotationFilterOptions returns the annotation-based safety
// filters shared by every retrieve_tools registration — the default server
// and both routing-mode builders — so the schema cannot drift between
// surfaces.
func retrieveToolsAnnotationFilterOptions() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithBoolean("read_only_only",
			mcp.Description("Only return tools with readOnlyHint=true. Use to self-restrict to safe read operations."),
		),
		mcp.WithBoolean("exclude_destructive",
			mcp.Description("Exclude tools with destructiveHint=true or unset (MCP default is destructive). Use to avoid destructive operations."),
		),
		mcp.WithBoolean("exclude_open_world",
			mcp.Description("Exclude tools with openWorldHint=true or unset (MCP default is open-world). Use to restrict to local/sandboxed tools."),
		),
	}
}

// retrieveToolsDiagnosticsNote is appended verbatim to all three
// retrieve_tools descriptions. The closing sentence is the
// load-bearing one: the counts describe the ranked window THIS call searched,
// so an agent must not read them as a statement about the whole catalog.
const retrieveToolsDiagnosticsNote = " ANNOTATION FILTERS: read_only_only, exclude_destructive and exclude_open_world self-restrict discovery. " +
	"When they withhold tools that matched your query, the response carries a 'filter_diagnostics' block with per-filter counts " +
	"(split into missing upstream annotations vs. explicitly unsafe ones) and one suggestion; it is absent when nothing was withheld. " +
	"Filter diagnostics describe this call's candidate window, not the whole catalog."

// registerTools registers all proxy tools with the MCP server
func (p *MCPProxyServer) registerTools() {
	// retrieve_tools - THE PRIMARY TOOL FOR DISCOVERING TOOLS - Enhanced with clear instructions
	retrieveToolsOpts := []mcp.ToolOption{
		mcp.WithDescription("🔍 CALL THIS FIRST to discover relevant tools! This is the primary tool discovery mechanism that searches across ALL upstream MCP servers using intelligent BM25 full-text search. Always use this before attempting to call any specific tools. Use natural language to describe what you want to accomplish (e.g., 'create GitHub repository', 'query database', 'weather forecast'). Results include 'annotations' (tool behavior hints like destructiveHint) and 'call_with' recommendation indicating which tool variant to use (call_tool_read/write/destructive). Then use the recommended variant with an 'intent' parameter. Compact mode returns one-line signatures ('sig': '*'=required, '~'=lossy) with first-sentence 'desc'; call describe_tool for full schemas. New and changed tools require approval before use. TO ADD NEW SERVERS:" + retrieveToolsReachNote + retrieveToolsDiagnosticsNote),
		mcp.WithTitleAnnotation("Retrieve Tools"),
		mcp.WithToolTitle("Retrieve Tools"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Natural language description of what you want to accomplish. Be specific about your task (e.g., 'create a new GitHub repository', 'get weather for London', 'query SQLite database for users'). The search will find the most relevant tools across all connected servers."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of tools to return (default: configured tools_limit, max: 100)"),
		),
		mcp.WithBoolean("include_stats",
			mcp.Description("Include usage statistics for returned tools (default: false)"),
		),
		mcp.WithBoolean("include_disabled",
			mcp.Description("Set true to also surface tools that exist but are currently locked by config, user, or quarantine (default: false). Returns a 'disabled' list (name/server/description/status) plus a 'remediation' map; callable results are unaffected and listed first."),
		),
		retrieveToolsDetailOption(),
	}
	retrieveToolsOpts = append(retrieveToolsOpts, retrieveToolsAnnotationFilterOptions()...)
	retrieveToolsTool := newProxyTool("retrieve_tools", retrieveToolsOpts...)
	p.server.AddTool(retrieveToolsTool, p.handleRetrieveTools)

	p.server.AddTool(buildDescribeToolTool(), p.handleDescribeTool)

	// set_profile switches the session's active profile
	p.server.AddTool(buildSetProfileTool(), p.handleSetProfile)

	callToolReadTool := buildCallToolVariantTool(contracts.ToolVariantRead)
	p.server.AddTool(callToolReadTool, p.handleCallToolRead)

	callToolWriteTool := buildCallToolVariantTool(contracts.ToolVariantWrite)
	p.server.AddTool(callToolWriteTool, p.handleCallToolWrite)

	callToolDestructiveTool := buildCallToolVariantTool(contracts.ToolVariantDestructive)
	p.server.AddTool(callToolDestructiveTool, p.handleCallToolDestructive)

	// read_cache - Access paginated data when responses are truncated
	readCacheTool := newProxyTool("read_cache",
		mcp.WithDescription("Retrieve paginated data when mcpproxy indicates a tool response was truncated. Use the cache key provided in truncation messages to access the complete dataset with pagination."),
		mcp.WithTitleAnnotation("Read Cache"),
		mcp.WithToolTitle("Read Cache"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("Cache key provided by mcpproxy when a response was truncated (e.g. 'Use read_cache tool: key=\"abc123def...\"')"),
		),
		mcp.WithNumber("offset",
			mcp.Description("Starting record offset for pagination (default: 0)"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of records to return per page (default: 50, max: 1000)"),
		),
	)
	p.server.AddTool(readCacheTool, p.handleReadCache)

	// code_execution - JavaScript/TypeScript code execution for multi-tool
	// orchestration. Execution is authorized against the client permission on
	// every call.
	for _, st := range p.buildCodeExecutionTool() {
		p.server.AddTool(st.Tool, st.Handler)
	}
}

// handleRetrieveTools implements retrieve_tools.
func (p *MCPProxyServer) handleRetrieveToolsForMode(routingMode string) func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return p.handleRetrieveToolsWithMode(ctx, request, routingMode)
	}
}

func (p *MCPProxyServer) handleRetrieveTools(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return p.handleRetrieveToolsWithMode(ctx, request, "")
}

func (p *MCPProxyServer) handleRetrieveToolsWithMode(ctx context.Context, request mcp.CallToolRequest, routingMode string) (*mcp.CallToolResult, error) {
	// A tool retrieval is real work — it earns the session a record.
	if sid := sessionIDFromContext(ctx); sid != "" {
		p.markSessionWorked(ctx, sid)
	}

	startTime := time.Now()

	// Extract session info for activity logging
	var sessionID string
	if sess := mcpserver.ClientSessionFromContext(ctx); sess != nil {
		sessionID = sess.SessionID()
	}
	requestID := mintCorrelationID("retrieve_tools")

	query, err := request.RequireString("query")
	if err != nil {
		// Emit internal tool call event for error case
		p.emitActivityInternalToolCall(ctx, "retrieve_tools", "", "", "", sessionID, requestID, "error", err.Error(), time.Since(startTime).Milliseconds(), nil, nil, nil, "")
		return mcp.NewToolResultError(fmt.Sprintf("Missing required parameter 'query': %v", err)), nil
	}

	// Get optional parameters
	limit := int(request.GetFloat("limit", float64(p.config.ToolsLimit)))
	includeStats := request.GetBool("include_stats", false)

	// Resolve the serialization mode for THIS call — per-call `detail`
	// override > live-config tool_response_mode > full. The mode is consulted
	// only by buildToolEntry below, after the query/rank/filter pipeline.
	detailParam := request.GetString("detail", "")
	responseMode := p.effectiveToolResponseMode(detailParam)
	if routingMode == config.RoutingModeCodeExecution {
		// describe_tool is not exposed on the code-execution surface, so a
		// compact response would point the agent at an unavailable tool. This
		// surface is always FULL, ignoring both the global mode and any
		// smuggled detail arg (the param is absent from this mode's schema,
		// but arguments are not schema-enforced).
		detailParam = ""
		responseMode = config.ToolResponseModeFull
	}

	// Annotation-based filtering parameters
	readOnlyOnly := request.GetBool("read_only_only", false)
	excludeDestructive := request.GetBool("exclude_destructive", false)
	excludeOpenWorld := request.GetBool("exclude_open_world", false)

	// Build arguments map for activity logging
	args := map[string]interface{}{
		"query": query,
		"limit": limit,
	}
	if includeStats {
		args["include_stats"] = true
	}

	if readOnlyOnly {
		args["read_only_only"] = true
	}
	if excludeDestructive {
		args["exclude_destructive"] = true
	}
	if excludeOpenWorld {
		args["exclude_open_world"] = true
	}
	if detailParam != "" {
		args["detail"] = detailParam
	}

	// Validate limit
	if limit > 100 {
		limit = 100
	}

	// Resolve the effective profile (token pin > URL > session
	// set_profile > none) and search the matching per-profile index when one is
	// in effect. The per-profile index physically holds only that profile's
	// servers' tools, so switching costs no re-index. The shared index remains
	// the allow-all fallback; profileScope still post-filters as defense in depth
	// (and covers the fallback path below).
	//
	// A deny-all scope (an empty profile, or the scope a stale client token pin
	// resolves to) deliberately skips ForProfile: that call lazily CREATES and
	// caches an on-disk per-profile index, so honoring it would let a request
	// that is allowed to see nothing leave a new index directory behind, possibly
	// for a profile that does not exist. The post-filter below returns the same
	// empty result set from the shared index.
	profileName, profileScope, profileIdx, profileSource := p.resolveActiveProfileWithSource(ctx)
	ctx, profileResolution := p.resolveForDispatch(ctx, profileIdx)
	if profileResolution.Scope != nil {
		profileName = profileResolution.Name
		profileScope = profileResolution.Scope
		profileSource = profile.Source(profileResolution.Source)
	}
	var policy *profile.CompiledPolicy
	if profileName != "" {
		policy = profileIdx.PolicyFor(profileName)
	}
	hasProfile := profileName != ""
	// The cache stamp is the authorization THIS search runs under, captured
	// now rather than re-resolved when the response is cut.
	// cacheAuthorizationWith derives the stamped ProfileServers from this exact
	// index/snapshot pair, never a second, independently resolved one.
	producer := p.cacheAuthorizationWith(ctx, profileName, profileScope, profileIdx)
	searchIndex := p.index
	if profileName != "" && !profileScope.DeniesAll() {
		if pIdx, perr := p.index.ForProfile(profileName); perr == nil && pIdx != nil {
			searchIndex = pIdx
		} else if perr != nil {
			p.logger.Warn("per-profile index unavailable; falling back to shared index with post-filter",
				zap.String("profile", profileName), zap.Error(perr))
		}
	}

	// Filter results to only include callable tools
	// from servers the agent can access. Disabled/blocked tools are treated as non-existent
	// for runtime discovery. The auth context is hoisted out of the search and the loop
	// below because it's a per-request value and AuthContextFromContext does a
	// context.Value lookup we don't want to repeat per result.
	authCtx := auth.AuthContextFromContext(ctx)

	// serverDiscoverable applies the agent-scope and profile filters BEFORE
	// classification so an agent never learns a tool exists on a server it
	// cannot access. profileScope filters independently of agent scope
	// (nil = allow all). It delegates to the shared visibility resolver's
	// scope step (mcp_visibility.go) — shared
	// by the search below, the callable-result loop, the quarantined-tool
	// discovery pass, and describe_tool, so they never drift.
	serverDiscoverable := func(serverName string) bool {
		return p.serverInScope(authCtx, profileScope, serverName)
	}

	admitPolicy := func(hit index.Hit) index.Admission {
		if !serverDiscoverable(hit.Server) {
			return index.RejectScope
		}
		if policy == nil {
			return index.Admit
		}
		annotations, found := p.EffectiveAnnotations(hit.Server, hit.Tool)
		intrinsic := profile.IntrinsicTier(annotations, found)
		if admitted, _, _ := policy.Decide(hit.Server, hit.Tool, intrinsic); !admitted {
			return index.RejectPolicy
		}
		return index.Admit
	}

	var results []*config.SearchResult
	var hiddenByPolicy int
	switch {
	case profileName != "":
		results, hiddenByPolicy, err = searchIndex.SearchToolsAdmitted(query, limit, admitPolicy)
	default:
		results, err = searchIndex.SearchToolsScoped(query, limit, serverDiscoverable)
	}
	if err != nil {
		p.logger.Error("Search failed", zap.String("query", query), zap.Error(err))
		p.emitActivityInternalToolCall(ctx, "retrieve_tools", "", "", "", sessionID, requestID, "error", err.Error(), time.Since(startTime).Milliseconds(), args, nil, nil, "")
		return mcp.NewToolResultError(fmt.Sprintf("Search failed: %v", err)), nil
	}

	// Opt-in discovery of locked tools. When false (default) disabled tools are
	// dropped. droppedCount is tallied unconditionally so the zero-result nudge
	// is free on the default path.
	includeDisabled := request.GetBool("include_disabled", false)
	if includeDisabled {
		args["include_disabled"] = true
	}

	var callableResults []*config.SearchResult
	var disabledEntries []contracts.LockedToolEntry
	droppedCount := 0
	for _, result := range results {
		serverName := result.Tool.ServerName
		// The RAW tool name, derived from the index hit exactly once: the index
		// reads identity back from the docID, so a raw
		// "a:erase" on server "a" (canonical "a:a:erase") is gated below as
		// itself rather than as the suffix sibling "erase".
		toolName := config.RawToolName(result.Tool)
		if serverName == "" {
			// Fallback: try to extract from "server:tool" format
			if parts := strings.SplitN(result.Tool.Name, ":", 2); len(parts) == 2 {
				serverName = parts[0]
				toolName = parts[1]
			}
		}

		visible, reason := p.indexedToolVisible(authCtx, profileScope, policy, serverName, toolName)
		if visible {
			callableResults = append(callableResults, result)
			continue
		}

		if reason == visReasonServerNotInScope || reason == visReasonToolPolicyExcluded {
			// Out-of-scope servers and profile-excluded tools are both
			// invisible, never "locked": a profile-hidden tool must not be
			// named or described the way a locked entry would.
			continue
		}

		droppedCount++
		if includeDisabled {
			disabledEntries = append(disabledEntries, contracts.LockedToolEntry{
				Name:        result.Tool.Name,
				Server:      serverName,
				Description: result.Tool.Description,
				Status:      p.classifyDisabledTool(serverName, toolName),
			})
		}
	}
	results = callableResults
	managementResults, managementErr := p.searchManagementTools(ctx, query, limit)
	if managementErr != nil {
		return mcp.NewToolResultError("Management tool search failed"), nil
	}
	results = append(results, managementResults...)

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Tool.Name < results[j].Tool.Name
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	seen := make(map[string]bool, len(callableResults)+len(disabledEntries))
	for _, r := range callableResults {
		seen[r.Tool.Name] = true
	}
	for _, e := range disabledEntries {
		seen[e.Name] = true
	}
	quarantinedMatches := p.collectQuarantinedToolMatches(query, serverDiscoverable, seen, p.serverToolNames)
	// collectQuarantinedToolMatches filters only by server scope, so a tool
	// that is BOTH policy-excluded and quarantined/pending/changed would
	// otherwise be named and counted as a locked entry. The policy post-filter
	// makes such a tool silently invisible, like an in-scope-but-excluded
	// index hit.
	quarantinedMatches = p.filterLockedMatchesByPolicy(quarantinedMatches, policy)
	droppedCount += len(quarantinedMatches)
	if includeDisabled {
		disabledEntries = append(quarantinedMatches, disabledEntries...)
	}

	// Resolve annotations for each result and apply annotation-based filtering
	// before building the MCP tool response. This allows agents to self-restrict discovery.
	//
	// The same pass records WHY each candidate was withheld, so a
	// caller can tell "no such tool" from "withheld by a filter, and here is
	// the remediation". filterDiag stays nil unless a filter was active.
	annotationFilterActive := readOnlyOnly || excludeDestructive || excludeOpenWorld
	var filterDiag *filterDiagnostics
	if annotationFilterActive {
		var annotatedResults []annotatedSearchResult
		for i, result := range results {
			serverName := result.Tool.ServerName
			toolName := result.Tool.Name
			if serverName == "" {
				if parts := strings.SplitN(result.Tool.Name, ":", 2); len(parts) == 2 {
					serverName = parts[0]
					toolName = parts[1]
				}
			}
			var annotations *config.ToolAnnotations
			if serverName != "" {
				if serverName == config.ManagementServerName {
					annotations = result.Tool.Annotations
				} else {
					annotations = p.lookupToolAnnotations(serverName, toolName)
				}
			}
			annotatedResults = append(annotatedResults, annotatedSearchResult{
				serverName:  serverName,
				toolName:    toolName,
				annotations: annotations,
				resultIndex: i,
			})
		}

		filtered, diag := filterByAnnotationsWithDiagnostics(annotatedResults, readOnlyOnly, excludeDestructive, excludeOpenWorld)
		filterDiag = diag
		var filteredResults []*config.SearchResult
		for _, ar := range filtered {
			filteredResults = append(filteredResults, results[ar.resultIndex])
		}
		results = filteredResults
	}

	// Convert results to MCP tool format. Compact mode renders
	// {id, score, sig, desc, lossy} from the shared signature cache. The
	// ranked order of `results` is already final here — the mode selects
	// serialization only.
	entryOpts := toolEntryOpts{includeStats: includeStats}
	// Must be non-nil so zero matches serialize as [] — strict MCP clients
	// crash iterating a null tools array.
	mcpTools := make([]map[string]interface{}, 0, len(results))
	for _, result := range results {
		mcpTools = append(mcpTools, p.buildToolEntry(result, responseMode, entryOpts))
	}

	// Build mode-aware usage instructions
	var usageInstructions string
	switch routingMode {
	case config.RoutingModeCodeExecution:
		usageInstructions = "TOOL CALLING GUIDE: Use the `code_execution` tool to call any discovered tool via JavaScript: " +
			"call_tool(serverName, toolName, args). Example: call_tool('github', 'create_issue', {title: 'Bug fix'}). " +
			"The 'call_with' field indicates each tool's permission tier (read/write/destructive) for your reference. " +
			"Do NOT use call_tool_read, call_tool_write, or call_tool_destructive — they are not available in code execution mode."
	default:
		usageInstructions = "TOOL SELECTION GUIDE: Check the 'call_with' field for each tool, then use the matching tool variant. " +
			"DECISION RULES BY TOOL NAME: " +
			"(1) READ (call_tool_read): search, query, list, get, fetch, find, check, view, read, show, describe, lookup, retrieve, browse, explore, discover, scan, inspect, analyze, examine, validate, verify. DEFAULT choice when unsure. " +
			"(2) WRITE (call_tool_write): create, update, modify, add, set, send, edit, change, write, post, put, patch, insert, upload, submit, assign, configure, enable, register, subscribe, publish, move, copy, rename, merge. " +
			"(3) DESTRUCTIVE (call_tool_destructive): delete, remove, drop, revoke, disable, destroy, purge, reset, clear, unsubscribe, cancel, terminate, close, archive, ban, block, disconnect, kill, wipe, truncate, force, hard. " +
			"INTENT TRACKING: Always provide intent_reason (why you're calling this tool) and intent_data_sensitivity (public/internal/private/unknown) to enable activity auditing."
	}

	response := map[string]interface{}{
		"tools":              mcpTools,
		"query":              query,
		"total":              len(results),
		"usage_instructions": usageInstructions,
	}

	if hasProfile {
		response["hidden_by_profile"] = hiddenByPolicy
		if profileDisclosedTo(profileSource) && policy != nil {
			response["profile"] = profileName
		}
	}

	// Explain the annotation filters only when they actually withheld
	// something; otherwise the key is absent.
	if filterDiag != nil && filterDiag.OmittedTotal >= 1 {
		response["filter_diagnostics"] = filterDiag
	}

	// Compact responses carry one deterministic hint line explaining the lossy
	// marker and the describe_tool second stage. Full mode has no hint key.
	if responseMode == config.ToolResponseModeCompact {
		response["hint"] = compactModeHint
	}

	// Opt-in locked-tool discovery. Callable results above are
	// untouched and listed first; locked entries follow, capped at
	// min(limit,10) so a restrictive config can't blow the token budget. The
	// remediation map is emitted once, keyed ONLY by statuses actually present.
	if includeDisabled && len(disabledEntries) > 0 {
		cap := limit
		if cap > 10 {
			cap = 10
		}
		if len(disabledEntries) > cap {
			disabledEntries = disabledEntries[:cap]
		}
		remediation := map[string]string{}
		for _, e := range disabledEntries {
			if _, ok := remediation[e.Status]; !ok {
				remediation[e.Status] = disabledToolRemediation(e.Status)
			}
		}
		response["disabled"] = disabledEntries
		response["remediation"] = remediation
	}

	// An agent that didn't set the flag still gets nudged when
	// every match was locked. Count only — never the entries — so this stays a
	// few tokens regardless of how many tools are locked.
	if !includeDisabled && len(results) == 0 && droppedCount > 0 {
		response["notice"] = fmt.Sprintf(
			"%d relevant tool(s) exist but are locked; retry with include_disabled:true for details and remediation.",
			droppedCount)
	}

	if p.mainServer != nil && p.mainServer.runtime != nil {
		if sup := p.mainServer.runtime.Supervisor(); sup != nil {
			snapshot := sup.StateView().Snapshot()
			// session_risk reflects only the caller's own authorized servers'
			// tool annotations — otherwise the level (and the lethal-trifecta
			// verdict) leaks the existence and shape of a hidden server's tools.
			risk := analyzeSessionRiskScoped(snapshot, serverDiscoverable)
			response["session_risk"] = buildSessionRiskResponse(risk)
		}
	}

	// Add tool statistics summary if requested
	if includeStats {
		// The usage ranking is computed
		// over the caller's authorized population's APPROVED tools only — a stale usage
		// record for a tool that was removed, hidden, or is still pending
		// review must never resurface. Population membership alone would admit
		// a pending tool (it is indexed); approval alone never checks the tool
		// still exists (mcp_direct_callability.go) — both gates are required.
		// The sort-then-cut happens AFTER filtering (GetToolStatsFiltered), so
		// a used-but-hidden tool can never evict an authorized one from the
		// top-10 window.
		stats, statsErr := p.storage.GetToolStatsFiltered(func(fullToolName string) bool {
			serverName, rawToolName, ok := splitServerTool(fullToolName)
			if !ok {
				return false
			}
			return p.usageStatEligible(authCtx, profileScope, policy, serverName, rawToolName)
		}, 10)
		if statsErr == nil {
			response["usage_summary"] = map[string]interface{}{
				"top_tools": stats,
			}
		}
	}

	// Inject auth identity into activity metadata (separate copy for logging only)
	activityArgs := injectAuthMetadata(ctx, args)

	jsonResult, err := json.Marshal(response)
	if err != nil {
		p.emitActivityInternalToolCall(ctx, "retrieve_tools", "", "", "", sessionID, requestID, "error", err.Error(), time.Since(startTime).Milliseconds(), activityArgs, nil, nil, "")
		return mcp.NewToolResultError(fmt.Sprintf("Failed to serialize results: %v", err)), nil
	}

	// A discovery response is a tool response like any other: enforce
	// tool_response_limit and park the full payload behind a read_cache key.
	// The record path is PINNED to "tools" rather than inferred: the banner
	// promises tool entries, and the inference heuristic prefers the array with
	// the most elements — which here can be the "disabled" list (it
	// can outnumber the callable results) or, with ≤2 results, an array nested
	// inside a tool's input schema. Pinning is what makes paginableUnits =
	// len(mcpTools) the right guard too: it counts units of the same array the
	// key pages, so the single-record short-circuit (0 or 1 entries: nothing to
	// subdivide, text flows through unchanged) applies to the right array.
	// `args` (not activityArgs) is the cache-key derivation input.
	text := string(jsonResult)
	var wasTruncated bool
	if routingMode != config.RoutingModeCodeExecution {
		// The code-execution surface does not register read_cache (see
		// buildCodeExecModeTools), so a banner there would point the agent
		// at an unavailable tool — the same reasoning that pins it to full mode.
		text, wasTruncated = maybeTruncateAndCacheTextPinned(
			text,
			"retrieve_tools",
			args,
			len(mcpTools),
			"tools",
			p.truncator,
			p.cacheStoreAs(producer),
			p.logger,
		)
	}
	if wasTruncated {
		p.logger.Info("retrieve_tools output exceeded the response limit and was truncated",
			zap.String("query", query),
			zap.Int("tools", len(mcpTools)),
			zap.Int("full_bytes", len(jsonResult)),
		)
	}

	// Emit success event with args and response. The FULL response
	// goes to the activity log — truncation only shapes what the agent sees
	// (same order handleReadCache uses). Which is exactly why wasTruncated has
	// to travel with it: the stored response is not what the agent paid for, and
	// nothing downstream can tell the two apart without the flag.
	p.emitActivityInternalToolCallTruncated(ctx, "retrieve_tools", "", "", "", sessionID, requestID, "success", "", time.Since(startTime).Milliseconds(), activityArgs, response, nil, "", wasTruncated)

	return mcp.NewToolResultText(text), nil
}

// handleCallToolRead implements call_tool_read.
func (p *MCPProxyServer) handleCallToolRead(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return p.handleCallToolVariant(ctx, request, contracts.ToolVariantRead)
}

// handleCallToolWrite implements call_tool_write.
func (p *MCPProxyServer) handleCallToolWrite(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return p.handleCallToolVariant(ctx, request, contracts.ToolVariantWrite)
}

// handleCallToolDestructive implements call_tool_destructive.
func (p *MCPProxyServer) handleCallToolDestructive(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return p.handleCallToolVariant(ctx, request, contracts.ToolVariantDestructive)
}

// handleCallToolVariant is the common handler for all call_tool_* variants.
func (p *MCPProxyServer) handleCallToolVariant(ctx context.Context, request mcp.CallToolRequest, toolVariant string) (callResult *mcp.CallToolResult, callErr error) {
	// Track start time for internal tool call logging
	internalStartTime := time.Now()

	// Panic recovery uses named return values so a recovered panic never
	// returns (nil, nil), which mcp-go would dereference as a nil
	// *CallToolResult.
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("Recovered from panic in handleCallToolVariant",
				zap.Any("panic", r),
				zap.String("tool_variant", toolVariant),
				zap.Any("request", request),
				zap.Stack("stacktrace"))
			callResult = mcp.NewToolResultError(fmt.Sprintf("Internal proxy error: recovered from panic: %v", r))
			callErr = nil
		}
	}()

	toolName, err := request.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Missing required parameter 'name': %v", err)), nil
	}
	// Whitespace is never significant in a canonical id; case is (every gate
	// below keys on the exact name), so padding is trimmed and nothing else.
	toolName = strings.TrimSpace(toolName)

	// Parse server and tool name early for activity logging, through the same
	// splitServerTool the describe_tool path uses: it trims EACH segment, so
	// "github: create_issue" resolves exactly like "github:create_issue"
	// instead of missing every exact-name gate below and being dispatched
	// upstream padded. The canonical id is rebuilt from the trimmed segments
	// so the validator cache key and every message see the normalized form.
	var serverName, actualToolName string
	if parsedServer, parsedTool, ok := splitServerTool(toolName); ok {
		serverName, actualToolName = parsedServer, parsedTool
		toolName = parsedServer + ":" + parsedTool
	}

	if serverName == config.ManagementServerName {
		intent, err := p.extractIntent(request)
		if err != nil {
			return mcp.NewToolResultError("Invalid intent: " + err.Error()), nil
		}
		if _, refusal := p.validateIntentForVariant(intent, toolVariant); refusal != nil {
			return refusal, nil
		}
		args, argsErr := parseToolArguments(request.GetArguments())
		if argsErr != nil {
			return mcp.NewToolResultError(argsErr.Error()), nil
		}
		return p.callManagementTool(ctx, actualToolName, args, toolVariant)
	}

	// Helper to get session ID for activity logging
	getSessionID := func() string {
		if sess := mcpserver.ClientSessionFromContext(ctx); sess != nil {
			return sess.SessionID()
		}
		return ""
	}

	// Mint the activity request id HERE, above the first policy gate, not at
	// the point the upstream call is prepared. Intent extraction and intent
	// validation below can both block, and a block emitted before the id exists
	// is an activity record nothing can be correlated with. serverName and
	// actualToolName may still be empty for a malformed tool name; the
	// timestamp alone keeps the id unique.
	requestID := mintActivityRequestID(serverName, actualToolName)

	var args map[string]interface{}
	var argsErrMsg string
	if arguments, ok := request.Params.Arguments.(map[string]interface{}); ok {
		if value, exists := arguments["args"]; exists && value != nil {
			var valid bool
			args, valid = value.(map[string]interface{})
			if !valid {
				argsErrMsg = "args must be a JSON object"
			}
		}
		for key := range arguments {
			switch key {
			case "name", "args", "intent_data_sensitivity", "intent_reason":
			default:
				argsErrMsg = fmt.Sprintf("unknown parameter %q; put tool arguments inside args", key)
			}
		}
	}

	_, _, profileIdx := p.resolveActiveProfileWithIndex(ctx)
	ctx, profileResolution := p.resolveForDispatch(ctx, profileIdx)
	// Dispatch uses this single resolution, including the authoritative
	// empty-base case: any other selection when it resolves no profile would
	// bypass switchable_to admission for client credentials.
	profileSlug := profileResolution.Name
	profileScope := profileResolution.Scope

	// Extract intent (optional - operation_type is inferred from tool variant)
	intent, err := p.extractIntent(request)
	if err != nil {
		errMsg := fmt.Sprintf("Invalid intent parameter: %v", err)
		// Record activity error for invalid intent (use "unknown" if server name not parsed yet)
		logServer := serverName
		if logServer == "" {
			logServer = "unknown"
		}
		logTool := actualToolName
		if logTool == "" {
			logTool = toolName
		}
		p.emitActivityPolicyDecision(ctx, logServer, logTool, getSessionID(), requestID, "blocked", errMsg)
		return mcp.NewToolResultError(errMsg), nil
	}

	// Validate intent and infer operation_type from tool variant
	intent, errResult := p.validateIntentForVariant(intent, toolVariant)
	if errResult != nil {
		// Record activity error for intent validation failure
		logServer := serverName
		if logServer == "" {
			logServer = "unknown"
		}
		logTool := actualToolName
		if logTool == "" {
			logTool = toolName
		}
		p.emitActivityPolicyDecision(ctx, logServer, logTool, getSessionID(), requestID, "blocked", "Intent validation failed")
		return errResult, nil
	}

	if argsErrMsg != "" {
		return mcp.NewToolResultError(argsErrMsg), nil
	}

	// Handle upstream tools via upstream manager (requires server:tool format)
	if !strings.Contains(toolName, ":") {
		return mcp.NewToolResultError(fmt.Sprintf("Invalid tool name format: %s (expected server:tool)", toolName)), nil
	}

	// Validate tool name was parsed correctly
	if serverName == "" || actualToolName == "" {
		return mcp.NewToolResultError(fmt.Sprintf("Invalid tool name format: %s", toolName)), nil
	}

	producer := p.cacheAuthorizationWith(ctx, profileSlug, profileScope, profileIdx)
	// Effective scope is profile ∩ token, checked in ONE evaluation with ONE
	// refusal body, so a server inside the pin but outside the token (or the
	// reverse) is indistinguishable from a nonexistent one.
	authCtx := auth.AuthContextFromContext(ctx)
	if !p.serverInScope(authCtx, profileScope, serverName) {
		errMsg := fmt.Sprintf("Server '%s' is not in scope for this client token", serverName)
		p.emitActivityPolicyDecision(ctx, serverName, actualToolName, getSessionID(), requestID, "blocked", errMsg)
		return mcp.NewToolResultError(errMsg), nil
	}

	// ONE evaluation of the shared policy gates — server quarantine,
	// tool-level quarantine and callability — through the same classifier
	// the preflight evaluator uses, so a tool dispatch refuses can never
	// preflight as `ready`. It is a pure read, taken HERE, above every gate
	// that consumes it, and it carries the target's registration identity
	// resolved against the SAME persisted record its quarantined / disabled
	// verdicts are decided from: the identity gate, the target-tier gate, the
	// intent validation and the server-level verdicts below all answer from
	// this one read, so they can never come from two different snapshots. It
	// is also the dispatch's ONLY persisted read: the live certification
	// further down hydrates from gate.serverConfig rather than re-reading
	// storage, so an operator's write that lands after this point owns the
	// NEXT dispatch, not a second verdict for this one.
	// Found=false means the proxy holds no metadata for the pair (server
	// unknown, tool undiscovered, or no runtime). The pair was split from the
	// canonical id above, so it is read EXACTLY: a raw name that starts with
	// the server's own prefix must not be normalized again. The response
	// order below is quarantine → approval lock → generic block.
	gate := p.evaluateExactToolGate(serverName, actualToolName)
	identity := gate.identity
	annotations, annotationsFound := identity.Annotations, identity.Found

	// Client token scope restrictions. The server-scope gate above (effective
	// scope = profile ∩ token) already ran before the identity gate so a scoped caller learns nothing about a
	// server outside its scope from the shape of the refusal. The variant
	// permission gate runs after tool identity and profile policy below, per
	// the refusal precedence contract.

	if identity.Unresolved() {
		errMsg := unresolvedToolIdentityMessage(serverName, actualToolName, identity.DiscoveryDone)
		p.logger.Debug("handleCallToolVariant: refusing unresolved tool identity",
			zap.String("server_name", serverName),
			zap.String("tool_name", actualToolName))
		// Logged as tool_not_callable, not token_permission: the refusal is
		// about the TOOL's identity, not the caller's grant, and the
		// token_permission bucket would misreport
		// an identity failure as a scope problem.
		p.emitActivityPolicyDecision(ctx, serverName, actualToolName, getSessionID(), requestID, "blocked", errMsg)
		return mcp.NewToolResultError(errMsg), nil
	}

	// Discovery and dispatch share the same compiled policy
	// and effective annotation identity. This gate follows server-scope and
	// identity resolution, but precedes token permissions, global gates,
	// server state, and tool approval. A profile denial therefore never
	// reaches the upstream. It names the profile only to a caller whose
	// effective profile is its own (pin, binding, url, session);
	// an anonymous caller keeps the non-disclosing text.
	if policy := profileResolution.Policy; policy != nil {
		intrinsic := profile.IntrinsicTier(annotations, annotationsFound)
		admitted, reason, tier := policy.Decide(serverName, actualToolName, intrinsic)
		if !admitted && reason != profile.ReasonServerNotInProfile {
			errMsg, blockReason := profileToolPolicyRefusal(reason, tier, serverName, actualToolName, profileRefusalSubject(profileResolution))
			p.emitActivityPolicyDecisionWithBlockReason(ctx, serverName, actualToolName, getSessionID(), requestID, "blocked", errMsg, string(blockReason))
			return mcp.NewToolResultError(errMsg), nil
		}
	}

	p.logger.Debug("handleCallToolVariant: processing request",
		zap.String("tool_variant", toolVariant),
		zap.String("tool_name", toolName),
		zap.String("server_name", serverName),
		zap.String("intent_operation", intent.OperationType))

	// Determine content trust level based on openWorldHint annotation
	contentTrust := contracts.ContentTrustForTool(annotations)
	if contentTrust == contracts.ContentTrustUntrusted {
		p.logger.Debug("handleCallToolVariant: open-world tool detected, tagging as untrusted",
			zap.String("server_name", serverName),
			zap.String("tool_name", actualToolName),
			zap.String("content_trust", contentTrust))
	}

	// Validate intent against server annotations (unless call_tool_destructive which is most permissive)
	if errResult := p.validateIntentAgainstServer(intent, toolVariant, serverName, actualToolName, annotations); errResult != nil {
		// Record activity error for server annotation mismatch
		reason := fmt.Sprintf("Intent rejected: tool variant '%s' conflicts with server annotations for %s:%s", toolVariant, serverName, actualToolName)
		p.emitActivityPolicyDecision(ctx, serverName, actualToolName, getSessionID(), requestID, "blocked", reason)
		return errResult, nil
	}

	// Extract session information from context early (needed for activity events, including early failures)
	var sessionID, clientName, clientVersion string
	if sess := mcpserver.ClientSessionFromContext(ctx); sess != nil {
		sessionID = sess.SessionID()
		if sessInfo := p.sessionStore.GetSession(sessionID); sessInfo != nil {
			clientName = sessInfo.ClientName
			clientVersion = sessInfo.ClientVersion
		}
	}

	// Determine activity source from context (CLI/API calls set this, MCP calls have session)
	activitySource := "mcp"
	if reqSource := reqcontext.GetRequestSource(ctx); reqSource != reqcontext.SourceUnknown {
		switch reqSource {
		case reqcontext.SourceRESTAPI:
			activitySource = "api"
		default:
			activitySource = "mcp"
		}
	}

	// requestID was minted above the first policy gate; every activity event
	// below shares that one value. profileSlug is tagged on every activity
	// record (success AND error paths).

	// Inject auth identity into a separate copy for activity logging only.
	// The original args must not be mutated — upstream servers reject unknown fields
	// (e.g. FastMCP's Pydantic validate_call).
	activityArgs := injectAuthMetadata(ctx, args)

	// The shared policy gates, from the ONE evaluation taken above the
	// identity gate: quarantine → approval lock → generic block.
	switch gate.lockStatus {
	case storage.ToolApprovalStatusPending:
		p.logger.Debug("handleCallToolVariant: tool is pending approval (quarantined)",
			zap.String("server_name", serverName),
			zap.String("tool_name", actualToolName))

		p.emitActivityPolicyDecision(ctx, serverName, actualToolName, getSessionID(), requestID, "blocked",
			"Tool is pending approval (new unapproved tool)")

		return toolPendingApprovalResult(serverName, actualToolName, gate.approval), nil
	case storage.ToolApprovalStatusChanged:
		p.logger.Debug("handleCallToolVariant: tool description changed (quarantined)",
			zap.String("server_name", serverName),
			zap.String("tool_name", actualToolName))

		p.emitActivityPolicyDecision(ctx, serverName, actualToolName, getSessionID(), requestID, "blocked",
			"Tool description/schema changed since last approval")

		return toolChangedApprovalResult(serverName, actualToolName, gate.approval), nil
	}

	if !gate.callable() {
		errMsg := gate.blockedMessage()
		p.emitActivityPolicyDecision(ctx, serverName, actualToolName, sessionID, requestID, "blocked", errMsg)
		return mcp.NewToolResultError(errMsg), nil
	}

	// Pre-dispatch argument validation — after the target is resolved and
	// callable, before any dispatch work. The schema source is the tool's
	// indexed ParamsJSON (the same source signatures render from), memoized by
	// the tool hash. Fail-open: tools absent from the index or with
	// uncompilable schemas dispatch unvalidated. The self-healing error embeds
	// the FULL stored schema + hint regardless of tool_response_mode, and the
	// upstream is never called on a validation failure.
	if meta := p.lookupIndexedTool(serverName, actualToolName); meta != nil {
		if ok, verr, _ := p.inputValidator.validateArgs(toolName, meta.Hash, meta.ParamsJSON, args); !ok {
			detail := oneLineValidationDetail(verr)
			p.logger.Debug("handleCallToolVariant: pre-dispatch argument validation failed",
				zap.String("tool_name", toolName),
				zap.String("tool_variant", toolVariant),
				zap.String("detail", detail))
			var intentMap map[string]interface{}
			if intent != nil {
				intentMap = intent.ToMap()
			}
			errMsg := fmt.Sprintf("invalid arguments for %s: %s", toolName, detail)

			p.emitActivityToolCallStarted(ctx, serverName, actualToolName, sessionID, requestID, activitySource, activityArgs)
			p.emitActivityToolCallCompleted(ctx, serverName, actualToolName, sessionID, requestID, activitySource, "error", errMsg, 0, activityArgs, errMsg, false, toolVariant, intentMap, contentTrust, profileSlug, 0, 0, "")
			return invalidParamsErrorResult(toolName, meta.ParamsJSON, detail), nil
		}
	}

	// Check connection status before attempting tool call to prevent hanging
	var certified toolIdentity
	if client, exists := p.upstreamManager.GetClient(serverName); exists {
		if !client.IsConnected() {
			state := client.GetState()
			errMsg := ""
			if client.IsConnecting() {
				errMsg = fmt.Sprintf("Server '%s' is currently connecting - please wait for connection to complete (state: %s)", serverName, state.String())
			} else {
				errMsg = fmt.Sprintf("Server '%s' is not connected (state: %s) - use 'manage_servers' tool to check server configuration", serverName, state.String())
			}
			// Log the early failure to activity
			var intentMap map[string]interface{}
			if intent != nil {
				intentMap = intent.ToMap()
			}

			p.emitActivityToolCallStarted(ctx, serverName, actualToolName, sessionID, requestID, activitySource, activityArgs)
			p.emitActivityToolCallCompleted(ctx, serverName, actualToolName, sessionID, requestID, activitySource, "error", errMsg, 0, activityArgs, errMsg, false, toolVariant, intentMap, contentTrust, profileSlug, 0, 0, "")
			return mcp.NewToolResultError(errMsg), nil
		}
		// The identity gate above may have deferred because the StateView
		// read the server as not connected while the live client IS
		// connected (the connected event is in flight or was dropped). So
		// re-read the snapshot and refuse anything short of a CERTIFIED name
		// here, with zero upstream calls — an unlisted name, and equally a
		// name the not-hydrated snapshot still lists from the previous
		// generation's retained set. The tier gate already ran with the
		// destructive fallback (the strictest tier), so no re-check is
		// needed for scoped callers. The re-read also runs for a HYDRATED
		// snapshot: the live client is the authority on which connection the
		// snapshot's stamp describes, so a name certified by a previous
		// connection is refused with the discovery-window body. The re-read
		// covers the StateView and the live client only: the persisted
		// record is the ONE the gate captured above, so an operator's write
		// since then owns the next dispatch.
		live, errMsg, refuse := p.liveIdentityRefusal(gate.serverConfig, serverName, actualToolName, identity, client)
		if refuse {
			p.logger.Debug("handleCallToolVariant: refusing unresolved tool identity (live client connected, snapshot stale)",
				zap.String("server_name", serverName),
				zap.String("tool_name", actualToolName))
			p.emitActivityPolicyDecision(ctx, serverName, actualToolName, sessionID, requestID, "blocked", errMsg)
			return mcp.NewToolResultError(errMsg), nil
		}
		// The dispatch below is pinned to the generation this check
		// certified: the managed client re-checks it after
		// the admission queue and immediately before the transport.
		certified = live
	} else {
		// Get list of available servers for helpful error message, filtered
		// through the SAME effective-scope predicate (serverInScope = profile ∩
		// token) the dispatch gates above evaluated — an unauthorized server's
		// mere existence must not leak into the hint.
		availableServers := make([]string, 0)
		for _, name := range p.upstreamManager.GetAllServerNames() {
			if p.serverInScope(authCtx, profileScope, name) {
				availableServers = append(availableServers, name)
			}
		}
		serverList := strings.Join(availableServers, ", ")
		if len(availableServers) == 0 {
			serverList = "(no servers configured)"
		}

		p.logger.Error("handleCallToolVariant: no client found for server",
			zap.String("server_name", serverName),
			zap.Strings("available_servers", availableServers))
		errMsg := fmt.Sprintf(
			"No client found for server: %s. Available servers: [%s]. "+
				"IMPORTANT: Use 'retrieve_tools' first to discover tools and their exact server:tool names, "+
				"or use 'manage_servers operation=\"list\"' to see all configured servers.",
			serverName, serverList)
		// Log the early failure to activity
		var intentMap map[string]interface{}
		if intent != nil {
			intentMap = intent.ToMap()
		}

		p.emitActivityToolCallStarted(ctx, serverName, actualToolName, sessionID, requestID, activitySource, activityArgs)
		p.emitActivityToolCallCompleted(ctx, serverName, actualToolName, sessionID, requestID, activitySource, "error", errMsg, 0, activityArgs, errMsg, false, toolVariant, intentMap, contentTrust, profileSlug, 0, 0, "")
		return mcp.NewToolResultError(errMsg), nil
	}

	// Emit activity started event with determined source
	p.emitActivityToolCallStarted(ctx, serverName, actualToolName, sessionID, requestID, activitySource, activityArgs)

	startTime := time.Now()
	// The history write is bound to the incarnation the gate read together
	// with the record the history is built from, so a call that outlives a
	// removal — or a remove and re-add that lands after the gate — leaves no
	// history for the removed server or its replacement.
	callInc := gate.incarnation
	result, err := p.dispatchOnEpoch(ctx, certified, toolName, args)

	duration := time.Since(startTime)

	p.logger.Debug("handleCallToolVariant: upstream call completed",
		zap.String("tool_name", toolName),
		zap.String("tool_variant", toolVariant),
		zap.Duration("duration", duration),
		zap.Error(err))

	// Derive server ID safely (the gate's server record is nil when the storage
	// lookup failed)
	var serverID string
	if gate.serverConfig != nil {
		serverID = storage.GenerateServerID(gate.serverConfig)
	}

	// Record tool call for history (even if error)
	toolCallRecord := &storage.ToolCallRecord{
		ID:         mintCorrelationID(actualToolName),
		ServerID:   serverID,
		ServerName: serverName,
		ToolName:   actualToolName,
		Arguments:  activityArgs,
		Duration:   int64(duration),
		Timestamp:  startTime,
		RequestID:  requestID,

		ExecutionType:    "direct",
		MCPSessionID:     sessionID,
		MCPClientName:    clientName,
		MCPClientVersion: clientVersion,
		Annotations:      annotations,
		// Intent metadata is passed to the activity system via emitActivityToolCallCompleted.
	}

	if err != nil {
		// The connection generation moved
		// between the identity check above and the transport, so the
		// managed client refused to send. Nothing reached the upstream; the
		// answer is the same discovery-window refusal the pre-dispatch gate
		// gives a name whose generation is not certified, in the same
		// telemetry bucket, with the started record closed as an error so
		// the activity funnel sees the call end.
		if errors.Is(err, managed.ErrConnectionGenerationChanged) {
			errMsg := unresolvedToolIdentityMessage(serverName, actualToolName, false)
			toolCallRecord.Error = errMsg
			if storeErr := p.recordToolCallAt(toolCallRecord, callInc); storeErr != nil {
				p.logger.Warn("Failed to record refused tool call", zap.Error(storeErr))
			}
			p.logger.Debug("handleCallToolVariant: refusing dispatch, connection generation changed since the identity was certified",
				zap.String("server_name", serverName),
				zap.String("tool_name", actualToolName))
			var intentMap map[string]interface{}
			if intent != nil {
				intentMap = intent.ToMap()
			}

			p.emitActivityToolCallCompleted(ctx, serverName, actualToolName, sessionID, requestID, activitySource, "error", errMsg, duration.Milliseconds(), activityArgs, "", false, toolVariant, intentMap, contentTrust, profileSlug, 0, 0, "")
			p.emitActivityPolicyDecision(ctx, serverName, actualToolName, sessionID, requestID, "blocked", errMsg)
			return mcp.NewToolResultError(errMsg), nil
		}

		// Record error in tool call history
		toolCallRecord.Error = err.Error()

		// Log upstream errors for debugging server stability
		p.logger.Debug("Upstream tool call failed",
			zap.String("server", serverName),
			zap.String("tool", actualToolName),
			zap.String("tool_variant", toolVariant),
			zap.Error(err),
			zap.String("error_type", "upstream_failure"))

		// Store error tool call
		if storeErr := p.recordToolCallAt(toolCallRecord, callInc); storeErr != nil {
			p.logger.Warn("Failed to record failed tool call", zap.Error(storeErr))
		}

		// Update session call count.
		if sessionID != "" {
			p.markSessionWorked(ctx, sessionID)
			p.sessionStore.UpdateSessionStats(sessionID)
		}

		// Emit activity completed event for error (with intent metadata)
		var intentMap map[string]interface{}
		if intent != nil {
			intentMap = intent.ToMap()
		}

		p.emitActivityToolCallCompleted(ctx, serverName, actualToolName, sessionID, requestID, activitySource, "error", err.Error(), duration.Milliseconds(), activityArgs, "", false, toolVariant, intentMap, contentTrust, profileSlug, 0, 0, "")

		// Emit internal tool call event for error
		internalToolName := "call_tool_" + intent.OperationType // e.g., "call_tool_read"
		p.emitActivityInternalToolCall(ctx, internalToolName, serverName, actualToolName, toolVariant, sessionID, requestID, "error", err.Error(), time.Since(internalStartTime).Milliseconds(), activityArgs, nil, intentMap, "")

		return p.createDetailedErrorResponse(err, serverName, actualToolName), nil
	}

	activityStatus, activityErrMsg := activityStatusForResult(result)

	// Record the response. An isError answer is still a response — it is stored
	// as one, with the upstream's explanation mirrored into Error so the tool
	// call history agrees with the activity log.
	toolCallRecord.Response = result
	toolCallRecord.Error = activityErrMsg

	// Increment usage stats
	if err := p.storage.IncrementToolUsage(toolName); err != nil {
		p.logger.Warn("Failed to update tool stats", zap.String("tool_name", toolName), zap.Error(err))
	}

	// Measure raw sizes before truncation.
	activityResponseBytes := rawByteSize(result)
	activityRequestBytes := rawByteSize(activityArgs)

	forwarded, response, wasTruncated := forwardContentResult(result, p.truncator, p.cacheStoreAs(producer), p.logger, toolName, args)

	// Output-schema validation. Strict mode blocks a violating result
	// (returns an error); warn mode forwards unchanged after recording a
	// policy_decision. No-op when disabled / no schema / error result.
	if blockResult := p.applyOutputValidation(ctx, serverName, actualToolName, requestID, forwarded); blockResult != nil {
		return blockResult, nil
	}

	response = forwardedText(forwarded, response)

	// Store successful tool call in history
	if err := p.recordToolCallAt(toolCallRecord, callInc); err != nil {
		p.logger.Warn("Failed to record successful tool call", zap.Error(err))
	}

	// Update session call count.
	if sessionID != "" {
		p.markSessionWorked(ctx, sessionID)
		p.sessionStore.UpdateSessionStats(sessionID)
	}

	// Emit activity completed event for success (with intent metadata)
	responseTruncated := wasTruncated
	var intentMap map[string]interface{}
	if intent != nil {
		intentMap = intent.ToMap()
	}
	p.emitActivityToolCallCompleted(ctx, serverName, actualToolName, sessionID, requestID, activitySource, activityStatus, activityErrMsg, duration.Milliseconds(), activityArgs, response, responseTruncated, toolVariant, intentMap, contentTrust, profileSlug, activityRequestBytes, activityResponseBytes, "")

	// Emit internal tool call event. It carries the SAME classification as the
	// tool_call record above — the two describe one dispatch.
	internalToolName := "call_tool_" + intent.OperationType // e.g., "call_tool_read"
	p.emitActivityInternalToolCall(ctx, internalToolName, serverName, actualToolName, toolVariant, sessionID, requestID, activityStatus, activityErrMsg, time.Since(internalStartTime).Milliseconds(), activityArgs, result, intentMap, "")

	return forwarded, nil
}

func (p *MCPProxyServer) toolCallIncarnation(serverName string) int64 {
	if p.mainServer != nil && p.mainServer.runtime != nil {
		if inc, ok := p.mainServer.runtime.ServerIncarnation(serverName); ok {
			return inc
		}
	}
	return 0
}

// recordToolCallAt records a call's history only while its server still has
// the incarnation the call started on.
func (p *MCPProxyServer) recordToolCallAt(rec *storage.ToolCallRecord, inc int64) error {
	written, err := p.storage.RecordToolCallIfIncarnation(rec, inc)
	if err == nil && !written {
		p.logger.Debug("Tool call history dropped: the server was removed or replaced during the call",
			zap.String("server", rec.ServerName), zap.String("tool", rec.ToolName))
	}
	return err
}

// handleReadCache implements the read_cache functionality
func (p *MCPProxyServer) handleReadCache(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	startTime := time.Now()

	// Extract session info for activity logging
	var sessionID string
	if sess := mcpserver.ClientSessionFromContext(ctx); sess != nil {
		sessionID = sess.SessionID()
	}
	requestID := mintCorrelationID("read_cache")

	key, err := request.RequireString("key")
	if err != nil {
		p.emitActivityInternalToolCall(ctx, "read_cache", "", "", "", sessionID, requestID, "error", err.Error(), time.Since(startTime).Milliseconds(), nil, nil, nil, "")
		return mcp.NewToolResultError(fmt.Sprintf("Missing required parameter 'key': %v", err)), nil
	}

	// Get optional parameters
	offset := int(request.GetFloat("offset", 0))
	limit := int(request.GetFloat("limit", 50))

	// Build arguments map for activity logging
	args := map[string]interface{}{
		"key":    key,
		"offset": offset,
		"limit":  limit,
	}
	// Auth identity on the activity record only (never in the
	// cache-key derivation input, which stays `args`).
	activityArgs := injectAuthMetadata(ctx, args)

	// Validate parameters
	if offset < 0 {
		p.emitActivityInternalToolCall(ctx, "read_cache", "", "", "", sessionID, requestID, "error", "Offset must be non-negative", time.Since(startTime).Milliseconds(), activityArgs, nil, nil, "")
		return mcp.NewToolResultError("Offset must be non-negative"), nil
	}
	if limit <= 0 || limit > 1000 {
		p.emitActivityInternalToolCall(ctx, "read_cache", "", "", "", sessionID, requestID, "error", "Limit must be between 1 and 1000", time.Since(startTime).Milliseconds(), activityArgs, nil, nil, "")
		return mcp.NewToolResultError("Limit must be between 1 and 1000"), nil
	}

	// Retrieve cached data. The read is gated on the authorization the entry
	// was produced under: a cache key is a hash, not a
	// credential, and the MCP session is shared by whichever tokens present
	// on it, so a narrower token must not page a payload a broader one
	// generated. The gate runs here, on every page.
	reader := p.cacheAuthorization(ctx)
	response, err := p.cacheManager.GetRecordsAs(key, offset, limit, reader)
	if err != nil {
		// The activity record keeps the real reason; the body below may not.
		p.emitActivityInternalToolCall(ctx, "read_cache", "", "", "", sessionID, requestID, "error", err.Error(), time.Since(startTime).Milliseconds(), activityArgs, nil, nil, "")
		return readCacheRefusal(err, reader), nil
	}

	// Serialize response
	jsonResult, err := json.Marshal(response)
	if err != nil {
		p.emitActivityInternalToolCall(ctx, "read_cache", "", "", "", sessionID, requestID, "error", err.Error(), time.Since(startTime).Milliseconds(), activityArgs, nil, nil, "")
		return mcp.NewToolResultError(fmt.Sprintf("Failed to serialize response: %v", err)), nil
	}

	// Apply the same truncate-and-cache contract to read_cache output itself so
	// oversized pagination responses don't quietly blow past the tool-response
	// limit. The paginableUnits guard (len(response.Records)) prevents an
	// infinite-recursion loop when a single record is bigger than the limit —
	// in that case there's nothing this layer can subdivide, so the oversize
	// text flows through unchanged. p.logger receives a zap.Warn if the cache
	// write fails so the resulting "cache key not found" is diagnosable.
	//
	// A re-cached page is stamped with the PARENT entry's snapshot, not the
	// redeemer's (monotone recursive provenance). Because
	// the child's snapshot equals the parent's, the child's readers are
	// exactly the parent's readers: a child never becomes redeemable by a
	// caller the parent refused, nor unreadable to the producer whose payload
	// it continues. (Under caller-kind-first ordering the redeemer is not
	// necessarily broader in reach — a session-profiled administrator passes
	// the gate — which is why the parent's snapshot, not "the narrower of the
	// two", is the rule.)
	text, reTruncated := maybeTruncateAndCacheText(
		string(jsonResult),
		"read_cache",
		args,
		len(response.Records),
		p.truncator,
		p.cacheStoreAs(childPageProducer(response, reader)),
		p.logger,
	)

	if reTruncated {
		p.logger.Info("read_cache output exceeded the response limit and was truncated",
			zap.String("requested_key", key),
			zap.Int("offset", offset),
			zap.Int("limit", limit),
			zap.Int("page_records", len(response.Records)),
			zap.Int("full_bytes", len(jsonResult)),
		)
	}

	// Emit success event with args and response
	p.emitActivityInternalToolCall(ctx, "read_cache", "", "", "", sessionID, requestID, "success", "", time.Since(startTime).Milliseconds(), activityArgs, response, nil, "")

	return mcp.NewToolResultText(text), nil
}

// classifyUpstreamInvalidParams best-effort-classifies an upstream error as
// an argument-validation failure. Errors qualify only on narrow
// schema-validation phrasing (invalidParamsMessageRe)
// AND with no transport/auth/timeout/HTTP-status smell (invalidParamsDenyRe)
// — e.g. "401 Unauthorized: invalid parameters" is an auth failure, not an
// argument bug. Returns the one-line detail for the self-healing error body.
func classifyUpstreamInvalidParams(err error) (detail string, ok bool) {
	msg := err.Error()
	if invalidParamsMessageRe.MatchString(msg) && !invalidParamsDenyRe.MatchString(msg) {
		return msg, true
	}
	return "", false
}

// createDetailedErrorResponse creates an enhanced error response with HTTP and troubleshooting context
func (p *MCPProxyServer) createDetailedErrorResponse(err error, serverName, toolName string) *mcp.CallToolResult {
	// Upstream invalid-params failures get the same self-healing error the
	// pre-dispatch validator renders — full stored schema + hint — when a
	// schema source exists in the index. Without a stored schema the generic
	// shapes below apply (a self-healing error without a schema would be an
	// empty promise).
	if detail, isInvalidParams := classifyUpstreamInvalidParams(err); isInvalidParams {
		if meta := p.lookupIndexedTool(serverName, toolName); meta != nil && meta.ParamsJSON != "" {
			return invalidParamsErrorResult(serverName+":"+toolName, meta.ParamsJSON, detail)
		}
	}

	// Extract status codes and helpful info from error message for enhanced responses
	errStr := err.Error()
	if strings.Contains(errStr, "status code") || strings.Contains(errStr, "HTTP") {
		// Try to extract HTTP status code for troubleshooting advice
		statusCode := p.extractStatusCodeFromError(errStr)

		errorDetails := map[string]interface{}{
			"error":       errStr,
			"server_name": serverName,
			"tool_name":   toolName,
		}

		if statusCode > 0 {
			errorDetails["http_status"] = statusCode
			errorDetails["troubleshooting"] = p.generateTroubleshootingAdvice(statusCode, errStr)
		}

		jsonResponse, _ := json.Marshal(errorDetails)
		return mcp.NewToolResultError(string(jsonResponse))
	}

	// Fallback to enhanced error message
	errorDetails := map[string]interface{}{
		"error":           errStr,
		"server_name":     serverName,
		"tool_name":       toolName,
		"troubleshooting": "Check server configuration, connectivity, and authentication credentials",
	}

	jsonResponse, _ := json.Marshal(errorDetails)
	return mcp.NewToolResultError(string(jsonResponse))
}

// extractStatusCodeFromError attempts to extract HTTP status code from error message
func (p *MCPProxyServer) extractStatusCodeFromError(errStr string) int {
	// Common patterns for status codes in error messages
	patterns := []string{
		`status code (\d+)`,
		`HTTP (\d+)`,
		`(\d+) [A-Za-z\s]+$`, // "400 Bad Request" pattern
	}

	for _, pattern := range patterns {
		if matches := regexp.MustCompile(pattern).FindStringSubmatch(errStr); len(matches) > 1 {
			if code, err := strconv.Atoi(matches[1]); err == nil {
				return code
			}
		}
	}

	return 0
}

// generateTroubleshootingAdvice provides specific troubleshooting advice based on HTTP status codes and error content
func (p *MCPProxyServer) generateTroubleshootingAdvice(statusCode int, errorBody string) string {
	switch statusCode {
	case 400:
		if strings.Contains(strings.ToLower(errorBody), "api key") || strings.Contains(strings.ToLower(errorBody), "key") {
			return "Check API key configuration. Ensure the API key is correctly set in server environment variables or configuration."
		}
		if strings.Contains(strings.ToLower(errorBody), "auth") {
			return "Authentication issue. Verify authentication credentials and configuration."
		}
		return "Bad request. Check tool parameters, API endpoint configuration, and request format."

	case 401:
		return "Authentication required. Check API keys, tokens, or authentication credentials in server configuration."

	case 403:
		return "Access forbidden. Verify API key permissions, user authorization, or check if the service requires additional authentication."

	case 404:
		return "Resource not found. Check API endpoint URL, server configuration, or verify the requested resource exists."

	case 429:
		return "Rate limit exceeded. Wait before retrying or check if you need a higher rate limit plan."

	case 500:
		return "Internal server error. The upstream service is experiencing issues. Try again later or contact the service provider."

	case 502, 503, 504:
		return "Service unavailable or timeout. The upstream service may be down or overloaded. Check service status and try again later."

	default:
		if strings.Contains(strings.ToLower(errorBody), "api key") {
			return "API key issue detected. Check environment variables and server configuration for correct API key setup."
		}
		if strings.Contains(strings.ToLower(errorBody), "timeout") {
			return "Request timeout. The server may be slow or overloaded. Check network connectivity and server responsiveness."
		}
		if strings.Contains(strings.ToLower(errorBody), "connection") {
			return "Connection issue. Check network connectivity, server URL, and firewall settings."
		}
		return "Check server configuration, network connectivity, and authentication settings. Review server logs for more details."
	}
}

// extractIntent extracts the IntentDeclaration from MCP request parameters.
// Returns nil if intent is not present (caller should handle missing intent error).
func (p *MCPProxyServer) extractIntent(request mcp.CallToolRequest) (*contracts.IntentDeclaration, error) {
	// Get intent from flat request parameters (intent_data_sensitivity, intent_reason)
	if request.Params.Arguments == nil {
		return nil, nil
	}

	argumentsMap, ok := request.Params.Arguments.(map[string]interface{})
	if !ok {
		return nil, nil
	}

	// Extract flat intent parameters
	dataSensitivity, _ := argumentsMap["intent_data_sensitivity"].(string)
	reason, _ := argumentsMap["intent_reason"].(string)

	// If neither field is provided, return nil (intent is optional)
	if dataSensitivity == "" && reason == "" {
		return nil, nil
	}

	// Build intent from flat parameters
	intent := &contracts.IntentDeclaration{
		DataSensitivity: dataSensitivity,
		Reason:          reason,
	}

	return intent, nil
}

// validateIntentForVariant validates intent for a specific tool variant.
// If intent is nil, creates a default intent with operation_type inferred from tool variant.
// Returns an error response if validation fails, nil if validation passes.
// Also returns the intent (possibly created) for use by caller.
func (p *MCPProxyServer) validateIntentForVariant(intent *contracts.IntentDeclaration, toolVariant string) (*contracts.IntentDeclaration, *mcp.CallToolResult) {
	// Create default intent if not provided
	if intent == nil {
		intent = &contracts.IntentDeclaration{}
	}

	// Validate and set operation_type from tool variant
	if err := intent.ValidateForToolVariant(toolVariant); err != nil {
		return nil, mcp.NewToolResultError(err.Message)
	}

	return intent, nil
}

func (p *MCPProxyServer) validateIntentAgainstServer(
	intent *contracts.IntentDeclaration,
	toolVariant string,
	serverName string,
	toolName string,
	annotations *config.ToolAnnotations,
) *mcp.CallToolResult {
	if err := intent.ValidateAgainstServerAnnotations(toolVariant, fmt.Sprintf("%s:%s", serverName, toolName), annotations); err != nil {
		return mcp.NewToolResultError(err.Message)
	}

	return nil
}

func (p *MCPProxyServer) classifyServerToolStatus(serverName, toolName string) contracts.DisabledToolStatus {
	gate := p.evaluateExactToolGate(serverName, toolName)
	switch gate.class {
	case preflight.ToolClassServerNotConfigured:
		return contracts.DisabledStatusUnknown
	case preflight.ToolClassServerDisabled:
		return contracts.DisabledStatusServerDisabled
	case preflight.ToolClassBlockedByUser:
		return contracts.DisabledStatusByUser
	case preflight.ToolClassPendingApproval, preflight.ToolClassChanged:
		return contracts.DisabledStatusPendingApproval
	case preflight.ToolClassReady:
		return "" // callable
	default:
		return contracts.DisabledStatusUnknown
	}
}

// serverToolNames returns the best-available list of a server's tool names:
// the StateView snapshot when hydrated, otherwise a live ListTools.
func (p *MCPProxyServer) serverToolNames(serverName string) []string {
	if p.mainServer != nil && p.mainServer.runtime != nil {
		if sup := p.mainServer.runtime.Supervisor(); sup != nil {
			if ss, ok := sup.StateView().Snapshot().Servers[serverName]; ok && len(ss.Tools) > 0 {
				names := make([]string, 0, len(ss.Tools))
				for _, tl := range ss.Tools {
					names = append(names, tl.Name)
				}
				return names
			}
		}
	}
	if client, exists := p.upstreamManager.GetClient(serverName); exists {
		if tools, err := client.ListTools(context.Background()); err == nil {
			names := make([]string, 0, len(tools))
			for _, tl := range tools {
				names = append(names, tl.Name)
			}
			return names
		}
	}
	return nil
}

// isExactToolCallable is isToolCallable for a pair that is already split into
// server and RAW tool name (a live ListTools / StateView name, or a pair a
// resolver normalized once). It never re-normalizes, so a raw name carrying
// the server's own prefix keys availability and approval as itself.
func (p *MCPProxyServer) isExactToolCallable(serverName, toolName string) bool {
	if serverName == "" || toolName == "" {
		return false
	}

	serverConfig, err := p.storage.GetUpstreamServer(serverName)
	if err != nil || serverConfig == nil {
		return false
	}

	if !serverConfig.Enabled {
		return false
	}

	approval, err := p.lookupToolApproval(serverName, toolName)
	switch {
	case err == nil:
		if approval != nil && approval.Disabled {
			return false
		}
	case errors.Is(err, storage.ErrToolApprovalNotFound):
		// no record → use the implicit default (enabled / callable)
	default:
		// real storage error — fail closed to avoid silently re-enabling a
		// tool the user disabled.
		p.logger.Warn("isToolCallable: storage error treated as not-callable",
			zap.String("server", serverName),
			zap.String("tool", toolName),
			zap.Error(err))
		return false
	}

	return true
}

// blockedToolMessage explains the UI availability control and review gate.
func blockedToolMessage() string {
	return "TOOL_BLOCKED: Tool is disabled and not callable. " +
		"It may be disabled by the user or pending tool approval; " +
		"ask the user to enable or review it on the mcpproxy Tools page. " +
		"Run retrieve_tools with include_disabled:true to see unavailable tools and remediation."
}

// classifyDisabledTool returns the single reason a non-callable tool is locked.
func (p *MCPProxyServer) classifyDisabledTool(serverName, toolName string) contracts.DisabledToolStatus {
	// Single source of truth: classifyServerToolStatus. It returns "" only for
	// callable tools; classifyDisabledTool is only ever called for tools
	// already known non-callable, so "" → Unknown (never lie about a reason).
	if status := p.classifyServerToolStatus(serverName, toolName); status != "" {
		return status
	}
	return contracts.DisabledStatusUnknown
}

// disabledToolRemediation maps a status to one agent-actionable instruction
// Emitted once per response, never per tool.
func disabledToolRemediation(status contracts.DisabledToolStatus) string {
	switch status {
	case contracts.DisabledStatusServerDisabled:
		return "Its server is disabled. Ask the user to enable the server first."
	case contracts.DisabledStatusByUser:
		return "Disabled by the user. Ask the user to re-enable it in the mcpproxy UI " +
			"(Tools page) or via the API."
	case contracts.DisabledStatusPendingApproval:
		return "Awaiting security approval. Ask the user to review and approve it in the mcpproxy UI."
	default:
		return "Reason undetermined; check server logs."
	}
}

// maxQuarantinedMatches bounds how many quarantined tools the discovery
// second-pass collects before stopping. The response itself is capped lower
// (min(limit,10)); this just bounds work on the opt-in path.
const maxQuarantinedMatches = 50

func (p *MCPProxyServer) filterLockedMatchesByPolicy(matches []contracts.LockedToolEntry, policy *profile.CompiledPolicy) []contracts.LockedToolEntry {
	if policy == nil || len(matches) == 0 {
		return matches
	}
	filtered := make([]contracts.LockedToolEntry, 0, len(matches))
	for _, m := range matches {
		toolName := strings.TrimPrefix(m.Name, m.Server+":")
		annotations, found := p.EffectiveAnnotations(m.Server, toolName)
		intrinsic := profile.IntrinsicTier(annotations, found)
		if admitted, _, _ := policy.Decide(m.Server, toolName, intrinsic); !admitted {
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered
}

func (p *MCPProxyServer) collectQuarantinedToolMatches(query string, allowServer func(string) bool, seen map[string]bool, toolNamesFor func(string) []string) []contracts.LockedToolEntry {
	tokens := queryTokens(query)
	if len(tokens) == 0 {
		return nil
	}

	servers, err := p.storage.ListUpstreamServers()
	if err != nil {
		p.logger.Debug("collectQuarantinedToolMatches: failed to list upstreams", zap.Error(err))
		return nil
	}

	var matches []contracts.LockedToolEntry
	// add records a quarantined tool; returns false once the work cap is hit.
	add := func(sc *config.ServerConfig, toolName string, status contracts.DisabledToolStatus) bool {
		key := sc.Name + ":" + toolName
		if seen[key] || !toolNameMatchesQuery(toolName, tokens) {
			return true
		}
		seen[key] = true
		matches = append(matches, contracts.LockedToolEntry{Name: key, Server: sc.Name, Status: status})
		return len(matches) < maxQuarantinedMatches
	}

	for _, sc := range servers {
		if sc == nil || !sc.Enabled || !allowServer(sc.Name) {
			continue
		}

		approvals, aerr := p.storage.ListToolApprovals(sc.Name)
		if aerr != nil {
			continue
		}
		for _, a := range approvals {
			if a == nil || a.Disabled {
				continue
			}
			if a.Status != storage.ToolApprovalStatusPending && a.Status != storage.ToolApprovalStatusChanged {
				continue
			}
			if !add(sc, a.ToolName, contracts.DisabledStatusPendingApproval) {
				return matches
			}
		}
	}
	return matches
}

// queryTokens lowercases the query and splits it into alphanumeric tokens, used
// for a lightweight name match against quarantined tools (which are not in the
// BM25 index and so can't be ranked normally). Tokens of length >= 2 are kept so
// short-but-meaningful capability keywords ("ci", "ui", "qa", "db", "os") still
// match; if the query has only single-character fields they are kept as a
// fallback rather than returning nothing.
func queryTokens(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) >= 2 {
			tokens = append(tokens, f)
		}
	}
	if len(tokens) == 0 {
		for _, f := range fields {
			if f != "" {
				tokens = append(tokens, f)
			}
		}
	}
	return tokens
}

// toolNameMatchesQuery reports whether a quarantined tool's name is relevant to
// the query: any query token appears in the lowercased tool name. Tool names
// usually carry the capability keywords (e.g. "emulator_build_web"), so this is
// enough to surface "this exists but is quarantined" without the description.
func toolNameMatchesQuery(toolName string, tokens []string) bool {
	lower := strings.ToLower(toolName)
	for _, tok := range tokens {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

func (p *MCPProxyServer) lookupToolAnnotations(serverName, toolName string) *config.ToolAnnotations {
	annotations, _ := p.lookupToolAnnotationsFound(serverName, toolName)
	return annotations
}

// lookupToolAnnotationsFound is lookupToolAnnotations that also reports whether
// the StateView knows the tool at all. A nil result with found=true is a
// discovered tool that publishes no annotations; found=false means the proxy
// has no metadata for it (server unknown, not yet discovered, or no runtime)
// and no tier can be established from it.
//
// Callers pass the canonical "server:tool" identity, while the StateView
// stores bare tool names on the live path — the prefix is stripped so the
// name match cannot silently miss. A
// caller that already holds a SPLIT pair must use lookupExactToolAnnotations
// instead: normalizing it again would strip a raw name's own leading segment.
func (p *MCPProxyServer) lookupToolAnnotationsFound(serverName, toolName string) (*config.ToolAnnotations, bool) {
	serverName, toolName = normalizeServerTool(serverName, toolName)
	return p.lookupExactToolAnnotations(serverName, toolName)
}

// lookupExactToolAnnotations is lookupToolAnnotationsFound for a pair that is
// ALREADY split into server and raw tool name — handleCallToolVariant's and
// handleCallTool's parsed id, the sandbox's callTool(server, tool) arguments.
// It never re-normalizes: a raw tool name may itself begin with the server's
// own prefix ("a:ns:erase" on server "a"), and normalizeServerTool would
// strip that segment and classify the pair as the suffix tool "ns:erase"
// while dispatch still targets "a:ns:erase".
func (p *MCPProxyServer) lookupExactToolAnnotations(serverName, toolName string) (*config.ToolAnnotations, bool) {
	identity := p.resolveExactToolIdentity(serverName, toolName)
	return identity.Annotations, identity.Found
}

// toolIdentity is the outcome of resolving one split (server, RAW tool) pair
// against the live discovery snapshot. It is
// the registration identity every dispatch path authorizes against: the
// annotations feed the target-tier gate, and the two booleans tell an
// undiscovered name on a server the proxy knows apart from a server the
// snapshot has never held.
type toolIdentity struct {
	// ServerKnown reports whether the snapshot holds the server at all. False
	// also covers a proxy with no runtime wired, which therefore never
	// refuses on identity grounds.
	ServerKnown      bool
	SnapshotHydrated bool
	// DiscoveryDone reports whether a discovery pass has completed for the
	// LIVE connection: stateview.ServerStatus.ToolsDiscovered is set AND the
	// connection token the stamp was captured under
	// (ServerStatus.DiscoveryEpoch) is the live client's current one
	// (managed.Client.ConnectionEpoch). Then the snapshot's tool list —
	// possibly EMPTY, for an upstream that lists no tools — is its
	// authoritative result. A connected server whose discovery has not run
	// yet is NOT resolvable: its snapshot is empty for want of a pass, not
	// because the name is absent, and admitting names in that window with
	// the destructive-tier fallback would let an upstream that hides tools
	// from tools/list keep them callable with zero approval. Nor is a server
	// whose stamp belongs to a PREVIOUS connection: the
	// connect/disconnect events that clear the stamp are best-effort, and a
	// name connection A discovered says nothing about what connection B
	// serves under it.
	DiscoveryDone bool
	// StaleConnection reports that DiscoveryDone was withdrawn because the
	// live client's connection token differs from the snapshot's stamp: the
	// discovery result is a previous connection's.
	StaleConnection bool
	// DiscoveryEpoch is the connection generation the snapshot's discovery
	// stamp was captured under (stateview.ServerStatus.DiscoveryEpoch). When
	// DiscoveryDone stands it is the LIVE client's generation too, so it is
	// the generation a certified identity is valid on — and the one the
	// dispatch that follows must be pinned to (see certified).
	DiscoveryEpoch int64
	// Found reports whether the server's snapshot lists the raw name, verbatim.
	Found bool
	// Description and Annotations are the snapshot's metadata for the tool;
	// zero when Found is false.
	Description string
	Annotations *config.ToolAnnotations
}

func (id toolIdentity) Unresolved() bool {
	return id.ServerKnown && id.SnapshotHydrated && (!id.DiscoveryDone || !id.Found)
}

// certified reports that the live snapshot lists the raw name as a completed
// discovery result of the CURRENT connection generation — the only state in
// which DiscoveryEpoch names the generation the identity is valid on. A
// dispatch that follows a certified read is pinned to that generation
// (managed.Client.CallToolOnEpoch): it reaches the upstream on exactly the
// connection the name, tier and approval hash were certified against, or not
// at all. On a CONNECTED live client it is also the only admitting outcome
// of liveIdentityRefusal; the unpinned dispatch remains only for the shapes in which
// that check stands aside (no runtime, server not in the StateView, client
// not connected) — the server-existence and not-connected handling own it.
func (id toolIdentity) certified() bool {
	return id.ServerKnown && id.SnapshotHydrated && id.DiscoveryDone && id.Found
}

// resolveExactToolIdentity resolves a pair that is ALREADY split into server
// and raw tool name against the StateView snapshot. It never re-normalizes:
// a raw tool name may itself begin with the server's own prefix ("a:ns:erase"
// on server "a"), and normalizeServerTool would strip that segment and
// classify the pair as the suffix tool "ns:erase" while dispatch still
// targets "a:ns:erase".
//
// It reads the server's PERSISTED record for the hydration verdict (see
// resolveExactToolIdentityIn). A caller that already holds that
// record — the shared gate, evaluateExactToolGate, and every step of a
// dispatch that follows the gate (liveIdentityRefusal, the sandbox bridge's
// hydration) — must resolve through resolveExactToolIdentityWith instead, so
// its server-level verdicts and the identity derive from ONE read (one
// persisted record per dispatch). This convenience is for the non-dispatch
// readers (describe_tool, visibility, direct callability).
func (p *MCPProxyServer) resolveExactToolIdentity(serverName, toolName string) toolIdentity {
	return p.resolveExactToolIdentityWith(p.persistedServerRecord(serverName), serverName, toolName)
}

// resolveExactToolIdentityWith is resolveExactToolIdentity for a caller that
// already holds the server's persisted record (nil when there is none).
func (p *MCPProxyServer) resolveExactToolIdentityWith(record *config.ServerConfig, serverName, toolName string) toolIdentity {
	if p.mainServer == nil || p.mainServer.runtime == nil {
		return toolIdentity{}
	}

	supervisor := p.mainServer.runtime.Supervisor()
	if supervisor == nil {
		return toolIdentity{}
	}

	return p.resolveExactToolIdentityIn(supervisor.StateView().Snapshot().Servers, serverName, toolName, record)
}

func (p *MCPProxyServer) persistedServerRecord(serverName string) *config.ServerConfig {
	if p.storage == nil || serverName == "" {
		return nil
	}
	record, err := p.storage.GetUpstreamServer(serverName)
	if err != nil {
		return nil
	}
	return record
}

func (p *MCPProxyServer) resolveExactToolIdentityIn(servers map[string]*stateview.ServerStatus, serverName, toolName string, record *config.ServerConfig) toolIdentity {
	serverStatus, exists := servers[serverName]
	if !exists || serverStatus == nil {
		return toolIdentity{}
	}
	enabled := serverStatus.Enabled
	if record != nil {
		enabled = record.Enabled
	}
	identity := toolIdentity{
		ServerKnown:      true,
		SnapshotHydrated: enabled && serverStatus.Connected,
		DiscoveryDone:    serverStatus.ToolsDiscovered,
		DiscoveryEpoch:   serverStatus.DiscoveryEpoch,
	}
	if identity.SnapshotHydrated {
		switch live, state := p.liveConnectionState(serverName); state {
		case liveClientDisconnected:
			identity.SnapshotHydrated = false
		case liveClientConnected:
			if identity.DiscoveryDone && live != serverStatus.DiscoveryEpoch {
				identity.DiscoveryDone = false
				identity.StaleConnection = true
			}
		}
	}

	for i := range serverStatus.Tools {
		if serverStatus.Tools[i].Name == toolName {
			identity.Found = true
			identity.Description = serverStatus.Tools[i].Description
			identity.Annotations = serverStatus.Tools[i].Annotations
			return identity
		}
	}

	return identity
}

// liveConnectionEpoch returns the live client's connection-instance token
// for a server, and whether there is a CONNECTED live client to read it from
// (managed.Client.ConnectionEpoch). No upstream manager, no client, or a
// client that is not connected reports false, and the caller then makes no
// token claim.
func (p *MCPProxyServer) liveConnectionEpoch(serverName string) (int64, bool) {
	epoch, state := p.liveConnectionState(serverName)
	return epoch, state == liveClientConnected
}

// liveClientState is what the live upstream client says about a server,
// independently of the StateView snapshot.
type liveClientState int

const (
	// liveClientAbsent: no upstream manager or no client
	// for the server — the live side makes no claim either way.
	liveClientAbsent liveClientState = iota
	// liveClientDisconnected: a client exists and is NOT connected
	// (disconnected, connecting, OAuth-pending, ...): whatever the snapshot
	// reads, the server is not callable and the server-level verdicts own it.
	liveClientDisconnected
	// liveClientConnected: a client exists and is connected; the epoch
	// returned alongside is its current connection generation.
	liveClientConnected
)

// liveConnectionState is liveConnectionEpoch with the not-connected case
// told apart from the no-client case, so resolveExactToolIdentityIn can
// overrule a snapshot whose Connected flag the live client contradicts.
func (p *MCPProxyServer) liveConnectionState(serverName string) (int64, liveClientState) {
	if p.upstreamManager == nil {
		return 0, liveClientAbsent
	}
	client, ok := p.upstreamManager.GetClient(serverName)
	if !ok || client == nil {
		return 0, liveClientAbsent
	}
	if !client.IsConnected() {
		return 0, liveClientDisconnected
	}
	return client.ConnectionEpoch(), liveClientConnected
}

// unresolvedToolIdentityMessage is the insufficient-permission body every
// dispatch path answers for a name it cannot resolve on a known server
// It is worded as a permission refusal —
// the caller holds no tier for a tool the proxy cannot identify — and
// deliberately does not echo any upstream answer: the upstream is never
// asked. The remediation depends on WHY the name is unresolved
// (toolIdentity.DiscoveryDone): while the server's discovery has not
// completed for this connection, no tool list exists yet to refresh from and
// the caller should simply retry shortly; once discovery has completed and
// the name is absent from its result, the caller must refresh with
// retrieve_tools and pick a listed name.
func unresolvedToolIdentityMessage(serverName, toolName string, discoveryDone bool) string {
	if !discoveryDone {
		return fmt.Sprintf("Permission denied: tool '%s:%s' cannot be resolved because tool discovery has not completed for server '%s' yet, so no permission tier applies to it; retry shortly, once the server's tools have been discovered",
			serverName, toolName, serverName)
	}
	return fmt.Sprintf("Permission denied: tool '%s:%s' cannot be resolved against the current tool list of server '%s' (undiscovered or stale name), so no permission tier applies to it; refresh the tool list with retrieve_tools and retry with a listed name",
		serverName, toolName, serverName)
}

func (p *MCPProxyServer) liveIdentityRefusal(record *config.ServerConfig, serverName, toolName string, deferred toolIdentity, client interface{ IsConnected() bool }) (toolIdentity, string, bool) {
	if !deferred.ServerKnown || client == nil || !client.IsConnected() {
		return toolIdentity{}, "", false
	}
	live := p.resolveExactToolIdentityWith(record, serverName, toolName)
	if live.certified() {
		return live, "", false
	}
	// Not hydrated (Connected still false in the snapshot), no stamp, a
	// previous generation's stamp, or an unlisted name: refused. The
	// stale-name body applies only to a hydrated, completed pass of THIS
	// generation that does not list the name; everything else is the
	// discovery window.
	return live, unresolvedToolIdentityMessage(serverName, toolName, live.SnapshotHydrated && live.DiscoveryDone), true
}

// dispatchOnEpoch routes one upstream dispatch through the manager, pinned
// to the certified identity's generation when there is one and unpinned
// otherwise (see toolIdentity.certified).
func (p *MCPProxyServer) dispatchOnEpoch(ctx context.Context, certified toolIdentity, toolName string, args map[string]interface{}) (interface{}, error) {
	if certified.certified() {
		return p.upstreamManager.CallToolOnEpoch(ctx, toolName, args, certified.DiscoveryEpoch)
	}
	return p.upstreamManager.CallTool(ctx, toolName, args)
}

// lookupOutputSchema returns the declared output schema (raw JSON) for a tool,
// or "" when the tool declares none or cannot be found. Mirrors
// lookupToolAnnotations, reading from the in-memory stateview snapshot so the
// hot path stays a cheap map lookup.
func (p *MCPProxyServer) lookupOutputSchema(serverName, toolName string) string {
	if p.mainServer == nil || p.mainServer.runtime == nil {
		return ""
	}
	supervisor := p.mainServer.runtime.Supervisor()
	if supervisor == nil {
		return ""
	}
	snapshot := supervisor.StateView().Snapshot()
	serverStatus, exists := snapshot.Servers[serverName]
	if !exists {
		return ""
	}
	for _, tool := range serverStatus.Tools {
		if tool.Name == toolName || tool.Name == serverName+":"+toolName {
			return tool.OutputSchemaJSON
		}
	}
	return ""
}

// applyOutputValidation checks the upstream structured result and records violations.
// Strict mode returns an error result; other modes leave the result unchanged.
func (p *MCPProxyServer) applyOutputValidation(ctx context.Context, serverName, toolName, requestID string, forwarded *mcp.CallToolResult) *mcp.CallToolResult {
	// Disabled (mode=off) or validator not constructed -> no-op.
	if p.outputValidator == nil || !p.config.OutputValidation.IsEnabled() {
		return nil
	}
	if forwarded == nil || forwarded.IsError {
		return nil // no successful structured payload to validate
	}
	schemaJSON := p.lookupOutputSchema(serverName, toolName)
	if schemaJSON == "" {
		return nil // tool declares no output schema
	}

	d := evaluateOutputValidation(
		p.outputValidator,
		serverName+":"+toolName,
		schemaJSON,
		p.config.OutputValidation.IsStrict(),
		p.config.OutputValidation.BlockOnMissingStructured(),
		forwarded,
	)
	if d.decision == "" {
		return nil // clean pass / no-op
	}

	sessionID := ""
	if sess := mcpserver.ClientSessionFromContext(ctx); sess != nil {
		sessionID = sess.SessionID()
	}

	p.emitActivityPolicyDecision(ctx, serverName, toolName, sessionID, requestID, d.decision, d.reason)
	if d.block {
		return mcp.NewToolResultError("output schema validation failed: " + d.reason)
	}
	// Warn mode: forward the original payload unchanged.
	return nil
}

// rawByteSize returns the JSON-serialized byte count of v, or 0 on error.
// Used to capture pre-truncation sizes for activity records.
//
// Profiling note: on the call-tool hot path the result is Marshaled here and
// again inside forwardContentResult, so a large response is JSON-encoded twice.
// If this shows up in profiles, capture the size from a single shared Marshal.
func rawByteSize(v interface{}) int {
	if v == nil {
		return 0
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// mcpListCacheTTLMs is the freshness window (SEP-2549 ttlMs) advertised on
// mcpproxy's list and read results. Listings change with quarantine approvals,
// profile switches and upstream reconnects (list_changed covers connected
// sessions), so the window is deliberately short.
const mcpListCacheTTLMs int64 = 5000

// cacheHintServerOptions returns the SEP-2549 cache-hint options for the proxy
// MCP server. Every cacheable surface is private: tools/list and
// resources/* are filtered per caller by profile, client token scope and
// per-tool quarantine, so no result is provably caller-independent and none
// may be shared across authorization contexts. Per-method hints are set explicitly so a change to the library
// default cannot silently widen the scope.
func cacheHintServerOptions() []mcpserver.ServerOption {
	opts := []mcpserver.ServerOption{
		mcpserver.WithCacheHints(mcpListCacheTTLMs, mcp.CacheScopePrivate),
	}
	for _, m := range []mcp.MCPMethod{
		mcp.MethodToolsList,
		mcp.MethodResourcesList,
		mcp.MethodResourcesTemplatesList,
		mcp.MethodResourcesRead,
		mcp.MethodServerDiscover,
	} {
		opts = append(opts, mcpserver.WithMethodCacheHints(m, mcpListCacheTTLMs, mcp.CacheScopePrivate))
	}
	return opts
}
