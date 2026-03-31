package activity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	openaigo "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"sentinel/internal/agent"
	modelapi "sentinel/internal/llm/openai"
	"sentinel/internal/stream"
)

const (
	compactOutputTokenLimit = 4096
	maxCompactSummaryBytes  = 64 << 10
	maxCompactItemBytes     = 8 << 10
	maxCompactListItems     = 64
)

const compactInstructions = `You compact the working context of a read-only software-analysis Agent.
Return one JSON object and nothing else. Preserve the task requirements, constraints,
verified decisions and current state, unresolved questions, errors, child Agent results,
and verified artifact or source references. Do not invent facts, tools, resources, or
references that do not appear in the supplied context. Prefer concise statements over
narrative, and omit information that is no longer needed for the remaining task.`

const compactDirective = `Produce the compacted context JSON now.`

// Compact replaces the Agent's current Context with a validated model summary.
// Raw events, requests, and responses remain immutable; only the Context pointer
// advances. It is safe to retry: a saved response is reused and publication is
// guarded by the Agent head's row version.
func (a *Activities) Compact(ctx context.Context, input agent.CompactInput) (agent.Progress, error) {
	stopHeartbeat := modelHeartbeat(ctx)
	defer stopHeartbeat()

	record, task, err := a.load(ctx, input.Session)
	if err != nil {
		return agent.Progress{}, err
	}
	if record.Status == "completed" {
		return progress(record, task), nil
	}
	if record.Status != "running" {
		return agent.Progress{}, permanent("agent_not_running")
	}
	if record.Head.SnapshotRef == "" || record.Head.ContextVersion == 0 {
		return agent.Progress{}, permanent("context_not_initialized")
	}
	if record.Head.SnapshotRecordID != "" {
		compacted, err := a.Store.IsCompactedContext(ctx, input.Session.TenantID, input.Session.AgentID, record.Head.SnapshotRecordID)
		if err != nil {
			return agent.Progress{}, err
		}
		if compacted {
			// The Activity may be retried after publication committed; the
			// current Context already contains the compacted summary.
			return progress(record, task), nil
		}
	}
	input.RowVersion = record.Head.RowVersion

	compact, err := a.Store.LoadCompact(ctx, input)
	if errors.Is(err, agent.ErrNotFound) {
		request, buildErr := a.buildCompactRequest(ctx, record, task)
		if buildErr != nil {
			return agent.Progress{}, buildErr
		}
		data, marshalErr := json.Marshal(request)
		if marshalErr != nil {
			return agent.Progress{}, permanent("invalid_compact_request")
		}
		if len(data) > task.Limits.MaxContextBytes {
			return agent.Progress{}, permanent("context_budget_exhausted")
		}
		ref, putErr := a.Payloads.Put(ctx, input.Session.TenantID, "compact-request", data)
		if putErr != nil {
			return agent.Progress{}, putErr
		}
		hash := sha256.Sum256(data)
		compact, err = a.Store.PrepareCompact(ctx, agent.CompactRecord{
			Input: input.Session, ID: newID(),
			OperationID:          agent.CompactOperation(record.Input.AgentID, record.Head.RowVersion),
			SourceRowVersion:     record.Head.RowVersion,
			SourceContextVersion: record.Head.ContextVersion,
			SourceEventSequence:  record.Head.LastEventSequence,
			RequestRef:           ref,
			RequestHash:          hash[:],
		})
	}
	if err != nil {
		return agent.Progress{}, runtimeError("prepare_compact", err)
	}
	if compact.Status == "published" {
		record, task, err = a.load(ctx, input.Session)
		if err != nil {
			return agent.Progress{}, err
		}
		return progress(record, task), nil
	}
	if compact.ResponseRef == "" {
		compact, err = a.Store.ClaimCompact(ctx, input, newID(), task.Limits.ModelTimeout+time.Minute)
		if err != nil {
			return agent.Progress{}, runtimeError("claim_compact", err)
		}
		if compact.ResponseRef == "" {
			compact, err = a.sendCompact(ctx, task, compact)
			if err != nil {
				return agent.Progress{}, err
			}
		}
	}

	var response responses.Response
	if err := a.getJSON(ctx, input.Session.TenantID, compact.ResponseRef, &response); err != nil {
		return agent.Progress{}, err
	}
	summary, err := parseCompactSummary(response)
	if err != nil {
		return agent.Progress{}, err
	}
	if err := a.validateCompactSummary(ctx, record, task, summary); err != nil {
		return agent.Progress{}, err
	}
	snapshot, err := compactSnapshot(record, summary, compact.SourceContextVersion+1, compact.SourceEventSequence)
	if err != nil {
		return agent.Progress{}, permanent("invalid_compact_context")
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return agent.Progress{}, permanent("invalid_compact_context")
	}
	if len(data) > maxCompactSummaryBytes {
		return agent.Progress{}, permanent("context_compact_too_large")
	}
	assessment, err := a.assessBudget(task, snapshot.Items, 0, 0)
	if err != nil {
		return agent.Progress{}, err
	}
	if assessment.NeedsCompact {
		return agent.Progress{}, permanent("context_compact_ineffective")
	}
	ref, err := a.Payloads.Put(ctx, input.Session.TenantID, "agent-context", data)
	if err != nil {
		return agent.Progress{}, err
	}
	record, err = a.Store.PublishCompact(ctx, agent.CompactPublication{
		Record: compact, ContextRecordID: newID(), SnapshotRef: ref, SourceRefs: summary.SourceRefs})
	if err != nil {
		return agent.Progress{}, runtimeError("publish_compact", err)
	}
	a.publishEvent(ctx, input.Session.TenantID, input.Session.AgentID, stream.EventContextCompacted, 0, 0,
		map[string]int64{"context_version": record.Head.ContextVersion})
	return progress(record, task), nil
}

