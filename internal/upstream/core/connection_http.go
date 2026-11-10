package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/transport"
	"go.uber.org/zap"
)

// authStrategy pairs an auth strategy's display name with its attempt function.
type authStrategy struct {
	name string
	fn   func(context.Context) error
}

// httpAuthStrategies returns the ordered HTTP auth strategies to attempt.
//
// Auto mode tries headers -> no-auth -> OAuth. OAuth mode "on" makes OAuth the
// only strategy (see oauthRequiredByConfig); "off" never tries it.
func (c *Client) httpAuthStrategies() []authStrategy {
	switch c.config.OAuthMode() {
	case config.OAuthModeOn:
		return []authStrategy{{"OAuth", c.tryOAuthAuth}}
	case config.OAuthModeOff:
		return []authStrategy{{"headers", c.tryHeadersAuth}, {"no-auth", c.tryNoAuth}}
	}
	return []authStrategy{
		{"headers", c.tryHeadersAuth},
		{"no-auth", c.tryNoAuth},
		{"OAuth", c.tryOAuthAuth},
	}
}

// sseAuthStrategies is the SSE counterpart of httpAuthStrategies, with the same
// OAuth mode rules.
func (c *Client) sseAuthStrategies() []authStrategy {
	switch c.config.OAuthMode() {
	case config.OAuthModeOn:
		return []authStrategy{{"OAuth", c.trySSEOAuthAuth}}
	case config.OAuthModeOff:
		return []authStrategy{{"headers", c.trySSEHeadersAuth}, {"no-auth", c.trySSENoAuth}}
	}
	return []authStrategy{
		{"headers", c.trySSEHeadersAuth},
		{"no-auth", c.trySSENoAuth},
		{"OAuth", c.trySSEOAuthAuth},
	}
}

// oauthRequiredByConfig reports whether the operator set OAuth mode to "on".
//
// The no-auth strategy decides "no auth needed" from a successful
// anonymous initialize. Upstreams that authorise per method (Google's Gmail
// MCP answers initialize/tools/list anonymously and 401s only tools/call)
// pass that probe, so in auto mode the OAuth strategy is never reached. With
// OAuth on, the anonymous probe must not win; the OAuth strategy either
// attaches the stored token to every request or surfaces the sign-in
// requirement.
//
// The headers strategy draws the same conclusion from a successful initialize,
// so it is dropped too: a stale static Authorization header would otherwise
// win the anonymous handshake. Static headers ride on the OAuth transport
// instead, where the token store owns the Authorization header.
func (c *Client) oauthRequiredByConfig() bool {
	return c.config.OAuthMode() == config.OAuthModeOn
}

// AuthStrategyOAuth is the name of the OAuth auth strategy as recorded by
// AuthStrategy. The other strategies ("headers", "no-auth") are internal; only
// OAuth is something a consumer needs to recognise, because it is the one
// strategy whose credential lives in the oauth_tokens bucket.
const AuthStrategyOAuth = "OAuth"

// AuthStrategy reports the name of the auth strategy the CURRENT connection
// was established with ("headers", "no-auth" or AuthStrategyOAuth), or "" when
// the client is not connected over HTTP/SSE. It is a fact about the live
// connection, not the config: a server with a stored OAuth token that
// connected through its static headers did not use that token.
func (c *Client) AuthStrategy() string {
	if v, ok := c.authStrategy.Load().(string); ok {
		return v
	}
	return ""
}

// connectHTTP establishes HTTP transport connection with auth fallback
func (c *Client) connectHTTP(ctx context.Context) error {
	// Strategy order (and, with OAuth mode "on", the single OAuth
	// strategy) is decided by httpAuthStrategies.
	return c.runAuthStrategies(ctx, c.httpAuthStrategies(), "")
}

// connectSSE establishes SSE transport connection with auth fallback
func (c *Client) connectSSE(ctx context.Context) error {
	// Strategy order (and, with OAuth mode "on", the single OAuth
	// strategy) is decided by sseAuthStrategies.
	return c.runAuthStrategies(ctx, c.sseAuthStrategies(), "SSE ")
}

