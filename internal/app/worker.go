package app

import (
	"context"
	"log/slog"

	"go.temporal.io/sdk/worker"

	"sentinel/internal/platform/config"
	"sentinel/internal/platform/temporal"
)

func RunAgentWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	activities, closeRuntime, err := openAgentRuntime(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeRuntime()
	return temporal.RunWorker(ctx, cfg.Temporal, cfg.Temporal.AgentTaskQueue, cfg.ShutdownTimeout, logger, func(registry worker.Registry) {
		registerAgent(registry, activities)
	})
}

// Register source synchronization and read-only snapshot Activities here.
func RunSourceWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	return temporal.RunWorker(ctx, cfg.Temporal, cfg.Temporal.SourceTaskQueue, cfg.ShutdownTimeout, logger, nil)
}

// Register outbox and event-index Activities here when the relay is implemented.
func RunRelayWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	return temporal.RunWorker(ctx, cfg.Temporal, cfg.Temporal.RelayTaskQueue, cfg.ShutdownTimeout, logger, nil)
}
