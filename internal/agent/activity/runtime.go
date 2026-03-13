package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/openai/openai-go/v3/responses"
	"go.temporal.io/sdk/temporal"

	"sentinel/internal/agent"
	modelapi "sentinel/internal/llm/openai"
	"sentinel/internal/payload"
)

type Model interface {
	Complete(context.Context, responses.ResponseNewParams, string) (modelapi.Completion, error)
}

// Tool handlers must be read-only or implement idempotency using OperationID.
// Child Agents are scheduled by the Workflow, never by an Activity handler.
type Tool struct {
	Definition responses.FunctionToolParam
	Execute    func(context.Context, ToolRequest) (json.RawMessage, error)
}

type ToolRequest struct {
	Agent       agent.AgentRecord
	OperationID string
	Arguments   json.RawMessage
}

type Activities struct {
	Store    agent.RuntimeStore
	Payloads payload.Store
	Model    Model
	Tools    map[string]Tool
}

type ContextSnapshot struct {
	Version   int64                   `json:"version"`
	Items     []json.RawMessage       `json:"items"`
	Resources []agent.ResourceBinding `json:"resources"`
}

// InitialContext is a caller-provided text document, not a system-message list.
type InitialContext struct {
	Text string `json:"text"`
}

type StructuredResult struct {
	AgentID     string            `json:"agent_id"`
	Summary     string            `json:"summary"`
	ResponseRef string            `json:"response_ref"`
	Artifacts   []string          `json:"artifacts"`
	SourceRefs  []json.RawMessage `json:"source_refs"`
}

func (a *Activities) Initialize(ctx context.Context, input agent.InitializeInput) (agent.Progress, error) {
	if err := validateSession(input.Session); err != nil {
		return agent.Progress{}, err
	}
	record, task, err := a.load(ctx, input.Session)
	if err != nil {
		return agent.Progress{}, err
	}
	if record.WorkflowID != input.WorkflowID {
		return agent.Progress{}, permanent("workflow_identity_mismatch")
	}
	record, err = a.Store.StartAgent(ctx, input)
	if err != nil {
		return agent.Progress{}, runtimeError("start_agent", err)
	}
	return progress(record, task), nil
}

// CreateRoot saves the task and resources before the Workflow is scheduled.
// A failed Temporal start can be retried using the returned persisted Agent ID.
func (a *Activities) CreateRoot(ctx context.Context, tenantID string, task agent.Task, resources []agent.ResourceBinding) (agent.AgentRecord, error) {
	if !validID(tenantID) {
		return agent.AgentRecord{}, errors.New("tenant must be a nonzero UUID")
	}
	task.Depth = 0
	task.DelegationScope = resources
	if err := a.validateTask(task); err != nil {
		return agent.AgentRecord{}, err
	}
	if err := a.Store.ValidateResources(ctx, tenantID, resources); err != nil {
		return agent.AgentRecord{}, err
	}
	if task.InitialContextRef != "" {
		var initial InitialContext
		if err := a.getJSON(ctx, tenantID, task.InitialContextRef, &initial); err != nil {
			return agent.AgentRecord{}, err
		}
		task.DelegationScope = append(append([]agent.ResourceBinding(nil), resources...), agent.ResourceBinding{
			BindingName: "initial_context", Kind: agent.ResourceObject, ObjectID: task.InitialContextRef})
	}
	taskRef, err := a.putJSON(ctx, tenantID, "agent-task", task)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	input := agent.SessionInput{TenantID: tenantID, SessionID: newID(), AgentID: newID(), TaskRef: taskRef, ContextRef: task.InitialContextRef}
	return a.Store.CreateAgent(ctx, agent.AgentRecord{Input: input, Resources: resources,
		WorkflowID: agent.AgentWorkflowID(tenantID, input.AgentID)})
}

