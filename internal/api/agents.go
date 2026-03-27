package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	sdkclient "go.temporal.io/sdk/client"

	"sentinel/internal/agent"
)

type agentView struct {
	TenantID              string                  `json:"tenant_id"`
	SessionID             string                  `json:"session_id"`
	AgentID               string                  `json:"agent_id"`
	WorkflowID            string                  `json:"workflow_id"`
	ParentAgentID         string                  `json:"parent_agent_id,omitempty"`
	ParentToolExecutionID string                  `json:"parent_tool_execution_id,omitempty"`
	Status                string                  `json:"status"`
	ResultRef             string                  `json:"result_ref,omitempty"`
	ErrorRef              string                  `json:"error_ref,omitempty"`
	RowVersion            int64                   `json:"row_version"`
	ContextVersion        int64                   `json:"context_version"`
	LastEventSequence     int64                   `json:"last_event_sequence"`
	NextStepID            int64                   `json:"next_step_id"`
	CreatedAt             time.Time               `json:"created_at"`
	StartedAt             *time.Time              `json:"started_at,omitempty"`
	FinishedAt            *time.Time              `json:"finished_at,omitempty"`
	Resources             []agent.ResourceBinding `json:"resources,omitempty"`
}

func viewOfAgent(record agent.AgentRecord) agentView {
	return agentView{
		TenantID: record.Input.TenantID, SessionID: record.Input.SessionID, AgentID: record.Input.AgentID,
		WorkflowID: record.WorkflowID, ParentAgentID: record.ParentAgentID,
		ParentToolExecutionID: record.ParentToolExecutionID, Status: record.Status,
		ResultRef: record.ResultRef, ErrorRef: record.ErrorRef,
		RowVersion: record.Head.RowVersion, ContextVersion: record.Head.ContextVersion,
		LastEventSequence: record.Head.LastEventSequence, NextStepID: record.Head.NextStepID,
		CreatedAt: record.CreatedAt, StartedAt: record.StartedAt, FinishedAt: record.FinishedAt,
		Resources: record.Resources,
	}
}

type createAgentRequest struct {
	Task           string                  `json:"task"`
	ContextRef     string                  `json:"context_ref"`
	ExpectedOutput string                  `json:"expected_output"`
	Resources      []agent.ResourceBinding `json:"resources"`
	MaxSteps       *int64                  `json:"max_steps"`
	MaxChildDepth  *int                    `json:"max_child_depth"`
}

type initialContextDocument struct {
	Text string `json:"text"`
}

