package config

import (
	"strings"
	"testing"
	"time"
)

func lookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadDefaultsAllowAPIWithoutExternalServices(t *testing.T) {
	cfg, err := load(lookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Address != "127.0.0.1:8080" || cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected HTTP defaults: %+v", cfg.HTTP)
	}
	if cfg.Temporal.AgentTaskQueue == cfg.Temporal.SourceTaskQueue || cfg.Temporal.SourceTaskQueue == cfg.Temporal.RelayTaskQueue {
		t.Fatal("worker task queues must be distinct")
	}
	if cfg.OpenAI.APIKey != "" || cfg.OpenAI.Model != "" || cfg.Postgres.DSN != "" {
		t.Fatal("credentials and model selection must be explicitly configured")
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(lookup(map[string]string{
		"HTTP_ADDR":          "[::1]:9090",
		"HTTP_READ_TIMEOUT":  "45s",
		"TEMPORAL_NAMESPACE": "analysis",
		"S3_USE_TLS":         "true",
		"SHUTDOWN_TIMEOUT":   "20s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Address != "[::1]:9090" || cfg.HTTP.ReadTimeout != 45*time.Second || !cfg.S3.UseTLS || cfg.ShutdownTimeout != 20*time.Second || cfg.Temporal.Namespace != "analysis" {
		t.Fatal("environment overrides were not applied")
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{"HTTP_READ_TIMEOUT", "soon"},
		{"HTTP_WRITE_TIMEOUT", "0s"},
		{"TEMPORAL_DIAL_TIMEOUT", "-1s"},
		{"OPENAI_REQUEST_TIMEOUT", "-2m"},
		{"SHUTDOWN_TIMEOUT", "0"},
		{"S3_USE_TLS", "sometimes"},
		{"HTTP_ADDR", "localhost"},
		{"TEMPORAL_ADDRESS", "localhost"},
	} {
		t.Run(test.key, func(t *testing.T) {
			_, err := load(lookup(map[string]string{test.key: test.value}))
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("expected an error identifying %s, got %v", test.key, err)
			}
		})
	}
}