// runAuthStrategies attempts the strategies in order until one connects,
// recording the winner so AuthStrategy can report it. transportLabel is "" for
// streamable HTTP and "SSE " for SSE; it only prefixes the log and error text.
// Other packages match on "all authentication strategies failed".
func (c *Client) runAuthStrategies(ctx context.Context, authStrategies []authStrategy, transportLabel string) error {
	var lastErr error
	// Tell the OAuth strategy whether an anonymous attempt saw a
	// real 401, so a park without one can be re-probed instead of stranded.
	evidence := &authEvidence{}
	ctx = withAuthEvidence(ctx, evidence)
	for i, strategy := range authStrategies {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.logger.Debug("🔐 Trying "+transportLabel+"authentication strategy",
			zap.Int("strategy_index", i),
			zap.String("strategy", strategy.name))

		if err := strategy.fn(ctx); err != nil {
			lastErr = err
			c.logger.Debug("🚫 "+transportLabel+"Auth strategy failed",
				zap.Int("strategy_index", i),
				zap.String("strategy", strategy.name),
				// The transport error quotes the request URL with its query
				// credential.
				logSafeErrorField(err))

			// For configuration errors (like no headers), always try next strategy
			if c.isConfigError(err) {
				continue
			}

			// A DNS/dial/timeout failure says nothing about auth, and its text
			// quotes addresses and ports that the substring classifiers below
			// can misread as "401"/"403". Return it so the managed
			// client retries with backoff instead of falling into OAuth.
			if IsNetworkError(err) {
				return err
			}

			if hasHTTPAuthChallenge(err) {
				evidence.challenged = true
			}

			// For OAuth errors, continue to OAuth strategy
			if c.isOAuthError(err) {
				continue
			}

			// If it's not an auth error, don't try fallback
			if !c.isAuthError(err) {
				return err
			}
			continue
		}
		c.logger.Info("✅ "+transportLabel+"Authentication successful",
			zap.Int("strategy_index", i),
			zap.String("strategy", strategy.name))
		c.authStrategy.Store(strategy.name)

		// Register notification handler for tools/list_changed
		c.registerNotificationHandler()

		return nil
	}

	return fmt.Errorf("all "+transportLabel+"authentication strategies failed, last error: %w", lastErr)
}

// tryHeadersAuth attempts authentication using configured headers
func (c *Client) tryHeadersAuth(ctx context.Context) error {
	if len(c.config.Headers) == 0 {
		return fmt.Errorf("no headers configured")
	}

	httpConfig := c.httpTransportConfig(c.config, nil)
	httpClient, err := transport.CreateHTTPClient(httpConfig)
	if err != nil {
		return fmt.Errorf("failed to create HTTP client with headers: %w", err)
	}

	c.client = httpClient

	// Start the client
	if err := c.client.Start(ctx); err != nil {
		return err
	}

	// Run initialize() here so an OAuth error during MCP initialization lets
	// the OAuth strategy be tried next.
	if err := c.initialize(ctx); err != nil {
		return fmt.Errorf("MCP initialize failed during headers-auth strategy: %w", err)
	}

	return nil
}

// tryNoAuth attempts connection without authentication
func (c *Client) tryNoAuth(ctx context.Context) error {
	// Create config without headers
	configNoAuth := *c.config
	configNoAuth.Headers = nil

	httpConfig := c.httpTransportConfig(&configNoAuth, nil)
	httpClient, err := transport.CreateHTTPClient(httpConfig)
	if err != nil {
		return fmt.Errorf("failed to create HTTP client without auth: %w", err)
	}

	c.client = httpClient

	// Start the client
	if err := c.client.Start(ctx); err != nil {
		return err
	}

	// Run initialize() here so an OAuth error during MCP initialization lets
	// the OAuth strategy be tried next.
	if err := c.initialize(ctx); err != nil {
		return fmt.Errorf("MCP initialize failed during no-auth strategy: %w", err)
	}

	return nil
}

