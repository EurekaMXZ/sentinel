package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	sdkclient "go.temporal.io/sdk/client"

	"sentinel/internal/agent"
	"sentinel/internal/payload"
	"sentinel/internal/source"
)

const (
	testTenant = "0194e229-8576-7f42-8f08-3496913847cf"
	testAgent  = "0194e229-8576-7f42-8f08-3496913847ce"
	testRepo   = "0194e229-8576-7f42-8f08-3496913847cd"
	testSync   = "0194e229-8576-7f42-8f08-3496913847cc"
)

type fakeAgentReader struct {
	records []agent.AgentRecord
	err     error
}

func (f *fakeAgentReader) LoadAgent(_ context.Context, _ string, id string) (agent.AgentRecord, error) {
	if f.err != nil {
		return agent.AgentRecord{}, f.err
	}
	for _, record := range f.records {
		if record.Input.AgentID == id {
			return record, nil
		}
	}
	return agent.AgentRecord{}, agent.ErrNotFound
}

func (f *fakeAgentReader) ListAgents(context.Context, string, int) ([]agent.AgentRecord, error) {
	return f.records, f.err
}

type fakeAgentCreator struct {
	record    agent.AgentRecord
	err       error
	task      agent.Task
	resources []agent.ResourceBinding
	calls     int
}

func (f *fakeAgentCreator) CreateRoot(_ context.Context, _ string, task agent.Task, resources []agent.ResourceBinding) (agent.AgentRecord, error) {
	f.calls++
	f.task, f.resources = task, resources
	return f.record, f.err
}

type fakeAgentResources struct{ err error }

func (f *fakeAgentResources) ValidateResources(context.Context, string, []agent.ResourceBinding) error {
	return f.err
}

type fakeSourceReader struct {
	repository   source.Repository
	repositories []source.Repository
	versions     []source.Snapshot
	more         bool
	job          source.SyncJob
	err          error
}

func (f *fakeSourceReader) LoadRepository(_ context.Context, _ string, id string) (source.Repository, error) {
	if f.err != nil {
		return source.Repository{}, f.err
	}
	if id != f.repository.ID {
		return source.Repository{}, source.ErrNotFound
	}
	return f.repository, nil
}

func (f *fakeSourceReader) CreateRepository(_ context.Context, repository source.Repository) (source.Repository, error) {
	f.repository = repository
	return repository, f.err
}

func (f *fakeSourceReader) ListRepositories(context.Context, string, int) ([]source.Repository, error) {
	return f.repositories, f.err
}

func (f *fakeSourceReader) ListReadySnapshots(context.Context, string, string, int, int) ([]source.Snapshot, bool, error) {
	return f.versions, f.more, f.err
}

func (f *fakeSourceReader) LoadSync(_ context.Context, request source.SyncRequest) (source.SyncJob, error) {
	if f.err != nil {
		return source.SyncJob{}, f.err
	}
	if request.SyncID != f.job.Request.SyncID {
		return source.SyncJob{}, source.ErrNotFound
	}
	return f.job, nil
}

type fakeSourceCreator struct {
	job   source.SyncJob
	err   error
	calls int
}

func (f *fakeSourceCreator) Create(_ context.Context, repository source.Repository) (source.SyncJob, error) {
	f.calls++
	f.job.Request.TenantID = repository.TenantID
	f.job.Request.RepositoryID = repository.ID
	return f.job, f.err
}

type fakePayloads struct {
	data map[string][]byte
	err  error
}

func (f *fakePayloads) Get(_ context.Context, tenantID, ref string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.data[tenantID+":"+ref]
	if !ok {
		return nil, payload.ErrNotFound
	}
	return data, nil
}

type fakeWorkflows struct {
	started   []sdkclient.StartWorkflowOptions
	cancelled []string
	err       error
}

func (f *fakeWorkflows) ExecuteWorkflow(_ context.Context, options sdkclient.StartWorkflowOptions, _ any, _ ...any) (sdkclient.WorkflowRun, error) {
	f.started = append(f.started, options)
	return nil, f.err
}

func (f *fakeWorkflows) CancelWorkflow(_ context.Context, workflowID, _ string) error {
	f.cancelled = append(f.cancelled, workflowID)
	return f.err
}

