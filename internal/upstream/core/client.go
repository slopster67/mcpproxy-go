package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/hash"
	"github.com/slopster67/mcpproxy-go/internal/logs"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/reqcontext"
	"github.com/slopster67/mcpproxy-go/internal/secret"
	"github.com/slopster67/mcpproxy-go/internal/secureenv"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	proxytransport "github.com/slopster67/mcpproxy-go/internal/transport"
	"github.com/slopster67/mcpproxy-go/internal/upstream/dialgate"
	"github.com/slopster67/mcpproxy-go/internal/upstream/launcher"

	"github.com/mark3labs/mcp-go/v2/client"
	"github.com/mark3labs/mcp-go/v2/client/transport"
	"github.com/mark3labs/mcp-go/v2/mcp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Client implements basic MCP client functionality without state management
type Client struct {
	peerExecutor    peering.Executor
	peerGeneration  int64
	suppressBrowser bool
	config          *config.ServerConfig
	globalConfig    *config.Config
	storage         storage.OAuthStore
	logger          *zap.Logger

	// Upstream server specific logger for debugging
	upstreamLogger *zap.Logger
	// upstreamLogCloser releases upstreamLogger's file sink. Disconnect
	// closes it; the sink reopens on the next write, so a
	// reconnecting server logs on as before. nil for console (CLI) loggers.
	upstreamLogCloser io.Closer

	// MCP client and server info
	client     *client.Client
	serverInfo *mcp.InitializeResult

	envManager *secureenv.Manager

	// Connection state protection
	mu         sync.RWMutex
	connected  bool
	connecting bool // Prevent concurrent connection attempts

	// authStrategy holds the name of the HTTP/SSE auth strategy the current
	// connection was established with (see AuthStrategy). Atomic rather than
	// under c.mu because Connect holds c.mu for the whole attempt — an OAuth
	// flow can take minutes — and the server-list projection must be able to
	// read it without waiting on that.
	authStrategy atomic.Value

	// OAuth progress tracking (separate mutex to prevent reentrant deadlock)
	oauthMu            sync.RWMutex
	oauthInProgress    bool
	oauthCompleted     bool
	lastOAuthTimestamp time.Time

	// SSE request serialization (prevent concurrent requests on SSE transport)
	// SSE transport has limitations with concurrent requests - responses can get lost
	// when multiple requests are in-flight simultaneously
	sseRequestMu sync.Mutex

	// retryAfter collects the `Retry-After` hints this upstream's HTTP/SSE
	// responses carry. mcp-go flattens a 429 into an error string long before
	// the connection state machine sees it, so the hint is captured by a
	// RoundTripper installed under the MCP client and read back here via
	// RetryAfterDeadline.
	//
	// It is a generation pointer, not a fixed recorder: each connect attempt
	// swaps in a fresh one (beginRetryAfterGeneration). Transports built for an
	// earlier attempt keep the recorder they captured at construction, so a
	// request still in flight on a superseded client cannot write into the
	// current attempt's slate — and the current attempt starts empty by
	// construction rather than by racing a Clear.
	retryAfter atomic.Pointer[proxytransport.RetryAfterRecorder]

	// Transport type and stderr access (for stdio)
	transportType string
	stderr        io.Reader

	// monitoringMu serializes the stderr/process monitoring lifecycle methods
	// (Start*/Stop*Monitoring). Connect (StartStderrMonitoring) and Disconnect
	// (StopStderrMonitoring) can run concurrently on the same client during a
	// reconcile-vs-shutdown overlap, racing the ctx/cancel/WaitGroup fields
	// below (notably WG.Add vs WG.Wait). This mutex makes start and stop
	// mutually exclusive. It is never held across c.mu.
	monitoringMu sync.Mutex

	// Stderr monitoring. stderrMonitoringDone is a per-cycle channel closed by
	// the monitor goroutine when it exits; Stop waits on it instead of a reused
	// sync.WaitGroup, so an abandoned (timed-out) wait never races a later
	// Start's counter. Both fields are written only under monitoringMu.
	stderrMonitoringCancel context.CancelFunc
	stderrMonitoringDone   chan struct{}

	// Ring buffer of recent stderr lines from the subprocess.
	// Populated by monitorStderr; surfaced in initialize failure messages so
	// users don't have to hunt through server logs to see why the child
	// process never responded.
	recentStderrMu sync.Mutex
	recentStderr   []string

	processCmd     *exec.Cmd
	processGroupID int // Process group ID for proper cleanup

	launcherHandle launcher.Handle

	// launcherProc is the launched child registered with the gate. Guarded
	// by mu like the two fields above.
	launcherProc *dialgate.Process

	// Notification callback for tools/list_changed
	onToolsChanged func(serverName string)

	// gate is the instance's retirement barrier. It is
	// created with the client and shared by every transport, OAuth helper and
	// launch the client starts; the managed client retires it when the
	// upstream manager unregisters the instance. A client that is never
	// retired (the CLI client, a transient helper without SetGate) keeps an
	// open gate. Read through Gate(); replaced only by SetGate before use.
	gate atomic.Pointer[dialgate.Gate]

	// binding is the committed credential binding of the view this instance
	// was built for. Read through CredentialBinding(); set
	// only by SetCredentialBinding before use.
	binding atomic.Pointer[commitview.CredentialBinding]

	// stdioProc is the stdio child registered with the gate, so a
	// normal Disconnect can hand it back to its own teardown. Guarded by mu.
	stdioProc *dialgate.Process
}

