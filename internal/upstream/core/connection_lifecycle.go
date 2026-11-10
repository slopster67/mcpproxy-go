package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/v2/client"
	"github.com/mark3labs/mcp-go/v2/mcp"
	"github.com/slopster67/mcpproxy-go/internal/diagnostics"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"go.uber.org/zap"
)

// initialize performs MCP initialization handshake
func (c *Client) initialize(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	initRequest := mcp.InitializeRequest{}
	// Pinned to the newest pre-2026-07-28 revision rather than
	// mcp.LATEST_PROTOCOL_VERSION (2026-07-28): in that era Ping is a no-op (so
	// health probes stop proving liveness) and server-initiated requests are
	// gone (so roots-based workspace discovery cannot work).
	initRequest.Params.ProtocolVersion = mcp.LATEST_LEGACY_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "mcpproxy-go",
		Version: "1.0.0",
	}
	initRequest.Params.Capabilities = mcp.ClientCapabilities{}

	// Log request for trace debugging - use main logger for CLI debug mode
	if reqBytes, err := json.MarshalIndent(initRequest, "", "  "); err == nil {
		c.logger.Debug("🔍 JSON-RPC INITIALIZE REQUEST",
			zap.String("method", "initialize"),
			zap.String("formatted_json", string(reqBytes)))
	}

	initStart := time.Now()
	serverInfo, err := c.client.Initialize(ctx, initRequest)
	if err != nil {
		// Log initialization failure to server-specific log
		if c.upstreamLogger != nil {
			// The transport error quotes the configured URL,
			// credentials and all, and this line is written to the per-server
			// log FILE which `upstream_servers tail_log` hands to any MCP
			// caller.
			c.upstreamLogger.Error("MCP initialization failed",
				logSafeErrorField(err))
		}

		if c.transportType != transportStdio && isOAuthHeaderFailure(err) {
			return diagnostics.WrapError(diagnostics.OAuthTokenUnavailable, fmt.Errorf("%s: %w", diagnostics.OAuthCredentialsUnavailableMessage, err))
		}

		// Surface the useful context that the raw "context deadline exceeded"
		// swallows: how long we waited and whatever the child process wrote
		// to stderr before dying. Non-timeout errors pass through unchanged.
		if errors.Is(err, context.DeadlineExceeded) {
			return c.initializeTimeout(err, time.Since(initStart))
		}

		// STDIO subprocess exited before completing the handshake: mcp-go reports
		// a closed transport / EOF on the pipe (not a typed exit error). Surface
		// the captured stderr so the user sees the real, often self-serviceable
		// cause (e.g. "Error: --brave-api-key is required") instead of a bare
		// "transport closed" that the diagnostics layer marks UNKNOWN. Gated to
		// stdio: initialize() is shared by HTTP/SSE transports, which have no
		// local subprocess and must keep their generic diagnostics.
		if c.transportType == transportStdio && isTransportClosedErr(err) {
			return enrichTransportClosedError(c.formatRecentStderr(), err)
		}

		return err
	}

	// Log response for trace debugging - use main logger for CLI debug mode
	if respBytes, err := json.MarshalIndent(serverInfo, "", "  "); err == nil {
		c.logger.Debug("🔍 JSON-RPC INITIALIZE RESPONSE",
			zap.String("method", "initialize"),
			zap.String("formatted_json", string(respBytes)))
	}

	// The pin above controls what mcpproxy asks for; this checks what it got.
	// mcp-go accepts a 2026-07-28 answer to an older request and applies it, so
	// a server answering with that era would silently flip this hop to it. A
	// compliant server answers with a version the client can speak, so this
	// fires only for a misbehaving upstream.
	if negotiated := serverInfo.ProtocolVersion; mcp.IsModernProtocol(negotiated) {
		return fmt.Errorf("upstream answered MCP protocol version %s to a %s request; mcpproxy does not yet serve the 2026-07-28 era on the upstream hop",
			negotiated, initRequest.Params.ProtocolVersion)
	}

	c.serverInfo = serverInfo
	c.logger.Info("MCP initialization successful",
		zap.String("server_name", serverInfo.ServerInfo.Name),
		zap.String("server_version", serverInfo.ServerInfo.Version))

	// Log initialization success to server-specific log
	if c.upstreamLogger != nil {
		c.upstreamLogger.Info("MCP initialization completed successfully",
			zap.String("server_name", serverInfo.ServerInfo.Name),
			zap.String("server_version", serverInfo.ServerInfo.Version),
			zap.String("protocol_version", serverInfo.ProtocolVersion))
	}

	return nil
}