func (a *Activities) load(ctx context.Context, input agent.SessionInput) (agent.AgentRecord, agent.Task, error) {
	record, err := a.Store.LoadAgent(ctx, input.TenantID, input.AgentID)
	if err != nil {
		return record, agent.Task{}, runtimeError("load_agent", err)
	}
	if record.Input.SessionID != input.SessionID || record.Input.TaskRef != input.TaskRef {
		return record, agent.Task{}, permanent("agent_identity_mismatch")
	}
	var task agent.Task
	if err := a.getJSON(ctx, input.TenantID, input.TaskRef, &task); err != nil {
		return record, task, err
	}
	if err := a.validateTask(task); err != nil {
		return record, task, err
	}
	if input.ContextRef != "" && input.ContextRef != task.InitialContextRef {
		return record, task, permanent("initial_context_mismatch")
	}
	return record, task, nil
}

func (a *Activities) validateTask(task agent.Task) error {
	if strings.TrimSpace(task.Prompt) == "" || len(task.Prompt) > 128<<10 || strings.TrimSpace(task.Model) == "" ||
		task.Depth < 0 || task.Depth > task.Limits.MaxChildDepth {
		return permanent("invalid_agent_task")
	}
	if err := task.Limits.Validate(); err != nil {
		return permanent("invalid_agent_limits")
	}
	seen := make(map[string]bool)
	for _, name := range task.Tools {
		if seen[name] {
			return permanent("duplicate_tool")
		}
		seen[name] = true
		if name != agent.ChildToolName {
			tool, ok := a.Tools[name]
			if !ok || tool.Execute == nil || tool.Definition.Name != name {
				return permanent("unregistered_tool")
			}
		}
	}
	for _, binding := range task.DelegationScope {
		if err := binding.Validate(); err != nil {
			return permanent("invalid_delegation_scope")
		}
	}
	return nil
}

func progress(record agent.AgentRecord, task agent.Task) agent.Progress {
	return agent.Progress{State: agent.RuntimeState{TenantID: record.Input.TenantID, SessionID: record.Input.SessionID,
		AgentID: record.Input.AgentID, ContextVersion: record.Head.ContextVersion, LastEventSequence: record.Head.LastEventSequence,
		NextStepID: record.Head.NextStepID}, Limits: task.Limits, Completed: record.Status == "completed", ResultRef: record.ResultRef}
}

func (a *Activities) Finish(ctx context.Context, input agent.FinishInput) error {
	if err := validateSession(input.Session); err != nil {
		return err
	}
	ref, err := a.putJSON(ctx, input.Session.TenantID, "agent-error", map[string]string{"code": input.Code, "status": input.Status})
	if err != nil {
		return err
	}
	return a.Store.FinishAgent(ctx, input, ref)
}

func (a *Activities) putJSON(ctx context.Context, tenantID, kind string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", permanent("invalid_payload")
	}
	return a.Payloads.Put(ctx, tenantID, kind, data)
}

func (a *Activities) getJSON(ctx context.Context, tenantID, ref string, value any) error {
	data, err := a.Payloads.Get(ctx, tenantID, ref)
	if errors.Is(err, payload.ErrNotFound) {
		return permanent("payload_not_found")
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return permanent("corrupt_payload")
	}
	return nil
}

func strictJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON input")
	}
	return nil
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil
}

func validateSession(input agent.SessionInput) error {
	if !validID(input.TenantID) || !validID(input.SessionID) || !validID(input.AgentID) || !validID(input.TaskRef) ||
		(input.ContextRef != "" && !validID(input.ContextRef)) {
		return permanent("invalid_session_input")
	}
	return nil
}

func newID() string { return uuid.Must(uuid.NewV7()).String() }

func permanent(code string) error { return temporal.NewNonRetryableApplicationError(code, code, nil) }

func runtimeError(code string, err error) error {
	if errors.Is(err, agent.ErrUncertain) {
		return permanent("upstream_outcome_uncertain")
	}
	if errors.Is(err, agent.ErrNotFound) || errors.Is(err, agent.ErrTerminal) {
		return permanent(code)
	}
	return fmt.Errorf("%s: %w", code, err)
}
