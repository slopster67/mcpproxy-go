package transport

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/upstream/dialgate"

	"github.com/mark3labs/mcp-go/v2/client"
	"github.com/mark3labs/mcp-go/v2/client/transport"
	"go.uber.org/zap"
)

const (
	TransportHTTP           = "http"
	TransportStreamableHTTP = "streamable-http"

	TransportStdio = "stdio"
)

// ErrEndpointDeprecated represents a 410 Gone response indicating the endpoint has been deprecated
type ErrEndpointDeprecated struct {
	URL            string `json:"url"`
	Message        string `json:"message"`
	MigrationGuide string `json:"migration_guide,omitempty"`
	NewEndpoint    string `json:"new_endpoint,omitempty"`
}

func (e *ErrEndpointDeprecated) Error() string {
	if e.NewEndpoint != "" {
		return fmt.Sprintf("endpoint deprecated (410 Gone): %s - migrate to: %s", e.Message, e.NewEndpoint)
	}
	if e.MigrationGuide != "" {
		return fmt.Sprintf("endpoint deprecated (410 Gone): %s - see: %s", e.Message, e.MigrationGuide)
	}
	return fmt.Sprintf("endpoint deprecated (410 Gone): %s", e.Message)
}

// IsEndpointDeprecatedError checks if an error indicates a deprecated endpoint (HTTP 410)
func IsEndpointDeprecatedError(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*ErrEndpointDeprecated)
	return ok
}

// NewEndpointDeprecatedError creates a new ErrEndpointDeprecated from response details
func NewEndpointDeprecatedError(url, message, migrationGuide, newEndpoint string) *ErrEndpointDeprecated {
	return &ErrEndpointDeprecated{
		URL:            url,
		Message:        message,
		MigrationGuide: migrationGuide,
		NewEndpoint:    newEndpoint,
	}
}

// HTTPTransportConfig holds configuration for HTTP transport
type HTTPTransportConfig struct {
	URL         string
	Headers     map[string]string
	OAuthConfig *client.OAuthConfig
	UseOAuth    bool
	// RetryAfter, when set, receives the `Retry-After` hints observed on this
	// upstream's rate-limited responses. mcp-go flattens non-2xx responses
	// into strings, so a RoundTripper is the last place the header still
	// exists. nil disables the wrapper entirely.
	RetryAfter *RetryAfterRecorder

	// Gate is the upstream instance's retirement barrier (required). Every
	// branch builds on a gated *http.Transport (its dials and connection
	// writes stop when the instance is retired) and the returned client's
	// mcp-go transport is wrapped by dialgate.WrapTransport.
	Gate *dialgate.Gate
}

// baseTransport returns the http.RoundTripper a branch builds on: the
// instance's gated transport over tuned (or over the default settings when
// tuned is nil).
func (cfg *HTTPTransportConfig) baseTransport(tuned *http.Transport) http.RoundTripper {
	if tuned == nil {
		return cfg.Gate.HTTPTransport()
	}
	return cfg.Gate.Transport(tuned)
}

// oauthConfigForGate returns the OAuth config handed to mcp-go. An OAuth
// config that carries no HTTP client of its own gets the instance's
// gated one, so mcp-go's metadata, registration, token and refresh requests
// never fall back to an ungated default client.
func (cfg *HTTPTransportConfig) oauthConfigForGate() client.OAuthConfig {
	oc := *cfg.OAuthConfig
	if oc.HTTPClient == nil {
		oc.HTTPClient = cfg.Gate.HTTPClient(30 * time.Second)
	}
	return oc
}

// wrapClient re-creates the mcp-go client over the gated transport wrapper.
// mcp-go's constructors used here pass no client option, so the rebuilt
// client is equivalent apart from the gate.
func (cfg *HTTPTransportConfig) wrapClient(c *client.Client) *client.Client {
	if c == nil {
		return c
	}
	return client.NewClient(dialgate.WrapTransport(cfg.Gate, c.GetTransport()))
}

// upstreamRoundTripper composes the outbound transport wrappers for an upstream
// HTTP/SSE client: frame tracing (opt-in) and the Retry-After recorder.
// It always WRAPS the given base rather than replacing it, and it sits strictly
// below mcp-go's OAuth handling — the OAuth authorize/token wrappers live above
// this layer and are untouched.
func (cfg *HTTPTransportConfig) upstreamRoundTripper(base http.RoundTripper, logger *zap.Logger) http.RoundTripper {
	rt := base
	if rt == nil {
		rt = cfg.baseTransport(nil)
	}

	if cfg.RetryAfter != nil {
		rt = NewRetryAfterTransport(rt, cfg.RetryAfter, logger)
	}

	return rt
}

// maxRedirects mirrors net/http's default redirect limit.
const maxRedirects = 10

