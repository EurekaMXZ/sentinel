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

// StreamDelta is the normalized subset of upstream streaming events exposed to
// the Agent runtime. Reasoning content is never forwarded.
type StreamDelta struct {
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	ItemID      string `json:"item_id,omitempty"`
	OutputIndex int64  `json:"output_index,omitempty"`
}

// Stream sends the same Responses request as Complete and forwards bounded text
// and function-argument deltas. The final completed response is returned in the
// same shape as Complete so the Activity can persist it unchanged.
func (m *Model) Stream(ctx context.Context, request responses.ResponseNewParams, operationID string, emit func(StreamDelta) error) (Completion, error) {
	key := sha256.Sum256([]byte(operationID))
	stream := m.client.Responses.NewStreaming(ctx, request,
		option.WithHeader("X-Client-Request-Id", hex.EncodeToString(key[:])))
	defer stream.Close()
	var final responses.Response
	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "response.output_text.delta":
			if emit != nil && event.Delta != "" {
				if err := emit(StreamDelta{Kind: "text", Text: event.Delta, ItemID: event.ItemID,
					OutputIndex: event.OutputIndex}); err != nil {
					return Completion{}, err
				}
			}
		case "response.function_call_arguments.delta":
			if emit != nil && event.Delta != "" {
				if err := emit(StreamDelta{Kind: "arguments", Text: event.Delta, ItemID: event.ItemID,
					OutputIndex: event.OutputIndex}); err != nil {
					return Completion{}, err
				}
			}
		case "response.completed", "response.incomplete", "response.failed":
			if event.Response.ID != "" {
				final = event.Response
			}
		}
	}
	if err := stream.Err(); err != nil {
		failure := &RequestError{Uncertain: true, cause: err}
		var apiError *openaigo.Error
		if errors.As(err, &apiError) {
			failure.Status = apiError.StatusCode
			failure.Retryable = apiError.StatusCode == http.StatusTooManyRequests
			failure.Uncertain = apiError.StatusCode >= 500 || apiError.StatusCode == http.StatusRequestTimeout || apiError.StatusCode == http.StatusConflict
		}
		return Completion{}, failure
	}
	if final.ID == "" {
		return Completion{}, &RequestError{Uncertain: true, cause: errors.New("stream ended without a final response")}
	}
	return Completion{Response: final}, nil
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