// NewClient creates a new core MCP client
func NewClient(id string, serverConfig *config.ServerConfig, logger *zap.Logger, logConfig *config.LogConfig, globalConfig *config.Config, storage storage.OAuthStore, secretResolver *secret.Resolver) (*Client, error) {
	// Deep-copy the config so expansion never mutates the caller's value.
	// ExpandStructSecretsCollectErrors resolves ${env:...} and ${secret:...} refs in
	// every string field of ServerConfig and its nested structs (OAuthConfig).
	resolvedServerConfig := config.CopyServerConfig(serverConfig)
	if secretResolver != nil && serverConfig.ExecutionHost() == "" {
		ctx := context.Background()
		errs := secretResolver.ExpandStructSecretsCollectErrors(ctx, resolvedServerConfig)
		for _, e := range errs {
			logger.Error("CRITICAL: Failed to resolve secret reference - field will use UNRESOLVED placeholder",
				zap.String("server", serverConfig.Name),
				zap.String("field", e.FieldPath),
				zap.String("reference", e.Reference),
				zap.Error(e.Err),
				zap.String("help", "Use Web UI (http://localhost:8080/ui/) or API to add the secret to secret store"))
		}
	}

	c := &Client{
		config:       resolvedServerConfig,
		globalConfig: globalConfig,
		storage:      storage,
		logger: logger.With(
			zap.String("upstream_id", id),
			zap.String("upstream_name", serverConfig.Name),
		),
	}
	c.retryAfter.Store(proxytransport.NewRetryAfterRecorder())
	g := dialgate.New()
	g.SetLogger(logger, serverConfig.Name)
	c.gate.Store(g)

	// Create secure environment manager
	var envConfig *secureenv.EnvConfig
	if globalConfig != nil && globalConfig.Environment != nil {
		envConfig = globalConfig.Environment
	} else {
		envConfig = secureenv.DefaultEnvConfig()
	}

	c.envManager = secureenv.NewManager(envConfig, resolvedServerConfig.Env)

	// Create upstream server logger if provided
	if logConfig != nil {
		upstreamLogger, upstreamLogCloser, err := logs.NewUpstreamServerLogger(logConfig, serverConfig.Name)
		if err != nil {
			logger.Warn("Failed to create upstream server logger", zap.String("server", serverConfig.Name), zap.Error(err))
		} else {
			c.upstreamLogger = upstreamLogger
			c.upstreamLogCloser = upstreamLogCloser
		}
	}

	return c, nil
}