func sameOrigin(a, b *http.Request) bool {
	return strings.EqualFold(a.URL.Scheme, b.URL.Scheme) &&
		strings.EqualFold(a.URL.Hostname(), b.URL.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func effectivePort(r *http.Request) string {
	if p := r.URL.Port(); p != "" {
		return p
	}
	if strings.EqualFold(r.URL.Scheme, "https") {
		return "443"
	}
	return "80"
}

// upstreamRedirectPolicy keeps all MCP traffic on its configured origin.
// mcp-go attaches static headers and OAuth credentials in its RoundTripper,
// after Go's own redirect header filtering. Reject before another request is sent.
func upstreamRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if len(via) == 0 {
		return errors.New("redirect without a prior request")
	}
	if !sameOrigin(req, via[0]) {
		return fmt.Errorf("upstream MCP redirects must stay on the configured origin")
	}
	return nil
}

func (cfg *HTTPTransportConfig) streamableHTTPClient(oauthBranch bool, logger *zap.Logger) *http.Client {
	base := cfg.baseTransport(nil)
	timeout := time.Duration(0)
	if !oauthBranch {
		if len(cfg.Headers) == 0 {
			timeout = 180 * time.Second
		}
	}
	return &http.Client{
		Transport:     cfg.upstreamRoundTripper(base, logger),
		Timeout:       timeout,
		CheckRedirect: upstreamRedirectPolicy,
	}
}

// CreateHTTPClient creates a new MCP client using HTTP transport
func CreateHTTPClient(cfg *HTTPTransportConfig) (*client.Client, error) {
	logger := zap.L().Named("transport")

	logger.Error("🚨 TRANSPORT HTTP CLIENT CREATION",
		zap.String("url", cfg.logSafeURL()),
		zap.Bool("oauth_config_nil", cfg.OAuthConfig == nil),
		zap.Bool("use_oauth", cfg.UseOAuth))

	if cfg.URL == "" {
		return nil, fmt.Errorf("no URL specified for HTTP transport")
	}

	logger.Debug("Creating HTTP client",
		zap.String("url", cfg.logSafeURL()),
		zap.Bool("use_oauth", cfg.UseOAuth),
		zap.Bool("has_oauth_config", cfg.OAuthConfig != nil))

	if cfg.UseOAuth && cfg.OAuthConfig != nil {
		// Use OAuth-enabled client with Dynamic Client Registration
		logger.Info("Creating OAuth-enabled streamable HTTP client with Dynamic Client Registration",
			zap.String("url", cfg.logSafeURL()),
			zap.String("redirect_uri", cfg.OAuthConfig.RedirectURI),
			zap.Strings("scopes", cfg.OAuthConfig.Scopes),
			zap.Bool("pkce_enabled", cfg.OAuthConfig.PKCEEnabled))

		logger.Debug("OAuth config details",
			zap.String("client_id", cfg.OAuthConfig.ClientID),
			zap.Bool("has_client_secret", cfg.OAuthConfig.ClientSecret != ""),
			zap.Any("token_store", cfg.OAuthConfig.TokenStore))

		logger.Debug("🔧 About to create OAuth client with mcp-go library",
			zap.String("url", cfg.logSafeURL()),
			zap.String("redirect_uri", cfg.OAuthConfig.RedirectURI))

		logger.Info("Creating OAuth HTTP client with context-based timeout",
			zap.String("url", cfg.logSafeURL()),
			zap.String("note", "Using 30-minute context timeout from tray"))

		// Add detailed logging about the OAuth config and token store
		logger.Info("🔍 OAuth HTTP client creation details",
			zap.String("url", cfg.logSafeURL()),
			zap.String("redirect_uri", cfg.OAuthConfig.RedirectURI),
			zap.Strings("scopes", cfg.OAuthConfig.Scopes),
			zap.Bool("pkce_enabled", cfg.OAuthConfig.PKCEEnabled),
			zap.String("client_id", cfg.OAuthConfig.ClientID),
			zap.Bool("has_token_store", cfg.OAuthConfig.TokenStore != nil))

		// Log if extra params wrapper is active (custom HTTP client configured)
		if cfg.OAuthConfig.HTTPClient != nil {
			logger.Debug("🔧 Using custom HTTP client with OAuth extra params wrapper",
				zap.String("note", "Extra parameters will be injected into OAuth requests"))
		}

		oauthOpts := []transport.StreamableHTTPCOption{
			transport.WithHTTPBasicClient(cfg.streamableHTTPClient(true, logger)),
		}
		// Static headers ride along with the bearer, so a declared-OAuth
		// server keeps its non-credential headers (x-goog-user-project, …)
		// once OAuth wins the ladder. mcp-go sets Authorization from the token
		// store AFTER these, so a stale static Authorization never wins.
		if len(cfg.Headers) > 0 {
			logger.Debug("Adding static headers to OAuth HTTP client", zap.Int("header_count", len(cfg.Headers)))
			oauthOpts = append(oauthOpts, transport.WithHTTPHeaders(cfg.Headers))
		}

		client, err := client.NewOAuthStreamableHttpClient(cfg.URL, cfg.oauthConfigForGate(), oauthOpts...)
		if err != nil {
			// mcp-go builds this error from url.Parse, and
			// *url.Error quotes the raw URL it was handed.
			logger.Error("Failed to create OAuth client", logSafeErrorField(err))
			return nil, fmt.Errorf("failed to create OAuth client: %w", err)
		}

		logger.Info("✅ OAuth-enabled HTTP client created successfully")
		return cfg.wrapClient(client), nil
	}

	logger.Debug("Creating regular HTTP client", zap.String("url", cfg.logSafeURL()))

	headers := cfg.Headers

	opts := []transport.StreamableHTTPCOption{}
	if len(headers) > 0 {
		logger.Debug("Adding HTTP headers", zap.Int("header_count", len(headers)))
		opts = append(opts, transport.WithHTTPHeaders(headers))
	}

	opts = append(opts,
		transport.WithHTTPBasicClient(cfg.streamableHTTPClient(false, logger)))

	httpTransport, err := transport.NewStreamableHTTP(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP transport: %w", err)
	}
	return cfg.wrapClient(client.NewClient(httpTransport)), nil
}

// CreateSSEClient creates a new MCP client using SSE transport
func CreateSSEClient(cfg *HTTPTransportConfig) (*client.Client, error) {
	logger := zap.L().Named("transport")

	if cfg.URL == "" {
		return nil, fmt.Errorf("no URL specified for SSE transport")
	}

	logger.Debug("Creating SSE client",
		zap.String("url", cfg.logSafeURL()),
		zap.Bool("use_oauth", cfg.UseOAuth),
		zap.Bool("has_oauth_config", cfg.OAuthConfig != nil))

	if cfg.UseOAuth && cfg.OAuthConfig != nil {
		// Use OAuth-enabled SSE client with Dynamic Client Registration
		logger.Info("Creating OAuth-enabled SSE client with Dynamic Client Registration",
			zap.String("url", cfg.logSafeURL()),
			zap.String("redirect_uri", cfg.OAuthConfig.RedirectURI),
			zap.Strings("scopes", cfg.OAuthConfig.Scopes),
			zap.Bool("pkce_enabled", cfg.OAuthConfig.PKCEEnabled))

		logger.Debug("OAuth SSE config details",
			zap.String("client_id", cfg.OAuthConfig.ClientID),
			zap.Bool("has_client_secret", cfg.OAuthConfig.ClientSecret != ""),
			zap.Any("token_store", cfg.OAuthConfig.TokenStore))

		logger.Info("Creating OAuth SSE client with context-based timeout",
			zap.String("url", cfg.logSafeURL()),
			zap.String("note", "Using 30-minute context timeout from tray"))

		// Add detailed logging about the OAuth config and token store
		logger.Info("🔍 OAuth SSE client creation details",
			zap.String("url", cfg.logSafeURL()),
			zap.String("redirect_uri", cfg.OAuthConfig.RedirectURI),
			zap.Strings("scopes", cfg.OAuthConfig.Scopes),
			zap.Bool("pkce_enabled", cfg.OAuthConfig.PKCEEnabled),
			zap.String("client_id", cfg.OAuthConfig.ClientID),
			zap.Bool("has_token_store", cfg.OAuthConfig.TokenStore != nil))

		// Log if extra params wrapper is active (custom HTTP client configured)
		if cfg.OAuthConfig.HTTPClient != nil {
			logger.Debug("🔧 Using custom HTTP client with OAuth extra params wrapper",
				zap.String("note", "Extra parameters will be injected into OAuth requests"))
		}

		// As in the streamable-HTTP OAuth branch: OAuthConfig.HTTPClient covers
		// only the OAuth metadata/DCR/token calls, so the MCP requests need our
		// own client for the Retry-After recorder to see a 429. No
		// Timeout — SSE streams are long-lived by design.
		var oauthOpts []transport.ClientOption
		oauthOpts = append(oauthOpts, client.WithHTTPClient(&http.Client{
			Transport:     cfg.upstreamRoundTripper(cfg.baseTransport(nil), logger),
			CheckRedirect: upstreamRedirectPolicy,
		}))
		// Static headers ride along with the bearer, as in the streamable-HTTP
		// branch above.
		if len(cfg.Headers) > 0 {
			logger.Debug("Adding static headers to OAuth SSE client", zap.Int("header_count", len(cfg.Headers)))
			oauthOpts = append(oauthOpts, client.WithHeaders(cfg.Headers))
		}

		client, err := client.NewOAuthSSEClient(cfg.URL, cfg.oauthConfigForGate(), oauthOpts...)
		if err != nil {
			// The error may quote the raw URL; see the streamable-HTTP branch.
			logger.Error("Failed to create OAuth SSE client", logSafeErrorField(err))
			return nil, fmt.Errorf("failed to create OAuth SSE client: %w", err)
		}

		logger.Info("✅ OAuth-enabled SSE client created successfully")
		return cfg.wrapClient(client), nil
	}

	logger.Debug("Creating regular SSE client", zap.String("url", cfg.logSafeURL()))

	headers := cfg.Headers

	// Create custom HTTP client for SSE - NO Timeout field to allow indefinite streaming
	// The Timeout field covers the entire request duration, which kills long-lived SSE streams
	// Instead, we rely on IdleConnTimeout to detect dead connections
	baseTransport := cfg.baseTransport(&http.Transport{
		MaxIdleConns:        10,
		IdleConnTimeout:     300 * time.Second, // 5 minutes idle before closing
		DisableCompression:  false,
		DisableKeepAlives:   false, // Enable keep-alives for SSE stability
		MaxIdleConnsPerHost: 5,
		// ResponseHeaderTimeout can be used to timeout initial connection, but not ongoing stream
		ResponseHeaderTimeout: 30 * time.Second,
	})

	// Tracing and the Retry-After recorder both wrap the base transport; the
	// SSE-specific tuning above and the deliberately absent Client.Timeout
	// still apply.
	httpClient := &http.Client{
		Transport:     cfg.upstreamRoundTripper(baseTransport, logger),
		CheckRedirect: upstreamRedirectPolicy,
	}

	logger.Info("Creating SSE MCP client with indefinite timeout for long-lived streams",
		zap.String("url", cfg.logSafeURL()),
		zap.Duration("idle_timeout", 300*time.Second),
		zap.Duration("header_timeout", 30*time.Second),
		zap.Int("header_count", len(headers)),
		zap.String("note", "Removed http.Client.Timeout to allow SSE streams longer than 3 minutes"))

	// Enhanced trace-level debugging for SSE transport

	sseOpts := []transport.ClientOption{client.WithHTTPClient(httpClient)}
	if len(headers) > 0 {
		logger.Debug("Adding SSE headers", zap.Int("header_count", len(headers)))
		sseOpts = append(sseOpts, client.WithHeaders(headers))
	}

	sseClient, err := client.NewSSEMCPClient(cfg.URL, sseOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSE client: %w", err)
	}
	return cfg.wrapClient(sseClient), nil
}

// CreateHTTPTransportConfig creates an HTTP transport config from server config
func CreateHTTPTransportConfig(serverConfig *config.ServerConfig, oauthConfig *client.OAuthConfig) *HTTPTransportConfig {
	return &HTTPTransportConfig{
		URL:         serverConfig.URL,
		Headers:     serverConfig.Headers,
		OAuthConfig: oauthConfig,
		UseOAuth:    oauthConfig != nil,
		// Use global trace flag

	}
}

// DetermineTransportType determines the transport type based on URL and config
func DetermineTransportType(serverConfig *config.ServerConfig) string {
	if serverConfig.Protocol != "" && serverConfig.Protocol != "auto" {
		return serverConfig.Protocol
	}

	// Auto-detect based on command first (highest priority)
	if serverConfig.Command != "" {
		return TransportStdio
	}

	// Auto-detect based on URL
	if serverConfig.URL != "" {
		return TransportStreamableHTTP
	}

	// Default to stdio
	return TransportStdio
}

// logSafeURL renders the configured upstream URL for a LOG FIELD, with its
// credential-bearing query parameters and any userinfo password masked, the
// transport-layer twin of core's Client.logSafeURL.
//
// oauth.LogSafeURL leaves scheme, host, path and non-sensitive parameters
// verbatim, so the log field stays diagnosable, and it also catches a
// credential under an unrecognised parameter name, which a name-only rule
// would miss.
func (c *HTTPTransportConfig) logSafeURL() string {
	if c == nil {
		return ""
	}
	return oauth.LogSafeURL(c.URL)
}

// logSafeErrorField renders an error as a log field with any URL-embedded
// credential removed.
//
// A net/http error quotes the request URL inside its own message
// (`Post "https://host/mcp?token=…": dial tcp …`), so zap.Error alone would
// leak it. An error string has no enclosing key to judge it by, so
// ScrubUpstreamText's value-shaped detector runs as well — a name rule cannot
// see `?opaque=ghp_…`. It matches its twin in internal/upstream/core.
func logSafeErrorField(err error) zap.Field {
	if err == nil {
		return zap.Skip()
	}
	return zap.String("error", oauth.ScrubUpstreamText(err.Error()))
}