func createAgent(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request createAgentRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			errorJSON(c, http.StatusBadRequest, "invalid_request", "request body must be valid JSON")
			return
		}
		tenant := tenantID(c)
		if strings.TrimSpace(request.Task) == "" || len(request.Task) > 128<<10 {
			errorJSON(c, http.StatusBadRequest, "invalid_task", "task is required and limited to 128 KiB")
			return
		}
		if len(request.ExpectedOutput) > 32<<10 {
			errorJSON(c, http.StatusBadRequest, "invalid_expected_output", "expected_output is limited to 32 KiB")
			return
		}
		if request.ContextRef != "" && !validUUID(request.ContextRef) {
			errorJSON(c, http.StatusBadRequest, "invalid_context_ref", "context_ref must be a nonzero UUID")
			return
		}
		names := make(map[string]bool, len(request.Resources))
		for _, binding := range request.Resources {
			if err := binding.Validate(); err != nil {
				errorJSON(c, http.StatusBadRequest, "invalid_resource", err.Error())
				return
			}
			if names[binding.BindingName] {
				errorJSON(c, http.StatusBadRequest, "duplicate_resource", "resource binding names must be unique")
				return
			}
			names[binding.BindingName] = true
		}
		limits := deps.Limits
		if request.MaxSteps != nil {
			limits.MaxSteps = *request.MaxSteps
		}
		if request.MaxChildDepth != nil {
			limits.MaxChildDepth = *request.MaxChildDepth
		}
		if err := limits.Validate(); err != nil {
			errorJSON(c, http.StatusBadRequest, "invalid_limits", err.Error())
			return
		}
		if request.ContextRef != "" {
			data, err := deps.Payloads.Get(c.Request.Context(), tenant, request.ContextRef)
			if err != nil {
				errorJSON(c, http.StatusBadRequest, "invalid_context_ref", "initial context object is not available")
				return
			}
			var document initialContextDocument
			if err := json.Unmarshal(data, &document); err != nil {
				errorJSON(c, http.StatusBadRequest, "invalid_context_ref", "initial context object must contain a text field")
				return
			}
		}
		if err := deps.AgentResources.ValidateResources(c.Request.Context(), tenant, request.Resources); err != nil {
			errorJSON(c, http.StatusBadRequest, "resource_unavailable", err.Error())
			return
		}
		task := agent.NewRootTask(request.Task, request.ContextRef, deps.Model, limits,
			agent.DefaultTools(), deps.SourceTaskQueue)
		task.ExpectedOutput = request.ExpectedOutput
		record, err := deps.AgentTasks.CreateRoot(c.Request.Context(), tenant, task, request.Resources)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "create Agent task", "tenant_id", tenant, "error", err)
			errorJSON(c, http.StatusInternalServerError, "agent_create_failed", "failed to persist the Agent task")
			return
		}
		if err := scheduleAgent(c, deps, record); err != nil {
			logger.ErrorContext(c.Request.Context(), "schedule Agent workflow", "tenant_id", tenant,
				"agent_id", record.Input.AgentID, "error", err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "Agent was persisted but its Workflow was not started; resume with the agent ID",
				"code": "workflow_start_failed", "agent_id": record.Input.AgentID})
			return
		}
		c.JSON(http.StatusAccepted, viewOfAgent(record))
	}
}

func scheduleAgent(c *gin.Context, deps Dependencies, record agent.AgentRecord) error {
	_, err := deps.Workflows.ExecuteWorkflow(c.Request.Context(), sdkclient.StartWorkflowOptions{
		ID: record.WorkflowID, TaskQueue: deps.AgentTaskQueue,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, agent.WorkflowName, agent.WorkflowInput{Session: record.Input})
	return err
}

func listAgents(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, ok := pageLimit(c, 50, 200)
		if !ok {
			return
		}
		records, err := deps.Agents.ListAgents(c.Request.Context(), tenantID(c), limit)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "list Agents", "error", err)
			errorJSON(c, http.StatusInternalServerError, "agent_list_failed", "failed to list Agents")
			return
		}
		agents := make([]agentView, 0, len(records))
		for _, record := range records {
			agents = append(agents, viewOfAgent(record))
		}
		c.JSON(http.StatusOK, gin.H{"agents": agents})
	}
}

func getAgent(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := agentID(c)
		if !ok {
			return
		}
		record, err := deps.Agents.LoadAgent(c.Request.Context(), tenantID(c), id)
		if errors.Is(err, agent.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "agent_not_found", "Agent not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load Agent", "agent_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "agent_load_failed", "failed to load the Agent")
			return
		}
		c.JSON(http.StatusOK, viewOfAgent(record))
	}
}

