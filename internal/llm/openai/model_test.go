package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	openaigo "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	modelapi "sentinel/internal/llm/openai"
	"sentinel/internal/platform/config"
)

func TestOfficialSDKRequestAndReasoningRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected SDK request: %s %s", r.Method, r.URL.Path)
		}
		if len(r.Header.Get("X-Client-Request-Id")) != 64 {
			t.Error("missing operation correlation header")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["store"] != false || body["model"] != "test-model" || body["previous_response_id"] != nil {
			t.Errorf("unexpected request parameters: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req-test")
		io.WriteString(w, `{"id":"resp-1","object":"response","status":"completed","output":[{"type":"reasoning","id":"rs-1","summary":[],"encrypted_content":"opaque-reasoning"},{"type":"function_call","id":"fc-1","call_id":"call-1","name":"example","arguments":"{}","status":"completed"}],"usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}`)
	}))
	defer server.Close()
	model, err := modelapi.NewModel(config.OpenAI{APIKey: "test-key", Model: "test-model", BaseURL: server.URL + "/v1", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := model.Complete(t.Context(), responses.ResponseNewParams{Model: shared.ResponsesModel("test-model"), Store: openaigo.Bool(false),
		Input: responses.ResponseNewParamsInputUnion{OfString: openaigo.String("task")}}, "agent:1:model")
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestID != "req-test" || result.Response.Usage.InputTokens != 12 {
		t.Fatalf("unexpected SDK response metadata: %+v", result)
	}
	items, err := modelapi.OutputAsInput(result.Response.Output)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"encrypted_content":"opaque-reasoning"`) || !strings.Contains(string(data), `"call_id":"call-1"`) {
		t.Fatalf("response output was lost: %s", data)
	}
	var restored []responses.ResponseInputItemUnionParam
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(restored)
	var originalJSON, restoredJSON any
	_ = json.Unmarshal(data, &originalJSON)
	_ = json.Unmarshal(encoded, &restoredJSON)
	if !reflect.DeepEqual(originalJSON, restoredJSON) {
		t.Fatalf("persisted SDK input changed on recovery:\n%s\n%s", data, encoded)
	}
}

func TestStreamingForwardsFinalTextAndReturnsFinalResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["stream"] != true {
			t.Errorf("streaming request did not set stream=true: %v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test server is not flushable")
		}
		write := func(data string) {
			io.WriteString(w, "data: "+data+"\n\n")
			flusher.Flush()
		}
		write(`{"type":"response.created","response":{"id":"resp-stream","object":"response","status":"in_progress","output":[]}}`)
		write(`{"type":"response.output_text.done","text":"Hello","item_id":"msg-1","output_index":0,"content_index":0,"sequence_number":3,"logprobs":[]}`)
		write(`{"type":"response.completed","response":{"id":"resp-stream","object":"response","status":"completed","output":[{"type":"message","id":"msg-1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`)
	}))
	defer server.Close()
	model, err := modelapi.NewModel(config.OpenAI{APIKey: "test-key", Model: "test-model",
		BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []modelapi.StreamDelta
	completion, err := model.Stream(t.Context(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("test-model"), Store: openaigo.Bool(false),
		Input: responses.ResponseNewParamsInputUnion{OfString: openaigo.String("task")},
	}, "agent:1:model", func(delta modelapi.StreamDelta) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deltas, []modelapi.StreamDelta{
		{Kind: "text_done", Text: "Hello", ItemID: "msg-1"},
	}) {
		t.Fatalf("deltas=%+v", deltas)
	}
	if completion.Response.ID != "resp-stream" || completion.Response.OutputText() != "Hello" ||
		!json.Valid([]byte(completion.Response.RawJSON())) {
		t.Fatalf("completion=%+v raw=%s", completion, completion.Response.RawJSON())
	}
}

func TestSDKDoesNotRetryAndClassifiesFailures(t *testing.T) {
	for _, test := range []struct {
		status    int
		retryable bool
		uncertain bool
	}{{429, true, false}, {400, false, false}, {401, false, false}, {408, false, true}, {409, false, true}, {500, false, true}} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				io.WriteString(w, `{"error":{"message":"private task text","type":"test","code":"test"}}`)
			}))
			defer server.Close()
			model, err := modelapi.NewModel(config.OpenAI{APIKey: "test-key", Model: "test", BaseURL: server.URL, RequestTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Complete(t.Context(), responses.ResponseNewParams{Model: "test"}, "operation")
			var failure *modelapi.RequestError
			if !errors.As(err, &failure) || failure.Status != test.status || failure.Retryable != test.retryable || failure.Uncertain != test.uncertain {
				t.Fatalf("failure classification: %+v", err)
			}
			if calls.Load() != 1 || strings.Contains(err.Error(), "private task text") {
				t.Fatalf("calls=%d, error=%v", calls.Load(), err)
			}
		})
	}
}

func TestSDKCancellationIsUncertain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer server.Close()
	model, err := modelapi.NewModel(config.OpenAI{APIKey: "test", Model: "test", BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err = model.Complete(ctx, responses.ResponseNewParams{Model: "test"}, "cancelled-operation")
	var failure *modelapi.RequestError
	if !errors.As(err, &failure) || !failure.Uncertain || failure.Retryable {
		t.Fatalf("cancellation must not be resent automatically: %v", err)
	}
}
