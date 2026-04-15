package activity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	openaigo "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	sdkactivity "go.temporal.io/sdk/activity"

	"sentinel/internal/agent"
	modelapi "sentinel/internal/llm/openai"
	"sentinel/internal/stream"
)

type argumentsDocument struct {
	JSON string `json:"json"`
}

func (a *Activities) ModelStep(ctx context.Context, input agent.StepInput) (agent.ModelResult, error) {
	stopHeartbeat := modelHeartbeat(ctx)
	defer stopHeartbeat()
	record, task, err := a.load(ctx, input.Session)
	if err != nil {
		return agent.ModelResult{}, err
	}
	if input.StepID < 1 || input.StepID > task.Limits.MaxSteps {
		return agent.ModelResult{}, permanent("step_budget_exhausted")
	}
	step, err := a.Store.LoadStep(ctx, input)
	if errors.Is(err, agent.ErrNotFound) {
		if record.Status != "running" || record.Head.NextStepID != input.StepID {
			return agent.ModelResult{}, permanent("step_cursor_mismatch")
		}
		request, buildErr := a.buildRequest(ctx, record, task)
		if buildErr != nil {
			return agent.ModelResult{}, buildErr
		}
		data, marshalErr := json.Marshal(request)
		if marshalErr != nil {
			return agent.ModelResult{}, permanent("invalid_model_request")
		}
		if len(data) > task.Limits.MaxContextBytes {
			return agent.ModelResult{}, permanent("context_budget_exhausted")
		}
		ref, saveErr := a.Payloads.Put(ctx, input.Session.TenantID, "model-request", data)
		if saveErr != nil {
			return agent.ModelResult{}, saveErr
		}
		hash := sha256.Sum256(data)
		step, err = a.Store.PrepareStep(ctx, agent.StepRecord{Input: input, ID: newID(), OperationID: agent.ModelOperation(input.Session.AgentID, input.StepID),
			ContextVersion: record.Head.ContextVersion, Model: task.Model, RequestRef: ref, RequestHash: hash[:]})
	}
	if err != nil {
		return agent.ModelResult{}, runtimeError("prepare_model_request", err)
	}
	if step.ResponseRef == "" {
		step, err = a.Store.ClaimStep(ctx, input, newID(), task.Limits.ModelTimeout+time.Minute)
		if err != nil {
			return agent.ModelResult{}, runtimeError("claim_model_request", err)
		}
		if step.ResponseRef == "" {
			step, err = a.send(ctx, task, step)
			if err != nil {
				return agent.ModelResult{}, err
			}
		}
	}
	var response responses.Response
	if err := a.getJSON(ctx, input.Session.TenantID, step.ResponseRef, &response); err != nil {
		return agent.ModelResult{}, err
	}
	calls, err := a.responseCalls(ctx, input.Session.TenantID, task, response)
	return agent.ModelResult{ResponseRef: step.ResponseRef, Calls: calls}, err
}