// enrichTransportClosedError builds the actionable error for a stdio subprocess
// that exited before completing the MCP initialize handshake, folding in the
// captured stderr tail so the UI banner and per-server logs show the real,
// often self-serviceable cause (e.g. a missing API key) instead of a bare
// "transport closed". It wraps the original cause with %w so callers can still
// errors.Is/As it.
//
// The child exit code is not surfaced: on this path mcp-go has not reaped the
// process, so ProcessState is unset and any exit code would be unreliable.
func enrichTransportClosedError(stderrBlock string, cause error) error {
	if stderrBlock != "" {
		return &childOutputError{
			msg:   fmt.Sprintf("server process exited before completing the MCP initialize handshake; recent stderr:\n%s: %v", stderrBlock, cause),
			cause: cause,
		}
	}
	return fmt.Errorf("server process exited before completing the MCP initialize handshake and produced no stderr output (transport closed before the handshake): %w", cause)
}

type childOutputError struct {
	msg   string
	cause error
}

func (e *childOutputError) Error() string { return e.msg }
func (e *childOutputError) Unwrap() error { return e.cause }

// embedsChildOutput reports whether err, anywhere in its chain, re-emits the
// child's stderr.
func embedsChildOutput(err error) bool {
	var target *childOutputError
	return errors.As(err, &target)
}

// isTransportClosedErr reports whether an initialize() failure indicates the
// child process went away mid-handshake. mcp-go surfaces a premature stdio
// exit as a closed transport / EOF on the pipe rather than a typed exit error,
// so we match those shapes to distinguish "the process died" from a genuine
// malformed-handshake response.
func isTransportClosedErr(err error) bool {
	if errors.Is(err, io.EOF) {
		return true
	}
	lmsg := strings.ToLower(err.Error())
	return strings.Contains(lmsg, "transport closed") ||
		strings.Contains(lmsg, "broken pipe") ||
		strings.Contains(lmsg, "file already closed") ||
		strings.Contains(lmsg, "use of closed")
}

// registerNotificationHandler registers a handler for MCP notifications.
// This should be called after client.Start() and initialize() succeed.
// It handles notifications/tools/list_changed to trigger reactive tool discovery.
func (c *Client) registerNotificationHandler() {
	if c.client == nil {
		c.logger.Debug("Skipping notification handler registration - client is nil",
			zap.String("server", c.config.Name))
		return
	}

	c.client.OnNotification(func(notification mcp.JSONRPCNotification) {
		switch notification.Method {
		case string(mcp.MethodNotificationToolsListChanged):
			c.handleToolsListChangedNotification()
		default:
			// Ignore all other notifications (logging, resources, progress, ...).
		}
	})

	// Log capability status after registration
	if c.serverInfo != nil && c.serverInfo.Capabilities.Tools != nil && c.serverInfo.Capabilities.Tools.ListChanged {
		c.logger.Debug("Server supports tool change notifications - registered handler",
			zap.String("server", c.config.Name))
	} else {
		c.logger.Debug("Server does not advertise tool change notifications support",
			zap.String("server", c.config.Name))
	}
}

// handleToolsListChangedNotification forwards a notifications/tools/list_changed
// signal to the onToolsChanged callback. serverInfo and the callback are read
// under the same RLock.
func (c *Client) handleToolsListChangedNotification() {
	c.logger.Info("Received tools/list_changed notification from upstream server",
		zap.String("server", c.config.Name))

	c.mu.RLock()
	serverInfo := c.serverInfo
	callback := c.onToolsChanged
	c.mu.RUnlock()

	if serverInfo != nil && serverInfo.Capabilities.Tools != nil && serverInfo.Capabilities.Tools.ListChanged {
		c.logger.Debug("Server advertised tools.listChanged capability",
			zap.String("server", c.config.Name))
	} else {
		c.logger.Warn("Received tools notification from server that did not advertise listChanged capability",
			zap.String("server", c.config.Name))
	}

	if callback != nil {
		callback(c.config.Name)
	} else {
		c.logger.Debug("No onToolsChanged callback set - notification ignored",
			zap.String("server", c.config.Name))
	}
}

// Disconnect closes the connection
func (c *Client) Disconnect() error {
	return c.DisconnectWithContext(context.Background())
}

