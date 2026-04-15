package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sentinel/internal/agentctx"
	"sentinel/internal/eventlog"
	"sentinel/internal/source"
)

const (
	WorkflowName       = "sentinel.AgentSessionWorkflow"
	InitializeActivity = "sentinel.agent.initialize"
	ModelActivity      = "sentinel.agent.model"
	PrepareActivity    = "sentinel.agent.prepare_tool"
	ExecuteActivity    = "sentinel.agent.execute_tool"
	CompleteActivity   = "sentinel.agent.complete_tool"
	CheckpointActivity = "sentinel.agent.checkpoint"
	CompactActivity    = "sentinel.agent.compact"
	FinishActivity     = "sentinel.agent.finish"
	ChildToolName      = "call_child_agent"
)

var (
	ErrNotFound  = errors.New("runtime record not found")
	ErrConflict  = errors.New("runtime state changed")
	ErrBusy      = errors.New("model operation is still running")
	ErrUncertain = errors.New("model operation needs reconciliation before it can be retried")
	ErrTerminal  = errors.New("runtime record is terminal")
)

// Limits are saved with the task, so recovery does not change a running Agent's policy.
type Limits struct {
	MaxSteps            int64         `json:"max_steps"`
	ContinueAfterSteps  int           `json:"continue_after_steps"`
	MaxChildDepth       int           `json:"max_child_depth"`
	MaxToolCalls        int           `json:"max_tool_calls"`
	MaxContextBytes     int           `json:"max_context_bytes"`
	MaxOutputTokens     int64         `json:"max_output_tokens"`
	ContextWindowTokens int64         `json:"context_window_tokens"`
	ContextSafetyTokens int64         `json:"context_safety_tokens"`
	ModelTimeout        time.Duration `json:"model_timeout"`
}

func DefaultLimits() Limits {
	return Limits{MaxSteps: 64, ContinueAfterSteps: 16, MaxChildDepth: 3,
		MaxToolCalls: 16, MaxContextBytes: 4 << 20, MaxOutputTokens: 8192,
		ContextWindowTokens: 128000, ContextSafetyTokens: 8192,
		ModelTimeout: 2 * time.Minute}
}

// WithDefaults fills compact budget fields missing from tasks persisted before
// automatic compaction existed. Existing policy fields are never rewritten.
func (l Limits) WithDefaults() Limits {
	defaults := DefaultLimits()
	if l.ContextWindowTokens == 0 {
		l.ContextWindowTokens = defaults.ContextWindowTokens
	}
	if l.ContextSafetyTokens == 0 {
		l.ContextSafetyTokens = defaults.ContextSafetyTokens
	}
	return l
}

func (l Limits) Validate() error {
	if l.MaxSteps < 1 || l.MaxSteps > 10000 || l.ContinueAfterSteps < 1 || l.ContinueAfterSteps > 1000 ||
		l.MaxChildDepth < 0 || l.MaxChildDepth > 8 || l.MaxToolCalls < 1 || l.MaxToolCalls > 32 ||
		l.MaxContextBytes < 1024 || l.MaxContextBytes > 8<<20 || l.MaxOutputTokens < 1 ||
		l.ContextWindowTokens < 4096 || l.ContextWindowTokens > 10_000_000 ||
		l.ContextSafetyTokens < 1 || l.ContextSafetyTokens >= l.ContextWindowTokens ||
		l.ModelTimeout <= 0 || l.ModelTimeout > 30*time.Minute {
		return errors.New("invalid Agent execution limits")
	}
	if l.MaxOutputTokens+l.ContextSafetyTokens >= l.ContextWindowTokens {
		return errors.New("context window must exceed output and safety reserves")
	}
	return nil
}

// Task is a server-prepared immutable payload. DelegationScope is a separate
// upper bound on child inputs; resource bindings themselves are not ACL grants.
type Task struct {
	Prompt            string            `json:"task"`
	Instructions      string            `json:"instructions"`
	InitialContextRef string            `json:"initial_context_ref,omitempty"`
	ExpectedOutput    string            `json:"expected_output,omitempty"`
	Model             string            `json:"model"`
	Tools             []string          `json:"tools"`
	SourceTaskQueue   string            `json:"source_task_queue,omitempty"`
	Limits            Limits            `json:"limits"`
	Depth             int               `json:"depth"`
	DelegationScope   []ResourceBinding `json:"delegation_scope"`
}

