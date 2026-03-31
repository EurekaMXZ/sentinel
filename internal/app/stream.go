package app

import (
	"sentinel/internal/platform/config"
	"sentinel/internal/stream"
)

func streamConfig(cfg config.Config) stream.Config {
	return stream.Config{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password,
		DB: cfg.Redis.DB, TTL: cfg.Redis.StreamTTL}
}
