package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sentinel/internal/stream"
)

type fakeStreamReader struct {
	history    []stream.Message
	live       chan stream.Message
	historyErr error
}

func (f *fakeStreamReader) History(_ context.Context, _, _, afterID string, emit func(stream.Message) error) (string, error) {
	last := afterID
	for _, message := range f.history {
		if afterID != "" && stream.CompareIDs(message.ID, afterID) <= 0 {
			continue
		}
		if err := emit(message); err != nil {
			return last, err
		}
		last = message.ID
	}
	return last, f.historyErr
}

func (f *fakeStreamReader) Subscribe(context.Context, string, string) (<-chan stream.Message, func(), error) {
	return f.live, func() {}, nil
}

func streamMessages(t *testing.T) []stream.Message {
	t.Helper()
	first := stream.Event{Type: stream.EventModelText, Data: json.RawMessage(`{"kind":"text","text":"hello"}`)}
	second := stream.Event{Type: stream.EventAgentCompleted, Data: json.RawMessage(`{"result_ref":"result"}`)}
	firstPayload, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondPayload, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	return []stream.Message{
		{ID: "1-1", Event: first, Payload: firstPayload},
		{ID: "1-2", Event: second, Payload: secondPayload},
	}
}

func TestAgentStreamReplaysHistoryAndCloses(t *testing.T) {
	deps, reader, _, _, _, _, _ := controlDependencies()
	reader.records[0].Status = "completed"
	deps.Streams = &fakeStreamReader{history: streamMessages(t), live: make(chan stream.Message)}
	router := testRouter(deps)
	response := requestJSON(t, router, http.MethodGet, "/v1/agents/"+testAgent+"/stream", testTenant, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("unexpected content type %q", contentType)
	}
	body := response.Body.String()
	for _, expected := range []string{"retry: 3000", "id: 1-1", "event: model.text", `"text":"hello"`,
		"id: 1-2", "event: agent.completed", `"result_ref":"result"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("stream body is missing %q:\n%s", expected, body)
		}
	}
}

func TestAgentStreamHonorsLastEventID(t *testing.T) {
	deps, reader, _, _, _, _, _ := controlDependencies()
	reader.records[0].Status = "completed"
	deps.Streams = &fakeStreamReader{history: streamMessages(t), live: make(chan stream.Message)}
	router := testRouter(deps)
	request := httptest.NewRequest(http.MethodGet, "/v1/agents/"+testAgent+"/stream", nil)
	request.Header.Set("X-Tenant-ID", testTenant)
	request.Header.Set("Last-Event-ID", "1-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	if strings.Contains(body, "id: 1-1") || !strings.Contains(body, "id: 1-2") {
		t.Fatalf("unexpected replay body:\n%s", body)
	}

	invalid := httptest.NewRequest(http.MethodGet, "/v1/agents/"+testAgent+"/stream", nil)
	invalid.Header.Set("X-Tenant-ID", testTenant)
	invalid.Header.Set("Last-Event-ID", "not-an-id")
	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid Last-Event-ID status=%d", invalidResponse.Code)
	}
}

func TestAgentStreamAcceptsTenantQuery(t *testing.T) {
	deps, reader, _, _, _, _, _ := controlDependencies()
	reader.records[0].Status = "completed"
	deps.Streams = &fakeStreamReader{history: streamMessages(t), live: make(chan stream.Message)}
	router := testRouter(deps)
	request := httptest.NewRequest(http.MethodGet,
		"/v1/agents/"+testAgent+"/stream?tenant_id="+testTenant, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: model.text") {
		t.Fatalf("query tenant stream status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAgentStreamRequiresConfiguration(t *testing.T) {
	deps, _, _, _, _, _, _ := controlDependencies()
	deps.Streams = nil
	router := testRouter(deps)
	response := requestJSON(t, router, http.MethodGet, "/v1/agents/"+testAgent+"/stream", testTenant, nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled stream status=%d body=%s", response.Code, response.Body.String())
	}
}
