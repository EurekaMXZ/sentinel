package openai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	openaigo "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"sentinel/internal/platform/config"
)

type Completion struct {
	Response  responses.Response
	RequestID string
}

type Model struct {
	client openaigo.Client
}

func NewModel(cfg config.OpenAI) (*Model, error) {
	client, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &Model{client: client}, nil
}

// RequestError intentionally excludes the upstream body, which can contain task data.
type RequestError struct {
	Status    int
	Retryable bool
	Uncertain bool
	cause     error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("upstream model request failed (status=%d, uncertain=%t)", e.Status, e.Uncertain)
}

func (e *RequestError) Unwrap() error { return e.cause }

// Complete delegates all HTTP, authentication, serialization and response parsing
// to the official SDK. The client request ID is correlation, not an exactly-once guarantee.
func (m *Model) Complete(ctx context.Context, request responses.ResponseNewParams, operationID string) (Completion, error) {
	key := sha256.Sum256([]byte(operationID))
	var httpResponse *http.Response
	response, err := m.client.Responses.New(ctx, request,
		option.WithHeader("X-Client-Request-Id", hex.EncodeToString(key[:])),
		option.WithResponseInto(&httpResponse),
	)
	if err != nil {
		failure := &RequestError{Uncertain: true, cause: err}
		var apiError *openaigo.Error
		if errors.As(err, &apiError) {
			failure.Status = apiError.StatusCode
			failure.Retryable = apiError.StatusCode == http.StatusTooManyRequests
			failure.Uncertain = apiError.StatusCode >= 500 || apiError.StatusCode == http.StatusRequestTimeout || apiError.StatusCode == http.StatusConflict
		}
		return Completion{}, failure
	}
	result := Completion{Response: *response}
	if httpResponse != nil {
		result.RequestID = httpResponse.Header.Get("X-Request-Id")
	}
	return result, nil
}

// OutputAsInput retains every item, including encrypted reasoning and the call
// IDs needed by function_call_output. SDK ToParam preserves unknown JSON fields.
func OutputAsInput(output []responses.ResponseOutputItemUnion) ([]responses.ResponseInputItemUnionParam, error) {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(output))
	for _, item := range output {
		var input responses.ResponseInputItemUnion
		if err := json.Unmarshal([]byte(item.RawJSON()), &input); err != nil {
			return nil, fmt.Errorf("convert response item: %w", err)
		}
		items = append(items, input.ToParam())
	}
	return items, nil
}
