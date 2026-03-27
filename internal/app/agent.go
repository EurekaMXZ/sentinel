package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	enums "go.temporal.io/api/enums/v1"
	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"sentinel/internal/agent"
	"sentinel/internal/agent/activity"
	agentworkflow "sentinel/internal/agent/workflow"
	modelapi "sentinel/internal/llm/openai"
	"sentinel/internal/platform/config"
	"sentinel/internal/platform/temporal"
	"sentinel/internal/storage/postgres"
	"sentinel/internal/storage/s3"
)

func openAgentRuntime(ctx context.Context, cfg config.Config) (*activity.Activities, func(), error) {
	model, err := modelapi.NewModel(cfg.OpenAI)
	if err != nil {
		return nil, nil, err
	}
	limits := agent.DefaultLimits()
	limits.ModelTimeout = cfg.OpenAI.RequestTimeout
	if err := limits.Validate(); err != nil {
		return nil, nil, err
	}
	objectClient, err := s3.New(cfg.S3)
	if err != nil {
		return nil, nil, err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	exists, err := objectClient.BucketExists(checkCtx, cfg.S3.Bucket)
	if err != nil {
		return nil, nil, fmt.Errorf("check S3 bucket: %w", err)
	}
	if !exists {
		return nil, nil, errors.New("S3 bucket is missing; run minio-init first")
	}
	pool, err := postgres.Open(ctx, cfg.Postgres)
	if err != nil {
		return nil, nil, err
	}
	return &activity.Activities{Store: postgres.NewAgentStore(pool), Model: model, Tools: activity.SourceToolSet(),
		Payloads:                s3.NewPayloadStore(objectClient, cfg.S3.Bucket, postgres.NewPayloadIndex(pool)),
		CompactStructuredOutput: cfg.OpenAI.CompactStructuredOutput}, pool.Close, nil
}

func registerAgent(registry worker.Registry, activities *activity.Activities) {
	registry.RegisterWorkflowWithOptions(agentworkflow.AgentSessionWorkflow, sdkworkflow.RegisterOptions{Name: agent.WorkflowName})
	for _, registration := range []struct {
		name string
		fn   any
	}{
		{agent.InitializeActivity, activities.Initialize}, {agent.ModelActivity, activities.ModelStep},
		{agent.PrepareActivity, activities.PrepareTool}, {agent.ExecuteActivity, activities.ExecuteTool},
		{agent.CompleteActivity, activities.CompleteTool}, {agent.CheckpointActivity, activities.Checkpoint},
		{agent.CompactActivity, activities.Compact}, {agent.FinishActivity, activities.Finish},
	} {
		registry.RegisterActivityWithOptions(registration.fn, sdkactivity.RegisterOptions{Name: registration.name})
	}
}

type StartAgentOptions struct {
	TenantID   string
	Prompt     string
	ContextRef string
	Resources  []agent.ResourceBinding
	ResumeID   string
	Wait       bool
	Limits     agent.Limits
	Output     io.Writer
}

// StartAgent is an administrative CLI path until the business API is added.
// Tenant identity is explicit here; an HTTP adapter must obtain it from auth.
func StartAgent(ctx context.Context, cfg config.Config, logger *slog.Logger, options StartAgentOptions) error {
	tenantID, err := uuid.Parse(options.TenantID)
	if err != nil || tenantID == uuid.Nil {
		return errors.New("tenant must be a nonzero UUID")
	}
	if options.Output == nil {
		return errors.New("Agent output writer is required")
	}
	if options.ResumeID != "" && (options.Prompt != "" || options.ContextRef != "" || len(options.Resources) != 0) {
		return errors.New("resume accepts an existing Agent ID without new task inputs")
	}
	activities, closeRuntime, err := openAgentRuntime(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeRuntime()
	var record agent.AgentRecord
	if options.ResumeID != "" {
		if id, parseErr := uuid.Parse(options.ResumeID); parseErr != nil || id == uuid.Nil {
			return errors.New("resume must identify an existing Agent UUID")
		}
		record, err = activities.Store.LoadAgent(ctx, options.TenantID, options.ResumeID)
	} else {
		options.Limits.ModelTimeout = cfg.OpenAI.RequestTimeout
		options.Limits.ContextWindowTokens = cfg.OpenAI.ContextWindowTokens
		options.Limits.ContextSafetyTokens = cfg.OpenAI.ContextSafetyTokens
		task := agent.NewRootTask(options.Prompt, options.ContextRef, cfg.OpenAI.Model,
			options.Limits, agent.DefaultTools(), cfg.Temporal.SourceTaskQueue)
		record, err = activities.CreateRoot(ctx, options.TenantID, task, options.Resources)
	}
	if err != nil {
		return err
	}
	if record.ParentAgentID != "" {
		return errors.New("child Agents are started by their parent Workflow")
	}
	if record.Status == "failed" || record.Status == "cancelled" {
		return errors.New("a terminal Agent cannot be resumed automatically")
	}
	// Print the durable identity before scheduling: it remains usable if the
	// start request loses its response, or Temporal is temporarily unavailable.
	encoder := json.NewEncoder(options.Output)
	if err := encoder.Encode(map[string]string{"tenant_id": record.Input.TenantID, "session_id": record.Input.SessionID,
		"agent_id": record.Input.AgentID, "workflow_id": record.WorkflowID}); err != nil {
		return err
	}
	result := agent.Result{AgentID: record.Input.AgentID, ResultRef: record.ResultRef}
	if record.Status != "completed" {
		c, err := temporal.Dial(ctx, cfg.Temporal, logger)
		if err != nil {
			return err
		}
		defer c.Close()
		run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: record.WorkflowID,
			TaskQueue: cfg.Temporal.AgentTaskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING}, agent.WorkflowName, agent.WorkflowInput{Session: record.Input})
		if err != nil {
			return fmt.Errorf("start Agent Workflow (resume with the saved Agent ID): %w", err)
		}
		if !options.Wait {
			return nil
		}
		if err := run.Get(ctx, &result); err != nil {
			return err
		}
	}
	if !options.Wait {
		return nil
	}
	var body agent.StructuredResult
	data, err := activities.Payloads.Get(ctx, options.TenantID, result.ResultRef)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return err
	}
	if strings.TrimSpace(result.ResultRef) == "" || result.AgentID != record.Input.AgentID || body.AgentID != result.AgentID {
		return errors.New("invalid Agent result")
	}
	return encoder.Encode(struct {
		agent.Result
		Summary string `json:"summary"`
	}{Result: result, Summary: body.Summary})
}