const DefaultInstructions = `You are Sentinel, a task-oriented read-only software-analysis Agent.
Use only the tools you are given; resource bindings identify inputs, not their contents.
Read the relevant source before making claims. For every source-based conclusion, cite the repository path and line numbers you actually read.
Answer in Chinese unless the task requests another language.
Return a well-structured Markdown answer: start with a one-paragraph summary, then use "##" sections, bullet lists, and tables where useful.
Separate every block with a blank line. Put code, commands, and excerpts in fenced code blocks with a language tag.
Distinguish verified facts from inferences and unknown items. Never claim to have modified files. Delegate bounded tasks only when useful.`


// DefaultTools is the server-assigned tool policy for root Agents.
func DefaultTools() []string {
	return append([]string{ChildToolName}, source.ToolNames()...)
}

// NewRootTask builds the immutable task payload used by the CLI and HTTP API.
func NewRootTask(prompt, contextRef, model string, limits Limits, tools []string, sourceTaskQueue string) Task {
	return Task{Prompt: prompt, Instructions: DefaultInstructions, InitialContextRef: contextRef,
		Model: model, Tools: append([]string(nil), tools...), SourceTaskQueue: sourceTaskQueue, Limits: limits}
}

type WorkflowInput struct {
	Session SessionInput   `json:"session"`
	Resume  *ContinueState `json:"resume,omitempty"`
}

type InitializeInput struct {
	Session    SessionInput `json:"session"`
	WorkflowID string       `json:"workflow_id"`
	RunID      string       `json:"run_id"`
}

type Progress struct {
	State           RuntimeState `json:"state"`
	Limits          Limits       `json:"limits"`
	SourceTaskQueue string       `json:"source_task_queue,omitempty"`
	Completed       bool         `json:"completed"`
	ResultRef       string       `json:"result_ref,omitempty"`
	NeedsCompact    bool         `json:"needs_compact,omitempty"`
	ContextTokens   int64        `json:"context_tokens,omitempty"`
}

type StepInput struct {
	Session SessionInput `json:"session"`
	StepID  int64        `json:"step_id"`
}

type ToolCall struct {
	CallID       string `json:"call_id"`
	Name         string `json:"name"`
	ArgumentsRef string `json:"arguments_ref"`
}

type ModelResult struct {
	ResponseRef string     `json:"response_ref"`
	Calls       []ToolCall `json:"calls"`
}

type ToolInput struct {
	Step StepInput `json:"step"`
	Call ToolCall  `json:"call"`
}

type ChildLaunch struct {
	Input      WorkflowInput `json:"input"`
	WorkflowID string        `json:"workflow_id"`
}

type ToolPlan struct {
	Input       ToolInput    `json:"input"`
	ExecutionID string       `json:"execution_id"`
	OperationID string       `json:"operation_id"`
	ResultRef   string       `json:"result_ref,omitempty"`
	Child       *ChildLaunch `json:"child,omitempty"`
	// Source is set for source read tools. The Workflow dispatches it to the
	// Source Worker's task queue instead of executing it in the Agent Worker.
	Source *source.ToolRequest `json:"source,omitempty"`
}

type ToolCompletion struct {
	Plan            ToolPlan `json:"plan"`
	ChildResult     Result   `json:"child_result"`
	ChildFailed     bool     `json:"child_failed"`
	RemoteResultRef string   `json:"remote_result_ref,omitempty"`
	RemoteFailed    bool     `json:"remote_failed,omitempty"`
	RemoteCode      string   `json:"remote_code,omitempty"`
}

type FinishInput struct {
	Session SessionInput `json:"session"`
	Status  string       `json:"status"`
	Code    string       `json:"code"`
}