func controlDependencies() (Dependencies, *fakeAgentReader, *fakeAgentCreator, *fakeSourceReader, *fakeSourceCreator, *fakePayloads, *fakeWorkflows) {
	record := agent.AgentRecord{Input: agent.SessionInput{TenantID: testTenant, SessionID: testAgent, AgentID: testAgent,
		TaskRef: testSync}, WorkflowID: "workflow-1", Status: "running", CreatedAt: time.Now().UTC()}
	record.Head.RowVersion = 2
	reader := &fakeAgentReader{records: []agent.AgentRecord{record}}
	creator := &fakeAgentCreator{record: record}
	repository := source.Repository{ID: testRepo, TenantID: testTenant, RemoteURL: "https://github.com/owner/repository.git",
		TrackedRefs: []string{"refs/heads/*", "refs/tags/*"}, ObjectFormat: "sha1", Status: "active"}
	single := source.Snapshot{ID: "0194e229-8576-7f42-8f08-3496913847cb", RepositoryID: testRepo,
		CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TreeSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Status: source.SnapshotReady, CreatedAt: time.Now().UTC()}
	job := source.SyncJob{Request: source.SyncRequest{TenantID: testTenant, RepositoryID: testRepo, SyncID: testSync},
		WorkflowID: "sync-workflow", Status: "running"}
	sourceReader := &fakeSourceReader{repository: repository, repositories: []source.Repository{repository},
		versions: []source.Snapshot{single}, more: true, job: job}
	sourceCreator := &fakeSourceCreator{job: job}
	payloads := &fakePayloads{data: make(map[string][]byte)}
	workflows := &fakeWorkflows{}
	deps := Dependencies{Agents: reader, AgentTasks: creator, AgentResources: &fakeAgentResources{},
		Sources: sourceReader, SourceTasks: sourceCreator, Payloads: payloads, Workflows: workflows,
		AgentTaskQueue: "agents", SourceTaskQueue: "sources", Model: "test-model", Limits: agent.DefaultLimits()}
	return deps, reader, creator, sourceReader, sourceCreator, payloads, workflows
}

func testRouter(deps Dependencies) *gin.Engine {
	return NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), deps)
}

func requestJSON(t *testing.T, router http.Handler, method, path, tenant string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	if tenant != "" {
		request.Header.Set("X-Tenant-ID", tenant)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON response: %s", recorder.Body.String())
	}
	return body
}

func TestAgentControlEndpoints(t *testing.T) {
	deps, reader, creator, _, _, payloads, workflows := controlDependencies()
	router := testRouter(deps)
	created := requestJSON(t, router, http.MethodPost, "/v1/agents", testTenant, map[string]any{
		"task": "inspect", "max_steps": 12, "max_child_depth": 1,
		"resources": []map[string]any{{"binding_name": "src", "resource_kind": "source_repository", "repository_id": testRepo}},
	})
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if creator.calls != 1 || creator.task.Prompt != "inspect" || creator.task.Limits.MaxSteps != 12 ||
		creator.task.Limits.MaxChildDepth != 1 || len(creator.resources) != 1 ||
		creator.task.SourceTaskQueue != "sources" {
		t.Fatalf("unexpected create call: calls=%d task=%+v resources=%+v", creator.calls, creator.task, creator.resources)
	}
	if len(workflows.started) != 1 || workflows.started[0].TaskQueue != "agents" {
		t.Fatalf("workflow was not started: %+v", workflows.started)
	}
	if decode(t, created)["agent_id"] != testAgent {
		t.Fatalf("unexpected create response: %s", created.Body.String())
	}

	got := requestJSON(t, router, http.MethodGet, "/v1/agents/"+testAgent, testTenant, nil)
	if got.Code != http.StatusOK || decode(t, got)["status"] != "running" {
		t.Fatalf("get status=%d body=%s", got.Code, got.Body.String())
	}
	if missing := requestJSON(t, router, http.MethodGet, "/v1/agents/0194e229-8576-7f42-8f08-3496913847ca", testTenant, nil); missing.Code != http.StatusNotFound {
		t.Fatalf("missing agent status=%d", missing.Code)
	}
	if noTenant := requestJSON(t, router, http.MethodGet, "/v1/agents/"+testAgent, "", nil); noTenant.Code != http.StatusBadRequest {
		t.Fatalf("missing tenant status=%d", noTenant.Code)
	}

	reader.records[0].ResultRef = "result-object"
	payloads.data[testTenant+":result-object"] = mustJSON(agent.StructuredResult{AgentID: testAgent,
		Summary: "done", Artifacts: []string{}, SourceRefs: []agent.SourceRef{}})
	result := requestJSON(t, router, http.MethodGet, "/v1/agents/"+testAgent+"/result", testTenant, nil)
	if result.Code != http.StatusOK || decode(t, result)["summary"] != "done" {
		t.Fatalf("result status=%d body=%s", result.Code, result.Body.String())
	}

	cancelled := requestJSON(t, router, http.MethodPost, "/v1/agents/"+testAgent+"/cancel", testTenant, nil)
	if cancelled.Code != http.StatusAccepted || len(workflows.cancelled) != 1 {
		t.Fatalf("cancel status=%d cancelled=%v", cancelled.Code, workflows.cancelled)
	}
	resumed := requestJSON(t, router, http.MethodPost, "/v1/agents/"+testAgent+"/resume", testTenant, nil)
	if resumed.Code != http.StatusAccepted || len(workflows.started) != 2 {
		t.Fatalf("resume status=%d started=%d", resumed.Code, len(workflows.started))
	}

	reader.records[0].Status = "failed"
	terminalCancel := requestJSON(t, router, http.MethodPost, "/v1/agents/"+testAgent+"/cancel", testTenant, nil)
	if terminalCancel.Code != http.StatusConflict {
		t.Fatalf("terminal cancel status=%d", terminalCancel.Code)
	}
	terminalResume := requestJSON(t, router, http.MethodPost, "/v1/agents/"+testAgent+"/resume", testTenant, nil)
	if terminalResume.Code != http.StatusConflict {
		t.Fatalf("terminal resume status=%d", terminalResume.Code)
	}

	invalid := requestJSON(t, router, http.MethodPost, "/v1/agents", testTenant, map[string]any{})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid create status=%d", invalid.Code)
	}
}