// IsConnected returns whether the client is currently connected
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// Ping issues the MCP-standard lightweight liveness check (`ping`) to the
// upstream server. It is used by the managed client's health loop as a cheap
// replacement for re-listing every tool just to confirm the connection is
// alive. Returns an error if the client is not connected or
// the request fails so the caller can classify and drive reconnection.
func (c *Client) Ping(ctx context.Context) error {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()

	if !c.IsConnected() || client == nil {
		return fmt.Errorf("client not connected")
	}

	// mcp-go deprecates Ping because protocol 2026-07-28 removed the RPC; there
	// it returns success without sending anything. It is a real probe here
	// because the upstream handshake is pinned to the earlier era
	// (connection_lifecycle.go). Lifting that pin requires an era-aware
	// replacement, or the health loop reports a dead upstream as healthy.
	//nolint:staticcheck // SA1019: valid on the protocol era this client is pinned to; see above.
	return client.Ping(ctx)
}

// ListTools retrieves available tools from the upstream server
func (c *Client) ListTools(ctx context.Context) ([]*config.ToolMetadata, error) {
	c.mu.RLock()
	client := c.client
	serverInfo := c.serverInfo
	transportType := c.transportType
	c.mu.RUnlock()

	if !c.IsConnected() || client == nil {
		return nil, fmt.Errorf("client not connected")
	}

	// Check if we have server info and if server supports tools
	if serverInfo == nil {
		c.logger.Debug("Server info not available")
		return nil, fmt.Errorf("server info not available")
	}

	if serverInfo.Capabilities.Tools == nil {
		c.logger.Debug("Server does not support tools")
		return nil, nil
	}

	// SSE sends requests via HTTP POST but receives responses on a persistent
	// stream; concurrent requests can lose responses, so they are serialized.
	if transportType == "sse" {
		c.logger.Debug("SSE transport detected - serializing ListTools request",
			zap.String("server", c.config.Name))
		c.sseRequestMu.Lock()
		defer c.sseRequestMu.Unlock()
		c.logger.Debug("SSE request lock acquired",
			zap.String("server", c.config.Name))
	}

	// Always make direct call to upstream server (no caching)
	c.logger.Info("Making direct tools list call to upstream server",
		zap.String("server", c.config.Name))

	listReq := mcp.ListToolsRequest{}
	toolsResult, err := client.ListTools(ctx, listReq)
	if err != nil {
		// Debug, not Error: the callers (managed.Client.ListTools and
		// upstream.Manager.discoverTools) log this same failure and know
		// whether it matters.
		c.logger.Debug("Failed to list tools via direct call to upstream server",
			zap.String("server", c.config.Name),
			zap.Error(err))
		return nil, fmt.Errorf("failed to list tools: %w", err)
	}

	// Convert to our format
	tools := []*config.ToolMetadata{}
	for i := range toolsResult.Tools {
		tool := &toolsResult.Tools[i]
		var paramsJSON string
		if schemaBytes, err := json.Marshal(tool.InputSchema); err == nil {
			paramsJSON = string(schemaBytes)
		}

		// Capture the tool's declared output schema so it is available at call
		// time for output-schema validation. captureOutputSchemaJSON prefers raw
		// schema bytes and normalizes them for a stable contract hash; a tool
		// with no declared schema yields "", making validation a no-op.
		outputSchemaJSON := captureOutputSchemaJSON(tool)

		// Stamp the exact upstream-reported name as RawName. Name carries the
		// same raw string for the index/search seams, but only RawName is an unambiguous identity — a raw name may itself
		// contain colons ("ns:erase") or even begin with this server's own
		// prefix, and every producer downstream (approval records, index
		// docIDs) keys on it via config.RawToolName.
		toolMeta := &config.ToolMetadata{
			ServerName:       c.config.Name,
			Name:             tool.Name,
			RawName:          tool.Name,
			Description:      tool.Description,
			ParamsJSON:       paramsJSON,
			OutputSchemaJSON: outputSchemaJSON,
		}

		// ToolAnnotation is a value type in mcp-go. Its zero value is kept
		// as a non-nil record, so an upstream annotations:{} tool tiers as
		// "unannotated" in the informed review payload.
		hasAnnotations := tool.Annotations.Title != "" ||
			tool.Annotations.ReadOnlyHint != nil ||
			tool.Annotations.DestructiveHint != nil ||
			tool.Annotations.IdempotentHint != nil ||
			tool.Annotations.OpenWorldHint != nil

		// Log tool annotations at debug level for troubleshooting
		if hasAnnotations {
			c.logger.Debug("Tool with annotations from server",
				zap.String("server", c.config.Name),
				zap.String("tool", tool.Name),
				zap.String("title", tool.Annotations.Title))
		}

		toolMeta.Annotations = toolAnnotationsFromWire(tool.Annotations)

		// Compute hash for tool change detection.
		// Hash is based on serverName + toolName + description + inputSchema + outputSchema.
		toolMeta.Hash = hash.ComputeToolHashWithOutputSchema(c.config.Name, tool.Name, tool.Description, tool.InputSchema, outputSchemaJSON)

		tools = append(tools, toolMeta)
	}

	c.logger.Info("Successfully retrieved tools via direct call to upstream server",
		zap.String("server", c.config.Name),
		zap.Int("tool_count", len(tools)))

	return tools, nil
}

