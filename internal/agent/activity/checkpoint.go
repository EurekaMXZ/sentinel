package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	openaigo "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"sentinel/internal/agent"
	"sentinel/internal/eventlog"
	modelapi "sentinel/internal/llm/openai"
)

func (a *Activities) Checkpoint(ctx context.Context, input agent.StepInput) (agent.Progress, error) {
	record, task, err := a.load(ctx, input.Session)
	if err != nil {
		return agent.Progress{}, err
	}
	step, err := a.Store.LoadStep(ctx, input)
	if err != nil {
		return agent.Progress{}, runtimeError("load_step_checkpoint", err)
	}
	if step.Status == "succeeded" {
		return progress(record, task), nil
	}
	if record.Head.NextStepID != input.StepID || step.Status != "response_saved" || record.Status != "running" {
		return agent.Progress{}, permanent("checkpoint_cursor_mismatch")
	}
	var request struct {
		Input []json.RawMessage `json:"input"`
	}
	var response responses.Response
	if err := a.getJSON(ctx, input.Session.TenantID, step.RequestRef, &request); err != nil {
		return agent.Progress{}, err
	}
	if err := a.getJSON(ctx, input.Session.TenantID, step.ResponseRef, &response); err != nil {
		return agent.Progress{}, err
	}
	calls, err := a.responseCalls(ctx, input.Session.TenantID, task, response)
	if err != nil {
		return agent.Progress{}, err
	}
	output, err := modelapi.OutputAsInput(response.Output)
	if err != nil {
		return agent.Progress{}, permanent("invalid_response_context")
	}
	items := request.Input
	appendedBytes := 0
	for _, item := range output {
		raw, err := json.Marshal(item)
		if err != nil {
			return agent.Progress{}, permanent("invalid_response_context")
		}
		appendedBytes += len(raw)
		items = append(items, raw)
	}
	checkpoint := agent.Checkpoint{Step: input, ToolCount: len(calls), ExpectedHead: record.Head, ContextRecordID: newID()}
	now := time.Now().UTC()
	appendEvent := func(kind, operationID, ref string) {
		checkpoint.Events = append(checkpoint.Events, eventlog.Event{EventID: newID(), TenantID: input.Session.TenantID,
			AgentID: input.Session.AgentID, OperationID: operationID, Sequence: record.Head.LastEventSequence + int64(len(checkpoint.Events)) + 1,
			Type: kind, PayloadRef: ref, CreatedAt: now})
	}
	appendToolEvent := func(kind, operationID, ref string, tool agent.ToolRecord) {
		checkpoint.Events = append(checkpoint.Events, eventlog.Event{EventID: newID(), TenantID: input.Session.TenantID,
			AgentID: input.Session.AgentID, OperationID: operationID, Sequence: record.Head.LastEventSequence + int64(len(checkpoint.Events)) + 1,
			Type: kind, PayloadRef: ref, RepositoryID: tool.Source.RepositoryID,
			SourceSnapshotID: tool.Source.SourceSnapshotID, BaseSnapshotID: tool.Source.BaseSnapshotID,
			TargetSnapshotID: tool.Source.TargetSnapshotID, CreatedAt: now})
	}
	sourceRefs := make([]agent.ContextSourceRef, 0)
	seenSourceRefs := make(map[agent.ContextSourceRef]bool)
	addSourceRef := func(repositoryID, snapshotID, role string) {
		if repositoryID == "" || snapshotID == "" {
			return
		}
		ref := agent.ContextSourceRef{RepositoryID: repositoryID, SourceSnapshotID: snapshotID, Role: role}
		if !seenSourceRefs[ref] {
			seenSourceRefs[ref] = true
			sourceRefs = append(sourceRefs, ref)
		}
	}
	appendEvent("model_request", step.OperationID, step.RequestRef)
	appendEvent("model_response", step.OperationID, step.ResponseRef)
	for _, call := range calls {
		tool, err := a.Store.LoadTool(ctx, agent.ToolInput{Step: input, Call: call})
		if err != nil {
			return agent.Progress{}, runtimeError("load_checkpoint_tool", err)
		}
		if tool.ResultRef == "" || (tool.Status != "succeeded" && tool.Status != "failed") {
			return agent.Progress{}, permanent("incomplete_tool_result")
		}
		data, err := a.Payloads.Get(ctx, input.Session.TenantID, tool.ResultRef)
		if err != nil {
			return agent.Progress{}, err
		}
		item := responses.ResponseInputItemParamOfFunctionCallOutput(string(data))
		item.OfFunctionCallOutput.CallID = openaigo.String(call.CallID)
		raw, err := json.Marshal(item)
		if err != nil {
			return agent.Progress{}, permanent("invalid_tool_context")
		}
		appendedBytes += len(raw)
		items = append(items, raw)
		metadataRef, err := a.putJSON(ctx, input.Session.TenantID, "tool-execution", planFor(tool))
		if err != nil {
			return agent.Progress{}, err
		}
		if tool.Status == "succeeded" {
			addSourceRef(tool.Source.RepositoryID, tool.Source.SourceSnapshotID, "evidence")
			addSourceRef(tool.Source.RepositoryID, tool.Source.BaseSnapshotID, "diff_base")
			addSourceRef(tool.Source.RepositoryID, tool.Source.TargetSnapshotID, "diff_target")
		}
		appendToolEvent("assistant_tool_call", tool.OperationID, call.ArgumentsRef, tool)
		appendToolEvent("tool_execution", tool.OperationID, metadataRef, tool)
		appendToolEvent("tool_result", tool.OperationID, tool.ResultRef, tool)
	}
	checkpoint.SourceRefs = sourceRefs
	if len(calls) == 0 {
		resultRefs := make([]agent.SourceRef, 0)
		seenResultRefs := make(map[agent.SourceRef]bool)
		for _, ref := range sourceRefs {
			sourceRef := agent.SourceRef{RepositoryID: ref.RepositoryID, SourceSnapshotID: ref.SourceSnapshotID}
			if seenResultRefs[sourceRef] {
				continue
			}
			seenResultRefs[sourceRef] = true
			resultRefs = append(resultRefs, sourceRef)
		}
		checkpoint.ResultRef, err = a.putJSON(ctx, input.Session.TenantID, "agent-result", agent.StructuredResult{
			AgentID: input.Session.AgentID, Summary: responseSummary(response), ResponseRef: step.ResponseRef,
			Artifacts: []string{}, SourceRefs: resultRefs})
		if err != nil {
			return agent.Progress{}, err
		}
		appendEvent("agent_completed", step.OperationID, checkpoint.ResultRef)
	}
	checkpoint.ContextRef, err = a.putJSON(ctx, input.Session.TenantID, "agent-context", ContextSnapshot{
		Version: record.Head.ContextVersion + 1, Items: items, Resources: record.Resources})
	if err != nil {
		return agent.Progress{}, err
	}
	var segment bytes.Buffer
	encoder := json.NewEncoder(&segment)
	for _, event := range checkpoint.Events {
		if err := encoder.Encode(event); err != nil {
			return agent.Progress{}, permanent("invalid_event_segment")
		}
	}
	checkpoint.SegmentRef, err = a.Payloads.Put(ctx, input.Session.TenantID, "event-segment", segment.Bytes())
	if err != nil {
		return agent.Progress{}, err
	}
	record, err = a.Store.CommitStep(ctx, checkpoint)
	if err != nil {
		return agent.Progress{}, runtimeError("commit_step", err)
	}
	result := progress(record, task)
	if checkpoint.ResultRef == "" && record.Status == "running" {
		assessment, err := a.assessBudget(task, items, appendedBytes, step.InputTokens)
		if err != nil {
			return agent.Progress{}, err
		}
		result.NeedsCompact, result.ContextTokens = assessment.NeedsCompact, assessment.ContextTokens
	}
	return result, nil
}