func TestSourceControlEndpoints(t *testing.T) {
	deps, _, _, reader, creator, payloads, workflows := controlDependencies()
	router := testRouter(deps)
	created := requestJSON(t, router, http.MethodPost, "/v1/source/repositories", testTenant, map[string]any{
		"url": "https://github.com/owner/repository", "tracked_refs": []string{"refs/tags/v*", "refs/heads/*"},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create repository status=%d body=%s", created.Code, created.Body.String())
	}
	if reader.repository.RemoteURL != "https://github.com/owner/repository.git" ||
		len(reader.repository.TrackedRefs) != 2 || reader.repository.TrackedRefs[0] != "refs/heads/*" {
		t.Fatalf("unexpected repository: %+v", reader.repository)
	}
	listed := requestJSON(t, router, http.MethodGet, "/v1/source/repositories", testTenant, nil)
	if listed.Code != http.StatusOK || len(decode(t, listed)["repositories"].([]any)) != 1 {
		t.Fatalf("list repositories status=%d body=%s", listed.Code, listed.Body.String())
	}
	repositoryID := reader.repository.ID
	versions := requestJSON(t, router, http.MethodGet, "/v1/source/repositories/"+repositoryID+"/versions?limit=10", testTenant, nil)
	if versions.Code != http.StatusOK || decode(t, versions)["more"] != true {
		t.Fatalf("versions status=%d body=%s", versions.Code, versions.Body.String())
	}

	createdSync := requestJSON(t, router, http.MethodPost, "/v1/source/repositories/"+repositoryID+"/syncs", testTenant, nil)
	if createdSync.Code != http.StatusAccepted || creator.calls != 1 || len(workflows.started) != 1 {
		t.Fatalf("create sync status=%d calls=%d started=%d body=%s", createdSync.Code, creator.calls, len(workflows.started), createdSync.Body.String())
	}
	reader.job.Status = "succeeded"
	reader.job.ResultRef = "sync-result"
	payloads.data[testTenant+":sync-result"] = mustJSON(source.Candidate{Version: 1,
		Request: reader.job.Request, Manifest: source.Manifest{ID: "0194e229-8576-7f42-8f08-3496913847c9"},
		Snapshots: []source.Snapshot{{ID: "0194e229-8576-7f42-8f08-3496913847ca"}}})
	status := requestJSON(t, router, http.MethodGet, "/v1/source/syncs/"+testSync, testTenant, nil)
	if status.Code != http.StatusOK || decode(t, status)["manifest_id"] != "0194e229-8576-7f42-8f08-3496913847c9" {
		t.Fatalf("sync status=%d body=%s", status.Code, status.Body.String())
	}
	resumed := requestJSON(t, router, http.MethodPost, "/v1/source/syncs/"+testSync+"/resume", testTenant, nil)
	if resumed.Code != http.StatusOK {
		t.Fatalf("succeeded sync resume status=%d body=%s", resumed.Code, resumed.Body.String())
	}
	reader.job.Status = "failed"
	terminal := requestJSON(t, router, http.MethodPost, "/v1/source/syncs/"+testSync+"/resume", testTenant, nil)
	if terminal.Code != http.StatusConflict {
		t.Fatalf("terminal sync resume status=%d", terminal.Code)
	}
	missing := requestJSON(t, router, http.MethodGet, "/v1/source/repositories/0194e229-8576-7f42-8f08-3496913847c8", testTenant, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing repository status=%d", missing.Code)
	}
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
