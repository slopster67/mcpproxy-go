package core

import (
	"context"
	"fmt"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/logs"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/transport"
	"github.com/slopster67/mcpproxy-go/internal/upstream/dialgate"

	"go.uber.org/zap"
)

const (
	osLinux  = "linux"
	osDarwin = "darwin"

	// Subprocess shutdown timeouts
	// mcpClientCloseTimeout is the max time to wait for graceful MCP client close
	mcpClientCloseTimeout = 10 * time.Second
	// processGracefulTimeout is the max time to wait after SIGTERM before SIGKILL
	// Must be less than mcpClientCloseTimeout to complete within the close timeout
	processGracefulTimeout = 9 * time.Second
	// processTerminationPollInterval is how often to check if process exited
	processTerminationPollInterval = 100 * time.Millisecond

	// Transport types
	transportHTTP           = "http"
	transportHTTPStreamable = "streamable-http"
	transportSSE            = "sse"
)

// Context key types
type contextKey string

const (
	manualOAuthKey contextKey = "manual_oauth"
)

// logSafeURL renders the configured upstream URL for a LOG FIELD, with its
// query credentials masked.
//
// Connect() logs the URL on every attempt, to main.log and to the per-server
// server-<name>.log — and `upstream_servers tail_log` hands the latter to any
// MCP caller. A URL is one of the commonest places an MCP credential lives
// (`?token=…`, an Azure SAS `sig=`, an AWS `X-Amz-Signature=`), so the value is
// masked at the point it is written rather than only where it is read back:
// the file outlives the process and is readable by anything with disk access.
//
// The host and path survive, which is what makes the line diagnostic.
//
// oauth.LogSafeURL masks values deeply (not only by parameter name), because
// this value is also served to REST callers inside OAuthErrorDetails.ServerURL.
func (c *Client) logSafeURL() string {
	if c.config == nil {
		return ""
	}
	return oauth.LogSafeURL(c.config.URL)
}

// logSafeAuthURL renders an OAuth AUTHORIZATION URL for a LOG FIELD or for
// stdout.
//
// The authorize URL is the highest-frequency credential leak in the tree and
// needs no debug flag to reach main.log: oauth.autoDetectResource returns
// serverConfig.URL verbatim on every fallback branch (no resource_metadata in
// WWW-Authenticate, metadata without a `resource` field, a failed fetch, any
// 4xx/5xx), that value becomes extraParams["resource"], and every authorize-URL
// builder splices each extra param into the query, so a configured
// `https://host/mcp?token=SECRET` would otherwise reach disk and the terminal.
//
// URLValueDeep rather than RedactURLQueryParams because the credential arrives
// percent-encoded one level down (`resource=https%3A%2F%2Fhost%2Fmcp%3Ftoken%3D...`),
// where neither the sensitive-parameter name rule nor secretPattern can see it.
//
// The authorize endpoint's scheme, host, path and its own non-secret parameters
// survive, which is what lets an operator still diagnose the flow.
func logSafeAuthURL(authURL string) string {
	return oauth.RecordRedaction.URLValueDeep(authURL)
}

func logSafeArgs(args []string) []string {
	return oauth.RecordRedaction.SpawnArgv(args)
}

// redactURLCredentialsInError strips URL-embedded credentials from an error's
// TEXT while keeping the error itself intact for errors.Is/As and for the
// substring classification the connect paths do (isAuthError, isConfigError,
// "connection refused"): only the sensitive query values and userinfo
// passwords are rewritten.
//
// Masking the `url` LOG FIELDS is not enough, because the transport error
// carries the same URL inside its message —
// `Post "http://host/mcp?token=…": dial tcp: connection refused` — and that
// message is logged at Error level by the manager on every failed attempt, is
// written to the per-server log, and is stored as the client's last error.
// Redacting once, where the transport error enters mcpproxy, is what keeps a
// future log site from re-leaking it.
func redactURLCredentialsInError(err error) error {
	if err == nil {
		return nil
	}
	// ScrubUpstreamText rather than a name rule: a credential can sit under an
	// unrecognised query-parameter name (`?opaque=ghp_…`), and every downstream
	// log site (managed.Client, upstream.Manager, the supervisor, the runtime)
	// inherits whatever string this returns.
	msg := oauth.ScrubUpstreamText(err.Error())
	if msg == err.Error() {
		return err
	}
	return &urlRedactedError{msg: msg, cause: err}
}

