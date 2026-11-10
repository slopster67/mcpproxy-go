package core

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/logs"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/upstream/launcher"
)

// defaultLauncherWaitTimeout caps how long we'll wait for a locally-launched
// HTTP/SSE upstream to bind its listener. Tight enough that a misconfigured
// command surfaces as a connect failure within a single retry window;
// configurable per-server via ServerConfig.LauncherWaitTimeout.
const defaultLauncherWaitTimeout = 30 * time.Second

func (c *Client) connectWithLauncher(ctx context.Context) error {
	if c.config.Command == "" {
		return nil // launcher not requested
	}

	if err := validateStdioConfig(c.config); err != nil {
		return fmt.Errorf("launcher config invalid: %w", err)
	}
	if c.config.URL == "" {
		return fmt.Errorf("launcher requires url for transport %q", c.transportType)
	}
	if err := validateWorkingDir(c.config.WorkingDir); err != nil {
		return fmt.Errorf("launcher working_dir invalid: %w", err)
	}

	cmd := c.buildLauncherCmd()
	spec := &launcher.Spec{Cmd: cmd, LogSink: newLoggerWriter(c.upstreamLogger, c.logger), Name: c.config.Name, RedactArgs: logSafeArgs}

	// The spawn and the gate's tracking of the child are one admitted section, so a retired instance launches
	// nothing and a launch admitted before retirement is killed by Retire.
	handle, err := c.spawnLauncherAdmitted(ctx, spec)
	if err != nil {
		return fmt.Errorf("launcher spawn: %w", err)
	}

	// Caller (Client.Connect) already holds c.mu for the duration of
	// Connect, so we set the launcher fields directly here. Read paths
	// (watchLauncher, Disconnect, stopLauncher) use proper locking
	// against this write via the same c.mu / c.mu.RLock.
	//
	// Deliberately NOT setting c.processCmd / c.processGroupID — the
	// launcher.Handle owns the child's lifecycle and the existing
	// stdio-path cleanup helpers would race with it. The launcher
	// already places the child in its own pgroup via applyProcAttrs.
	c.launcherHandle = handle

	// Watch the child for unexpected exit during connect/steady-state. If
	// the launched process dies after Connect returns we want to react
	// fast (mark disconnected) instead of waiting for the transport's
	// keepalive to time out.
	go c.watchLauncher(handle)

	waitTimeout := c.config.LauncherWaitTimeout.Duration()
	if waitTimeout <= 0 {
		waitTimeout = defaultLauncherWaitTimeout
	}

	c.logger.Info("waiting for upstream URL to become reachable",
		zap.String("server", c.config.Name),
		zap.String("url", c.logSafeURL()),
		zap.Duration("timeout", waitTimeout))

	// The reachability probe dials through the gate: a retired
	// instance stops probing its old endpoint.
	if err := launcher.WaitForURLWithDial(ctx, c.config.URL, waitTimeout, c.Gate().DialContext(launcherProbeDialer.DialContext)); err != nil {
		// Tear down the child we just started — caller will see a
		// failed Connect and the next attempt should start fresh.
		// Release c.mu around handle.Stop because it blocks until the
		// child is reaped; the `connecting` flag (set by Connect)
		// prevents a concurrent Connect from sneaking in. Reacquire
		// before returning so the caller's deferred Unlock balances.
		//
		// The child stays tracked by the gate until Stop has reaped it:
		// a retirement during Stop's grace period kills
		// the group at once instead of finding nothing to kill.
		proc := c.launcherProc
		c.launcherHandle = nil
		c.launcherProc = nil
		c.mu.Unlock()
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if stopErr := handle.Stop(stopCtx); stopErr != nil {
			c.logger.Warn("failed to stop launcher after wait-for-url failure",
				zap.String("server", c.config.Name),
				zap.Error(stopErr))
			// Stop gave up (context expired): hard-kill the recorded group
			// so a descendant the launcher left behind is not orphaned.
			_ = handle.Kill()
		}
		cancel()
		proc.Untrack()

		c.mu.Lock()
		return fmt.Errorf("launcher wait-for-url: %w", err)
	}

	c.logger.Info("upstream URL is reachable",
		zap.String("server", c.config.Name),
		zap.String("url", c.logSafeURL()))
	return nil
}