// Records contain only metadata and object IDs, never prompts or model bodies.
type AgentRecord struct {
	Input                 SessionInput
	WorkflowID            string
	ParentAgentID         string
	ParentToolExecutionID string
	Status                string
	ResultRef             string
	ErrorRef              string
	CreatedAt             time.Time
	StartedAt             *time.Time
	FinishedAt            *time.Time
	Head                  agentctx.Head
	Resources             []ResourceBinding
}

type StepRecord struct {
	Input             StepInput
	ID                string
	OperationID       string
	ContextVersion    int64
	Model             string
	RequestRef        string
	ResponseRef       string
	RequestHash       []byte
	Status            string
	LeaseOwner        string
	FencingToken      int64
	ProviderRequestID string
	InputTokens       int64
	OutputTokens      int64
}

type ToolSourceRefs struct {
	RepositoryID     string `json:"repository_id,omitempty"`
	SourceSnapshotID string `json:"source_snapshot_id,omitempty"`
	BaseSnapshotID   string `json:"base_snapshot_id,omitempty"`
	TargetSnapshotID string `json:"target_snapshot_id,omitempty"`
}

type ToolRecord struct {
	Input       ToolInput
	ID          string
	OperationID string
	ResultRef   string
	Status      string
	Source      ToolSourceRefs
}

// ContextSourceRef records evidence retained by a published Context.
type ContextSourceRef struct {
	RepositoryID     string
	SourceSnapshotID string
	Role             string
}

type Checkpoint struct {
	Step            StepInput
	ToolCount       int
	ExpectedHead    agentctx.Head
	ContextRecordID string
	ContextRef      string
	SegmentRef      string
	Events          []eventlog.Event
	SourceRefs      []ContextSourceRef
	ResultRef       string
}

// RuntimeStore implements the transactional part of Activities. ClaimStep must
// fence concurrent attempts and refuse an expired, unsaved upstream request.
type RuntimeStore interface {
	CreateAgent(context.Context, AgentRecord) (AgentRecord, error)
	LoadAgent(context.Context, string, string) (AgentRecord, error)
	StartAgent(context.Context, InitializeInput) (AgentRecord, error)
	FindChild(context.Context, string, string) (AgentRecord, error)
	ValidateResources(context.Context, string, []ResourceBinding) error
	LoadStep(context.Context, StepInput) (StepRecord, error)
	PrepareStep(context.Context, StepRecord) (StepRecord, error)
	ClaimStep(context.Context, StepInput, string, time.Duration) (StepRecord, error)
	SaveResponse(context.Context, StepRecord) error
	ReleaseStep(context.Context, StepRecord, string, string) error
	PrepareTool(context.Context, ToolRecord) (ToolRecord, error)
	LoadTool(context.Context, ToolInput) (ToolRecord, error)
	SaveToolResult(context.Context, ToolRecord) (ToolRecord, error)
	CommitStep(context.Context, Checkpoint) (AgentRecord, error)
	FinishAgent(context.Context, FinishInput, string) error

	LoadCompact(context.Context, CompactInput) (CompactRecord, error)
	PrepareCompact(context.Context, CompactRecord) (CompactRecord, error)
	ClaimCompact(context.Context, CompactInput, string, time.Duration) (CompactRecord, error)
	SaveCompactResponse(context.Context, CompactRecord) error
	ReleaseCompact(context.Context, CompactRecord, string, string) error
	PublishCompact(context.Context, CompactPublication) (AgentRecord, error)
	CurrentEvidence(context.Context, string, string, string) ([]SourceRef, error)
	ValidateSourceRefs(context.Context, string, []SourceRef) error
	ValidateToolArtifacts(context.Context, string, []string) error
	IsCompactedContext(context.Context, string, string, string) (bool, error)

	LoadSourceSnapshot(context.Context, string, string) (string, string, error)
	ValidateStoredObject(context.Context, string, string, string) error
}

func ModelOperation(agentID string, stepID int64) string {
	return fmt.Sprintf("%s:%d:model", agentID, stepID)
}

func ToolOperation(agentID string, stepID int64, callID string) string {
	return fmt.Sprintf("%s:%d:%s:tool", agentID, stepID, callID)
}

func AgentWorkflowID(tenantID, agentID string) string {
	return "sentinel/" + tenantID + "/agent/" + agentID
}
