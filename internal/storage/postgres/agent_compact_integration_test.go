//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/responses"

	"sentinel/internal/agent"
	"sentinel/internal/agent/activity"
)

const compactToolResponse = `{"id":"resp-tool","object":"response","status":"completed","output":[{"type":"function_call","id":"fc-note","call_id":"call-note","name":"read_note","arguments":"{}","status":"completed"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

func testCompactSummary() agent.CompactSummary {
	return agent.CompactSummary{
		TaskRequirements:  []string{"inspect the note"},
		Constraints:       []string{"read-only"},
		Decisions:         []string{"read the note"},
		CurrentState:      []string{"note read"},
		OpenItems:         []string{},
		Errors:            []string{},
		ChildAgentResults: []string{},
		ToolArtifactRefs:  []string{},
		SourceRefs:        []agent.SourceRef{},
	}
}

func compactAnswer(text string) []byte {
	encoded, _ := json.Marshal(text)
	return []byte(fmt.Sprintf(`{"id":"resp-compact","object":"response","status":"completed","output":[{"type":"message","id":"msg-compact","role":"assistant","status":"completed","content":[{"type":"output_text","text":%s,"annotations":[]}]}],"usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25}}`, encoded))
}

func compactHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	summary, err := json.Marshal(testCompactSummary())
	if err != nil {
		t.Fatal(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		// Only the normal model request carries tool definitions. The compact
		// request must not expose tools to the model.
		if strings.Contains(string(body), `"tools"`) {
			io.WriteString(w, compactToolResponse)
			return
		}
		writeAnswer(w, string(summary))
	}
}

