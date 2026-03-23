package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"sentinel/internal/agent"
	agentworkflow "sentinel/internal/agent/workflow"
	"sentinel/internal/source"
)

type workflowHarness struct {
	mu              sync.Mutex
	limits          agent.Limits
	startStep       int64
	toolNames       []string
	modelSteps      []string
	tools           []string
	children        []string
	finished        []agent.FinishInput
	modelError      error
	blockModel      bool
	blockChild      bool
	childFails      bool
	childFailed     bool
	sequence        []string
	compactAtStep   int64
	compactNextStep int64
	compactErr      error
	compactCalls    int
	sourceCalls     int
	sourceResults   []string
	remoteResults   []string
	remoteFailed    []bool
}

func newEnvironment(h *workflowHarness) *testsuite.TestWorkflowEnvironment {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(agentworkflow.AgentSessionWorkflow, sdkworkflow.RegisterOptions{Name: agent.WorkflowName})
	register := func(name string, fn any) {
		env.RegisterActivityWithOptions(fn, sdkactivity.RegisterOptions{Name: name})
	}
	register(agent.InitializeActivity, func(ctx context.Context, input agent.InitializeInput) (agent.Progress, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		step := h.startStep
		if step == 0 || input.Session.AgentID == "child" {
			step = 1
		}
		return agent.Progress{Limits: h.limits, SourceTaskQueue: "source-queue",
			State: agent.RuntimeState{TenantID: input.Session.TenantID,
				SessionID: input.Session.SessionID, AgentID: input.Session.AgentID, NextStepID: step}}, nil
	})
	register(agent.ModelActivity, func(ctx context.Context, input agent.StepInput) (agent.ModelResult, error) {
		h.mu.Lock()
		h.modelSteps = append(h.modelSteps, fmt.Sprintf("%s:%d", input.Session.AgentID, input.StepID))
		h.sequence = append(h.sequence, fmt.Sprintf("model:%s:%d", input.Session.AgentID, input.StepID))
		failure, block := h.modelError, h.blockModel
		if input.Session.AgentID == "child" && h.childFails {
			failure = temporal.NewNonRetryableApplicationError("child_failed", "child_failed", nil)
		}
		if input.Session.AgentID == "child" && h.blockChild {
			block = true
		}
		h.mu.Unlock()
		if block {
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return agent.ModelResult{}, ctx.Err()
				case <-ticker.C:
					sdkactivity.RecordHeartbeat(ctx)
				}
			}
		}
		if failure != nil {
			return agent.ModelResult{}, failure
		}
		result := agent.ModelResult{ResponseRef: "response-ref"}
		if input.Session.AgentID == "root" && input.StepID == 1 {
			for i, name := range h.toolNames {
				result.Calls = append(result.Calls, agent.ToolCall{CallID: fmt.Sprintf("call-%d", i), Name: name, ArgumentsRef: "arguments-ref"})
			}
		}
		return result, nil
	})
	register(agent.PrepareActivity, func(ctx context.Context, input agent.ToolInput) (agent.ToolPlan, error) {
		plan := agent.ToolPlan{Input: input, ExecutionID: input.Call.CallID, OperationID: "operation"}
		if input.Call.Name == agent.ChildToolName {
			child := agent.SessionInput{TenantID: input.Step.Session.TenantID, SessionID: input.Step.Session.SessionID, AgentID: "child", TaskRef: "child-task-ref"}
			plan.Child = &agent.ChildLaunch{Input: agent.WorkflowInput{Session: child}, WorkflowID: "child-workflow"}
		}
		if source.IsTool(input.Call.Name) {
			plan.Source = &source.ToolRequest{Name: input.Call.Name, TenantID: input.Step.Session.TenantID,
				AgentID: input.Step.Session.AgentID, OperationID: "source-operation", ToolCallID: input.Call.CallID}
		}
		return plan, nil
	})
	register(source.ReadActivity, func(ctx context.Context, request source.ToolRequest) (source.ToolResult, error) {
		h.mu.Lock()
		h.sourceCalls++
		h.sourceResults = append(h.sourceResults, request.Name)
		h.mu.Unlock()
		return source.ToolResult{ResultRef: "source-result-ref"}, nil
	})
	register(agent.ExecuteActivity, func(ctx context.Context, plan agent.ToolPlan) (agent.ToolPlan, error) {
		h.mu.Lock()
		h.tools = append(h.tools, plan.Input.Call.CallID)
		h.mu.Unlock()
		plan.ResultRef = "tool-result-ref"
		return plan, nil
	})
	register(agent.CompleteActivity, func(ctx context.Context, input agent.ToolCompletion) (agent.ToolPlan, error) {
		h.mu.Lock()
		h.children = append(h.children, input.ChildResult.AgentID)
		h.childFailed = input.ChildFailed
		h.remoteResults = append(h.remoteResults, input.RemoteResultRef)
		h.remoteFailed = append(h.remoteFailed, input.RemoteFailed)
		h.mu.Unlock()
		input.Plan.ResultRef = "child-result-ref"
		return input.Plan, nil
	})
	register(agent.CheckpointActivity, func(ctx context.Context, input agent.StepInput) (agent.Progress, error) {
		completed := input.Session.AgentID == "child" || input.StepID > 1 || len(h.toolNames) == 0
		h.mu.Lock()
		needsCompact := h.compactAtStep != 0 && input.StepID == h.compactAtStep
		h.mu.Unlock()
		return agent.Progress{Limits: h.limits, Completed: completed, ResultRef: input.Session.AgentID + "-result",
			NeedsCompact: needsCompact,
			State: agent.RuntimeState{TenantID: input.Session.TenantID, SessionID: input.Session.SessionID,
				AgentID: input.Session.AgentID, NextStepID: input.StepID + 1, ContextVersion: input.StepID, LastEventSequence: input.StepID * 5}}, nil
	})
	register(agent.CompactActivity, func(ctx context.Context, input agent.CompactInput) (agent.Progress, error) {
		h.mu.Lock()
		h.compactCalls++
		h.sequence = append(h.sequence, "compact")
		failure, next := h.compactErr, h.compactNextStep
		h.mu.Unlock()
		if failure != nil {
			return agent.Progress{}, failure
		}
		if next == 0 {
			next = 1
		}
		return agent.Progress{Limits: h.limits, State: agent.RuntimeState{TenantID: input.Session.TenantID,
			SessionID: input.Session.SessionID, AgentID: input.Session.AgentID, NextStepID: next}}, nil
	})
	register(agent.FinishActivity, func(ctx context.Context, input agent.FinishInput) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.finished = append(h.finished, input)
		return nil
	})
	return env
}

