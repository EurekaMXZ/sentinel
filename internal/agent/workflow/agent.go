package workflow

import (
	"time"

	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"sentinel/internal/agent"
	"sentinel/internal/source"
)

// AgentSessionWorkflow is shared by root and child Agents. All non-deterministic
// work happens in Activities; history contains references and cursors only.
func AgentSessionWorkflow(ctx workflow.Context, input agent.WorkflowInput) (result agent.Result, retErr error) {
	if input.Resume != nil && (input.Resume.TenantID != input.Session.TenantID || input.Resume.SessionID != input.Session.SessionID ||
		input.Resume.AgentID != input.Session.AgentID || input.Resume.PendingOperationID != "") {
		return result, temporal.NewNonRetryableApplicationError("invalid_resume_state", "invalid_resume_state", nil)
	}
	ioCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute, ScheduleToCloseTimeout: 10 * time.Minute, WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: 30 * time.Second, MaximumAttempts: 8},
	})
	info := workflow.GetInfo(ctx)
	var progress agent.Progress
	if err := workflow.ExecuteActivity(ioCtx, agent.InitializeActivity, agent.InitializeInput{
		Session: input.Session, WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID}).Get(ctx, &progress); err != nil {
		return result, err
	}
	defer func() {
		if retErr == nil || workflow.IsContinueAsNewError(retErr) {
			return
		}
		cleanup, _ := workflow.NewDisconnectedContext(ctx)
		cleanup = workflow.WithActivityOptions(cleanup, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second,
			ScheduleToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3}})
		status, code := "failed", "agent_execution_failed"
		if temporal.IsCanceledError(retErr) || ctx.Err() != nil {
			status, code = "cancelled", "agent_cancelled"
		}
		if err := workflow.ExecuteActivity(cleanup, agent.FinishActivity, agent.FinishInput{
			Session: input.Session, Status: status, Code: code}).Get(cleanup, nil); err != nil {
			workflow.GetLogger(ctx).Error("failed to persist terminal Agent status", "agent_id", input.Session.AgentID)
		}
	}()
	modelCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    progress.Limits.ModelTimeout + time.Minute,
		ScheduleToCloseTimeout: progress.Limits.ModelTimeout + 10*time.Minute, WaitForCancellation: true,
		HeartbeatTimeout: 30 * time.Second,
		RetryPolicy:      &temporal.RetryPolicy{InitialInterval: 2 * time.Second, BackoffCoefficient: 2, MaximumInterval: 30 * time.Second},
	})
	sourceCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute, ScheduleToCloseTimeout: 30 * time.Minute,
		HeartbeatTimeout: 45 * time.Second, WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{InitialInterval: 2 * time.Second, BackoffCoefficient: 2,
			MaximumInterval: 30 * time.Second, MaximumAttempts: 5},
	})
	if progress.SourceTaskQueue != "" {
		sourceCtx = workflow.WithTaskQueue(sourceCtx, progress.SourceTaskQueue)
	}
	stepsThisRun := 0
	for !progress.Completed {
		if progress.State.NextStepID < 1 || progress.State.NextStepID > progress.Limits.MaxSteps {
			return result, temporal.NewNonRetryableApplicationError("step_budget_exhausted", "step_budget_exhausted", nil)
		}
		if progress.NeedsCompact {
			var compacted agent.Progress
			if err := workflow.ExecuteActivity(modelCtx, agent.CompactActivity, agent.CompactInput{
				Session: input.Session, RowVersion: progress.State.RowVersion}).Get(ctx, &compacted); err != nil {
				return result, err
			}
			if compacted.NeedsCompact {
				return result, temporal.NewNonRetryableApplicationError("context_compact_ineffective", "context_compact_ineffective", nil)
			}
			progress = compacted
			continue
		}
		step := agent.StepInput{Session: input.Session, StepID: progress.State.NextStepID}
		var response agent.ModelResult
		if err := workflow.ExecuteActivity(modelCtx, agent.ModelActivity, step).Get(ctx, &response); err != nil {
			return result, err
		}
		for _, call := range response.Calls {
			var plan agent.ToolPlan
			if err := workflow.ExecuteActivity(ioCtx, agent.PrepareActivity, agent.ToolInput{Step: step, Call: call}).Get(ctx, &plan); err != nil {
				return result, err
			}
			if plan.ResultRef != "" {
				continue
			}
			if plan.Source != nil {
				if progress.SourceTaskQueue == "" {
					return result, temporal.NewNonRetryableApplicationError("source_task_queue_missing", "source_task_queue_missing", nil)
				}
				var sourceResult source.ToolResult
				if err := workflow.ExecuteActivity(sourceCtx, source.ReadActivity, *plan.Source).Get(ctx, &sourceResult); err != nil {
					return result, err
				}
				if err := workflow.ExecuteActivity(ioCtx, agent.CompleteActivity, agent.ToolCompletion{
					Plan: plan, RemoteResultRef: sourceResult.ResultRef, RemoteFailed: sourceResult.Failed,
					RemoteCode: sourceResult.Code}).Get(ctx, &plan); err != nil {
					return result, err
				}
				continue
			}
			if plan.Child != nil {
				childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: plan.Child.WorkflowID,
					WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
					ParentClosePolicy:     enums.PARENT_CLOSE_POLICY_REQUEST_CANCEL, WaitForCancellation: true})
				var childResult agent.Result
				err := workflow.ExecuteChildWorkflow(childCtx, agent.WorkflowName, plan.Child.Input).Get(ctx, &childResult)
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				if err := workflow.ExecuteActivity(ioCtx, agent.CompleteActivity, agent.ToolCompletion{
					Plan: plan, ChildResult: childResult, ChildFailed: err != nil}).Get(ctx, &plan); err != nil {
					return result, err
				}
			} else {
				if err := workflow.ExecuteActivity(ioCtx, agent.ExecuteActivity, plan).Get(ctx, &plan); err != nil {
					return result, err
				}
			}
		}
		if err := workflow.ExecuteActivity(ioCtx, agent.CheckpointActivity, step).Get(ctx, &progress); err != nil {
			return result, err
		}
		stepsThisRun++
		if !progress.Completed && (stepsThisRun >= progress.Limits.ContinueAfterSteps || workflow.GetInfo(ctx).GetContinueAsNewSuggested()) {
			state := progress.State
			next := agent.WorkflowInput{Session: input.Session, Resume: &agent.ContinueState{
				TenantID: state.TenantID, SessionID: state.SessionID, AgentID: state.AgentID, ContextVersion: state.ContextVersion,
				LastEventSequence: state.LastEventSequence, NextStepID: state.NextStepID}}
			return result, workflow.NewContinueAsNewError(ctx, agent.WorkflowName, next)
		}
	}
	return agent.Result{AgentID: input.Session.AgentID, ResultRef: progress.ResultRef}, nil
}