func (a *Activities) sendCompact(ctx context.Context, task agent.Task, record agent.CompactRecord) (agent.CompactRecord, error) {
	var saved json.RawMessage
	if err := a.getJSON(ctx, record.Input.TenantID, record.RequestRef, &saved); err != nil {
		return record, err
	}
	request := param.Override[responses.ResponseNewParams](saved)
	requestCtx, cancel := context.WithTimeout(ctx, task.Limits.ModelTimeout)
	completion, callErr := a.Model.Complete(requestCtx, request, record.OperationID)
	cancel()
	// A known outcome is retained even if cancellation raced the response. This
	// bounded cleanup never sends another upstream request.
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
		ref, err := a.putJSON(saveCtx, record.Input.TenantID, "compact-error", map[string]string{"code": code})
		if err != nil {
			return record, err
		}
		if err := a.Store.ReleaseCompact(saveCtx, record, status, ref); err != nil {
			return record, err
		}
		if status == "pending" {
			return record, errors.New(code)
		}
		return record, permanent(code)
	}
	raw := []byte(completion.Response.RawJSON())
	if !json.Valid(raw) {
		return record, errors.New("SDK response has no valid raw body")
	}
	ref, err := a.Payloads.Put(saveCtx, record.Input.TenantID, "compact-response", raw)
	if err != nil {
		return record, err
	}
	record.ResponseRef = ref
	if err := a.Store.SaveCompactResponse(saveCtx, record); err != nil {
		return record, err
	}
	record.Status = "response_saved"
	return record, nil
}

func (a *Activities) buildCompactRequest(ctx context.Context, record agent.AgentRecord, task agent.Task) (responses.ResponseNewParams, error) {
	items, err := a.inputItems(ctx, record, task)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	items = append(items, responses.ResponseInputItemParamOfMessage(compactDirective, responses.EasyInputMessageRoleUser))
	request := responses.ResponseNewParams{
		Model: shared.ResponsesModel(task.Model), Instructions: openaigo.String(compactInstructions),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: items}, Store: openaigo.Bool(false),
		Include:           []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		Truncation:        responses.ResponseNewParamsTruncationDisabled,
		MaxOutputTokens:   openaigo.Int(compactOutputTokens(task)),
		ParallelToolCalls: openaigo.Bool(false),
	}
	if a.CompactStructuredOutput {
		format := responses.ResponseFormatTextConfigParamOfJSONSchema("agent_context_compact", compactSchema())
		format.OfJSONSchema.Strict = openaigo.Bool(true)
		request.Text = responses.ResponseTextConfigParam{Format: format}
	}
	return request, nil
}

func compactOutputTokens(task agent.Task) int64 {
	if task.Limits.MaxOutputTokens < compactOutputTokenLimit {
		return task.Limits.MaxOutputTokens
	}
	return compactOutputTokenLimit
}