// stopLauncher tears down the spawned child, if any. Safe to call when no
// launcher is active. Called from DisconnectWithContext and from the
// connection-failure cleanup path in Connect.
func (c *Client) stopLauncher(ctx context.Context) {
	c.mu.Lock()
	handle := c.launcherHandle
	proc := c.launcherProc
	c.launcherHandle = nil
	c.launcherProc = nil
	c.mu.Unlock()
	defer proc.Untrack()

	if handle == nil {
		return
	}

	if err := handle.Stop(ctx); err != nil {
		c.logger.Warn("error stopping upstream child",
			zap.String("server", c.config.Name),
			zap.Error(err))
		// Stop gave up (context expired) before the group was down: Kill
		// signals the recorded process group, which Handle.Kill guards
		// against a reused group number, so a descendant left behind by a
		// launcher that exited first is not orphaned.
		_ = handle.Kill()
	}
}

// watchLauncher reacts to an unexpected child exit by closing the MCP
// client and clearing connected state. The reconnect loop in
// internal/upstream/manager.go can then take over.
func (c *Client) watchLauncher(handle launcher.Handle) {
	<-handle.Done()
	c.mu.RLock()
	stillOurs := c.launcherHandle == handle
	connected := c.connected
	c.mu.RUnlock()
	if !stillOurs {
		// Disconnect already cleaned up — the child exit is expected.
		return
	}
	if !connected {
		// We're still inside Connect; the wait-for-url path will see
		// the failure on its own (TCP dial will eventually fail).
		return
	}
	c.logger.Warn("upstream child exited unexpectedly, disconnecting",
		zap.String("server", c.config.Name))
	if err := c.Disconnect(); err != nil {
		c.logger.Error("disconnect after unexpected child exit failed",
			zap.String("server", c.config.Name),
			zap.Error(err))
	}
}

func (c *Client) buildLauncherCmd() *exec.Cmd {
	envVars := c.envManager.BuildSecureEnvironment()
	finalCommand, finalArgs := c.config.Command, c.config.Args
	cmd := exec.Command(finalCommand, finalArgs...)
	cmd.Env = envVars
	if c.config.WorkingDir != "" {
		cmd.Dir = c.config.WorkingDir
	}
	c.logger.Debug("launcher command prepared",
		zap.String("server", c.config.Name),
		zap.String("command", finalCommand),
		zap.Strings("args", logSafeArgs(finalArgs)),
		zap.String("working_dir", c.config.WorkingDir))
	return cmd
}

type loggerWriter struct {
	primary  *zap.Logger
	fallback *zap.Logger
}

func newLoggerWriter(primary, fallback *zap.Logger) io.Writer {
	if primary == nil && fallback == nil {
		return io.Discard
	}
	return &loggerWriter{primary: primary, fallback: fallback}
}

func (w *loggerWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		w.writeLine(strings.TrimRight(line, "\r"))
	}
	return len(p), nil
}

func (w *loggerWriter) writeLine(line string) {
	if line == "" {
		return
	}
	line = oauth.ScrubUpstreamText(line)
	switch {
	case w.primary != nil:
		w.primary.Info("launcher", zap.String("message", line), logs.ChildOutputField())
	case w.fallback != nil:
		w.fallback.Info("launcher", zap.String("message", line), logs.ChildOutputField())
	}
}

// launcherProbeDialer is the per-attempt dialer of the launcher's
// reachability probe; the gate wraps it.
var launcherProbeDialer = &net.Dialer{Timeout: 2 * time.Second}

// spawnLauncherAdmitted spawns the launcher child inside one admitted
// section of the instance's gate and registers it with the gate there. A
// child the gate refuses to track (retired during the section) is killed
// before this returns.
func (c *Client) spawnLauncherAdmitted(ctx context.Context, spec *launcher.Spec) (launcher.Handle, error) {
	gate := c.Gate()
	release, err := gate.Admit(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	handle, err := launcher.Spawn(ctx, spec, c.logger)
	if err != nil {
		return nil, err
	}
	proc, err := gate.TrackProcess(func() { _ = handle.Kill() })
	if err != nil {
		return nil, err
	}
	c.launcherProc = proc
	return handle, nil
}
