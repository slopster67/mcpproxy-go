package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/branding"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/logs"
	"github.com/slopster67/mcpproxy-go/internal/server"
)

var version = "v0.1.0" // Injected at build time.

func main() {
	if err := runCommand(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(classifyError(err))
	}
}

// runCommand selects the process mode. Deployment options are environment-only.
func runCommand(args []string, out io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) || (len(args) == 2 && (args[0] == "serve" || args[0] == "peer") && (args[1] == "--help" || args[1] == "-h")) {
		fmt.Fprintf(out, "MCPProxy\n\nUsage: mcpproxy serve | peer\n\nAll settings use MCPPROXY_* environment variables. Secret fields also accept _FILE inputs.\n\n%s\n%s\n", branding.Repo, branding.Docs)
		return nil
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		fmt.Fprintf(out, "MCPProxy %s\n", version)
		return nil
	}
	if len(args) != 1 {
		return fmt.Errorf("expected only serve or peer; settings must be provided through environment variables")
	}
	switch args[0] {
	case "serve":
		return runServer()
	case "peer":
		return runPeer()
	default:
		return fmt.Errorf("unknown command; use mcpproxy --help")
	}
}

func runServer() error {
	cfg, err := config.Load(config.Primary)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	logging := cfg.EffectiveLogConfig()
	logger, err := logs.SetupLogger(logging)
	if err != nil {
		return fmt.Errorf("failed to setup logger: %w", err)
	}
	defer func() {
		_ = logger.Sync()
	}()

	logger.Info("Log directory configured", zap.String("path", logging.LogDir))

	logger.Info("Starting mcpproxy",
		zap.String("version", version),
		zap.String("log_level", logging.Level),
		zap.Bool("log_to_file", logging.EnableFile))

	server.SetMCPServerVersion(version)

	logger.Info("Configuration loaded",
		zap.String("data_dir", cfg.DataDir),
		zap.Int("servers_count", len(cfg.Servers)))

	srv, err := server.NewServer(cfg, logger)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	// Setup signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// The received signal is recorded in the system_stop activity event.
	var receivedSignal atomic.Value
	receivedSignal.Store("")

	go runSignalHandler(sigChan, cancel, func(sig os.Signal) { receivedSignal.Store(sig.String()) }, logger)

	logger.Info("Starting mcpproxy server")
	if err := srv.StartServer(ctx); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	// Wait for context cancellation (signal) or a fatal serve failure
	select {
	case <-ctx.Done():
		logger.Info("Shutting down server")
		srv.SetShutdownInfo("signal", receivedSignal.Load().(string))

		if err := srv.Shutdown(); err != nil {
			logger.Error("Error shutting down server", zap.Error(err))
		}
		return nil
	case err := <-srv.ServeErr():
		logger.Error("Server failed, shutting down", zap.Error(err))
		srv.SetShutdownInfo("error", "")
		if shutdownErr := srv.Shutdown(); shutdownErr != nil {
			logger.Error("Error shutting down server", zap.Error(shutdownErr))
		}
		return fmt.Errorf("server failed: %w", err)
	}
}

// classifyError categorizes errors to return appropriate exit codes
func classifyError(err error) int {
	// Check for port conflict errors
	var portErr *server.PortInUseError
	if errors.As(err, &portErr) {
		return ExitCodePortConflict
	}

	// Check for permission errors (exit code 5)
	var permErr *server.PermissionError
	if errors.As(err, &permErr) {
		return ExitCodePermissionError
	}

	// Check for string-based error messages from various sources
	errMsg := strings.ToLower(err.Error())

	// Port conflict indicators
	if strings.Contains(errMsg, "address already in use") ||
		strings.Contains(errMsg, "port") && strings.Contains(errMsg, "in use") ||
		strings.Contains(errMsg, "bind: address already in use") {
		return ExitCodePortConflict
	}

	// Database lock indicators
	if strings.Contains(errMsg, "database is locked") ||
		strings.Contains(errMsg, "database locked") ||
		strings.Contains(errMsg, "bolt") && strings.Contains(errMsg, "timeout") {
		return ExitCodeDBLocked
	}

	// Configuration error indicators
	if strings.Contains(errMsg, "invalid configuration") ||
		strings.Contains(errMsg, "config") && (strings.Contains(errMsg, "invalid") || strings.Contains(errMsg, "error")) ||
		strings.Contains(errMsg, "failed to load configuration") {
		return ExitCodeConfigError
	}

	// Permission error indicators (fallback for string-based errors)
	if strings.Contains(errMsg, "permission denied") ||
		strings.Contains(errMsg, "access denied") ||
		strings.Contains(errMsg, "operation not permitted") {
		return ExitCodePermissionError
	}

	// Default to general error
	return ExitCodeGeneralError
}