func toolAnnotationsFromWire(annotation mcp.ToolAnnotation) *config.ToolAnnotations {
	return &config.ToolAnnotations{
		Title:           annotation.Title,
		ReadOnlyHint:    annotation.ReadOnlyHint,
		DestructiveHint: annotation.DestructiveHint,
		IdempotentHint:  annotation.IdempotentHint,
		OpenWorldHint:   annotation.OpenWorldHint,
	}
}

// callTimeoutError is the error CallTool returns when its own deadline fires.
// It is recognisable as context.DeadlineExceeded and as a net.Error timeout,
// so the call-error classifier can tell a timeout from an unrelated failure
// without reading the message.
type callTimeoutError struct {
	tool    string
	timeout time.Duration
}

func (e *callTimeoutError) Error() string {
	return fmt.Sprintf("CallTool '%s' timed out after %v", e.tool, e.timeout)
}

// Is makes errors.Is(err, context.DeadlineExceeded) true.
func (e *callTimeoutError) Is(target error) bool { return target == context.DeadlineExceeded }

// Timeout and Temporary satisfy net.Error.
func (e *callTimeoutError) Timeout() bool   { return true }
func (e *callTimeoutError) Temporary() bool { return true }

func (c *Client) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	c.mu.RLock()
	client := c.client
	transportType := c.transportType
	c.mu.RUnlock()

	if !c.IsConnected() || client == nil {
		return nil, fmt.Errorf("client not connected")
	}

	// SSE transport requires request serialization to prevent concurrent request issues
	if transportType == "sse" {
		c.logger.Debug("SSE transport detected - serializing CallTool request",
			zap.String("server", c.config.Name),
			zap.String("tool", toolName))
		c.sseRequestMu.Lock()
		defer c.sseRequestMu.Unlock()
		c.logger.Debug("SSE request lock acquired for CallTool",
			zap.String("server", c.config.Name),
			zap.String("tool", toolName))
	}

	request := mcp.CallToolRequest{}
	request.Params.Name = toolName
	if args == nil {
		args = map[string]interface{}{}
	}
	request.Params.Arguments = args

	// Log to server-specific log
	if c.upstreamLogger != nil {
		c.upstreamLogger.Info("Starting CallTool operation",
			zap.String("tool_name", toolName))
	}

	// Log request for trace debugging
	if c.upstreamLogger != nil {
		if reqBytes, err := json.MarshalIndent(request, "", "  "); err == nil {
			c.upstreamLogger.Debug("JSON-RPC CallTool Request",
				zap.String("method", "tools/call"),
				zap.String("tool", toolName),
				zap.String("formatted_json", string(reqBytes)))
		}
	}

	// Add timeout wrapper to prevent hanging indefinitely
	// Use configured timeout or default to 2 minutes
	var timeout time.Duration
	if c.globalConfig != nil && c.globalConfig.CallToolTimeout.Duration() > 0 {
		timeout = c.globalConfig.CallToolTimeout.Duration()
	} else {
		timeout = 2 * time.Minute // Default fallback
	}

	if remaining, ok := ctx.Value(peerCallTimeoutKey{}).(time.Duration); ok && remaining > 0 {
		timeout = remaining
	}

	// If the provided context doesn't have a timeout, add one
	callCtx := ctx
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > timeout {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Extra debug before sending request through transport
	c.logger.Debug("Starting upstream CallTool",
		zap.String("server", c.config.Name),
		zap.String("tool", toolName))

	result, err := client.CallTool(callCtx, request)
	if err != nil {
		// Log CallTool failure to server-specific log
		if c.upstreamLogger != nil {
			c.upstreamLogger.Error("CallTool operation failed",
				zap.String("tool_name", toolName),
				zap.Error(err))
		}

		// Provide more specific error context
		if callCtx.Err() == context.DeadlineExceeded {
			c.logCallInterrupted(ctx, callCtx, toolName)
			return nil, &callTimeoutError{tool: toolName, timeout: timeout}
		}
		if callCtx.Err() != nil {
			c.logCallInterrupted(ctx, callCtx, toolName)
		}

		// Extra diagnostics for broken pipe/closed pipe
		errStr := err.Error()
		if strings.Contains(errStr, "broken pipe") || strings.Contains(errStr, "closed pipe") {
			c.logger.Warn("CallTool write failed due to pipe closure",
				zap.String("server", c.config.Name),
				zap.String("tool", toolName),
				zap.String("transport", c.transportType))
		}

		return nil, fmt.Errorf("CallTool failed for '%s': %w", toolName, err)
	}

	// Log successful CallTool to server-specific log
	if c.upstreamLogger != nil {
		c.upstreamLogger.Info("CallTool operation completed successfully",
			zap.String("tool_name", toolName))
	}

	// Log response for trace debugging
	if c.upstreamLogger != nil {
		if respBytes, err := json.MarshalIndent(result, "", "  "); err == nil {
			c.upstreamLogger.Debug("JSON-RPC CallTool Response",
				zap.String("method", "tools/call"),
				zap.String("tool", toolName),
				zap.String("formatted_json", oauth.ScrubUpstreamText(string(respBytes))))
		}
	}

	return result, nil
}

