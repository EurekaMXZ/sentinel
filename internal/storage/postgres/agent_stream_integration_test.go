//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel/internal/agent"
	modelapi "sentinel/internal/llm/openai"
	"sentinel/internal/platform/config"
	"sentinel/internal/stream"
)

type recordingPublisher struct {
	mu     sync.Mutex
	events []stream.Event
}

func (p *recordingPublisher) Publish(_ context.Context, _, _ string, event stream.Event) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return fmt.Sprintf("1-%d", len(p.events)), nil
}

func (p *recordingPublisher) snapshot() []stream.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]stream.Event(nil), p.events...)
}

func TestModelStepStreamsDeltasAndPersistsFinalResponse(t *testing.T) {
	f := newRuntimeFixture(t)
	root := f.root(t, "stream a task", nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if !strings.Contains(string(body), `"stream":true`) {
			t.Errorf("model request was not streamed: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test server is not flushable")
		}
		write := func(data string) {
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		write(`{"type":"response.created","response":{"id":"resp-stream","object":"response","status":"in_progress","output":[]}}`)
		write(`{"type":"response.output_text.delta","delta":"streamed","item_id":"msg-1","output_index":0,"content_index":0}`)
		write(`{"type":"response.completed","response":{"id":"resp-stream","object":"response","status":"completed","output":[{"type":"message","id":"msg-1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"streamed answer","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`)
	}))
	t.Cleanup(server.Close)
	model, err := modelapi.NewModel(config.OpenAI{APIKey: "test", Model: "test-model",
		BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.a.Model = model
	publisher := &recordingPublisher{}
	f.a.Streams = publisher

	step := agent.StepInput{Session: root.Input, StepID: 1}
	modelResult, err := f.a.ModelStep(t.Context(), step)
	if err != nil || len(modelResult.Calls) != 0 {
		t.Fatalf("model=%+v err=%v", modelResult, err)
	}
	progress, err := f.a.Checkpoint(t.Context(), step)
	if err != nil || !progress.Completed {
		t.Fatalf("checkpoint=%+v err=%v", progress, err)
	}
	var deltaText string
	seen := make(map[string]bool)
	for _, event := range publisher.snapshot() {
		seen[event.Type] = true
		if event.Type == stream.EventModelDelta {
			var delta modelapi.StreamDelta
			if err := json.Unmarshal(event.Data, &delta); err != nil {
				t.Fatal(err)
			}
			deltaText += delta.Text
		}
	}
	for _, eventType := range []string{stream.EventModelStarted, stream.EventModelDelta,
		stream.EventModelCompleted, stream.EventAgentCompleted} {
		if !seen[eventType] {
			t.Fatalf("missing stream event %q: %+v", eventType, publisher.snapshot())
		}
	}
	if deltaText != "streamed" {
		t.Fatalf("delta text=%q", deltaText)
	}
}
