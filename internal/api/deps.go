package api

import (
	"context"

	"go.temporal.io/sdk/client"

	"sentinel/internal/agent"
	"sentinel/internal/control"
	"sentinel/internal/source"
	"sentinel/internal/stream"
)

// AgentReader is the read side of the Agent control API.
type AgentReader interface {
	LoadAgent(ctx context.Context, tenantID, agentID string) (agent.AgentRecord, error)
	ListAgents(ctx context.Context, tenantID string, limit int) ([]agent.AgentRecord, error)
}

// AgentCreator persists a root Agent task before its Workflow is scheduled.
type AgentCreator interface {
	CreateRoot(ctx context.Context, tenantID string, task agent.Task, resources []agent.ResourceBinding) (agent.AgentRecord, error)
}

type AgentResources interface {
	ValidateResources(ctx context.Context, tenantID string, bindings []agent.ResourceBinding) error
}

// SourceReader is the read side of the source control API.
type SourceReader interface {
	LoadRepository(ctx context.Context, tenantID, repositoryID string) (source.Repository, error)
	CreateRepository(ctx context.Context, repository source.Repository) (source.Repository, error)
	ListRepositories(ctx context.Context, tenantID string, limit int) ([]source.Repository, error)
	ListReadySnapshots(ctx context.Context, tenantID, repositoryID string, offset, limit int) ([]source.Snapshot, bool, error)
	LoadSync(ctx context.Context, request source.SyncRequest) (source.SyncJob, error)
}

type SourceCreator interface {
	Create(ctx context.Context, repository source.Repository) (source.SyncJob, error)
}

// SourceAdmin covers repository management and target lookup for analyses.
type SourceAdmin interface {
	UpdateRepository(ctx context.Context, repository source.Repository) (source.Repository, error)
	LoadSnapshot(ctx context.Context, tenantID, repositoryID, snapshotID string) (source.Snapshot, error)
	ListRefs(ctx context.Context, tenantID, repositoryID string, limit int) ([]source.RefRecord, error)
	ListSyncs(ctx context.Context, tenantID, repositoryID string, limit int) ([]source.SyncRecord, error)
}

type PayloadReader interface {
	Get(ctx context.Context, tenantID, ref string) ([]byte, error)
}

// Workflows is the small Temporal surface used by the control plane.
type Workflows interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error)
	CancelWorkflow(ctx context.Context, workflowID, runID string) error
}

// Dependencies are wired by the app package. The API has no authentication
// yet; the tenant header is an explicit administrative input.
type Dependencies struct {
	Agents          AgentReader
	AgentTasks      AgentCreator
	AgentResources  AgentResources
	Sources         SourceReader
	SourceTasks     SourceCreator
	SourceAdmin     SourceAdmin
	Control         control.Store
	Payloads        PayloadReader
	Workflows       Workflows
	Streams         stream.Reader
	AgentTaskQueue  string
	SourceTaskQueue string
	Model           string
	Limits          agent.Limits
}

func (d Dependencies) enabled() bool {
	return d.Agents != nil && d.AgentTasks != nil && d.AgentResources != nil &&
		d.Sources != nil && d.SourceTasks != nil && d.Payloads != nil && d.Workflows != nil
}
