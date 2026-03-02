package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"sentinel/internal/platform/config"
)

func apiTestConfig() config.Config {
	return config.Config{
		HTTP:            config.HTTP{Address: "127.0.0.1:0", ReadHeaderTimeout: time.Second},
		ShutdownTimeout: time.Second,
	}
}

func TestAPIReturnsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() {
		result <- RunAPI(ctx, apiTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP server did not stop after cancellation")
	}
}

func TestAPIReportsOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := apiTestConfig()
	cfg.HTTP.Address = listener.Addr().String()
	if err := RunAPI(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("expected an error for an occupied port")
	}
}
