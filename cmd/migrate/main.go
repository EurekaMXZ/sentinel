// Command migrate applies or rolls back the embedded PostgreSQL schema.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/pressly/goose/v3"

	"sentinel/internal/app"
	"sentinel/internal/platform/config"
	"sentinel/internal/storage/postgres"
)

func main() {
	timeout := flag.Duration("timeout", 5*time.Minute, "maximum time for the migration command")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Usage: migrate [-timeout 5m] <up|status|down>")
		fmt.Fprintln(flag.CommandLine.Output(), "POSTGRES_DSN selects the database. down rolls back one migration and deletes its data.")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 || *timeout <= 0 {
		flag.Usage()
		os.Exit(2)
	}
	command := flag.Arg(0)
	if command != "up" && command != "status" && command != "down" {
		flag.Usage()
		os.Exit(2)
	}
	app.Main("migrate", func(ctx context.Context, cfg config.Config, logger *slog.Logger) (retErr error) {
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		provider, err := postgres.OpenMigrations(ctx, cfg.Postgres)
		if err != nil {
			return err
		}
		defer func() { retErr = errors.Join(retErr, provider.Close()) }()
		switch command {
		case "up":
			results, err := provider.Up(ctx)
			if err != nil {
				return fmt.Errorf("apply migrations: %w", err)
			}
			for _, result := range results {
				logger.Info("migration applied", "version", result.Source.Version, "file", result.Source.Path, "duration", result.Duration)
			}
			if len(results) == 0 {
				logger.Info("schema is up to date")
			}
		case "status":
			statuses, err := provider.Status(ctx)
			if err != nil {
				return fmt.Errorf("read migration status: %w", err)
			}
			for _, status := range statuses {
				logger.Info("migration status", "version", status.Source.Version, "file", status.Source.Path,
					"state", status.State, "applied_at", status.AppliedAt)
			}
		case "down":
			result, err := provider.Down(ctx)
			if errors.Is(err, goose.ErrNoNextVersion) {
				logger.Info("no migration to roll back")
				return nil
			}
			if err != nil {
				return fmt.Errorf("roll back migration: %w", err)
			}
			logger.Info("migration rolled back", "version", result.Source.Version, "file", result.Source.Path, "duration", result.Duration)
		}
		return nil
	})
}