func modelHeartbeat(ctx context.Context) func() {
	if !sdkactivity.IsActivity(ctx) {
		return func() {}
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	sdkactivity.RecordHeartbeat(ctx)
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				sdkactivity.RecordHeartbeat(ctx)
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (a *Activities) send(ctx context.Context, task agent.Task, step agent.StepRecord) (agent.StepRecord, error) {
	var saved json.RawMessage
	if err := a.getJSON(ctx, step.Input.Session.TenantID, step.RequestRef, &saved); err != nil {
		// The lease has been claimed, but no upstream call took place. Explicitly
		// fail instead of allowing an ambiguous retry to send a different request.
		return step, err
	}
	// Use the SDK's raw-parameter support to replay the exact persisted request.
	// Decoding nested request unions and re-encoding them can omit input items.
	request := param.Override[responses.ResponseNewParams](saved)
	requestCtx, cancel := context.WithTimeout(ctx, task.Limits.ModelTimeout)
	var completion modelapi.Completion
	var callErr error
	var publishDisabled atomic.Bool
	if a.Streams != nil {
		started, err := stream.NewEvent(stream.EventModelStarted, step.Input.StepID, step.FencingToken,
			map[string]string{"model": task.Model})
		if err == nil {
			if err := a.publishStreamEvent(ctx, step, started); err != nil {
				publishDisabled.Store(true)
			}
		}
		completion, callErr = a.Model.Stream(requestCtx, request, step.OperationID, func(delta modelapi.StreamDelta) error {
			switch delta.Kind {
			case "text_done":
				a.publishModelText(requestCtx, step, &publishDisabled, delta.ItemID, delta.Text)
			case "arguments_done":
				a.publishModelArguments(requestCtx, step, &publishDisabled, delta.ItemID, delta.Text)
			}
			return nil
		})
	} else {
		completion, callErr = a.Model.Complete(requestCtx, request, step.OperationID)
	}
	cancel()
	// Preserve a known outcome even if cancellation arrived while the SDK was
	// returning. This bounded cleanup never sends another upstream request.
	saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer stop()
	if callErr != nil {
		status, code := "uncertain", "upstream_outcome_uncertain"
		var failure *modelapi.RequestError
		if errors.As(callErr, &failure) && !failure.Uncertain {
			status, code = "failed", "upstream_request_rejected"
			if failure.Retryable {
				status, code = "pending", "upstream_rate_limited"
			}
		}
		ref, err := a.putJSON(saveCtx, step.Input.Session.TenantID, "model-error", map[string]string{"code": code})
		if err != nil {
			return step, err
		}
		if err := a.Store.ReleaseStep(saveCtx, step, status, ref); err != nil {
			return step, err
		}
		a.publishModelStatus(saveCtx, step, stream.EventModelFailed, map[string]string{"status": status, "code": code})
		if status == "pending" {
			return step, errors.New(code)
		}
		return step, permanent(code)
	}
	// Response unions are decoded SDK views, not round-trip request types.
	// Persist the SDK's original body so arguments and unknown fields survive.
	raw := []byte(completion.Response.RawJSON())
	if !json.Valid(raw) {
		return step, errors.New("SDK response has no valid raw body")
	}
	ref, err := a.Payloads.Put(saveCtx, step.Input.Session.TenantID, "model-response", raw)
	if err != nil {
		return step, err
	}
	step.ResponseRef, step.ProviderRequestID = ref, completion.RequestID
	step.InputTokens, step.OutputTokens = completion.Response.Usage.InputTokens, completion.Response.Usage.OutputTokens
	if err := a.Store.SaveResponse(saveCtx, step); err != nil {
		return step, err
	}
	step.Status = "response_saved"
	a.publishModelStatus(saveCtx, step, stream.EventModelCompleted, map[string]string{"status": "completed"})
	return step, nil
}

func (a *Activities) publishModelStatus(ctx context.Context, step agent.StepRecord, eventType string, data any) {
	a.publishEvent(ctx, step.Input.Session.TenantID, step.Input.Session.AgentID, eventType,
		step.Input.StepID, step.FencingToken, data)
}

func (a *Activities) publishStreamEvent(ctx context.Context, step agent.StepRecord, event stream.Event) error {
	if a.Streams == nil {
		return nil
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, err := a.Streams.Publish(publishCtx, step.Input.Session.TenantID, step.Input.Session.AgentID, event)
	return err
}

type modelTextData struct {
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	ItemID string `json:"item_id,omitempty"`
}

type modelArgumentsData struct {
	ItemID    string `json:"item_id,omitempty"`
	Arguments string `json:"arguments"`
}

func (a *Activities) publishModelText(ctx context.Context, step agent.StepRecord, disabled *atomic.Bool, itemID, text string) {
	if disabled.Load() || strings.TrimSpace(text) == "" {
		return
	}
	event, err := stream.NewEvent(stream.EventModelText, step.Input.StepID, step.FencingToken,
		modelTextData{Kind: "text", Text: text, ItemID: itemID})
	if err != nil {
		return
	}
	if err := a.publishStreamEvent(ctx, step, event); err != nil {
		disabled.Store(true)
	}
}

func (a *Activities) publishModelArguments(ctx context.Context, step agent.StepRecord, disabled *atomic.Bool, itemID, arguments string) {
	if disabled.Load() || strings.TrimSpace(arguments) == "" {
		return
	}
	event, err := stream.NewEvent(stream.EventModelArguments, step.Input.StepID, step.FencingToken,
		modelArgumentsData{ItemID: itemID, Arguments: arguments})
	if err != nil {
		return
	}
	if err := a.publishStreamEvent(ctx, step, event); err != nil {
		disabled.Store(true)
	}
}

func (a *Activities) buildRequest(ctx context.Context, record agent.AgentRecord, task agent.Task) (responses.ResponseNewParams, error) {
	items, err := a.inputItems(ctx, record, task)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	return a.modelRequest(task, items), nil
}

func (a *Activities) inputItems(ctx context.Context, record agent.AgentRecord, task agent.Task) ([]responses.ResponseInputItemUnionParam, error) {
	var items []responses.ResponseInputItemUnionParam
	if record.Head.SnapshotRef != "" {
		var snapshot ContextSnapshot
		if err := a.getJSON(ctx, record.Input.TenantID, record.Head.SnapshotRef, &snapshot); err != nil {
			return nil, err
		}
		if snapshot.Version != record.Head.ContextVersion {
			return nil, permanent("context_version_mismatch")
		}
		items = append(items, rawInputItems(snapshot.Items)...)
		return items, nil
	}
	if task.InitialContextRef != "" {
		var initial InitialContext
		if err := a.getJSON(ctx, record.Input.TenantID, task.InitialContextRef, &initial); err != nil {
			return nil, err
		}
		items = append(items, responses.ResponseInputItemParamOfMessage("Initial context:\n"+initial.Text, responses.EasyInputMessageRoleUser))
	}
	text := task.Prompt
	if task.ExpectedOutput != "" {
		text += "\n\nExpected output:\n" + task.ExpectedOutput
	}
	resources, err := json.Marshal(record.Resources)
	if err != nil {
		return nil, err
	}
	text += "\n\nResource bindings (identifiers only; use available tools to read content):\n" + string(resources)
	items = append(items, responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleUser))
	return items, nil
}

func rawInputItems(raw []json.RawMessage) []responses.ResponseInputItemUnionParam {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(raw))
	for _, item := range raw {
		items = append(items, param.Override[responses.ResponseInputItemUnionParam](item))
	}
	return items
}

func (a *Activities) modelRequest(task agent.Task, items []responses.ResponseInputItemUnionParam) responses.ResponseNewParams {
	request := responses.ResponseNewParams{
		Model: shared.ResponsesModel(task.Model), Instructions: openaigo.String(task.Instructions),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: items}, Store: openaigo.Bool(false),
		Include:    []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		Truncation: responses.ResponseNewParamsTruncationDisabled, MaxOutputTokens: openaigo.Int(task.Limits.MaxOutputTokens),
		ParallelToolCalls: openaigo.Bool(true),
	}
	for _, name := range task.Tools {
		if name == agent.ChildToolName {
			if task.Depth < task.Limits.MaxChildDepth {
				definition := childDefinition()
				request.Tools = append(request.Tools, responses.ToolUnionParam{OfFunction: &definition})
			}
		} else {
			definition := a.Tools[name].Definition
			request.Tools = append(request.Tools, responses.ToolUnionParam{OfFunction: &definition})
		}
	}
	return request
}