// httpTransportConfig builds the transport config for this client's HTTP/SSE
// connections, threading the per-server Retry-After recorder into every
// mcp-go client we construct. Every HTTP/SSE connect path must go through it —
// a branch that calls proxytransport.CreateHTTPTransportConfig directly would
// silently lose the rate-limit hint for that auth strategy.
func (c *Client) httpTransportConfig(serverConfig *config.ServerConfig, oauthConfig *client.OAuthConfig) *proxytransport.HTTPTransportConfig {
	cfg := proxytransport.CreateHTTPTransportConfig(serverConfig, oauthConfig)
	// The transport captures THIS generation's recorder. A later attempt swaps
	// the pointer, and this client keeps writing to the recorder it was built
	// with, which keeps generations from bleeding into each other.
	cfg.RetryAfter = c.retryAfter.Load()
	// Every branch builds on the instance's gated transport and the mcp-go
	// transport is wrapped by the gate.
	cfg.Gate = c.Gate()
	return cfg
}

// beginRetryAfterGeneration retires the current recorder and installs a fresh
// one for a new connect attempt. Swapping rather than clearing means a request
// still in flight on a superseded transport writes into the retired recorder,
// where it can no longer be mistaken for something this attempt observed.
func (c *Client) beginRetryAfterGeneration() {
	c.retryAfter.Store(proxytransport.NewRetryAfterRecorder())
}

