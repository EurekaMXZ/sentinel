//go:build integration

package postgres_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel/internal/agent"
	agentactivity "sentinel/internal/agent/activity"
	modelapi "sentinel/internal/llm/openai"
	"sentinel/internal/platform/config"
	"sentinel/internal/source"
	"sentinel/internal/storage/postgres"
)

func TestAgentSourceToolReadsAndRecordsEvidence(t *testing.T) {
	f := newSourceFixture(t)
	sourceCommand(t, f.git.remote, "config", "core.autocrlf", "false")
	writeSourceTree(t, f.git.remote, "root.txt", "alpha\nbeta\ngamma\n")
	commit := commitSourceTree(t, f, "first")
	f.synchronize(t)
	snapshots, _, err := f.store.ListReadySnapshots(t.Context(), f.repo.TenantID, f.repo.ID, 0, 10)
	if err != nil || len(snapshots) != 1 || snapshots[0].CommitSHA != commit {
		t.Fatalf("snapshots=%+v err=%v", snapshots, err)
	}
	snapshot := snapshots[0]

	store := postgres.NewAgentStore(f.pool)
	tools := make(map[string]agentactivity.Tool)
	for _, definition := range agentactivity.SourceToolDefinitions() {
		tools[definition.Name] = agentactivity.Tool{Definition: definition, Source: true}
	}
	activities := &agentactivity.Activities{Store: store, Payloads: f.a.Payloads, Tools: tools}
	arguments, err := json.Marshal(map[string]string{"source_snapshot_id": snapshot.ID, "path": "root.txt"})
	if err != nil {
		t.Fatal(err)
	}
	quotedArguments, err := json.Marshal(string(arguments))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), source.ToolReadFile) {
			t.Errorf("source tool was not offered to the model: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"resp-tools","object":"response","status":"completed","output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":%q,"arguments":%s,"status":"completed"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`,
			source.ToolReadFile, quotedArguments)
	}))
	t.Cleanup(server.Close)
	model, err := modelapi.NewModel(config.OpenAI{APIKey: "test", Model: "test-model",
		BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	activities.Model = model

	resources := []agent.ResourceBinding{{BindingName: "source", Kind: agent.ResourceSourceSnapshot,
		RepositoryID: f.repo.ID, SourceSnapshotID: snapshot.ID}}
	root, err := activities.CreateRoot(t.Context(), f.repo.TenantID, agent.Task{
		Prompt: "read the note", Instructions: "test instructions", Model: "test-model",
		Tools: []string{source.ToolReadFile}, SourceTaskQueue: "source-queue",
		Limits: agent.DefaultLimits()}, resources)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activities.Initialize(t.Context(), agent.InitializeInput{
		Session: root.Input, WorkflowID: root.WorkflowID, RunID: "run-source"}); err != nil {
		t.Fatal(err)
	}
	step := agent.StepInput{Session: root.Input, StepID: 1}
	modelResult, err := activities.ModelStep(t.Context(), step)
	if err != nil || len(modelResult.Calls) != 1 {
		t.Fatalf("model=%+v err=%v", modelResult, err)
	}
	plan, err := activities.PrepareTool(t.Context(), agent.ToolInput{Step: step, Call: modelResult.Calls[0]})
	if err != nil || plan.Source == nil {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if plan.Source.RepositoryID != f.repo.ID || plan.Source.SourceSnapshotID != snapshot.ID || plan.Source.Path != "root.txt" {
		t.Fatalf("unexpected source request: %+v", plan.Source)
	}
	sourceResult, err := f.a.Read(t.Context(), *plan.Source)
	if err != nil || sourceResult.Failed || sourceResult.ResultRef == "" {
		t.Fatalf("source read=%+v err=%v", sourceResult, err)
	}
	completed, err := activities.CompleteTool(t.Context(), agent.ToolCompletion{
		Plan: plan, RemoteResultRef: sourceResult.ResultRef})
	if err != nil || completed.ResultRef == "" {
		t.Fatalf("complete=%+v err=%v", completed, err)
	}
	resultData, err := activities.Payloads.Get(t.Context(), f.repo.TenantID, completed.ResultRef)
	if err != nil || !strings.Contains(string(resultData), "beta") ||
		!strings.Contains(string(resultData), sourceResult.ResultRef) {
		t.Fatalf("tool result=%s err=%v", resultData, err)
	}
	progress, err := activities.Checkpoint(t.Context(), step)
	if err != nil || progress.Completed || progress.State.ContextVersion != 1 {
		t.Fatalf("checkpoint=%+v err=%v", progress, err)
	}

	var repositoryID, snapshotID string
	if err := f.pool.QueryRow(t.Context(), `SELECT COALESCE(repository_id::text,''),COALESCE(source_snapshot_id::text,'')
		FROM tool_executions WHERE tenant_id=$1 AND agent_id=$2 AND step_id=1 AND tool_call_id='call-1'`,
		f.repo.TenantID, root.Input.AgentID).Scan(&repositoryID, &snapshotID); err != nil {
		t.Fatal(err)
	}
	if repositoryID != f.repo.ID || snapshotID != snapshot.ID {
		t.Fatalf("tool source refs repo=%q snapshot=%q", repositoryID, snapshotID)
	}
	var evidence int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM context_source_refs r
		JOIN context_records c ON c.tenant_id=r.tenant_id AND c.context_record_id=r.context_record_id
		WHERE c.agent_id=$1 AND r.source_snapshot_id=$2 AND r.role='evidence'`,
		root.Input.AgentID, snapshot.ID).Scan(&evidence); err != nil || evidence != 1 {
		t.Fatalf("context evidence=%d err=%v", evidence, err)
	}
	var eventRepository, eventSnapshot string
	if err := f.pool.QueryRow(t.Context(), `SELECT COALESCE(repository_id::text,''),COALESCE(source_snapshot_id::text,'')
		FROM event_index WHERE agent_id=$1 AND event_type='tool_result'`,
		root.Input.AgentID).Scan(&eventRepository, &eventSnapshot); err != nil {
		t.Fatal(err)
	}
	if eventRepository != f.repo.ID || eventSnapshot != snapshot.ID {
		t.Fatalf("event source refs repo=%q snapshot=%q", eventRepository, eventSnapshot)
	}
}
