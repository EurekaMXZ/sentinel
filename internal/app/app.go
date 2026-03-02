// Package app assembles process entrypoints and owns their lifecycle.
package app

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"sentinel/internal/platform/config"
)

type Runner func(context.Context, config.Config, *slog.Logger) error

func Main(service string, run Runner) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("process stopped", "error", err)
		os.Exit(1)
	}
}