// RetryAfterDeadline reports the instant before which this upstream asked us not
// to come back (from a `Retry-After` on a 429/503), or the zero time when it gave
// no such hint. The managed client stamps it onto the state machine so both
// reconnect gates honour it.
func (c *Client) RetryAfterDeadline() time.Time {
	return c.retryAfter.Load().Deadline()
}

// ClearRetryAfter drops any recorded rate-limit hint. Called once a connection
// succeeds so a hint observed by an auth strategy that was superseded by a
// working one cannot hold back a later, unrelated reconnect.
func (c *Client) ClearRetryAfter() {
	c.retryAfter.Load().Clear()
}

// GetServerInfo returns server information from initialization
func (c *Client) GetServerInfo() *mcp.InitializeResult {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverInfo
}

// GetOAuthHandler returns the OAuth handler if the transport supports OAuth.
// Returns nil if no OAuth handler is configured or transport doesn't support OAuth.
func (c *Client) GetOAuthHandler() *transport.OAuthHandler {
	c.mu.RLock()
	mcpClient := c.client
	c.mu.RUnlock()

	return extractOAuthHandler(mcpClient)
}

// getOAuthHandlerLocked returns the OAuth handler without acquiring c.mu.
// MUST only be called when c.mu is already held by the caller (e.g., from Connect()).
func (c *Client) getOAuthHandlerLocked() *transport.OAuthHandler {
	return extractOAuthHandler(c.client)
}

// extractOAuthHandler extracts the OAuth handler from an MCP client's transport.
func extractOAuthHandler(mcpClient *client.Client) *transport.OAuthHandler {
	if mcpClient == nil {
		return nil
	}

	// Both StreamableHTTP and SSE transports implement GetOAuthHandler()
	type oauthTransport interface {
		GetOAuthHandler() *transport.OAuthHandler
	}

	t := innerTransport(mcpClient)
	if ot, ok := t.(oauthTransport); ok {
		return ot.GetOAuthHandler()
	}
	return nil
}

// RefreshOAuthTokenDirect forces an OAuth token refresh without reconnecting.
// This is used by the RefreshManager for proactive token refresh.
// This refreshes even a still-valid token.
//
// The refresh runs as a flight of the process-wide
// oauth.RefreshCoordinator, shared with the reactive path of the token store
// handed to mcp-go, so one expiry causes at most one token request. The flight
// uses the same refresh function as the reactive path (oauthRefreshFunc):
// mcp-go's handler.RefreshToken when the live handler has a client id, else
// the stored DCR credentials.
func (c *Client) RefreshOAuthTokenDirect(ctx context.Context) error {
	handler := c.GetOAuthHandler()
	if handler == nil {
		return fmt.Errorf("no OAuth handler available for %s", c.config.Name)
	}

	if c.storage == nil {
		return fmt.Errorf("no storage available for token refresh")
	}

	serverKey := oauth.GenerateServerKey(c.config.Name, c.config.URL)
	record, err := c.storage.GetOAuthTokenBound(serverKey, c.CredentialBinding())
	if err != nil {
		return fmt.Errorf("failed to get stored token for %s: %w", c.config.Name, err)
	}
	if record.RefreshToken == "" {
		return fmt.Errorf("no refresh token available for %s", c.config.Name)
	}

	c.logger.Info("Executing direct OAuth token refresh",
		zap.String("server", c.config.Name),
		zap.Time("current_expiry", record.ExpiresAt),
		zap.Bool("handler_has_credentials", handler.GetClientID() != ""),
		zap.Bool("storage_has_credentials", record.ClientID != ""))

	req := c.oauthRefreshRequest(serverKey, handler)
	req.ObservedRefreshToken = record.RefreshToken
	req.Trigger = oauth.RefreshTriggerProactive

	_, skipped, err := oauth.DefaultRefreshCoordinator().Do(ctx, req)
	if err != nil {
		c.logger.Error("Direct OAuth token refresh failed",
			zap.String("server", c.config.Name),
			zap.Error(err))
		return fmt.Errorf("OAuth refresh failed for %s: %w", c.config.Name, err)
	}

	c.logger.Info("Direct OAuth token refresh completed successfully",
		zap.String("server", c.config.Name),
		zap.Bool("network_skipped", skipped))
	return nil
}