// urlRedactedError re-renders an error's message with credentials removed and
// keeps the original reachable through Unwrap.
type urlRedactedError struct {
	msg   string
	cause error
}

func (e *urlRedactedError) Error() string { return e.msg }
func (e *urlRedactedError) Unwrap() error { return e.cause }

// logSafeErrorField renders an error as a log field with any URL-embedded
// credential removed. Use it instead of zap.Error on the connection paths.
// ScrubUpstreamText is used because a transport error is free-form upstream
// text that can carry a credential under any parameter name.
func logSafeErrorField(err error) zap.Field {
	if err == nil {
		return zap.Skip()
	}
	return zap.String("error", oauth.ScrubUpstreamText(err.Error()))
}

func (c *Client) recordConnectionFailure(err error) {
	if c.upstreamLogger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("transport", c.transportType),
		zap.Error(err),
	}
	if embedsChildOutput(err) {
		fields = append(fields, logs.ChildOutputField())
	}
	c.upstreamLogger.Error("Connection failed", fields...)
}

func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Reject concurrent connection attempts so no duplicate child is launched.
	if c.connecting {
		c.logger.Debug("Connection already in progress, rejecting concurrent attempt",
			zap.String("server", c.config.Name))
		return fmt.Errorf("connection already in progress")
	}

	// Allow reconnection if OAuth was recently completed (bypass "already connected" check)
	if c.connected && !c.wasOAuthRecentlyCompleted() {
		c.logger.Debug("Client already connected and OAuth not recent",
			zap.String("server", c.config.Name),
			zap.Bool("connected", c.connected))
		return fmt.Errorf("client already connected")
	}

	// Set connecting flag to prevent concurrent attempts
	c.connecting = true
	defer func() {
		c.connecting = false
	}()

	// A retired instance never starts a connection. This is only the early
	// exit: every launch, dial and write below is refused by the gate itself.
	if c.Gate().Retired() {
		return fmt.Errorf("connect refused: %w", dialgate.ErrRetired)
	}

	// Reset connection state for fresh connection attempt
	if c.connected {
		c.logger.Info("🔄 Reconnecting after OAuth completion",
			zap.String("server", c.config.Name))
		c.connected = false
		// Release the old stdio child from the gate before the close
		// reaps it: its process-group ID must not stay registered for a
		// retirement to signal.
		if c.stdioProc != nil {
			c.stdioProc.Untrack()
			c.stdioProc = nil
		}
		if c.client != nil {
			c.client.Close()
			c.client = nil
		}
	}

	// Start each attempt with an empty rate-limit slate, so a hint recorded by
	// an earlier attempt cannot re-park a reconnect that fails for an unrelated
	// reason. Whatever this attempt observes is recorded again.
	// A swap, not a Clear: a request still in flight on the superseded client
	// keeps the recorder it was built with instead of writing into this attempt.
	c.beginRetryAfterGeneration()

	// The strategy that wins THIS attempt is recorded by runAuthStrategies;
	// until then nothing is known about how (or whether) we are connected.
	c.authStrategy.Store("")

	c.logger.Info("Connecting to upstream MCP server",
		zap.String("server", c.config.Name),
		zap.String("url", c.logSafeURL()),
		zap.String("command", c.config.Command),
		zap.String("protocol", c.config.Protocol))

	// Determine transport type
	c.transportType = transport.DetermineTransportType(c.config)

	// Log to server-specific log file as well
	if c.upstreamLogger != nil {
		c.upstreamLogger.Info("Starting connection attempt",
			zap.String("transport", c.transportType),
			zap.String("url", c.logSafeURL()),
			zap.String("command", c.config.Command),
			zap.String("protocol", c.config.Protocol))
	}

	// Debug: Show transport type determination
	c.logger.Debug("🔍 Transport Type Determination",
		zap.String("server", c.config.Name),
		zap.String("command", c.config.Command),
		zap.String("url", c.logSafeURL()),
		zap.String("protocol", c.config.Protocol),
		zap.String("determined_transport", c.transportType))

	// Create and connect client based on transport type
	var err error
	if c.config.ExecutionHost() == "" {
		switch c.transportType {
		case transportHTTP, transportHTTPStreamable, transportSSE:
			if c.config.Command != "" {
				c.logger.Debug("🚀 Launching local upstream before HTTP/SSE connect",
					zap.String("server", c.config.Name),
					zap.String("transport", c.transportType))
				if launchErr := c.connectWithLauncher(ctx); launchErr != nil {
					return fmt.Errorf("failed to launch local upstream: %w", launchErr)
				}
			}
		}
	}
	if c.config.ExecutionHost() != "" {
		err = c.connectPeer(ctx)
	} else {
		switch c.transportType {
		case transportStdio:
			c.logger.Debug("📡 Using STDIO transport")
			err = c.connectStdio(ctx)
		case transportHTTP, transportHTTPStreamable:
			c.logger.Debug("🌐 Using HTTP transport")
			err = c.connectHTTP(ctx)
		case transportSSE:
			c.logger.Debug("📡 Using SSE transport")
			err = c.connectSSE(ctx)
		default:
			return fmt.Errorf("unsupported transport type: %s", c.transportType)
		}

	}

	if err != nil {
		// The transport error text embeds the configured URL, credentials and
		// all. Redact it here, before it is logged, returned, or stored as
		// the client's last error — every consumer downstream inherits the safe
		// rendering, and the original is still reachable through Unwrap.
		err = redactURLCredentialsInError(err)

		// Log connection failure to server-specific log
		c.recordConnectionFailure(err)

		// Clean up the process group so a failed connection leaves no zombies.
		if c.processGroupID > 0 {
			c.logger.Warn("Connection failed - cleaning up process group to prevent zombie processes",
				zap.String("server", c.config.Name),
				zap.Int("pgid", c.processGroupID))

			if err := killProcessGroup(c.processGroupID, c.logger, c.config.Name); err != nil {
				c.logger.Error("Failed to clean up process group after connection failure",
					zap.String("server", c.config.Name),
					zap.Int("pgid", c.processGroupID),
					zap.Error(err))
			}
			c.processGroupID = 0
		}
		if c.stdioProc != nil {
			c.stdioProc.Untrack()
			c.stdioProc = nil
		}

		// Stop any locally-launched upstream child the HTTP/SSE path
		// started — connectWithLauncher itself only stops it on
		// wait-for-url failure, not on subsequent transport-level
		// connect failure.
		//
		// IMPORTANT: c.mu is held for the duration of Connect (see
		// the c.mu.Lock at the top of this function), so we can read
		// the launcher fields directly. We release the lock briefly
		// around handle.Stop because Stop blocks until the child is
		// reaped and we don't want to hold c.mu that long; the
		// `connecting` flag already prevents a concurrent Connect.
		if c.launcherHandle != nil {
			handle := c.launcherHandle
			// The child stays tracked by the gate until Stop has reaped
			// it, so a retirement during the grace period kills it at
			// once.
			proc := c.launcherProc
			c.launcherHandle = nil
			c.launcherProc = nil
			c.mu.Unlock()
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if stopErr := handle.Stop(stopCtx); stopErr != nil {
				c.logger.Warn("error stopping launcher during connect-failure cleanup",
					zap.String("server", c.config.Name),
					zap.Error(stopErr))
			}
			stopCancel()
			proc.Untrack()

			c.mu.Lock()
		}

		return fmt.Errorf("failed to connect: %w", err)
	}

	// Each auth strategy runs both client.Start() and initialize(), so
	// connection and OAuth errors were already detected above.

	c.connected = true

	// The upstream answered, so any rate-limit window we were holding is spent.
	// Dropping it here keeps a stale hint from parking a future reconnect.
	c.ClearRetryAfter()

	// If we had an OAuth flow in progress and connection succeeded, mark OAuth as complete
	if c.isOAuthInProgress() {
		c.logger.Info("✅ OAuth flow completed successfully - connection established with token",
			zap.String("server", c.config.Name))
		c.markOAuthComplete()
	}

	c.logger.Info("Successfully connected to upstream MCP server",
		zap.String("server", c.config.Name),
		zap.String("transport", c.transportType))

	// Log successful connection to server-specific log
	if c.upstreamLogger != nil {
		if c.serverInfo != nil && c.serverInfo.ServerInfo.Name != "" {
			c.upstreamLogger.Info("Successfully connected and initialized",
				zap.String("transport", c.transportType),
				zap.String("server_name", c.serverInfo.ServerInfo.Name),
				zap.String("server_version", c.serverInfo.ServerInfo.Version),
				zap.String("protocol_version", c.serverInfo.ProtocolVersion))
		} else {
			c.upstreamLogger.Info("Successfully connected",
				zap.String("transport", c.transportType),
				zap.String("note", "serverInfo not yet available"))
		}
	}

	return nil
}
