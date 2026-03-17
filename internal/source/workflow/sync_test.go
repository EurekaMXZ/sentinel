package workflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"sentinel/internal/source"
	sourceworkflow "sentinel/internal/source/workflow"
)

func TestSourceWorkflowRetryAndResultReferences(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	request := source.SyncRequest{TenantID: "tenant", RepositoryID: "repo", SyncID: "sync", OperationID: "operation"}
	calls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, got source.SyncRequest) (source.SyncResult, error) {
		if got != request {
			return source.SyncResult{}, errors.New("unstable request identity")
		}
		calls++
		if calls == 1 {
			return source.SyncResult{}, source.ErrBusy
		}
		return source.SyncResult{RepositoryID: got.RepositoryID, SyncID: got.SyncID, ManifestID: "manifest", ResultRef: "result", SnapshotCount: 2}, nil
	}, sdkactivity.RegisterOptions{Name: source.SyncActivity})
	env.ExecuteWorkflow(sourceworkflow.SourceSyncWorkflow, source.WorkflowInput{Request: request})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var result source.SyncResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || result.ResultRef != "result" || result.SnapshotCount != 2 {
		t.Fatalf("calls=%d result=%+v", calls, result)
	}
}

func TestSourceWorkflowFinishesFailureAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancellation"}[cancelled], func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.RegisterActivityWithOptions(func(ctx context.Context, _ source.SyncRequest) (source.SyncResult, error) {
				if !cancelled {
					return source.SyncResult{}, temporal.NewNonRetryableApplicationError("unavailable repository", "source_terminal", nil)
				}
				ticker := time.NewTicker(50 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return source.SyncResult{}, ctx.Err()
					case <-ticker.C:
						sdkactivity.RecordHeartbeat(ctx)
					}
				}
			}, sdkactivity.RegisterOptions{Name: source.SyncActivity})
			finished := ""
			env.RegisterActivityWithOptions(func(_ context.Context, input source.FinishInput) error {
				finished = input.Status
				return nil
			}, sdkactivity.RegisterOptions{Name: source.FinishActivity})
			if cancelled {
				env.RegisterDelayedCallback(env.CancelWorkflow, time.Second)
			}
			env.ExecuteWorkflow(sourceworkflow.SourceSyncWorkflow, source.WorkflowInput{})
			if err := env.GetWorkflowError(); err == nil {
				t.Fatal("expected unsuccessful workflow")
			}
			want := "failed"
			if cancelled {
				want = "cancelled"
			}
			if finished != want {
				t.Fatalf("finish=%q want %q", finished, want)
			}
		})
	}
}
