package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/mark3labs/mcp-go/v2/client"
	uptransport "github.com/mark3labs/mcp-go/v2/client/transport"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/diagnostics"
	"github.com/slopster67/mcpproxy-go/internal/upstream/dialgate"
	"go.uber.org/zap"
)

// packageRunnerNoArgs lists commands that behave as "runner for a package
// named on the command line" — invoking them with no args prints help and
// exits, which manifests downstream as an opaque "context deadline exceeded"
// on MCP initialize. We fail fast instead.
var packageRunnerNoArgs = map[string]string{
	"uvx":  "the Python package to run (e.g. [\"obsidian-mcp\"])",
	"npx":  "the npm package to run (e.g. [\"-y\", \"some-mcp-server\"])",
	"pipx": "the subcommand and package (e.g. [\"run\", \"obsidian-mcp\"])",
}

// validateStdioConfig runs cheap pre-flight checks on a stdio server
// configuration before launching a subprocess.
func validateStdioConfig(cfg *config.ServerConfig) error {
	if cfg.Command == "" {
		return fmt.Errorf("no command specified for stdio transport")
	}
	if len(cfg.Args) == 0 {
		if hint, ok := packageRunnerNoArgs[cfg.Command]; ok {
			// Attributed with WrapError rather than left to the classifier's
			// free-text fallback. This is mcpproxy's OWN pre-spawn verdict —
			// no subprocess has run, so nothing here can be a child's stderr —
			// which makes it the kind of structured signal the retry policy is
			// allowed to park a server on (diagnostics.ParkableCode).
			return diagnostics.WrapError(diagnostics.ConfigInvalidCommand,
				fmt.Errorf("server %q: command %q has no args — %s is required", cfg.Name, cfg.Command, hint))
		}
	}
	return nil
}

func (c *Client) connectStdio(ctx context.Context) error {
	if err := validateStdioConfig(c.config); err != nil {
		return err
	}

	// Validate working directory if specified
	if err := validateWorkingDir(c.config.WorkingDir); err != nil {
		// Log warning to both main logger and server-specific logger
		c.logger.Error("Invalid working directory for stdio server",
			zap.String("server", c.config.Name),
			zap.String("working_dir", c.config.WorkingDir),
			zap.Error(err))

		if c.upstreamLogger != nil {
			c.upstreamLogger.Error("Server startup failed due to invalid working directory",
				zap.String("working_dir", c.config.WorkingDir),
				zap.Error(err))
		}

		return fmt.Errorf("invalid working directory for server %s: %w", c.config.Name, err)
	}

	// Build environment variables using secure environment manager
	envVars := c.envManager.BuildSecureEnvironment()

	finalCommand, finalArgs := c.config.Command, c.config.Args

	// The command func places the child in its own process group and records
	// it on c (processCmd, processGroupID) for lifecycle management.
	commandFunc := createProcessGroupCommandFunc(c, c.config.WorkingDir, c.logger)
	stdioTransport := uptransport.NewStdioWithOptions(finalCommand, envVars, finalArgs,
		uptransport.WithCommandFunc(commandFunc))

	// The mcp-go transport is wrapped by the instance's gate, so a retired instance sends no request or
	// notification on the child's stdin.
	c.client = client.NewClient(dialgate.WrapTransport(c.Gate(), stdioTransport))

	// Log final stdio configuration for debugging
	c.logger.Debug("Initialized stdio transport",
		zap.String("server", c.config.Name),
		zap.String("command", finalCommand),
		zap.Strings("args", logSafeArgs(finalArgs)),
		zap.String("working_dir", c.config.WorkingDir))

	if err := c.startStdioAdmitted(ctx); err != nil {
		return fmt.Errorf("failed to start stdio client: %w", err)
	}

	// Start stderr monitoring immediately after the process starts, so startup
	// errors (like missing API keys) are captured even if initialization fails.
	c.stderr = stdioTransport.Stderr()
	if c.stderr != nil {
		c.StartStderrMonitoring()
		c.logger.Debug("Started early stderr monitoring to capture startup errors",
			zap.String("server", c.config.Name))
	}

	if err := c.initialize(ctx); err != nil {
		// Clean up the process group so a failed initialization leaves no zombies.
		if c.processGroupID > 0 {
			c.logger.Warn("Initialization failed - cleaning up process group to prevent zombie processes",
				zap.String("server", c.config.Name),
				zap.Int("pgid", c.processGroupID))

			if err := killProcessGroup(c.processGroupID, c.logger, c.config.Name); err != nil {
				c.logger.Error("Failed to clean up process group after initialization failure",
					zap.String("server", c.config.Name),
					zap.Int("pgid", c.processGroupID),
					zap.Error(err))
			}
			c.processGroupID = 0
		}

		return fmt.Errorf("stdio transport (command=%q): %w", finalCommand, err)
	}

	if c.processCmd != nil && c.processCmd.Process != nil {
		if c.processGroupID <= 0 {
			c.processGroupID = extractProcessGroupID(c.processCmd, c.logger, c.config.Name)
		}
		c.logger.Info("Stdio process tracked for lifecycle management",
			zap.String("server", c.config.Name),
			zap.Int("pid", c.processCmd.Process.Pid),
			zap.Int("pgid", c.processGroupID))
	}

	// Register notification handler for tools/list_changed
	c.registerNotificationHandler()

	return nil
}

func hasCommand(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

// validateWorkingDir checks if the working directory exists and is accessible
// Returns error if directory doesn't exist or isn't accessible
func validateWorkingDir(workingDir string) error {
	if workingDir == "" {
		// Empty working directory is valid (uses current directory)
		return nil
	}

	fi, err := os.Stat(workingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("working directory does not exist: %s", workingDir)
		}
		return fmt.Errorf("cannot access working directory %s: %w", workingDir, err)
	}

	if !fi.IsDir() {
		return fmt.Errorf("working directory path is not a directory: %s", workingDir)
	}

	return nil
}

func (c *Client) startStdioAdmitted(ctx context.Context) error {
	gate := c.Gate()
	release, err := gate.Admit(ctx)
	if err != nil {
		return err
	}
	defer release()

	if err := c.client.Start(gate.Context()); err != nil {
		return err
	}
	if c.processCmd == nil || c.processCmd.Process == nil {
		return nil
	}

	// The child leads its own process group (Setpgid at start); 0 means the
	// group could not be read because the child already died.
	c.processGroupID = extractProcessGroupID(c.processCmd, c.logger, c.config.Name)
	if c.processGroupID <= 0 {
		// The child may already be dead (a retirement during the section
		// cancelled the gate context, which kills the immediate child):
		// its group, and the descendants in it, still need the kill.
		c.processGroupID = startedProcessGroupID(c.processCmd)
	}
	pgid, cmd := c.processGroupID, c.processCmd
	proc, err := gate.TrackProcess(func() { killProcessTreeNow(pgid, cmd) })
	if err != nil {
		// Retired while the section ran: TrackProcess already killed the
		// group. Leave the section, then reap the child through the
		// transport's own Close so no zombie is left behind. The recorded
		// group ID is cleared first: once the child is
		// reaped the number can be handed out again, and Connect's failure
		// cleanup would otherwise signal it unchecked.
		c.processGroupID = 0
		release()
		_ = c.client.Close()
		return err
	}
	c.stdioProc = proc
	return nil
}
