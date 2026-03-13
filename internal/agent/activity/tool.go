package activity

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"sentinel/internal/agent"
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
	_, task, err := a.verifyTool(ctx, input)
	if err != nil {
		return agent.ToolPlan{}, err
	}
	record, err := a.Store.PrepareTool(ctx, agent.ToolRecord{Input: input, ID: newID(),
		OperationID: agent.ToolOperation(input.Step.Session.AgentID, input.Step.StepID, input.Call.CallID)})
	if err != nil {
		return agent.ToolPlan{}, runtimeError("prepare_tool", err)
	}
	plan := planFor(record)
	if record.ResultRef != "" {
		return plan, nil
	}
	if !slices.Contains(task.Tools, input.Call.Name) {
		return a.toolFailure(ctx, record, "tool_not_allowed")
	}
	var arguments argumentsDocument
	if err := a.getJSON(ctx, input.Step.Session.TenantID, input.Call.ArgumentsRef, &arguments); err != nil {
		return plan, err
	}
	if !json.Valid([]byte(arguments.JSON)) {
		return a.toolFailure(ctx, record, "invalid_tool_arguments")
	}
	if input.Call.Name != agent.ChildToolName {
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
			ExpectedOutput: args.ExpectedOutput, Model: task.Model, Tools: task.Tools, Limits: task.Limits,
			Depth: task.Depth + 1, DelegationScope: slices.Clone(args.Resources)}
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
	if !ok || tool.Execute == nil || !slices.Contains(task.Tools, plan.Input.Call.Name) {
		return a.toolFailure(ctx, record, "tool_not_available")
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
	var result StructuredResult
	if err := a.getJSON(ctx, child.Input.TenantID, child.ResultRef, &result); err != nil {
		return input.Plan, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return input.Plan, err
	}
	return a.saveTool(ctx, record, toolOutput{OK: true, Data: data, ChildID: child.Input.AgentID, ResultRef: child.ResultRef})
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
	return planFor(record), err
}

func planFor(record agent.ToolRecord) agent.ToolPlan {
	return agent.ToolPlan{Input: record.Input, ExecutionID: record.ID, OperationID: record.OperationID, ResultRef: record.ResultRef}
}