func parseCompactSummary(response responses.Response) (agent.CompactSummary, error) {
	if response.Status != responses.ResponseStatusCompleted || response.Error.Code != "" {
		return agent.CompactSummary{}, permanent("compact_response_incomplete")
	}
	for _, item := range response.Output {
		switch item.Type {
		case "message", "reasoning":
		default:
			return agent.CompactSummary{}, permanent("invalid_compact_output")
		}
	}
	text := strings.TrimSpace(response.OutputText())
	if text == "" {
		return agent.CompactSummary{}, permanent("compact_response_empty")
	}
	var summary agent.CompactSummary
	if err := strictJSON([]byte(text), &summary); err != nil {
		return agent.CompactSummary{}, permanent("compact_response_invalid")
	}
	if summary.TaskRequirements == nil || summary.Constraints == nil || summary.Decisions == nil ||
		summary.CurrentState == nil || summary.OpenItems == nil || summary.Errors == nil ||
		summary.ChildAgentResults == nil || summary.ToolArtifactRefs == nil || summary.SourceRefs == nil {
		return agent.CompactSummary{}, permanent("compact_response_invalid")
	}
	return summary, nil
}

func (a *Activities) validateCompactSummary(ctx context.Context, record agent.AgentRecord, task agent.Task, summary agent.CompactSummary) error {
	for _, list := range [][]string{summary.TaskRequirements, summary.Constraints, summary.Decisions,
		summary.CurrentState, summary.OpenItems, summary.Errors, summary.ChildAgentResults} {
		if len(list) > maxCompactListItems {
			return permanent("context_compact_too_large")
		}
		for _, item := range list {
			if len(item) > maxCompactItemBytes {
				return permanent("context_compact_too_large")
			}
		}
	}
	if len(summary.ToolArtifactRefs) > maxCompactListItems || len(summary.SourceRefs) > maxCompactListItems {
		return permanent("context_compact_too_large")
	}
	data, err := json.Marshal(summary)
	if err != nil || len(data) > maxCompactSummaryBytes {
		return permanent("context_compact_too_large")
	}
	if len(summary.SourceRefs) > 0 {
		evidence, err := a.Store.CurrentEvidence(ctx, record.Input.TenantID, record.Input.AgentID, record.Head.SnapshotRecordID)
		if err != nil {
			return err
		}
		for _, ref := range summary.SourceRefs {
			if !ref.Valid() {
				return permanent("invalid_compact_source_ref")
			}
			allowed := agent.CanDelegate(task.DelegationScope, agent.ResourceBinding{
				BindingName: "compact_evidence", Kind: agent.ResourceSourceSnapshot,
				RepositoryID: ref.RepositoryID, SourceSnapshotID: ref.SourceSnapshotID})
			if !allowed {
				for _, existing := range evidence {
					if existing == ref {
						allowed = true
						break
					}
				}
			}
			if !allowed {
				return permanent("compact_source_ref_denied")
			}
		}
		if err := a.Store.ValidateSourceRefs(ctx, record.Input.TenantID, summary.SourceRefs); err != nil {
			return err
		}
	}
	if len(summary.ToolArtifactRefs) > 0 {
		if err := a.Store.ValidateToolArtifacts(ctx, record.Input.TenantID, summary.ToolArtifactRefs); err != nil {
			return err
		}
	}
	return nil
}

func compactSnapshot(record agent.AgentRecord, summary agent.CompactSummary, version, through int64) (ContextSnapshot, error) {
	data, err := json.Marshal(summary)
	if err != nil {
		return ContextSnapshot{}, err
	}
	text := fmt.Sprintf("Compacted context (version %d, through event %d). Earlier conversation was summarized as JSON:\n%s",
		version, through, data)
	raw, err := json.Marshal(responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleUser))
	if err != nil {
		return ContextSnapshot{}, err
	}
	return ContextSnapshot{Version: version, Items: []json.RawMessage{raw}, Resources: record.Resources}, nil
}

func compactSchema() map[string]any {
	stringList := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"task_requirements", "constraints", "decisions", "current_state", "open_items",
			"errors", "child_agent_results", "tool_artifact_refs", "source_refs",
		},
		"properties": map[string]any{
			"task_requirements":   stringList,
			"constraints":         stringList,
			"decisions":           stringList,
			"current_state":       stringList,
			"open_items":          stringList,
			"errors":              stringList,
			"child_agent_results": stringList,
			"tool_artifact_refs":  stringList,
			"source_refs": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"repository_id", "source_snapshot_id"},
					"properties": map[string]any{
						"repository_id":      map[string]any{"type": "string"},
						"source_snapshot_id": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}
