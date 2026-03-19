package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"

	"sentinel/internal/agent"
)

func compactTestResponse(text string) responses.Response {
	raw := fmt.Sprintf(`{"id":"resp-1","object":"response","status":"completed","output":[{"type":"message","id":"msg-1","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`, text)
	var response responses.Response
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		panic(err)
	}
	return response
}

func validCompactSummary() agent.CompactSummary {
	return agent.CompactSummary{
		TaskRequirements:  []string{"analyze auth"},
		Constraints:       []string{"read-only"},
		Decisions:         []string{"compare token expiry"},
		CurrentState:      []string{"located expiry check"},
		OpenItems:         []string{"confirm caller contract"},
		Errors:            []string{},
		ChildAgentResults: []string{},
		ToolArtifactRefs:  []string{},
		SourceRefs:        []agent.SourceRef{},
	}
}

func TestParseCompactSummaryRequiresStrictJSON(t *testing.T) {
	valid, err := json.Marshal(validCompactSummary())
	if err != nil {
		t.Fatal(err)
	}
	summary, err := parseCompactSummary(compactTestResponse(string(valid)))
	if err != nil || len(summary.TaskRequirements) != 1 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	for _, text := range []string{
		"",
		"not json",
		`{"task_requirements":[]}`,
		`{"task_requirements":[],"constraints":[],"decisions":[],"current_state":[],"open_items":[],"errors":[],"child_agent_results":[],"tool_artifact_refs":[],"source_refs":[],"extra":1}`,
		string(valid[:len(valid)-1]),
	} {
		if _, err := parseCompactSummary(compactTestResponse(text)); err == nil {
			t.Errorf("accepted invalid compact output: %q", text)
		}
	}
}

func TestCompactSummaryLimits(t *testing.T) {
	activities := &Activities{Store: &compactStoreStub{}}
	task := agent.Task{Prompt: "task", Model: "model", Limits: agent.DefaultLimits()}
	record := agent.AgentRecord{Input: agent.SessionInput{TenantID: strings.Repeat("a", 1)}}
	summary := validCompactSummary()
	summary.Constraints = make([]string, maxCompactListItems+1)
	if err := activities.validateCompactSummary(context.Background(), record, task, summary); err == nil {
		t.Fatal("accepted too many compact items")
	}
	summary = validCompactSummary()
	summary.CurrentState = []string{strings.Repeat("x", maxCompactItemBytes+1)}
	if err := activities.validateCompactSummary(context.Background(), record, task, summary); err == nil {
		t.Fatal("accepted an oversized compact item")
	}
	if err := activities.validateCompactSummary(context.Background(), record, task, validCompactSummary()); err != nil {
		t.Fatalf("rejected valid summary: %v", err)
	}
}

type compactStoreStub struct {
	agent.RuntimeStore
	evidence []agent.SourceRef
	validate []agent.SourceRef
}

func (s *compactStoreStub) CurrentEvidence(context.Context, string, string, string) ([]agent.SourceRef, error) {
	return s.evidence, nil
}

func (s *compactStoreStub) ValidateSourceRefs(_ context.Context, _ string, refs []agent.SourceRef) error {
	s.validate = refs
	return nil
}

func (s *compactStoreStub) ValidateToolArtifacts(context.Context, string, []string) error {
	return nil
}

func TestCompactSourceReferencesAreScoped(t *testing.T) {
	repo := "0194e229-8576-7f42-8f08-3496913847cf"
	other := "0194e229-8576-7f42-8f08-3496913847ce"
	snapshot := "0194e229-8576-7f42-8f08-3496913847cd"
	store := &compactStoreStub{}
	activities := &Activities{Store: store}
	task := agent.Task{Prompt: "task", Model: "model", Limits: agent.DefaultLimits(),
		DelegationScope: []agent.ResourceBinding{{BindingName: "src", Kind: agent.ResourceSourceSnapshot,
			RepositoryID: repo, SourceSnapshotID: snapshot}}}
	record := agent.AgentRecord{Input: agent.SessionInput{TenantID: repo}}
	summary := validCompactSummary()
	summary.SourceRefs = []agent.SourceRef{{RepositoryID: repo, SourceSnapshotID: snapshot}}
	if err := activities.validateCompactSummary(context.Background(), record, task, summary); err != nil {
		t.Fatalf("rejected delegated source ref: %v", err)
	}
	if len(store.validate) != 1 {
		t.Fatal("source reference readiness was not checked")
	}
	summary.SourceRefs = []agent.SourceRef{{RepositoryID: other, SourceSnapshotID: snapshot}}
	if err := activities.validateCompactSummary(context.Background(), record, task, summary); err == nil {
		t.Fatal("accepted a source reference outside the delegation scope")
	}
}

func TestCompactRequestHasNoToolsAndUsesStructuredOutput(t *testing.T) {
	activities := &Activities{CompactStructuredOutput: true, Tools: map[string]Tool{}}
	task := agent.Task{Prompt: "task", Instructions: "instructions", Model: "model",
		Tools: []string{agent.ChildToolName}, Limits: agent.DefaultLimits()}
	record := agent.AgentRecord{Input: agent.SessionInput{TenantID: "tenant"}}
	request, err := activities.buildCompactRequest(context.Background(), record, task)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Tools) != 0 || request.Text.Format.OfJSONSchema == nil ||
		!request.Text.Format.OfJSONSchema.Strict.Valid() || !request.Text.Format.OfJSONSchema.Strict.Value {
		t.Fatalf("unexpected compact request: tools=%v text=%+v", request.Tools, request.Text)
	}
	if request.Instructions.Value != compactInstructions {
		t.Fatalf("unexpected compact instructions: %q", request.Instructions.Value)
	}
}

func TestBudgetTriggersCompact(t *testing.T) {
	activities := &Activities{Tools: map[string]Tool{}}
	task := agent.Task{Prompt: "task", Model: "model", Limits: agent.DefaultLimits()}
	items := []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)}
	assessment, err := activities.assessBudget(task, items, 0, 100)
	if err != nil || assessment.NeedsCompact {
		t.Fatalf("unexpected early compact: %+v err=%v", assessment, err)
	}
	assessment, err = activities.assessBudget(task, items, 0, task.Limits.ContextWindowTokens)
	if err != nil || !assessment.NeedsCompact {
		t.Fatalf("expected compact at the context limit: %+v err=%v", assessment, err)
	}
	assessment, err = activities.assessBudget(task, items, int(3*task.Limits.ContextWindowTokens), 1)
	if err != nil || !assessment.NeedsCompact {
		t.Fatalf("expected compact for large appended output: %+v err=%v", assessment, err)
	}
}

func TestCompactSnapshotRendersSummary(t *testing.T) {
	record := agent.AgentRecord{Input: agent.SessionInput{AgentID: "agent"}, Resources: []agent.ResourceBinding{}}
	summary := validCompactSummary()
	snapshot, err := compactSnapshot(record, summary, 3, 9)
	if err != nil || snapshot.Version != 3 || len(snapshot.Items) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	var item map[string]any
	if err := json.Unmarshal(snapshot.Items[0], &item); err != nil {
		t.Fatal(err)
	}
	content, _ := json.Marshal(item)
	if !strings.Contains(string(content), "Compacted context (version 3, through event 9)") ||
		!strings.Contains(string(content), "task_requirements") {
		t.Fatalf("unexpected compacted message: %s", content)
	}
}
