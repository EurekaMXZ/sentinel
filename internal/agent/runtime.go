package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sentinel/internal/agentctx"
	"sentinel/internal/eventlog"
)

const (
	WorkflowName       = "sentinel.AgentSessionWorkflow"
	InitializeActivity = "sentinel.agent.initialize"
	ModelActivity      = "sentinel.agent.model"
	PrepareActivity    = "sentinel.agent.prepare_tool"
	ExecuteActivity    = "sentinel.agent.execute_tool"
	CompleteActivity   = "sentinel.agent.complete_tool"
	CheckpointActivity = "sentinel.agent.checkpoint"
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
	MaxSteps           int64         `json:"max_steps"`
	ContinueAfterSteps int           `json:"continue_after_steps"`
	MaxChildDepth      int           `json:"max_child_depth"`
	MaxToolCalls       int           `json:"max_tool_calls"`
	MaxContextBytes    int           `json:"max_context_bytes"`
	MaxOutputTokens    int64         `json:"max_output_tokens"`
	ModelTimeout       time.Duration `json:"model_timeout"`
}

func DefaultLimits() Limits {
	return Limits{MaxSteps: 64, ContinueAfterSteps: 16, MaxChildDepth: 3,
		MaxToolCalls: 16, MaxContextBytes: 4 << 20, MaxOutputTokens: 8192,
		ModelTimeout: 2 * time.Minute}
}

func (l Limits) Validate() error {
	if l.MaxSteps < 1 || l.MaxSteps > 10000 || l.ContinueAfterSteps < 1 || l.ContinueAfterSteps > 1000 ||
		l.MaxChildDepth < 0 || l.MaxChildDepth > 8 || l.MaxToolCalls < 1 || l.MaxToolCalls > 32 ||
		l.MaxContextBytes < 1024 || l.MaxContextBytes > 8<<20 || l.MaxOutputTokens < 1 ||
		l.ModelTimeout <= 0 || l.ModelTimeout > 30*time.Minute {
		return errors.New("invalid Agent execution limits")
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
	Limits            Limits            `json:"limits"`
	Depth             int               `json:"depth"`
	DelegationScope   []ResourceBinding `json:"delegation_scope"`
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
	State     RuntimeState `json:"state"`
	Limits    Limits       `json:"limits"`
	Completed bool         `json:"completed"`
	ResultRef string       `json:"result_ref,omitempty"`
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
}

type ToolCompletion struct {
	Plan        ToolPlan `json:"plan"`
	ChildResult Result   `json:"child_result"`
	ChildFailed bool     `json:"child_failed"`
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

type ToolRecord struct {
	Input       ToolInput
	ID          string
	OperationID string
	ResultRef   string
	Status      string
}

type Checkpoint struct {
	Step            StepInput
	ToolCount       int
	ExpectedHead    agentctx.Head
	ContextRecordID string
	ContextRef      string
	SegmentRef      string
	Events          []eventlog.Event
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