func workflowInput() agent.WorkflowInput {
	return agent.WorkflowInput{Session: agent.SessionInput{TenantID: "tenant", SessionID: "session", AgentID: "root", TaskRef: "task-ref"}}
}

func TestAgentLoopAndIndependentChild(t *testing.T) {
	for _, test := range []struct {
		name       string
		tools      []string
		wantModels []string
		wantTools  []string
		wantChild  bool
		childFails bool
	}{
		{"answer", nil, []string{"root:1"}, nil, false, false},
		{"multiple tools", []string{"first", "second"}, []string{"root:1", "root:2"}, []string{"call-0", "call-1"}, false, false},
		{"child", []string{agent.ChildToolName}, []string{"root:1", "child:1", "root:2"}, nil, true, false},
		{"child failure becomes tool result", []string{agent.ChildToolName}, []string{"root:1", "child:1", "root:2"}, nil, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &workflowHarness{limits: agent.DefaultLimits(), toolNames: test.tools, childFails: test.childFails}
			env := newEnvironment(h)
			env.ExecuteWorkflow(agent.WorkflowName, workflowInput())
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			var result agent.Result
			if err := env.GetWorkflowResult(&result); err != nil || result.AgentID != "root" || result.ResultRef != "root-result" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !reflect.DeepEqual(h.modelSteps, test.wantModels) || !reflect.DeepEqual(h.tools, test.wantTools) {
				t.Fatalf("models=%v tools=%v", h.modelSteps, h.tools)
			}
			if test.wantChild && (len(h.children) != 1 || h.childFailed != test.childFails) {
				t.Fatalf("child completions=%v, failed=%t", h.children, h.childFailed)
			}
			for _, finish := range h.finished {
				if finish.Session.AgentID == "root" {
					t.Fatalf("successful parent was finalized as failed: %+v", finish)
				}
			}
		})
	}
}

func TestSourceToolDispatchesToSourceWorker(t *testing.T) {
	h := &workflowHarness{limits: agent.DefaultLimits(), toolNames: []string{source.ToolReadFile}}
	env := newEnvironment(h)
	env.ExecuteWorkflow(agent.WorkflowName, workflowInput())
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if h.sourceCalls != 1 || !reflect.DeepEqual(h.sourceResults, []string{source.ToolReadFile}) {
		t.Fatalf("source calls=%d names=%v", h.sourceCalls, h.sourceResults)
	}
	if !reflect.DeepEqual(h.remoteResults, []string{"source-result-ref"}) ||
		!reflect.DeepEqual(h.remoteFailed, []bool{false}) {
		t.Fatalf("remote results=%v failed=%v", h.remoteResults, h.remoteFailed)
	}
	if !reflect.DeepEqual(h.modelSteps, []string{"root:1", "root:2"}) {
		t.Fatalf("model steps=%v", h.modelSteps)
	}
}

