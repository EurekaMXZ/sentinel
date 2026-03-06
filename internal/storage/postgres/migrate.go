package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"sentinel/internal/platform/config"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// OpenMigrations opens an independent migration connection. The caller must close
// the returned provider. API and Worker startup never runs migrations implicitly.
func OpenMigrations(ctx context.Context, cfg config.Postgres) (*goose.Provider, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, errors.New("POSTGRES_DSN is required")
	}
	connConfig, err := pgx.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse migration database configuration: %w", err)
	}
	db := stdlib.OpenDB(*connConfig)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to migration database: %w", err)
	}
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockTimeout(1, 60),
		lock.WithUnlockTimeout(1, 5),
	)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("configure migration lock: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files,
		goose.WithTableName("sentinel_schema_version"),
		goose.WithSessionLocker(locker),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("configure migrations: %w", err)
	}
	return provider, nil
}
