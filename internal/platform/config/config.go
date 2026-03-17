// Package config loads configuration from environment variables.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTP            HTTP
	Temporal        Temporal
	Postgres        Postgres
	S3              S3
	OpenAI          OpenAI
	Source          Source
	ShutdownTimeout time.Duration
}

type HTTP struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

type Temporal struct {
	Address         string
	Namespace       string
	AgentTaskQueue  string
	SourceTaskQueue string
	RelayTaskQueue  string
	DialTimeout     time.Duration
}

type Postgres struct {
	DSN string
}

type S3 struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	UseTLS    bool
}

type OpenAI struct {
	APIKey         string
	BaseURL        string
	Model          string
	RequestTimeout time.Duration
}

type Source struct {
	GitBinary      string
	CacheDir       string
	MaxBundleBytes int64
}

func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	env := func(key, fallback string) string {
		if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
		return fallback
	}
	cfg := Config{
		HTTP: HTTP{
			Address:           env("HTTP_ADDR", "127.0.0.1:8080"),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       time.Minute,
		},
		Temporal: Temporal{
			Address:         env("TEMPORAL_ADDRESS", "127.0.0.1:7233"),
			Namespace:       env("TEMPORAL_NAMESPACE", "default"),
			AgentTaskQueue:  env("TEMPORAL_AGENT_TASK_QUEUE", "sentinel-agent"),
			SourceTaskQueue: env("TEMPORAL_SOURCE_TASK_QUEUE", "sentinel-source"),
			RelayTaskQueue:  env("TEMPORAL_RELAY_TASK_QUEUE", "sentinel-relay"),
			DialTimeout:     5 * time.Second,
		},
		Postgres: Postgres{DSN: env("POSTGRES_DSN", "")},
		S3: S3{
			Endpoint:  env("S3_ENDPOINT", "127.0.0.1:9000"),
			Bucket:    env("S3_BUCKET", "sentinel"),
			Region:    env("S3_REGION", ""),
			AccessKey: env("S3_ACCESS_KEY", ""),
			SecretKey: env("S3_SECRET_KEY", ""),
		},
		OpenAI: OpenAI{
			APIKey:         env("OPENAI_API_KEY", ""),
			BaseURL:        env("OPENAI_BASE_URL", ""),
			Model:          env("OPENAI_MODEL", ""),
			RequestTimeout: 2 * time.Minute,
		},
		Source: Source{
			GitBinary:      env("GIT_BINARY", "git"),
			CacheDir:       env("SOURCE_CACHE_DIR", "var/git"),
			MaxBundleBytes: 512 << 20,
		},
		ShutdownTimeout: 10 * time.Second,
	}
	for _, setting := range []struct {
		key    string
		target *time.Duration
	}{
		{"HTTP_READ_HEADER_TIMEOUT", &cfg.HTTP.ReadHeaderTimeout},
		{"HTTP_READ_TIMEOUT", &cfg.HTTP.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", &cfg.HTTP.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", &cfg.HTTP.IdleTimeout},
		{"TEMPORAL_DIAL_TIMEOUT", &cfg.Temporal.DialTimeout},
		{"OPENAI_REQUEST_TIMEOUT", &cfg.OpenAI.RequestTimeout},
		{"SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout},
	} {
		if raw := env(setting.key, ""); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive duration", setting.key)
			}
			*setting.target = value
		}
	}
	if raw := env("S3_USE_TLS", ""); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("S3_USE_TLS must be a boolean")
		}
		cfg.S3.UseTLS = value
	}
	if raw := env("SOURCE_MAX_BUNDLE_BYTES", ""); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 || value > 5<<30 {
			return Config{}, fmt.Errorf("SOURCE_MAX_BUNDLE_BYTES must be between 1 and 5368709120")
		}
		cfg.Source.MaxBundleBytes = value
	}
	for _, address := range []struct {
		key   string
		value string
	}{
		{"HTTP_ADDR", cfg.HTTP.Address},
		{"TEMPORAL_ADDRESS", cfg.Temporal.Address},
	} {
		if _, _, err := net.SplitHostPort(address.value); err != nil {
			return Config{}, fmt.Errorf("%s must be a host:port address", address.key)
		}
	}
	return cfg, nil
}