func TestCompactRunsBeforeNextModelStep(t *testing.T) {
	h := &workflowHarness{limits: agent.DefaultLimits(), toolNames: []string{"first"},
		compactAtStep: 1, compactNextStep: 2}
	env := newEnvironment(h)
	env.ExecuteWorkflow(agent.WorkflowName, workflowInput())
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	want := []string{"model:root:1", "compact", "model:root:2"}
	if h.compactCalls != 1 || !reflect.DeepEqual(h.sequence, want) {
		t.Fatalf("compact calls=%d sequence=%v want=%v", h.compactCalls, h.sequence, want)
	}
}

func TestCompactFailureStopsTheAgent(t *testing.T) {
	h := &workflowHarness{limits: agent.DefaultLimits(), toolNames: []string{"first"},
		compactAtStep: 1, compactNextStep: 2,
		compactErr: temporal.NewNonRetryableApplicationError("compact failed", "compact_failed", nil)}
	env := newEnvironment(h)
	env.ExecuteWorkflow(agent.WorkflowName, workflowInput())
	if env.GetWorkflowError() == nil || h.compactCalls != 1 {
		t.Fatalf("err=%v compact calls=%d", env.GetWorkflowError(), h.compactCalls)
	}
	if !reflect.DeepEqual(h.modelSteps, []string{"root:1"}) {
		t.Fatalf("model ran after compaction failed: %v", h.modelSteps)
	}
	if len(h.finished) != 1 || h.finished[0].Status != "failed" {
		t.Fatalf("terminal status not persisted: %+v", h.finished)
	}
}

func TestContinueAsNewKeepsDurableCursor(t *testing.T) {
	h := &workflowHarness{limits: agent.DefaultLimits(), toolNames: []string{"first"}}
	h.limits.ContinueAfterSteps = 1
	env := newEnvironment(h)
	env.ExecuteWorkflow(agent.WorkflowName, workflowInput())
	var continued *sdkworkflow.ContinueAsNewError
	if !errors.As(env.GetWorkflowError(), &continued) {
		t.Fatalf("expected Continue-As-New: %v", env.GetWorkflowError())
	}
	var next agent.WorkflowInput
	if err := converter.GetDefaultDataConverter().FromPayloads(continued.Input, &next); err != nil {
		t.Fatal(err)
	}
	if next.Resume == nil || next.Resume.NextStepID != 2 || next.Resume.ContextVersion != 1 || len(h.finished) != 0 {
		t.Fatalf("resume=%+v finish=%v", next.Resume, h.finished)
	}
	h2 := &workflowHarness{limits: h.limits, startStep: next.Resume.NextStepID, toolNames: h.toolNames}
	env2 := newEnvironment(h2)
	env2.ExecuteWorkflow(agent.WorkflowName, next)
	if err := env2.GetWorkflowError(); err != nil || !reflect.DeepEqual(h2.modelSteps, []string{"root:2"}) {
		t.Fatalf("restarted models=%v err=%v", h2.modelSteps, err)
	}
}

func TestAgentFailureAndStepBudgetPersistStatus(t *testing.T) {
	for _, test := range []struct {
		name  string
		start int64
		err   error
	}{
		{"upstream outcome", 1, temporal.NewNonRetryableApplicationError("uncertain", "uncertain", nil)},
		{"budget", 65, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &workflowHarness{limits: agent.DefaultLimits(), startStep: test.start, modelError: test.err}
			env := newEnvironment(h)
			env.ExecuteWorkflow(agent.WorkflowName, workflowInput())
			if env.GetWorkflowError() == nil || len(h.finished) != 1 || h.finished[0].Status != "failed" {
				t.Fatalf("err=%v finished=%v", env.GetWorkflowError(), h.finished)
			}
		})
	}
}

func TestAgentCancellationPersistsCancelled(t *testing.T) {
	h := &workflowHarness{limits: agent.DefaultLimits(), blockModel: true}
	env := newEnvironment(h)
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, time.Second)
	env.ExecuteWorkflow(agent.WorkflowName, workflowInput())
	if env.GetWorkflowError() == nil || len(h.finished) != 1 || h.finished[0].Status != "cancelled" {
		t.Fatalf("err=%v finished=%v", env.GetWorkflowError(), h.finished)
	}
}

func TestParentCancellationReachesChild(t *testing.T) {
	h := &workflowHarness{limits: agent.DefaultLimits(), toolNames: []string{agent.ChildToolName}, blockChild: true}
	env := newEnvironment(h)
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, time.Second)
	env.ExecuteWorkflow(agent.WorkflowName, workflowInput())
	if env.GetWorkflowError() == nil {
		t.Fatal("cancelled parent completed successfully")
	}
	finished := make(map[string]string)
	for _, input := range h.finished {
		finished[input.Session.AgentID] = input.Status
	}
	if finished["root"] != "cancelled" || finished["child"] != "cancelled" {
		t.Fatalf("cancellation was not persisted for both Agents: %v", finished)
	}
	if len(h.children) != 0 {
		t.Fatalf("parent continued consuming child results after cancellation: %v", h.children)
	}
}
