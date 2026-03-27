//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/temporal"

	"sentinel/internal/agent"
	"sentinel/internal/agent/activity"
	modelapi "sentinel/internal/llm/openai"
	"sentinel/internal/payload"
	"sentinel/internal/platform/config"
	"sentinel/internal/storage/postgres"
)

// Payload bytes remain in memory, while all references and FK checks use the
// real PostgreSQL index. S3 integrity is covered separately by adapter tests.
type indexedPayloads struct {
	index *postgres.PayloadIndex
	mu    sync.Mutex
	data  map[string][]byte
}

func (p *indexedPayloads) Put(ctx context.Context, tenantID, kind string, data []byte) (string, error) {
	hash := sha256.Sum256(data)
	object, err := p.index.Register(ctx, payload.Object{ID: newRuntimeID(), TenantID: tenantID, Kind: kind,
		Bucket: "runtime-test", Key: tenantID + "/" + kind + "/" + hex.EncodeToString(hash[:]),
		Checksum: hash[:], Size: int64(len(data)), Codec: "identity"})
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.data[tenantID+":"+object.ID] = append([]byte(nil), data...)
	p.mu.Unlock()
	return object.ID, nil
}

func (p *indexedPayloads) Get(ctx context.Context, tenantID, ref string) ([]byte, error) {
	if _, err := p.index.Load(ctx, tenantID, ref); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	data, ok := p.data[tenantID+":"+ref]
	if !ok {
		return nil, payload.ErrNotFound
	}
	return append([]byte(nil), data...), nil
}

type runtimeFixture struct {
	pool     *pgxpool.Pool
	store    *postgres.AgentStore
	payloads *indexedPayloads
	a        *activity.Activities
	tenant   string
}

func newRuntimeFixture(t *testing.T) runtimeFixture {
	t.Helper()
	dsn := isolatedDatabase(t)
	provider := openProvider(t, t.Context(), dsn)
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewAgentStore(pool)
	payloads := &indexedPayloads{index: postgres.NewPayloadIndex(pool), data: make(map[string][]byte)}
	return runtimeFixture{pool: pool, store: store, payloads: payloads,
		a: &activity.Activities{Store: store, Payloads: payloads}, tenant: newRuntimeID()}
}

