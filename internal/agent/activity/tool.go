package activity

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"sentinel/internal/agent"
	"sentinel/internal/source"
	"sentinel/internal/stream"
)

type childArguments struct {
	Task           string                  `json:"task"`
	ContextRef     string                  `json:"context_ref,omitempty"`
	Resources      []agent.ResourceBinding `json:"resources,omitempty"`
	ExpectedOutput string                  `json:"expected_output,omitempty"`
}

type toolOutput struct {
	OK        bool            `json:"ok"`
	Code      string          `json:"code,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	ChildID   string          `json:"child_agent_id,omitempty"`
	ResultRef string          `json:"result_ref,omitempty"`
}

func (a *Activities) PrepareTool(ctx context.Context, input agent.ToolInput) (agent.ToolPlan, error) {
	agentRecord, task, err := a.verifyTool(ctx, input)
	if err != nil {
		return agent.ToolPlan{}, err
	}
	if !slices.Contains(task.Tools, input.Call.Name) {
		record, err := a.prepareToolRecord(ctx, input, agent.ToolSourceRefs{})
		if err != nil {
			return agent.ToolPlan{}, err
		}
		if record.ResultRef != "" {
			return planFor(record), nil
		}
		return a.toolFailure(ctx, record, "tool_not_allowed")
	}
	var arguments argumentsDocument
	if err := a.getJSON(ctx, input.Step.Session.TenantID, input.Call.ArgumentsRef, &arguments); err != nil {
		return agent.ToolPlan{}, err
	}
	if !json.Valid([]byte(arguments.JSON)) {
		record, err := a.prepareToolRecord(ctx, input, agent.ToolSourceRefs{})
		if err != nil {
			return agent.ToolPlan{}, err
		}
		if record.ResultRef != "" {
			return planFor(record), nil
		}
		return a.toolFailure(ctx, record, "invalid_tool_arguments")
	}
	var sourceRequest *source.ToolRequest
	var sourceRefs agent.ToolSourceRefs
	failureCode := ""
	if input.Call.Name != agent.ChildToolName {
		if a.Tools[input.Call.Name].Source {
			sourceRequest, sourceRefs, failureCode, err = a.prepareSourceTool(ctx, agentRecord, input, []byte(arguments.JSON))
			if err != nil {
				return agent.ToolPlan{}, err
			}
		}
	}
	record, err := a.prepareToolRecord(ctx, input, sourceRefs)
	if err != nil {
		return agent.ToolPlan{}, err
	}
	plan := planFor(record)
	if record.ResultRef != "" {
		return plan, nil
	}
	if failureCode != "" {
		return a.toolFailure(ctx, record, failureCode)
	}
	a.publishEvent(ctx, input.Step.Session.TenantID, input.Step.Session.AgentID, stream.EventToolStarted,
		input.Step.StepID, 0, map[string]string{"tool": input.Call.Name})
	if input.Call.Name != agent.ChildToolName {
		if sourceRequest != nil {
			plan.Source = sourceRequest
		}
		return plan, nil
	}
	if task.Depth >= task.Limits.MaxChildDepth {
		return a.toolFailure(ctx, record, "child_depth_exhausted")
	}
	var args childArguments
	if err := strictJSON([]byte(arguments.JSON), &args); err != nil || strings.TrimSpace(args.Task) == "" || len(args.Task) > 128<<10 || len(args.Resources) > 32 {
		return a.toolFailure(ctx, record, "invalid_child_arguments")
	}
	for _, binding := range args.Resources {
		if binding.Validate() != nil || !agent.CanDelegate(task.DelegationScope, binding) {
			return a.toolFailure(ctx, record, "child_resource_denied")
		}
	}
	if args.ContextRef != "" {
		contextResource := agent.ResourceBinding{BindingName: "initial_context", Kind: agent.ResourceObject, ObjectID: args.ContextRef}
		if contextResource.Validate() != nil || !agent.CanDelegate(task.DelegationScope, contextResource) {
			return a.toolFailure(ctx, record, "child_context_denied")
		}
		var initial InitialContext
		if err := a.getJSON(ctx, input.Step.Session.TenantID, args.ContextRef, &initial); err != nil {
			return a.toolFailure(ctx, record, "invalid_child_context")
		}
	}
	if err := a.Store.ValidateResources(ctx, input.Step.Session.TenantID, args.Resources); err != nil {
		return a.toolFailure(ctx, record, "child_resource_unavailable")
	}
	child, err := a.Store.FindChild(ctx, input.Step.Session.TenantID, record.ID)
	if errors.Is(err, agent.ErrNotFound) {
		childTask := agent.Task{Prompt: args.Task, Instructions: task.Instructions, InitialContextRef: args.ContextRef,
			ExpectedOutput: args.ExpectedOutput, Model: task.Model, Tools: task.Tools, SourceTaskQueue: task.SourceTaskQueue,
			Limits: task.Limits, Depth: task.Depth + 1, DelegationScope: slices.Clone(args.Resources)}
		if args.ContextRef != "" {
			childTask.DelegationScope = append(childTask.DelegationScope, agent.ResourceBinding{
				BindingName: "initial_context", Kind: agent.ResourceObject, ObjectID: args.ContextRef})
		}
		ref, saveErr := a.putJSON(ctx, input.Step.Session.TenantID, "agent-task", childTask)
		if saveErr != nil {
			return plan, saveErr
		}
		childInput := agent.SessionInput{TenantID: input.Step.Session.TenantID, SessionID: input.Step.Session.SessionID,
			AgentID: newID(), TaskRef: ref, ContextRef: args.ContextRef}
		child, err = a.Store.CreateAgent(ctx, agent.AgentRecord{Input: childInput, ParentAgentID: input.Step.Session.AgentID,
			ParentToolExecutionID: record.ID, Resources: args.Resources, WorkflowID: agent.AgentWorkflowID(childInput.TenantID, childInput.AgentID)})
	}
	if err != nil {
		return plan, runtimeError("prepare_child", err)
	}
	if child.Status == "completed" || child.Status == "failed" || child.Status == "cancelled" {
		return a.CompleteTool(ctx, agent.ToolCompletion{Plan: plan, ChildFailed: child.Status != "completed",
			ChildResult: agent.Result{AgentID: child.Input.AgentID, ResultRef: child.ResultRef}})
	}
	plan.Child = &agent.ChildLaunch{Input: agent.WorkflowInput{Session: child.Input}, WorkflowID: child.WorkflowID}
	return plan, nil
}

func (a *Activities) prepareToolRecord(ctx context.Context, input agent.ToolInput, refs agent.ToolSourceRefs) (agent.ToolRecord, error) {
	record, err := a.Store.PrepareTool(ctx, agent.ToolRecord{Input: input, ID: newID(), Source: refs,
		OperationID: agent.ToolOperation(input.Step.Session.AgentID, input.Step.StepID, input.Call.CallID)})
	if err != nil {
		return record, runtimeError("prepare_tool", err)
	}
	return record, nil
}

type sourceListVersionsArgs struct {
	RepositoryID string `json:"repository_id"`
	Cursor       string `json:"cursor"`
	Limit        int    `json:"limit"`
}

type sourceListFilesArgs struct {
	SourceSnapshotID string `json:"source_snapshot_id"`
	Path             string `json:"path"`
	Cursor           string `json:"cursor"`
	Limit            int    `json:"limit"`
}

type sourceReadFileArgs struct {
	SourceSnapshotID string `json:"source_snapshot_id"`
	Path             string `json:"path"`
	StartLine        int    `json:"start_line"`
	EndLine          int    `json:"end_line"`
}

type sourceSearchArgs struct {
	SourceSnapshotID string `json:"source_snapshot_id"`
	Query            string `json:"query"`
	Path             string `json:"path"`
	Cursor           string `json:"cursor"`
	Limit            int    `json:"limit"`
}

type sourceDiffArgs struct {
	BaseSnapshotID   string `json:"base_snapshot_id"`
	TargetSnapshotID string `json:"target_snapshot_id"`
	Path             string `json:"path"`
	Cursor           string `json:"cursor"`
	Limit            int    `json:"limit"`
}

func (a *Activities) prepareSourceTool(ctx context.Context, record agent.AgentRecord, input agent.ToolInput, raw []byte) (*source.ToolRequest, agent.ToolSourceRefs, string, error) {
	request := source.ToolRequest{TenantID: record.Input.TenantID, AgentID: record.Input.AgentID,
		OperationID: agent.ToolOperation(record.Input.AgentID, input.Step.StepID, input.Call.CallID),
		ToolCallID:  input.Call.CallID, Name: input.Call.Name}
	refs := agent.ToolSourceRefs{}
	switch input.Call.Name {
	case source.ToolListVersions:
		var args sourceListVersionsArgs
		if strictJSON(raw, &args) != nil || !source.ValidID(args.RepositoryID) {
			return nil, refs, "invalid_tool_arguments", nil
		}
		if !agent.CanAccessRepository(record.Resources, args.RepositoryID) {
			return nil, refs, "source_access_denied", nil
		}
		request.RepositoryID, request.Cursor, request.Limit = args.RepositoryID, args.Cursor, args.Limit
		refs.RepositoryID = args.RepositoryID
	case source.ToolListFiles:
		var args sourceListFilesArgs
		if strictJSON(raw, &args) != nil || !source.ValidID(args.SourceSnapshotID) || !source.ValidSourcePath(args.Path) {
			return nil, refs, "invalid_tool_arguments", nil
		}
		repositoryID, code, err := a.resolveSourceSnapshot(ctx, record, args.SourceSnapshotID)
		if err != nil || code != "" {
			return nil, refs, code, err
		}
		request.RepositoryID, request.SourceSnapshotID = repositoryID, args.SourceSnapshotID
		request.Path, request.Cursor, request.Limit = args.Path, args.Cursor, args.Limit
		refs.RepositoryID, refs.SourceSnapshotID = repositoryID, args.SourceSnapshotID
	case source.ToolReadFile:
		var args sourceReadFileArgs
		if strictJSON(raw, &args) != nil || !source.ValidID(args.SourceSnapshotID) ||
			strings.TrimSpace(args.Path) == "" || !source.ValidSourcePath(args.Path) {
			return nil, refs, "invalid_tool_arguments", nil
		}
		repositoryID, code, err := a.resolveSourceSnapshot(ctx, record, args.SourceSnapshotID)
		if err != nil || code != "" {
			return nil, refs, code, err
		}
		request.RepositoryID, request.SourceSnapshotID = repositoryID, args.SourceSnapshotID
		request.Path, request.StartLine, request.EndLine = args.Path, args.StartLine, args.EndLine
		refs.RepositoryID, refs.SourceSnapshotID = repositoryID, args.SourceSnapshotID
	case source.ToolSearch:
		var args sourceSearchArgs
		if strictJSON(raw, &args) != nil || !source.ValidID(args.SourceSnapshotID) ||
			!source.ValidSourceQuery(args.Query) || !source.ValidSourcePath(args.Path) {
			return nil, refs, "invalid_tool_arguments", nil
		}
		repositoryID, code, err := a.resolveSourceSnapshot(ctx, record, args.SourceSnapshotID)
		if err != nil || code != "" {
			return nil, refs, code, err
		}
		request.RepositoryID, request.SourceSnapshotID = repositoryID, args.SourceSnapshotID
		request.Query, request.Path, request.Cursor, request.Limit = args.Query, args.Path, args.Cursor, args.Limit
		refs.RepositoryID, refs.SourceSnapshotID = repositoryID, args.SourceSnapshotID
	case source.ToolDiff:
		var args sourceDiffArgs
		if strictJSON(raw, &args) != nil || !source.ValidID(args.BaseSnapshotID) ||
			!source.ValidID(args.TargetSnapshotID) || !source.ValidSourcePath(args.Path) {
			return nil, refs, "invalid_tool_arguments", nil
		}
		baseRepository, code, err := a.resolveSourceSnapshot(ctx, record, args.BaseSnapshotID)
		if err != nil || code != "" {
			return nil, refs, code, err
		}
		targetRepository, code, err := a.resolveSourceSnapshot(ctx, record, args.TargetSnapshotID)
		if err != nil || code != "" {
			return nil, refs, code, err
		}
		if baseRepository != targetRepository {
			return nil, refs, "source_repository_mismatch", nil
		}
		request.RepositoryID = baseRepository
		request.BaseSnapshotID, request.TargetSnapshotID = args.BaseSnapshotID, args.TargetSnapshotID
		request.Path, request.Cursor, request.Limit = args.Path, args.Cursor, args.Limit
		refs.RepositoryID = baseRepository
		refs.BaseSnapshotID, refs.TargetSnapshotID = args.BaseSnapshotID, args.TargetSnapshotID
	default:
		return nil, refs, "invalid_tool_arguments", nil
	}
	if err := request.Validate(); err != nil {
		return nil, agent.ToolSourceRefs{}, "invalid_tool_arguments", nil
	}
	return &request, refs, "", nil
}

func (a *Activities) resolveSourceSnapshot(ctx context.Context, record agent.AgentRecord, snapshotID string) (string, string, error) {
	if !source.ValidID(snapshotID) {
		return "", "invalid_tool_arguments", nil
	}
	repositoryID, status, err := a.Store.LoadSourceSnapshot(ctx, record.Input.TenantID, snapshotID)
	if errors.Is(err, agent.ErrNotFound) {
		return "", "source_snapshot_not_found", nil
	}
	if err != nil {
		return "", "", err
	}
	if status != "ready" {
		return "", "source_snapshot_not_ready", nil
	}
	binding := agent.ResourceBinding{BindingName: "source_snapshot", Kind: agent.ResourceSourceSnapshot,
		RepositoryID: repositoryID, SourceSnapshotID: snapshotID}
	if !agent.CanAccess(record.Resources, binding) {
		return "", "source_access_denied", nil
	}
	return repositoryID, "", nil
}

func (a *Activities) ExecuteTool(ctx context.Context, plan agent.ToolPlan) (agent.ToolPlan, error) {
	agentRecord, task, err := a.verifyTool(ctx, plan.Input)
	if err != nil {
		return plan, err
	}
	record, err := a.loadPlannedTool(ctx, plan)
	if err != nil || record.ResultRef != "" {
		return planFor(record), err
	}
	tool, ok := a.Tools[plan.Input.Call.Name]
	if !ok || !slices.Contains(task.Tools, plan.Input.Call.Name) || (!tool.Source && tool.Execute == nil) {
		return a.toolFailure(ctx, record, "tool_not_available")
	}
	if tool.Source {
		return plan, permanent("source_tool_requires_dispatch")
	}
	var arguments argumentsDocument
	if err := a.getJSON(ctx, plan.Input.Step.Session.TenantID, plan.Input.Call.ArgumentsRef, &arguments); err != nil {
		return plan, err
	}
	if !json.Valid([]byte(arguments.JSON)) {
		return a.toolFailure(ctx, record, "invalid_tool_arguments")
	}
	data, err := tool.Execute(ctx, ToolRequest{Agent: agentRecord, OperationID: record.OperationID, Arguments: json.RawMessage(arguments.JSON)})
	if err != nil {
		return plan, errors.New("tool_execution_failed")
	}
	if !json.Valid(data) {
		return a.toolFailure(ctx, record, "invalid_tool_output")
	}
	return a.saveTool(ctx, record, toolOutput{OK: true, Data: data})
}

func (a *Activities) CompleteTool(ctx context.Context, input agent.ToolCompletion) (agent.ToolPlan, error) {
	record, err := a.loadPlannedTool(ctx, input.Plan)
	if err != nil || record.ResultRef != "" {
		return planFor(record), err
	}
	if input.Plan.Source != nil {
		return a.completeSourceTool(ctx, input, record)
	}
	child, err := a.Store.FindChild(ctx, input.Plan.Input.Step.Session.TenantID, record.ID)
	if err != nil {
		return input.Plan, runtimeError("load_child_result", err)
	}
	if input.ChildFailed {
		return a.toolFailure(ctx, record, "child_agent_failed")
	}
	if child.Status != "completed" || child.Input.AgentID != input.ChildResult.AgentID || child.ResultRef == "" || child.ResultRef != input.ChildResult.ResultRef {
		return input.Plan, permanent("child_result_mismatch")
	}
	var result agent.StructuredResult
	if err := a.getJSON(ctx, child.Input.TenantID, child.ResultRef, &result); err != nil {
		return input.Plan, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return input.Plan, err
	}
	return a.saveTool(ctx, record, toolOutput{OK: true, Data: data, ChildID: child.Input.AgentID, ResultRef: child.ResultRef})
}

func (a *Activities) completeSourceTool(ctx context.Context, input agent.ToolCompletion, record agent.ToolRecord) (agent.ToolPlan, error) {
	tenantID := record.Input.Step.Session.TenantID
	if input.RemoteResultRef == "" {
		return input.Plan, permanent("missing_source_result")
	}
	if err := a.Store.ValidateStoredObject(ctx, tenantID, input.RemoteResultRef, "source-tool-result"); err != nil {
		return input.Plan, err
	}
	data, err := a.Payloads.Get(ctx, tenantID, input.RemoteResultRef)
	if err != nil {
		return input.Plan, err
	}
	output := toolOutput{OK: true, Data: data, ResultRef: input.RemoteResultRef}
	if input.RemoteFailed {
		output.OK = false
		output.Code = input.RemoteCode
		if output.Code == "" {
			output.Code = "source_tool_failed"
		}
	}
	return a.saveTool(ctx, record, output)
}

func (a *Activities) loadPlannedTool(ctx context.Context, plan agent.ToolPlan) (agent.ToolRecord, error) {
	record, err := a.Store.LoadTool(ctx, plan.Input)
	if err != nil {
		return record, runtimeError("load_tool", err)
	}
	if record.ID != plan.ExecutionID || record.OperationID != plan.OperationID || record.Input.Call != plan.Input.Call {
		return record, permanent("tool_identity_mismatch")
	}
	return record, nil
}

func (a *Activities) toolFailure(ctx context.Context, record agent.ToolRecord, code string) (agent.ToolPlan, error) {
	return a.saveTool(ctx, record, toolOutput{Code: code})
}

func (a *Activities) saveTool(ctx context.Context, record agent.ToolRecord, output toolOutput) (agent.ToolPlan, error) {
	ref, err := a.putJSON(ctx, record.Input.Step.Session.TenantID, "tool-result", output)
	if err != nil {
		return planFor(record), err
	}
	record.ResultRef, record.Status = ref, "failed"
	if output.OK {
		record.Status = "succeeded"
	}
	record, err = a.Store.SaveToolResult(ctx, record)
	if err == nil {
		a.publishEvent(ctx, record.Input.Step.Session.TenantID, record.Input.Step.Session.AgentID,
			stream.EventToolCompleted, record.Input.Step.StepID, 0,
			map[string]string{"tool": record.Input.Call.Name, "status": record.Status})
	}
	return planFor(record), err
}

func planFor(record agent.ToolRecord) agent.ToolPlan {
	return agent.ToolPlan{Input: record.Input, ExecutionID: record.ID, OperationID: record.OperationID, ResultRef: record.ResultRef}
}