// DisconnectWithContext closes the connection with context timeout
func (c *Client) DisconnectWithContext(_ context.Context) error {
	// Step 1: Read state under lock, then release for I/O operations
	c.mu.Lock()
	wasConnected := c.connected
	mcpClient := c.client
	pgid := c.processGroupID
	processCmd := c.processCmd
	serverName := c.config.Name
	stdioProc := c.stdioProc
	c.stdioProc = nil
	c.mu.Unlock()

	c.logger.Info("Disconnecting from upstream MCP server",
		zap.Bool("was_connected", wasConnected))

	if c.upstreamLogger != nil {
		c.upstreamLogger.Info("Disconnecting from server",
			zap.Bool("was_connected", wasConnected))
	}

	// Step 2: Stop monitoring (these have their own locks)
	c.StopStderrMonitoring()

	// The stdio child is this teardown's from here: the gate stops
	// tracking it BEFORE the close below reaps it. Once reaped, its process-group ID may be handed to an
	// unrelated process, which a retirement's kill must never signal. A
	// retirement before this line kills the group itself; after it, the
	// close and the force-kill below end the child, and the gate already
	// refuses every write to it.
	stdioProc.Untrack()

	// Step 4: Try graceful close via MCP client FIRST
	// This gives the subprocess a chance to exit cleanly via stdin/stdout close
	gracefulCloseSucceeded := false
	if mcpClient != nil {
		c.logger.Debug("Attempting graceful MCP client close",
			zap.String("server", serverName))

		closeDone := make(chan struct{})
		go func() {
			mcpClient.Close()
			close(closeDone)
		}()

		select {
		case <-closeDone:
			c.logger.Debug("MCP client closed gracefully",
				zap.String("server", serverName))
			gracefulCloseSucceeded = true
		case <-time.After(mcpClientCloseTimeout):
			c.logger.Warn("MCP client close timed out",
				zap.String("server", serverName),
				zap.Duration("timeout", mcpClientCloseTimeout))
		}
	}

	if !gracefulCloseSucceeded && pgid > 0 {
		c.logger.Info("Graceful close failed, force killing process group",
			zap.String("server", serverName),
			zap.Int("pgid", pgid))

		if err := killProcessGroup(pgid, c.logger, serverName); err != nil {
			c.logger.Error("Failed to kill process group",
				zap.String("server", serverName),
				zap.Int("pgid", pgid),
				zap.Error(err))
		}

		// Also try direct process kill as last resort
		if processCmd != nil && processCmd.Process != nil {
			if err := processCmd.Process.Kill(); err != nil {
				c.logger.Debug("Direct process kill failed (may already be dead)",
					zap.String("server", serverName),
					zap.Error(err))
			}
		}
	}

	// Step 6: Stop any locally-launched HTTP/SSE upstream. We do this
	// AFTER closing the MCP client — the child should see the network
	// transport go away first, giving it a clean shutdown signal before
	// we send SIGTERM.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	c.stopLauncher(stopCtx)
	stopCancel()

	// Step 7: Update state under lock
	c.mu.Lock()
	c.client = nil
	c.serverInfo = nil
	c.connected = false
	c.authStrategy.Store("")
	c.processGroupID = 0
	c.processCmd = nil
	c.mu.Unlock()

	c.closeUpstreamLog()

	c.logger.Debug("Disconnect completed successfully",
		zap.String("server", serverName))
	return nil
}

// closeUpstreamLog syncs and closes the upstream log sink, if the logger has
// one. Safe to call repeatedly: the sink reopens itself on the next write.
func (c *Client) closeUpstreamLog() {
	if c.upstreamLogger != nil {
		_ = c.upstreamLogger.Sync()
	}
	if c.upstreamLogCloser != nil {
		if err := c.upstreamLogCloser.Close(); err != nil {
			c.logger.Debug("Failed to close upstream log sink", zap.Error(err))
		}
	}
}

// initializeTimeout keeps subprocess diagnostics exclusive to stdio and retains
// the original deadline error for retry policy and authentication fallback.
func (c *Client) initializeTimeout(cause error, elapsed time.Duration) error {
	waited := elapsed.Round(100 * time.Millisecond)
	if c.transportType != transportStdio {
		return diagnostics.WrapError(diagnostics.HTTPTimeout, fmt.Errorf("MCP initialization timed out after %s: %w", waited, cause))
	}
	stderr := c.formatRecentStderr()
	message := fmt.Sprintf("server did not respond to MCP initialize within %s", waited)
	if stderr != "" {
		return diagnostics.WrapError(diagnostics.STDIOHandshakeTimeout, &childOutputError{msg: message + "; recent stderr:\n" + stderr, cause: cause})
	}
	return diagnostics.WrapError(diagnostics.STDIOHandshakeTimeout, fmt.Errorf("%s: %w", message, cause))
}

// SDK errors at this stage mean an OAuth header could not be obtained; no MCP
// request has been sent. Refresh cooldowns can wrap an earlier deadline error,
// so they must not be presented as a new zero-second handshake timeout.
func isOAuthHeaderFailure(err error) bool {
	if err == nil || client.IsOAuthAuthorizationRequiredError(err) {
		return false
	}
	return errors.Is(err, oauth.ErrTokenRefreshTransient) || errors.Is(err, oauth.ErrNoClientCredentials) ||
		strings.Contains(err.Error(), "failed to get authorization header:")
}