// compactRuntime creates a running Agent after one non-final tool step. The
// Agent has a complete Context snapshot but has not completed.
func compactRuntime(t *testing.T) (runtimeFixture, agent.AgentRecord, *atomic.Int32) {
	t.Helper()
	f := newRuntimeFixture(t)
	f.a.Tools = map[string]activity.Tool{
		"read_note": {
			Definition: responses.FunctionToolParam{Name: "read_note",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
			Execute: func(context.Context, activity.ToolRequest) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			},
		},
	}
	root, err := f.a.CreateRoot(t.Context(), f.tenant, agent.Task{
		Prompt: "read the note", Instructions: "test instructions", Model: "test-model",
		Tools: []string{"read_note"}, Limits: agent.DefaultLimits()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Initialize(t.Context(), agent.InitializeInput{Session: root.Input, WorkflowID: root.WorkflowID, RunID: "run-compact"}); err != nil {
		t.Fatal(err)
	}
	calls := f.useSDK(t, compactHandler(t))
	step := agent.StepInput{Session: root.Input, StepID: 1}
	modelResult, err := f.a.ModelStep(t.Context(), step)
	if err != nil || len(modelResult.Calls) != 1 {
		t.Fatalf("model=%+v err=%v", modelResult, err)
	}
	plan, err := f.a.PrepareTool(t.Context(), agent.ToolInput{Step: step, Call: modelResult.Calls[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.ExecuteTool(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	first, err := f.a.Checkpoint(t.Context(), step)
	if err != nil || first.Completed || first.State.ContextVersion != 1 || first.State.NextStepID != 2 {
		t.Fatalf("first checkpoint: %+v err=%v", first, err)
	}
	return f, root, calls
}

func prepareCompact(t *testing.T, f runtimeFixture, root agent.AgentRecord, lease time.Duration) (agent.AgentRecord, agent.CompactRecord) {
	t.Helper()
	record, err := f.store.LoadAgent(t.Context(), f.tenant, root.Input.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"model":"test-model"}`)
	requestRef, err := f.payloads.Put(t.Context(), f.tenant, "compact-request", request)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(request)
	compact, err := f.store.PrepareCompact(t.Context(), agent.CompactRecord{
		Input: root.Input, ID: newRuntimeID(),
		OperationID:          agent.CompactOperation(root.Input.AgentID, record.Head.RowVersion),
		SourceRowVersion:     record.Head.RowVersion,
		SourceContextVersion: record.Head.ContextVersion,
		SourceEventSequence:  record.Head.LastEventSequence,
		RequestRef:           requestRef,
		RequestHash:          hash[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	compact, err = f.store.ClaimCompact(t.Context(), agent.CompactInput{
		Session: root.Input, RowVersion: record.Head.RowVersion}, newRuntimeID(), lease)
	if err != nil {
		t.Fatal(err)
	}
	return record, compact
}

func TestCompactPublishesSummary(t *testing.T) {
	f, root, calls := compactRuntime(t)
	progress, err := f.a.Compact(t.Context(), agent.CompactInput{Session: root.Input})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Completed || progress.NeedsCompact || progress.State.ContextVersion != 2 ||
		progress.State.NextStepID != 2 || progress.State.LastEventSequence != 5 {
		t.Fatalf("unexpected compact progress: %+v", progress)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one step and one compact request, got %d", calls.Load())
	}
	var version, nextStep, rowVersion, lastEvent int64
	var snapshotID string
	if err := f.pool.QueryRow(t.Context(), `SELECT context_version,next_step_id,row_version,last_event_sequence,
		COALESCE(snapshot_record_id::text,'') FROM agent_head WHERE tenant_id=$1 AND agent_id=$2`,
		f.tenant, root.Input.AgentID).Scan(&version, &nextStep, &rowVersion, &lastEvent, &snapshotID); err != nil {
		t.Fatal(err)
	}
	if version != 2 || nextStep != 2 || rowVersion != 2 || lastEvent != 5 || snapshotID == "" {
		t.Fatalf("head version=%d next=%d row=%d events=%d snapshot=%q", version, nextStep, rowVersion, lastEvent, snapshotID)
	}
	var status, candidate, published string
	if err := f.pool.QueryRow(t.Context(), `SELECT status,COALESCE(candidate_snapshot_object_id::text,''),
		COALESCE(published_context_record_id::text,'') FROM compaction_jobs WHERE tenant_id=$1 AND agent_id=$2`,
		f.tenant, root.Input.AgentID).Scan(&status, &candidate, &published); err != nil {
		t.Fatal(err)
	}
	if status != "published" || candidate == "" || published != snapshotID {
		t.Fatalf("compaction status=%s candidate=%q published=%q", status, candidate, published)
	}
	var contexts, events int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM context_records WHERE agent_id=$1),
		(SELECT count(*) FROM event_index WHERE agent_id=$1)`, root.Input.AgentID).Scan(&contexts, &events); err != nil {
		t.Fatal(err)
	}
	if contexts != 2 || events != 5 {
		t.Fatalf("contexts=%d events=%d", contexts, events)
	}
	var objectID string
	if err := f.pool.QueryRow(t.Context(), `SELECT object_id::text FROM context_records
		WHERE agent_id=$1 AND context_version=2`, root.Input.AgentID).Scan(&objectID); err != nil {
		t.Fatal(err)
	}
	data, err := f.payloads.Get(t.Context(), f.tenant, objectID)
	if err != nil || !strings.Contains(string(data), "Compacted context (version 2") ||
		!strings.Contains(string(data), "task_requirements") || !strings.Contains(string(data), "inspect the note") {
		t.Fatalf("compacted context object=%s err=%v", data, err)
	}
	// A retried Activity after publication must not compact the new Context again.
	again, err := f.a.Compact(t.Context(), agent.CompactInput{Session: root.Input})
	if err != nil || again.State.ContextVersion != 2 || calls.Load() != 2 {
		t.Fatalf("post-publication retry compacted again: progress=%+v calls=%d err=%v", again, calls.Load(), err)
	}
}

func TestCompactReusesSavedResponse(t *testing.T) {
	f, root, calls := compactRuntime(t)
	_, compact := prepareCompact(t, f, root, time.Minute)
	responseRef, err := f.payloads.Put(t.Context(), f.tenant, "compact-response", compactAnswer(string(mustJSON(t, testCompactSummary()))))
	if err != nil {
		t.Fatal(err)
	}
	compact.ResponseRef = responseRef
	if err := f.store.SaveCompactResponse(t.Context(), compact); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	progress, err := f.a.Compact(t.Context(), agent.CompactInput{Session: root.Input})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before || progress.State.ContextVersion != 2 {
		t.Fatalf("saved compact response was resent: calls=%d progress=%+v", calls.Load(), progress)
	}
	var status string
	if err := f.pool.QueryRow(t.Context(), `SELECT status FROM compaction_jobs WHERE agent_id=$1`, root.Input.AgentID).Scan(&status); err != nil || status != "published" {
		t.Fatalf("compaction status=%s err=%v", status, err)
	}
}

func TestCompactStaledByAdvancedHead(t *testing.T) {
	f, root, _ := compactRuntime(t)
	_, compact := prepareCompact(t, f, root, time.Minute)
	responseRef, err := f.payloads.Put(t.Context(), f.tenant, "compact-response", compactAnswer(string(mustJSON(t, testCompactSummary()))))
	if err != nil {
		t.Fatal(err)
	}
	compact.ResponseRef = responseRef
	if err := f.store.SaveCompactResponse(t.Context(), compact); err != nil {
		t.Fatal(err)
	}
	step := agent.StepInput{Session: root.Input, StepID: 2}
	modelResult, err := f.a.ModelStep(t.Context(), step)
	if err != nil || len(modelResult.Calls) != 1 {
		t.Fatalf("second model=%+v err=%v", modelResult, err)
	}
	plan, err := f.a.PrepareTool(t.Context(), agent.ToolInput{Step: step, Call: modelResult.Calls[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.ExecuteTool(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	advanced, err := f.a.Checkpoint(t.Context(), step)
	if err != nil || advanced.Completed || advanced.State.ContextVersion != 2 {
		t.Fatalf("advanced checkpoint: %+v err=%v", advanced, err)
	}
	_, err = f.store.PublishCompact(t.Context(), agent.CompactPublication{
		Record: compact, ContextRecordID: newRuntimeID(), SnapshotRef: newRuntimeID()})
	if !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("expected a stale conflict, got %v", err)
	}
	var status string
	if err := f.pool.QueryRow(t.Context(), `SELECT status FROM compaction_jobs WHERE agent_id=$1 AND source_row_version=$2`,
		root.Input.AgentID, compact.SourceRowVersion).Scan(&status); err != nil || status != "stale" {
		t.Fatalf("compaction status=%s err=%v", status, err)
	}
}

func TestCompactExpiredLeaseIsUncertain(t *testing.T) {
	f, root, _ := compactRuntime(t)
	prepareCompact(t, f, root, 20*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	record, err := f.store.LoadAgent(t.Context(), f.tenant, root.Input.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.ClaimCompact(t.Context(), agent.CompactInput{Session: root.Input, RowVersion: record.Head.RowVersion},
		newRuntimeID(), time.Minute)
	if !errors.Is(err, agent.ErrUncertain) {
		t.Fatalf("expected an uncertain expired lease, got %v", err)
	}
	var status string
	if err := f.pool.QueryRow(t.Context(), `SELECT status FROM compaction_jobs WHERE agent_id=$1`, root.Input.AgentID).Scan(&status); err != nil || status != "uncertain" {
		t.Fatalf("compaction status=%s err=%v", status, err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