func getAgentResult(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := agentID(c)
		if !ok {
			return
		}
		record, err := deps.Agents.LoadAgent(c.Request.Context(), tenantID(c), id)
		if errors.Is(err, agent.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "agent_not_found", "Agent not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load Agent result", "agent_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "agent_load_failed", "failed to load the Agent")
			return
		}
		if record.ResultRef != "" {
			data, err := deps.Payloads.Get(c.Request.Context(), tenantID(c), record.ResultRef)
			if err != nil {
				logger.ErrorContext(c.Request.Context(), "read Agent result", "agent_id", id, "error", err)
				errorJSON(c, http.StatusInternalServerError, "result_unavailable", "failed to read the Agent result")
				return
			}
			var result agent.StructuredResult
			if err := json.Unmarshal(data, &result); err != nil {
				errorJSON(c, http.StatusInternalServerError, "result_corrupt", "Agent result is not valid JSON")
				return
			}
			if result.AgentID != id {
				errorJSON(c, http.StatusInternalServerError, "result_mismatch", "Agent result does not belong to this Agent")
				return
			}
			c.JSON(http.StatusOK, gin.H{"agent_id": id, "status": record.Status, "result_ref": record.ResultRef,
				"summary": result.Summary, "response_ref": result.ResponseRef,
				"artifacts": result.Artifacts, "source_refs": result.SourceRefs})
			return
		}
		if record.ErrorRef != "" {
			data, err := deps.Payloads.Get(c.Request.Context(), tenantID(c), record.ErrorRef)
			if err != nil {
				logger.ErrorContext(c.Request.Context(), "read Agent error", "agent_id", id, "error", err)
				errorJSON(c, http.StatusInternalServerError, "error_unavailable", "failed to read the Agent error")
				return
			}
			c.JSON(http.StatusOK, gin.H{"agent_id": id, "status": record.Status,
				"error_ref": record.ErrorRef, "error": json.RawMessage(data)})
			return
		}
		errorJSON(c, http.StatusConflict, "agent_not_finished", "Agent has not produced a result")
	}
}

func cancelAgent(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := agentID(c)
		if !ok {
			return
		}
		record, err := deps.Agents.LoadAgent(c.Request.Context(), tenantID(c), id)
		if errors.Is(err, agent.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "agent_not_found", "Agent not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load Agent for cancellation", "agent_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "agent_load_failed", "failed to load the Agent")
			return
		}
		if record.Status == "completed" || record.Status == "failed" || record.Status == "cancelled" {
			errorJSON(c, http.StatusConflict, "agent_terminal", "Agent is already terminal")
			return
		}
		err = deps.Workflows.CancelWorkflow(c.Request.Context(), record.WorkflowID, "")
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			errorJSON(c, http.StatusConflict, "agent_workflow_not_started", "Agent Workflow is not running")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "cancel Agent workflow", "agent_id", id, "error", err)
			errorJSON(c, http.StatusBadGateway, "workflow_cancel_failed", "failed to cancel the Agent Workflow")
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"agent_id": id, "workflow_id": record.WorkflowID, "status": "cancelling"})
	}
}

func resumeAgent(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := agentID(c)
		if !ok {
			return
		}
		record, err := deps.Agents.LoadAgent(c.Request.Context(), tenantID(c), id)
		if errors.Is(err, agent.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "agent_not_found", "Agent not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load Agent for resume", "agent_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "agent_load_failed", "failed to load the Agent")
			return
		}
		if record.Status == "failed" || record.Status == "cancelled" {
			errorJSON(c, http.StatusConflict, "agent_terminal", "a terminal Agent cannot be resumed")
			return
		}
		if record.Status == "completed" {
			c.JSON(http.StatusOK, viewOfAgent(record))
			return
		}
		if err := scheduleAgent(c, deps, record); err != nil {
			logger.ErrorContext(c.Request.Context(), "resume Agent workflow", "agent_id", id, "error", err)
			errorJSON(c, http.StatusBadGateway, "workflow_start_failed", "failed to resume the Agent Workflow")
			return
		}
		c.JSON(http.StatusAccepted, viewOfAgent(record))
	}
}

func agentID(c *gin.Context) (string, bool) {
	raw := strings.TrimSpace(c.Param("agent_id"))
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		errorJSON(c, http.StatusBadRequest, "invalid_agent_id", "agent_id must be a nonzero UUID")
		return "", false
	}
	return id.String(), true
}

func validUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil
}

func pageLimit(c *gin.Context, fallback, maximum int) (int, bool) {
	raw := strings.TrimSpace(c.Query("limit"))
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		errorJSON(c, http.StatusBadRequest, "invalid_limit", "limit is out of range")
		return 0, false
	}
	return value, true
}
