package workflow

import (
	"time"

	"go.temporal.io/sdk/temporal"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"sentinel/internal/source"
)

func SourceSyncWorkflow(ctx sdkworkflow.Context, input source.WorkflowInput) (source.SyncResult, error) {
	ctx = sdkworkflow.WithActivityOptions(ctx, sdkworkflow.ActivityOptions{
		StartToCloseTimeout: 20 * time.Minute, ScheduleToCloseTimeout: 24 * time.Hour,
		HeartbeatTimeout: 45 * time.Second, WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{InitialInterval: 5 * time.Second, MaximumInterval: time.Minute},
	})
	var result source.SyncResult
	err := sdkworkflow.ExecuteActivity(ctx, source.SyncActivity, input.Request).Get(ctx, &result)
	if err == nil {
		return result, nil
	}
	status := "failed"
	if temporal.IsCanceledError(err) || ctx.Err() != nil {
		status = "cancelled"
	}
	finishCtx, cancel := sdkworkflow.NewDisconnectedContext(ctx)
	defer cancel()
	finishCtx = sdkworkflow.WithActivityOptions(finishCtx, sdkworkflow.ActivityOptions{
		StartToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 5},
	})
	if finishErr := sdkworkflow.ExecuteActivity(finishCtx, source.FinishActivity, source.FinishInput{Request: input.Request, Status: status}).Get(finishCtx, nil); finishErr != nil {
		return result, finishErr
	}
	return result, err
}
