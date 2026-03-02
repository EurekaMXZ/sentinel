package app

import (
	"context"
	"log/slog"

	"sentinel/internal/platform/config"
	"sentinel/internal/platform/temporal"
)

// Register the Agent Workflows and Activities here when the runtime is implemented.
func RunAgentWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	return temporal.RunWorker(ctx, cfg.Temporal, cfg.Temporal.AgentTaskQueue, cfg.ShutdownTimeout, logger, nil)
}

// Register source synchronization and read-only snapshot Activities here.
func RunSourceWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	return temporal.RunWorker(ctx, cfg.Temporal, cfg.Temporal.SourceTaskQueue, cfg.ShutdownTimeout, logger, nil)
}

// Register outbox and event-index Activities here when the relay is implemented.
func RunRelayWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	return temporal.RunWorker(ctx, cfg.Temporal, cfg.Temporal.RelayTaskQueue, cfg.ShutdownTimeout, logger, nil)
}
