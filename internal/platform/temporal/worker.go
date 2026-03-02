package temporal

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/sdk/worker"

	"sentinel/internal/platform/config"
)

// Registration installs a process's Workflow and Activity implementations.
// Register each type identically on every process polling the same task queue.
type Registration func(worker.Registry)

func RunWorker(ctx context.Context, cfg config.Temporal, taskQueue string, stopTimeout time.Duration, logger *slog.Logger, register Registration) error {
	c, err := Dial(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer c.Close()

	// The skeleton verifies connectivity without polling tasks before handlers exist.
	if register == nil {
		logger.Warn("worker skeleton connected; no handlers registered, task polling disabled", "task_queue", taskQueue)
		<-ctx.Done()
		return nil
	}

	fatalErrors := make(chan error, 1)
	w := worker.New(c, taskQueue, worker.Options{
		WorkerStopTimeout: stopTimeout,
		OnFatalError: func(err error) {
			select {
			case fatalErrors <- err:
			default:
			}
		},
	})
	register(w)
	if err := w.Start(); err != nil {
		return fmt.Errorf("start Temporal worker: %w", err)
	}
	defer w.Stop()
	logger.Info("Temporal worker started", "task_queue", taskQueue)

	select {
	case <-ctx.Done():
		return nil
	case err := <-fatalErrors:
		return fmt.Errorf("Temporal worker failed: %w", err)
	}
}