// oauthTokenResponse represents the token response from an OAuth server
type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Error        string `json:"error,omitempty"`
	// ErrorDescription is decoded so redaction sees the value without any
	// JSON escaping the authorization server chose.
	ErrorDescription string `json:"error_description,omitempty"`
}

// refreshTokenWithStoredCredentials performs a token refresh using credentials
// from storage. A non-2xx response (or a 2xx carrying an RFC 6749 §5.2 error
// body) is returned as a typed *oauth.RefreshHTTPError.
func (c *Client) refreshTokenWithStoredCredentials(ctx context.Context, tokenEndpoint string, record *storage.OAuthTokenRecord) (*storage.OAuthTokenRecord, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", record.RefreshToken)
	data.Set("client_id", record.ClientID)
	if record.ClientSecret != "" {
		data.Set("client_secret", record.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	// The instance's gated transport: a retired instance sends no
	// refresh token and no client secret.
	httpClient := c.Gate().HTTPClient(30 * time.Second)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var tokenResp oauthTokenResponse
	jsonErr := json.Unmarshal(body, &tokenResp)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (jsonErr == nil && tokenResp.Error != "") {
		// This error is logged and returned to the REST caller. An OAuth
		// error_description is provider-authored free text that can echo
		// request parameters back (this form carries client_secret and
		// refresh_token), and the body is unbounded (a proxy's 502 HTML page),
		// so it is scrubbed with the free-text rule, then capped.
		// The exact credentials this request sent are removed verbatim first:
		// an opaque value echoed without a key=value shape is invisible to
		// the pattern scrubber.
		sent := []string{record.RefreshToken, record.ClientSecret, c.extraParamValue("client_secret")}
		raw := string(body)
		if jsonErr == nil && tokenResp.Error != "" {
			// A JSON error body is rendered from its DECODED fields: any
			// escaping the AS used (\u0026, \", \/ ...) is undone before the
			// sent credentials are matched.
			raw = tokenResp.Error
			if tokenResp.ErrorDescription != "" {
				raw += ": " + tokenResp.ErrorDescription
			}
		}
		detail := redactSent(raw, sent...)
		httpErr := &oauth.RefreshHTTPError{Status: resp.StatusCode, Detail: cappedScrub(detail, 512)}
		code := ""
		if jsonErr == nil {
			code = tokenResp.Error
		} else if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			// A non-JSON body is capped above; take the RFC 6749 §5.2 code
			// from the whole body so the cap cannot hide it from
			// classification. Not for 5xx/429: a gateway page that merely
			// mentions a code must stay transient (the status decides).
			code = rfc6749CodeIn(detail)
		}
		httpErr.OAuthCode = cappedScrub(redactSent(code, sent...), 64)
		return nil, httpErr
	}

	if jsonErr != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", jsonErr)
	}

	var expiresAt time.Time
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	} else {
		// Default to 1 hour if server doesn't specify (consistent with mcp-go behavior)
		expiresAt = time.Now().Add(1 * time.Hour)
	}

	return &storage.OAuthTokenRecord{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		ExpiresAt:    expiresAt,
	}, nil
}

// redactSent removes the exact credential values a token request sent.
func redactSent(s string, sent ...string) string {
	for _, v := range sent {
		for _, form := range oauth.SentValueSpellings(v) {
			s = strings.ReplaceAll(s, form, "[REDACTED]")
		}
	}
	return s
}

