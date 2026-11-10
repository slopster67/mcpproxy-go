package main

import (
	"context"
	"os"
	"time"

	"go.uber.org/zap"
)

const (
	// shutdownGraceWindow is how long we wait before reminding the operator
	// that the force-quit shortcut is still available. It does not bound the
	// shutdown itself.
	shutdownGraceWindow = 10 * time.Second

	shutdownHardDeadline = 60 * time.Second
)

// runSignalHandler starts a graceful shutdown on the first signal. A second
// signal, or the hard deadline, exits the process immediately.
//
// onSignal and cancel must be fast and non-blocking. Both run before the
// forcer goroutine starts, so a second signal already buffered in sigChan
// cannot end the process before the first one is recorded and graceful
// shutdown has begun. Logging happens only after the timers are armed and the
// forcer is running, so a blocked log sink cannot delay a forced exit.
func runSignalHandler(sigChan <-chan os.Signal, cancel context.CancelFunc, onSignal func(os.Signal), logger *zap.Logger) {
	sig := <-sigChan
	started := time.Now()
	graceWindow := time.After(shutdownGraceWindow)
	hardDeadline := time.After(shutdownHardDeadline)

	onSignal(sig)
	cancel()

	go func() {
		select {
		case <-sigChan:
			os.Exit(ExitCodeGeneralError)
		case <-hardDeadline:
			os.Exit(ExitCodeShutdownTimeout)
		}
	}()

	logger.Info("Received signal, shutting down", zap.String("signal", sig.String()))
	logger.Info("Press Ctrl+C again to force quit",
		zap.Int("force_quit_exit_code", ExitCodeGeneralError),
		zap.Duration("forced_exit_after", shutdownHardDeadline),
		zap.Int("forced_exit_code", ExitCodeShutdownTimeout))
	_ = logger.Sync()

	<-graceWindow
	logger.Info("Graceful shutdown still in progress, press Ctrl+C again to force quit",
		zap.Duration("elapsed", time.Since(started)),
		zap.Duration("forced_exit_after", shutdownHardDeadline))
	_ = logger.Sync()
}