func (a *Activities) responseCalls(ctx context.Context, tenantID string, task agent.Task, response responses.Response) ([]agent.ToolCall, error) {
	if response.Status != responses.ResponseStatusCompleted || response.Error.Code != "" {
		return nil, permanent("upstream_response_incomplete")
	}
	calls := make([]agent.ToolCall, 0)
	seen := make(map[string]bool)
	for _, item := range response.Output {
		switch item.Type {
		case "message", "reasoning":
		case "function_call":
			call := item.AsFunctionCall()
			if call.CallID == "" || len(call.CallID) > 256 || call.Name == "" || len(call.Name) > 128 || seen[call.CallID] {
				return nil, permanent("invalid_upstream_tool_call")
			}
			seen[call.CallID] = true
			if len(calls) >= task.Limits.MaxToolCalls {
				return nil, permanent("tool_call_budget_exhausted")
			}
			ref, err := a.putJSON(ctx, tenantID, "tool-arguments", argumentsDocument{JSON: call.Arguments})
			if err != nil {
				return nil, err
			}
			calls = append(calls, agent.ToolCall{CallID: call.CallID, Name: call.Name, ArgumentsRef: ref})
		default:
			return nil, permanent("unsupported_upstream_output")
		}
	}
	if len(response.Output) == 0 {
		return nil, permanent("empty_upstream_output")
	}
	if len(calls) == 0 && responseSummary(response) == "" {
		return nil, permanent("empty_upstream_answer")
	}
	return calls, nil
}

func responseSummary(response responses.Response) string {
	if text := response.OutputText(); text != "" {
		return text
	}
	for _, output := range response.Output {
		if output.Type == "message" {
			for _, content := range output.AsMessage().Content {
				if content.Type == "refusal" {
					return content.AsRefusal().Refusal
				}
			}
		}
	}
	return ""
}

func childDefinition() responses.FunctionToolParam {
	return responses.FunctionToolParam{Name: agent.ChildToolName, Strict: openaigo.Bool(false),
		Description: openaigo.String("Delegate a bounded task to an independent child Agent. Only explicitly supplied context and resources are passed; the child returns a structured result."),
		Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"task"}, "properties": map[string]any{
			"task":            map[string]any{"type": "string", "minLength": 1, "maxLength": 131072},
			"context_ref":     map[string]any{"type": "string", "description": "Optional authorized initial-context object ID."},
			"expected_output": map[string]any{"type": "string"},
			"resources": map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"binding_name", "resource_kind"}, "properties": map[string]any{
					"binding_name": map[string]any{"type": "string"}, "resource_kind": map[string]any{"type": "string", "enum": []string{"source_repository", "source_snapshot", "object"}},
					"repository_id": map[string]any{"type": "string"}, "source_snapshot_id": map[string]any{"type": "string"}, "object_id": map[string]any{"type": "string"},
				}}},
		}}}
}

func (a *Activities) verifyTool(ctx context.Context, input agent.ToolInput) (agent.AgentRecord, agent.Task, error) {
	record, task, err := a.load(ctx, input.Step.Session)
	if err != nil {
		return record, task, err
	}
	step, err := a.Store.LoadStep(ctx, input.Step)
	if err != nil {
		return record, task, runtimeError("load_model_response", err)
	}
	var response responses.Response
	if step.ResponseRef == "" {
		return record, task, permanent("missing_model_response")
	}
	if err := a.getJSON(ctx, input.Step.Session.TenantID, step.ResponseRef, &response); err != nil {
		return record, task, err
	}
	calls, err := a.responseCalls(ctx, input.Step.Session.TenantID, task, response)
	if err != nil {
		return record, task, err
	}
	for _, call := range calls {
		if call == input.Call {
			return record, task, nil
		}
	}
	return record, task, permanent("tool_call_not_in_response")
}
