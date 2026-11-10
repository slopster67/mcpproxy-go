package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/logs"
	"github.com/slopster67/mcpproxy-go/internal/peeragent"
)

func runPeer() error {
	cfg, err := config.Load(config.Peer)
	if err != nil {
		return err
	}
	logger, err := logs.SetupLogger(cfg.EffectiveLogConfig())
	if err != nil {
		return err
	}
	defer logger.Sync()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return peeragent.Run(ctx, cfg, logger)
}