// rfc6749CodeIn finds a known RFC 6749 §5.2 error code in a non-JSON body.
func rfc6749CodeIn(body string) string {
	for _, code := range []string{"invalid_grant", "invalid_client", "unauthorized_client",
		"unsupported_grant_type", "invalid_scope", "temporarily_unavailable", "server_error"} {
		if strings.Contains(body, code) {
			return code
		}
	}
	return ""
}

// GetConfig returns the server configuration
func (c *Client) GetConfig() *config.ServerConfig {
	return c.config
}

// SetOnToolsChangedCallback sets the callback invoked when a notifications/tools/list_changed
// notification is received from the upstream MCP server. This enables reactive tool re-indexing.
func (c *Client) SetOnToolsChangedCallback(callback func(serverName string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onToolsChanged = callback
}

func containsAny(str string, substrs []string) bool {
	for _, substr := range substrs {
		if substr != "" && strings.Contains(str, substr) {
			return true
		}
	}
	return false
}

func (c *Client) oauthLogger() *zap.Logger {
	switch {
	case c.upstreamLogger == nil:
		return c.logger
	case c.logger == nil:
		return c.upstreamLogger
	default:
		return zap.New(zapcore.NewTee(c.logger.Core(), c.upstreamLogger.Core()))
	}
}

// cappedScrub renders an upstream-authored response body for an error message:
// the free-form rule first, then the cap, and the cut moved back to a rune
// boundary because the mask rendering is multi-byte.
//
// Scrub-then-cap, not cap-then-scrub: cutting first hands the detectors a
// FRAGMENT of any credential straddling the boundary, and every vendor-shaped
// matcher is anchored on a complete token, so the fragment matches nothing and
// its leading bytes are published.
func cappedScrub(s string, limit int) string {
	scrubbed := oauth.ScrubUpstreamText(s)
	if len(scrubbed) <= limit {
		return scrubbed
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(scrubbed[cut]) {
		cut--
	}
	return scrubbed[:cut] + "… (truncated)"
}

type connectionGenerationKey struct{}

// WithConnectionGeneration records the managed client's connection epoch on
// ctx so core can attach it to structured tool-call timeout/cancel logs.
func WithConnectionGeneration(ctx context.Context, epoch int64) context.Context {
	return context.WithValue(ctx, connectionGenerationKey{}, epoch)
}

// Cancellation sources reported in the tool-call interruption log.
const (
	cancelSourceCallTimeout  = "call_timeout"
	cancelSourceCallerCancel = "caller_cancel"
)

// logCallInterrupted emits one structured Warn when a tools/call ends because
// its context expired or was canceled. parent is the context the
// caller passed in; callCtx is the one (possibly with the call timeout) handed
// to the transport. Log-only: it never alters the returned error.
func (c *Client) logCallInterrupted(parent, callCtx context.Context, toolName string) {
	source := cancelSourceCallerCancel
	// The call timeout fired on our wrapper while the caller's context is
	// still live; a caller deadline/cancel shows up on parent itself.
	if parent.Err() == nil && callCtx.Err() == context.DeadlineExceeded {
		source = cancelSourceCallTimeout
	}

	c.mu.RLock()
	pid := 0
	if c.processCmd != nil && c.processCmd.Process != nil {
		pid = c.processCmd.Process.Pid
	}
	connected := c.connected
	c.mu.RUnlock()

	state := "connected"
	if !connected {
		state = "disconnected"
	}

	var generation int64
	if g, ok := parent.Value(connectionGenerationKey{}).(int64); ok {
		generation = g
	}

	c.logger.Warn("Upstream tools/call interrupted",
		zap.String("server", c.config.Name),
		zap.String("tool", toolName),
		zap.String("transport", c.transportType),
		zap.Int("pid", pid),
		zap.Int64("connection_generation", generation),
		zap.String("request_id", reqcontext.GetRequestID(parent)),
		zap.String("cancellation_source", source),
		zap.String("resulting_state", state),
	)
}
