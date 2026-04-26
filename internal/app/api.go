package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"sentinel/internal/agent"
	agentactivity "sentinel/internal/agent/activity"
	"sentinel/internal/api"
	"sentinel/internal/platform/config"
	"sentinel/internal/platform/temporal"
	sourceactivity "sentinel/internal/source/activity"
	"sentinel/internal/storage/postgres"
	"sentinel/internal/storage/s3"
	"sentinel/internal/stream"
)

func openAPIRuntime(ctx context.Context, cfg config.Config, logger *slog.Logger) (*api.Dependencies, func(), error) {
	if strings.TrimSpace(cfg.OpenAI.Model) == "" {
		return nil, nil, errors.New("OPENAI_MODEL is required for the control API")
	}
	limits := agent.DefaultLimits()
	limits.ModelTimeout = cfg.OpenAI.RequestTimeout
	limits.ContextWindowTokens = cfg.OpenAI.ContextWindowTokens
	limits.ContextSafetyTokens = cfg.OpenAI.ContextSafetyTokens
	if err := limits.Validate(); err != nil {
		return nil, nil, err
	}
	objectClient, err := s3.New(cfg.S3)
	if err != nil {
		return nil, nil, err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	exists, err := objectClient.BucketExists(checkCtx, cfg.S3.Bucket)
	if err != nil {
		return nil, nil, fmt.Errorf("check S3 bucket: %w", err)
	}
	if !exists {
		return nil, nil, errors.New("S3 bucket is missing; run minio-init first")
	}
	pool, err := postgres.Open(ctx, cfg.Postgres)
	if err != nil {
		return nil, nil, err
	}
	temporalClient, err := temporal.Dial(ctx, cfg.Temporal, logger)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	index := postgres.NewPayloadIndex(pool)
	payloads := s3.NewPayloadStore(objectClient, cfg.S3.Bucket, index)
	agentStore := postgres.NewAgentStore(pool)
	sourceStore := postgres.NewSourceStore(pool)
	controlStore := postgres.NewControlStore(pool)
	var streamReader stream.Reader
	var redisStream *stream.Redis
	if cfg.Redis.Addr != "" {
		redisStream, err = stream.NewRedis(ctx, streamConfig(cfg))
		if err != nil {
			logger.Warn("streaming disabled", "error", err)
			redisStream = nil
		} else {
			streamReader = redisStream
		}
	}
	dependencies := &api.Dependencies{
		Agents:         agentStore,
		AgentTasks:     &agentactivity.Activities{Store: agentStore, Payloads: payloads, Tools: agentactivity.SourceToolSet()},
		AgentResources: agentStore,
		Sources:        sourceStore,
		SourceTasks:    &sourceactivity.Activities{Store: sourceStore, Payloads: payloads},
		SourceAdmin:    sourceStore,
		Control:        controlStore,
		Payloads:       payloads,
		Workflows:      temporalClient,
		Streams:        streamReader,
		AgentTaskQueue: cfg.Temporal.AgentTaskQueue, SourceTaskQueue: cfg.Temporal.SourceTaskQueue,
		Model: cfg.OpenAI.Model, Limits: limits,
	}
	closeRuntime := func() {
		if redisStream != nil {
			_ = redisStream.Close()
		}
		temporalClient.Close()
		pool.Close()
	}
	return dependencies, closeRuntime, nil
}

func RunAPI(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	dependencies, closeRuntime, err := openAPIRuntime(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer closeRuntime()
	return serveAPI(ctx, cfg, logger, api.NewRouter(logger, *dependencies))
}

func serveAPI(ctx context.Context, cfg config.Config, logger *slog.Logger, handler http.Handler) error {
	server := &http.Server{
		Addr:              cfg.HTTP.Address,
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen for HTTP requests: %w", err)
	}

	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	logger.Info("HTTP server started", "address", listener.Addr().String())

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP requests: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		if err := <-serveErrors; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("stop HTTP listener: %w", err)
		}
		logger.Info("HTTP server stopped")
		return nil
	}
}
