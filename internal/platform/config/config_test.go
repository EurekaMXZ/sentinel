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

func TestLoadDefaults(t *testing.T) {
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
	if cfg.OpenAI.ContextWindowTokens != 128000 || cfg.OpenAI.ContextSafetyTokens != 8192 || !cfg.OpenAI.CompactStructuredOutput {
		t.Fatalf("unexpected context compact defaults: %+v", cfg.OpenAI)
	}
	if cfg.Redis.Addr != "" || cfg.Redis.DB != 0 || cfg.Redis.StreamTTL != 168*time.Hour {
		t.Fatalf("unexpected Redis defaults: %+v", cfg.Redis)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(lookup(map[string]string{
		"HTTP_ADDR":                        "[::1]:9090",
		"HTTP_READ_TIMEOUT":                "45s",
		"TEMPORAL_NAMESPACE":               "analysis",
		"S3_USE_TLS":                       "true",
		"SHUTDOWN_TIMEOUT":                 "20s",
		"OPENAI_CONTEXT_WINDOW_TOKENS":     "64000",
		"OPENAI_CONTEXT_SAFETY_TOKENS":     "4096",
		"OPENAI_COMPACT_STRUCTURED_OUTPUT": "false",
		"REDIS_ADDR":                       "127.0.0.1:6380",
		"REDIS_DB":                         "2",
		"REDIS_STREAM_TTL":                 "24h",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Address != "[::1]:9090" || cfg.HTTP.ReadTimeout != 45*time.Second || !cfg.S3.UseTLS || cfg.ShutdownTimeout != 20*time.Second || cfg.Temporal.Namespace != "analysis" {
		t.Fatal("environment overrides were not applied")
	}
	if cfg.OpenAI.ContextWindowTokens != 64000 || cfg.OpenAI.ContextSafetyTokens != 4096 || cfg.OpenAI.CompactStructuredOutput {
		t.Fatalf("context compact overrides were not applied: %+v", cfg.OpenAI)
	}
	if cfg.Redis.Addr != "127.0.0.1:6380" || cfg.Redis.DB != 2 || cfg.Redis.StreamTTL != 24*time.Hour {
		t.Fatalf("Redis overrides were not applied: %+v", cfg.Redis)
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
		{"SOURCE_MAX_BUNDLE_BYTES", "0"},
		{"SOURCE_MAX_BUNDLE_BYTES", "5368709121"},
		{"SOURCE_MAX_BUNDLE_BYTES", "invalid"},
		{"OPENAI_CONTEXT_WINDOW_TOKENS", "1000"},
		{"OPENAI_CONTEXT_SAFETY_TOKENS", "0"},
		{"OPENAI_CONTEXT_SAFETY_TOKENS", "128000"},
		{"OPENAI_COMPACT_STRUCTURED_OUTPUT", "sometimes"},
		{"REDIS_DB", "16"},
		{"REDIS_STREAM_TTL", "0s"},
		{"REDIS_ADDR", "localhost"},
	} {
		t.Run(test.key, func(t *testing.T) {
			_, err := load(lookup(map[string]string{test.key: test.value}))
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("expected an error identifying %s, got %v", test.key, err)
			}
		})
	}
}