// trySSEHeadersAuth attempts SSE authentication using configured headers
func (c *Client) trySSEHeadersAuth(ctx context.Context) error {
	if len(c.config.Headers) == 0 {
		return fmt.Errorf("no headers configured")
	}

	httpConfig := c.httpTransportConfig(c.config, nil)
	sseClient, err := transport.CreateSSEClient(httpConfig)
	if err != nil {
		return fmt.Errorf("failed to create SSE client with headers: %w", err)
	}

	c.client = sseClient

	// Register connection lost handler for SSE transport to detect GOAWAY/disconnects
	c.client.OnConnectionLost(func(err error) {
		c.logger.Warn("⚠️ SSE connection lost detected",
			zap.String("server", c.config.Name),
			// A dropped-stream error quotes the stream URL.
			logSafeErrorField(err),
			zap.String("transport", "sse"),
			zap.String("note", "Connection dropped by server or network - will attempt reconnection"))
	})

	persistentCtx := c.Gate().Context() // cancelled when the instance is retired
	if err := c.client.Start(persistentCtx); err != nil {
		return err
	}

	// Run initialize() here, on the caller's context, so an OAuth error during
	// MCP initialization lets the OAuth strategy be tried next.
	if err := c.initialize(ctx); err != nil {
		return fmt.Errorf("MCP initialize failed during SSE headers-auth strategy: %w", err)
	}

	return nil
}

// trySSENoAuth attempts SSE connection without authentication
func (c *Client) trySSENoAuth(ctx context.Context) error {
	// Create config without headers
	configNoAuth := *c.config
	configNoAuth.Headers = nil

	httpConfig := c.httpTransportConfig(&configNoAuth, nil)
	sseClient, err := transport.CreateSSEClient(httpConfig)
	if err != nil {
		return fmt.Errorf("failed to create SSE client without auth: %w", err)
	}

	c.client = sseClient

	// Register connection lost handler for SSE transport to detect GOAWAY/disconnects
	c.client.OnConnectionLost(func(err error) {
		c.logger.Warn("⚠️ SSE connection lost detected",
			zap.String("server", c.config.Name),
			// A dropped-stream error quotes the stream URL.
			logSafeErrorField(err),
			zap.String("transport", "sse"),
			zap.String("note", "Connection dropped by server or network - will attempt reconnection"))
	})

	persistentCtx := c.Gate().Context() // cancelled when the instance is retired
	if err := c.client.Start(persistentCtx); err != nil {
		return err
	}

	// Run initialize() here, on the caller's context, so an OAuth error during
	// MCP initialization lets the OAuth strategy be tried next.
	if err := c.initialize(ctx); err != nil {
		return fmt.Errorf("MCP initialize failed during SSE no-auth strategy: %w", err)
	}

	return nil
}

// isAuthError checks if error indicates authentication failure (non-OAuth).
//
// The substring list is intentionally narrow: the strategy wrappers
// ("MCP initialize failed during headers-auth strategy", "no-auth strategy",
// "SSE headers-auth strategy") contain the literal token "auth", so any
// substring as permissive as "auth" or "authentication" would misclassify a
// wrapped transport/parse error (e.g. upstream HTTP 502 → JSON parse failure)
// as an auth failure and trigger an unwanted OAuth fallback. The patterns
// below match only genuine HTTP 403 responses and explicit "*failed"/"*failure"
// phrasings that upstreams emit, never the wrapper text.
func (c *Client) isAuthError(err error) bool {
	if err == nil {
		return false
	}

	// Don't catch OAuth errors here - they should be handled by isOAuthError() first
	if c.isOAuthError(err) {
		return false
	}

	errStr := err.Error()
	return containsAny(errStr, []string{
		"403", "Forbidden", "forbidden",
		"authentication failed", "authentication failure",
		"authorization failed", "authorization failure",
	})
}

// isConfigError checks if error indicates a configuration issue that should trigger fallback
func (c *Client) isConfigError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return containsAny(errStr, []string{
		"no headers configured",
		"no command specified",
	})
}

// isDeprecatedEndpointError checks if error indicates a deprecated/removed endpoint (HTTP 410 Gone)
// This helps detect when an MCP server has migrated to a new endpoint URL
func (c *Client) isDeprecatedEndpointError(err error) bool {
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
	}

	for _, indicator := range deprecationIndicators {
		if strings.Contains(errStr, indicator) {
			return true
		}
	}

	return false
}

// isServerSideError checks if error indicates a server-side error (HTTP 5xx).
// Some servers crash with 500 instead of returning 401 when they receive
// an invalid/revoked token, so 5xx during OAuth strategy may indicate
// a stale token rather than a genuine server error.
func (c *Client) isServerSideError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return containsAny(errStr, []string{
		"status 500",
		"status 502",
		"status 503",
	})
}