func (f runtimeFixture) root(t *testing.T, prompt string, bindings []agent.ResourceBinding) agent.AgentRecord {
	t.Helper()
	record, err := f.a.CreateRoot(t.Context(), f.tenant, agent.Task{Prompt: prompt, Instructions: "test instructions", Model: "test-model",
		Tools: []string{agent.ChildToolName}, Limits: agent.DefaultLimits()}, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Initialize(t.Context(), agent.InitializeInput{Session: record.Input, WorkflowID: record.WorkflowID, RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	return record
}

func newRuntimeID() string { return uuid.Must(uuid.NewV7()).String() }

func (f runtimeFixture) useSDK(t *testing.T, handler http.HandlerFunc) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	model, err := modelapi.NewModel(config.OpenAI{APIKey: "test", Model: "test-model", BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.a.Model = model
	return &calls
}

func writeAnswer(w http.ResponseWriter, text string) {
	encoded, _ := json.Marshal(text)
	fmt.Fprintf(w, `{"id":"resp-final","object":"response","status":"completed","output":[{"type":"message","id":"msg-final","role":"assistant","status":"completed","content":[{"type":"output_text","text":%s,"annotations":[]}]}],"usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25}}`, encoded)
}

func TestRuntimeSDKToolChildAndCheckpointRecovery(t *testing.T) {
	f := newRuntimeFixture(t)
	var childRequests atomic.Int32
	calls := f.useSDK(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var request struct {
			Input []map[string]any `json:"input"`
			Store bool             `json:"store"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
		}
		if request.Store {
			t.Error("remote response storage must be disabled")
		}
		if len(request.Input) == 0 {
			t.Error("persisted SDK request lost its input")
		}
		if strings.Contains(string(body), "child-only-task") && !strings.Contains(string(body), "parent-private-task") {
			childRequests.Add(1)
			writeAnswer(w, "child answer")
			return
		}
		outputs := make(map[string]bool)
		for _, item := range request.Input {
			if item["type"] == "function_call_output" {
				outputs[item["call_id"].(string)] = true
			}
		}
		if len(outputs) != 0 {
			if len(outputs) != 3 || !outputs["delegate"] || !outputs["unknown"] || !outputs["malformed"] || !strings.Contains(string(body), "encrypted-parent-reasoning") {
				t.Errorf("incomplete recovered request: %s", body)
			}
			writeAnswer(w, "parent final answer")
			return
		}
		io.WriteString(w, `{"id":"resp-tools","object":"response","status":"completed","output":[{"type":"reasoning","id":"rs-parent","summary":[],"encrypted_content":"encrypted-parent-reasoning"},{"type":"function_call","id":"fc-child","call_id":"delegate","name":"call_child_agent","arguments":"{\"task\":\"child-only-task\"}","status":"completed"},{"type":"function_call","id":"fc-unknown","call_id":"unknown","name":"unregistered","arguments":"{}","status":"completed"},{"type":"function_call","id":"fc-malformed","call_id":"malformed","name":"call_child_agent","arguments":"{","status":"completed"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`)
	})
	root := f.root(t, "parent-private-task", nil)
	step := agent.StepInput{Session: root.Input, StepID: 1}
	model, err := f.a.ModelStep(t.Context(), step)
	if err != nil || len(model.Calls) != 3 {
		t.Fatalf("model=%+v err=%v", model, err)
	}
	if _, err := f.a.ModelStep(t.Context(), step); err != nil || calls.Load() != 1 {
		t.Fatalf("saved response was sent again: calls=%d err=%v", calls.Load(), err)
	}
	for i, call := range model.Calls {
		plan, err := f.a.PrepareTool(t.Context(), agent.ToolInput{Step: step, Call: call})
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			if plan.ResultRef == "" || plan.Child != nil {
				t.Fatalf("invalid call should have a durable error result: %+v", plan)
			}
			continue
		}
		if plan.Child == nil || plan.Child.Input.Session.AgentID == root.Input.AgentID || plan.Child.Input.Session.TaskRef == root.Input.TaskRef {
			failure, _ := f.payloads.Get(t.Context(), f.tenant, plan.ResultRef)
			arguments, _ := f.payloads.Get(t.Context(), f.tenant, plan.Input.Call.ArgumentsRef)
			t.Fatalf("child not isolated: %+v result=%s args=%s", plan, failure, arguments)
		}
		retried, err := f.a.PrepareTool(t.Context(), plan.Input)
		if err != nil || retried.Child.Input.Session.AgentID != plan.Child.Input.Session.AgentID {
			t.Fatalf("child duplicated on retry: %+v %v", retried, err)
		}
		child, err := f.store.LoadAgent(t.Context(), f.tenant, plan.Child.Input.Session.AgentID)
		if err != nil || len(child.Resources) != 0 || child.ParentAgentID != root.Input.AgentID {
			t.Fatalf("child resources=%v err=%v", child.Resources, err)
		}
		if _, err := f.a.Initialize(t.Context(), agent.InitializeInput{Session: child.Input, WorkflowID: child.WorkflowID, RunID: "child-run"}); err != nil {
			t.Fatal(err)
		}
		childStep := agent.StepInput{Session: child.Input, StepID: 1}
		if childModel, err := f.a.ModelStep(t.Context(), childStep); err != nil || len(childModel.Calls) != 0 {
			t.Fatalf("child model: %+v err=%v", childModel, err)
		}
		childProgress, err := f.a.Checkpoint(t.Context(), childStep)
		if err != nil || !childProgress.Completed {
			t.Fatalf("child checkpoint: %+v %v", childProgress, err)
		}
		completion := agent.ToolCompletion{Plan: plan, ChildResult: agent.Result{AgentID: child.Input.AgentID, ResultRef: childProgress.ResultRef}}
		saved, err := f.a.CompleteTool(t.Context(), completion)
		if err != nil || saved.ResultRef == "" {
			t.Fatalf("complete child: %+v %v", saved, err)
		}
		if again, err := f.a.CompleteTool(t.Context(), completion); err != nil || again.ResultRef != saved.ResultRef {
			t.Fatalf("child result not idempotent: %+v %v", again, err)
		}
	}
	first, err := f.a.Checkpoint(t.Context(), step)
	if err != nil || first.Completed || first.State.NextStepID != 2 || first.State.ContextVersion != 1 {
		t.Fatalf("first checkpoint: %+v %v", first, err)
	}
	if _, err := f.a.Checkpoint(t.Context(), step); err != nil {
		t.Fatalf("repeated checkpoint: %v", err)
	}
	step.StepID = 2
	if _, err := f.a.ModelStep(t.Context(), step); err != nil {
		t.Fatal(err)
	}
	final, err := f.a.Checkpoint(t.Context(), step)
	if err != nil || !final.Completed || final.State.ContextVersion != 2 {
		t.Fatalf("final checkpoint: %+v %v", final, err)
	}
	if calls.Load() != 3 || childRequests.Load() != 1 {
		t.Fatalf("unexpected upstream calls: %d child=%d", calls.Load(), childRequests.Load())
	}
	var status string
	if err := f.pool.QueryRow(t.Context(), `SELECT status FROM agent_sessions WHERE session_id=$1`, root.Input.SessionID).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("session status=%s err=%v", status, err)
	}
	var events, outboxes int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM event_index WHERE agent_id=$1),(SELECT count(*) FROM outbox WHERE aggregate_id=$1)`, root.Input.AgentID).
		Scan(&events, &outboxes); err != nil || events != 14 || outboxes != 2 {
		t.Fatalf("checkpoint not atomic/idempotent: events=%d outbox=%d err=%v", events, outboxes, err)
	}
	if _, err := f.payloads.Get(t.Context(), newRuntimeID(), final.ResultRef); !errors.Is(err, payload.ErrNotFound) {
		t.Fatalf("cross-tenant payload access: %v", err)
	}
	agents, err := f.store.ListAgents(t.Context(), f.tenant, 10)
	if err != nil || len(agents) != 2 {
		t.Fatalf("list Agents: %+v err=%v", agents, err)
	}
	for _, listed := range agents {
		if listed.CreatedAt.IsZero() || listed.Head.TenantID != f.tenant || listed.Head.AgentID != listed.Input.AgentID {
			t.Fatalf("incomplete Agent listing: %+v", listed)
		}
	}
}

func TestRuntimeUpstreamFailureRecovery(t *testing.T) {
	for _, test := range []struct {
		status int
		want   string
	}{{429, "prepared"}, {400, "failed"}, {500, "uncertain"}} {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			f := newRuntimeFixture(t)
			calls := f.useSDK(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				io.WriteString(w, `{"error":{"message":"private upstream body","type":"test","code":"test"}}`)
			})
			root := f.root(t, "test failure", nil)
			input := agent.StepInput{Session: root.Input, StepID: 1}
			_, err := f.a.ModelStep(t.Context(), input)
			if err == nil || strings.Contains(err.Error(), "private upstream body") {
				t.Fatalf("failure=%v", err)
			}
			step, loadErr := f.store.LoadStep(t.Context(), input)
			if loadErr != nil || step.Status != test.want || step.RequestRef == "" || calls.Load() != 1 {
				t.Fatalf("status=%s calls=%d err=%v", step.Status, calls.Load(), loadErr)
			}
			if test.status != 429 {
				_, err = f.a.ModelStep(t.Context(), input)
				var failure *temporal.ApplicationError
				if !errors.As(err, &failure) || !failure.NonRetryable() || calls.Load() != 1 {
					t.Fatalf("terminal request resent: calls=%d err=%v", calls.Load(), err)
				}
			}
		})
	}
}

func TestRuntimeRateLimitRetriesTheSavedRequest(t *testing.T) {
	f := newRuntimeFixture(t)
	var mu sync.Mutex
	var bodies, keys []string
	calls := f.useSDK(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		keys = append(keys, r.Header.Get("X-Client-Request-Id"))
		first := len(bodies) == 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit"}}`)
			return
		}
		writeAnswer(w, "retry succeeded")
	})
	root := f.root(t, "keep this exact task", nil)
	input := agent.StepInput{Session: root.Input, StepID: 1}
	if _, err := f.a.ModelStep(t.Context(), input); err == nil {
		t.Fatal("expected the first rate-limit rejection")
	}
	first, err := f.store.LoadStep(t.Context(), input)
	if err != nil || first.Status != "prepared" {
		t.Fatalf("first attempt=%+v err=%v", first, err)
	}
	if result, err := f.a.ModelStep(t.Context(), input); err != nil || result.ResponseRef == "" {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
	if _, err := f.a.ModelStep(t.Context(), input); err != nil || calls.Load() != 2 {
		t.Fatalf("saved response was resent: calls=%d err=%v", calls.Load(), err)
	}
	second, err := f.store.LoadStep(t.Context(), input)
	if err != nil || first.RequestRef != second.RequestRef || second.FencingToken != 2 {
		t.Fatalf("request identity changed: first=%+v second=%+v err=%v", first, second, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || bodies[0] != bodies[1] || keys[0] != keys[1] || !strings.Contains(bodies[1], "keep this exact task") {
		t.Fatal("retry did not reuse the exact persisted SDK request and operation ID")
	}
}

func TestRuntimeCancellationPreventsAClaimedNewRequest(t *testing.T) {
	f := newRuntimeFixture(t)
	calls := f.useSDK(t, func(w http.ResponseWriter, r *http.Request) { writeAnswer(w, "must not run") })
	root := f.root(t, "cancel before claiming", nil)
	step := prepareRuntimeStep(t, f, root)
	if err := f.a.Finish(t.Context(), agent.FinishInput{Session: root.Input, Status: "cancelled", Code: "test_cancel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimStep(t.Context(), step.Input, newRuntimeID(), time.Minute); !errors.Is(err, agent.ErrTerminal) {
		t.Fatalf("cancelled Agent claimed a request: %v", err)
	}
	if _, err := f.a.ModelStep(t.Context(), step.Input); err == nil || calls.Load() != 0 {
		t.Fatalf("cancelled Agent reached upstream: calls=%d err=%v", calls.Load(), err)
	}
}

func prepareRuntimeStep(t *testing.T, f runtimeFixture, root agent.AgentRecord) agent.StepRecord {
	t.Helper()
	data := []byte(`{"model":"test-model","input":"test"}`)
	ref, err := f.payloads.Put(t.Context(), f.tenant, "model-request", data)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	input := agent.StepInput{Session: root.Input, StepID: 1}
	step, err := f.store.PrepareStep(t.Context(), agent.StepRecord{Input: input, ID: newRuntimeID(),
		OperationID: agent.ModelOperation(root.Input.AgentID, 1), Model: "test-model", RequestRef: ref, RequestHash: hash[:]})
	if err != nil {
		t.Fatal(err)
	}
	return step
}

func TestRuntimeConcurrentClaimFencingAndExpiredLease(t *testing.T) {
	f := newRuntimeFixture(t)
	root := f.root(t, "lease test", nil)
	step := prepareRuntimeStep(t, f, root)
	type claimResult struct {
		record agent.StepRecord
		err    error
	}
	claims := make(chan claimResult, 2)
	for range 2 {
		go func() {
			record, err := f.store.ClaimStep(t.Context(), step.Input, newRuntimeID(), time.Minute)
			claims <- claimResult{record, err}
		}()
	}
	var winner agent.StepRecord
	for range 2 {
		result := <-claims
		if result.err == nil {
			if winner.ID != "" {
				t.Fatal("two attempts claimed the same model request")
			}
			winner = result.record
		} else if !errors.Is(result.err, agent.ErrBusy) {
			t.Fatal(result.err)
		}
	}
	if winner.ID == "" || winner.FencingToken != 1 {
		t.Fatalf("no fenced winner: %+v", winner)
	}
	ref, err := f.payloads.Put(t.Context(), f.tenant, "model-response", []byte(`{"status":"completed"}`))
	if err != nil {
		t.Fatal(err)
	}
	stale := winner
	stale.ResponseRef, stale.FencingToken = ref, winner.FencingToken+1
	if err := f.store.SaveResponse(t.Context(), stale); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("stale response accepted: %v", err)
	}
	winner.ResponseRef = ref
	if err := f.store.SaveResponse(t.Context(), winner); err != nil {
		t.Fatal(err)
	}
	if saved, err := f.store.ClaimStep(t.Context(), step.Input, newRuntimeID(), time.Minute); err != nil || saved.ResponseRef != ref {
		t.Fatalf("saved response not reused: %+v %v", saved, err)
	}
	other := f.root(t, "expired request", nil)
	expired := prepareRuntimeStep(t, f, other)
	if _, err := f.store.ClaimStep(t.Context(), expired.Input, newRuntimeID(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE idempotency_records SET lease_until=now()-interval '1 second' WHERE tenant_id=$1 AND operation_id=$2`, f.tenant, expired.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimStep(t.Context(), expired.Input, newRuntimeID(), time.Minute); !errors.Is(err, agent.ErrUncertain) {
		t.Fatalf("expired request must require reconciliation: %v", err)
	}
	loaded, err := f.store.LoadStep(t.Context(), expired.Input)
	if err != nil || loaded.Status != "uncertain" || loaded.FencingToken != 1 {
		t.Fatalf("expired status=%+v err=%v", loaded, err)
	}
}

func TestRuntimeCheckpointCASAndTerminalStatus(t *testing.T) {
	f := newRuntimeFixture(t)
	f.useSDK(t, func(w http.ResponseWriter, r *http.Request) { writeAnswer(w, "verified answer") })
	root := f.root(t, "CAS test", nil)
	input := agent.StepInput{Session: root.Input, StepID: 1}
	if _, err := f.a.ModelStep(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	checkpoint := agent.Checkpoint{Step: input, ExpectedHead: root.Head}
	checkpoint.ExpectedHead.RowVersion++
	if _, err := f.store.CommitStep(t.Context(), checkpoint); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("stale Context head accepted: %v", err)
	}
	current, err := f.store.LoadAgent(t.Context(), f.tenant, root.Input.AgentID)
	if err != nil || current.Head.ContextVersion != 0 || current.Head.NextStepID != 1 {
		t.Fatalf("failed CAS changed the cursor: %+v %v", current.Head, err)
	}
	completed, err := f.a.Checkpoint(t.Context(), input)
	if err != nil || !completed.Completed {
		t.Fatalf("checkpoint=%+v err=%v", completed, err)
	}
	if err := f.a.Finish(t.Context(), agent.FinishInput{Session: root.Input, Status: "cancelled", Code: "late_cancel"}); err != nil {
		t.Fatal(err)
	}
	current, err = f.store.LoadAgent(t.Context(), f.tenant, root.Input.AgentID)
	if err != nil || current.Status != "completed" || current.ResultRef != completed.ResultRef {
		t.Fatalf("late cancellation overwrote completed result: %+v %v", current, err)
	}
}

func TestRuntimeChildResourcesAreExplicitAndScoped(t *testing.T) {
	f := newRuntimeFixture(t)
	allowed, err := f.payloads.Put(t.Context(), f.tenant, "document", []byte(`{"text":"allowed document"}`))
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.payloads.Put(t.Context(), f.tenant, "document", []byte(`{"text":"other document"}`))
	if err != nil {
		t.Fatal(err)
	}
	f.useSDK(t, func(w http.ResponseWriter, r *http.Request) {
		calls := []map[string]any{}
		for i, objectID := range []string{allowed, other} {
			args, _ := json.Marshal(map[string]any{"task": "explicit child", "resources": []agent.ResourceBinding{{
				BindingName: "document", Kind: agent.ResourceObject, ObjectID: objectID}}})
			calls = append(calls, map[string]any{"type": "function_call", "id": fmt.Sprint("fc-", i), "call_id": fmt.Sprint("call-", i),
				"name": agent.ChildToolName, "arguments": string(args), "status": "completed"})
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "resp-resources", "object": "response", "status": "completed", "output": calls})
	})
	root := f.root(t, "parent with document", []agent.ResourceBinding{{BindingName: "parent-document", Kind: agent.ResourceObject, ObjectID: allowed}})
	step := agent.StepInput{Session: root.Input, StepID: 1}
	model, err := f.a.ModelStep(t.Context(), step)
	if err != nil {
		t.Fatal(err)
	}
	for i, call := range model.Calls {
		plan, err := f.a.PrepareTool(t.Context(), agent.ToolInput{Step: step, Call: call})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if plan.Child == nil {
				failure, _ := f.payloads.Get(t.Context(), f.tenant, plan.ResultRef)
				t.Fatalf("authorized input rejected: %+v result=%s", plan, failure)
			}
			child, err := f.store.LoadAgent(t.Context(), f.tenant, plan.Child.Input.Session.AgentID)
			if err != nil || len(child.Resources) != 1 || child.Resources[0].ObjectID != allowed || child.Resources[0].BindingName != "document" {
				t.Fatalf("explicit child binding=%+v err=%v", child.Resources, err)
			}
		} else if plan.Child != nil || plan.ResultRef == "" {
			t.Fatalf("out-of-scope input accepted: %+v", plan)
		}
	}
}
