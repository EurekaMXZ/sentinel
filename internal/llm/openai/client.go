// Package openai adapts the official OpenAI SDK for model Activities.
package openai

import (
	"errors"

	openaigo "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"sentinel/internal/platform/config"
)

// New configures the SDK without performing a model request.
// Retries belong to the Activity and operation-id persistence layer.
func New(cfg config.OpenAI) (openaigo.Client, error) {
	if cfg.APIKey == "" || cfg.Model == "" {
		return openaigo.Client{}, errors.New("OPENAI_API_KEY and OPENAI_MODEL are required")
	}
	options := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithRequestTimeout(cfg.RequestTimeout),
		option.WithMaxRetries(0),
	}
	if cfg.BaseURL != "" {
		options = append(options, option.WithBaseURL(cfg.BaseURL))
	}
	return openaigo.NewClient(options...), nil
}
