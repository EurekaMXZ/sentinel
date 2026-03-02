// Package postgres contains PostgreSQL adapters for indexes, CAS, and idempotency.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"sentinel/internal/platform/config"
)

// Open returns a connected pool. The caller must close it.
func Open(ctx context.Context, cfg config.Postgres) (*pgxpool.Pool, error) {
	if cfg.DSN == "" {
		return nil, errors.New("POSTGRES_DSN is required")
	}
	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return pool, nil
}
