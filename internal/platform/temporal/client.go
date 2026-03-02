// Package temporal adapts the Temporal SDK and manages Worker lifecycle.
package temporal

import (
	"context"
	"fmt"
	"log/slog"

	"go.temporal.io/sdk/client"
	temporallog "go.temporal.io/sdk/log"

	"sentinel/internal/platform/config"
)

func Dial(ctx context.Context, cfg config.Temporal, logger *slog.Logger) (client.Client, error) {
	dialCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	c, err := client.DialContext(dialCtx, client.Options{
		HostPort:  cfg.Address,
		Namespace: cfg.Namespace,
		Logger:    temporallog.NewStructuredLogger(logger),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to Temporal: %w", err)
	}
	return c, nil
}
