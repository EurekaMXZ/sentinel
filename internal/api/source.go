package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	enums "go.temporal.io/api/enums/v1"
	sdkclient "go.temporal.io/sdk/client"

	"sentinel/internal/source"
)

type createRepositoryRequest struct {
	URL           string   `json:"url"`
	TrackedRefs   []string `json:"tracked_refs"`
	CredentialRef string   `json:"credential_ref"`
	ObjectFormat  string   `json:"object_format"`
}

type syncView struct {
	TenantID      string            `json:"tenant_id"`
	RepositoryID  string            `json:"repository_id"`
	SyncID        string            `json:"sync_id"`
	WorkflowID    string            `json:"workflow_id"`
	Status        string            `json:"status"`
	ResultRef     string            `json:"result_ref,omitempty"`
	ErrorRef      string            `json:"error_ref,omitempty"`
	ManifestID    string            `json:"manifest_id,omitempty"`
	SnapshotCount int               `json:"snapshot_count,omitempty"`
	Snapshots     []source.Snapshot `json:"snapshots,omitempty"`
	Error         json.RawMessage   `json:"error,omitempty"`
}

func createRepository(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request createRepositoryRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			errorJSON(c, http.StatusBadRequest, "invalid_request", "request body must be valid JSON")
			return
		}
		remote, err := source.NormalizeGitHubURL(request.URL)
		if err != nil {
			errorJSON(c, http.StatusBadRequest, "invalid_repository_url", err.Error())
			return
		}
		refs := request.TrackedRefs
		if len(refs) == 0 {
			refs = []string{"refs/heads/*", "refs/tags/*"}
		}
		format := request.ObjectFormat
		if format == "" {
			format = "sha1"
		}
		id, err := uuid.NewV7()
		if err != nil {
			errorJSON(c, http.StatusInternalServerError, "repository_create_failed", "failed to allocate a repository ID")
			return
		}
		repository := source.Repository{ID: id.String(), TenantID: tenantID(c), RemoteURL: remote,
			CredentialRef: request.CredentialRef, TrackedRefs: source.SortedRefs(refs),
			ObjectFormat: format, Status: "active"}
		if err := source.ValidateRepository(repository); err != nil {
			errorJSON(c, http.StatusBadRequest, "invalid_repository", err.Error())
			return
		}
		repository, err = deps.Sources.CreateRepository(c.Request.Context(), repository)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "create source repository", "error", err)
			errorJSON(c, http.StatusInternalServerError, "repository_create_failed", "failed to persist the repository")
			return
		}
		c.JSON(http.StatusCreated, repository)
	}
}

func listRepositories(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, ok := pageLimit(c, 50, 200)
		if !ok {
			return
		}
		repositories, err := deps.Sources.ListRepositories(c.Request.Context(), tenantID(c), limit)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "list source repositories", "error", err)
			errorJSON(c, http.StatusInternalServerError, "repository_list_failed", "failed to list repositories")
			return
		}
		c.JSON(http.StatusOK, gin.H{"repositories": repositories})
	}
}

func getRepository(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := repositoryID(c)
		if !ok {
			return
		}
		repository, err := deps.Sources.LoadRepository(c.Request.Context(), tenantID(c), id)
		if errors.Is(err, source.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "repository_not_found", "repository not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load source repository", "repository_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "repository_load_failed", "failed to load the repository")
			return
		}
		c.JSON(http.StatusOK, repository)
	}
}

func listVersions(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := repositoryID(c)
		if !ok {
			return
		}
		if _, err := deps.Sources.LoadRepository(c.Request.Context(), tenantID(c), id); errors.Is(err, source.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "repository_not_found", "repository not found")
			return
		} else if err != nil {
			logger.ErrorContext(c.Request.Context(), "load source repository", "repository_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "repository_load_failed", "failed to load the repository")
			return
		}
		offset, ok := offsetParam(c)
		if !ok {
			return
		}
		limit, ok := pageLimit(c, 50, 200)
		if !ok {
			return
		}
		versions, more, err := deps.Sources.ListReadySnapshots(c.Request.Context(), tenantID(c), id, offset, limit)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "list source versions", "repository_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "version_list_failed", "failed to list ready versions")
			return
		}
		response := gin.H{"repository_id": id, "versions": versions, "more": more, "offset": offset, "limit": limit}
		if more {
			response["next_offset"] = offset + len(versions)
		}
		c.JSON(http.StatusOK, response)
	}
}

func createSync(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := repositoryID(c)
		if !ok {
			return
		}
		repository, err := deps.Sources.LoadRepository(c.Request.Context(), tenantID(c), id)
		if errors.Is(err, source.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "repository_not_found", "repository not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load source repository", "repository_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "repository_load_failed", "failed to load the repository")
			return
		}
		job, err := deps.SourceTasks.Create(c.Request.Context(), repository)
		if errors.Is(err, source.ErrTerminal) {
			errorJSON(c, http.StatusConflict, "repository_not_active", "repository is not active")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "create source sync", "repository_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "sync_create_failed", "failed to create the synchronization job")
			return
		}
		if err := scheduleSync(c, deps, job); err != nil {
			logger.ErrorContext(c.Request.Context(), "schedule source workflow", "sync_id", job.Request.SyncID, "error", err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "sync was persisted but its Workflow was not started; resume with the sync ID",
				"code": "workflow_start_failed", "sync_id": job.Request.SyncID})
			return
		}
		c.JSON(http.StatusAccepted, viewOfSync(job))
	}
}

func getSync(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		syncID, ok := syncIDParam(c)
		if !ok {
			return
		}
		job, err := deps.Sources.LoadSync(c.Request.Context(), source.SyncRequest{TenantID: tenantID(c), SyncID: syncID})
		if errors.Is(err, source.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "sync_not_found", "sync not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load source sync", "sync_id", syncID, "error", err)
			errorJSON(c, http.StatusInternalServerError, "sync_load_failed", "failed to load the synchronization job")
			return
		}
		view, ok := syncViewWithPayload(c, deps, logger, job)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, view)
	}
}

func syncViewWithPayload(c *gin.Context, deps Dependencies, logger *slog.Logger, job source.SyncJob) (syncView, bool) {
	view := viewOfSync(job)
	if job.ResultRef != "" {
		data, err := deps.Payloads.Get(c.Request.Context(), tenantID(c), job.ResultRef)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "read source sync result", "sync_id", job.Request.SyncID, "error", err)
			errorJSON(c, http.StatusInternalServerError, "sync_result_unavailable", "failed to read the synchronization result")
			return view, false
		}
		var candidate source.Candidate
		if err := json.Unmarshal(data, &candidate); err != nil {
			errorJSON(c, http.StatusInternalServerError, "sync_result_corrupt", "synchronization result is not valid JSON")
			return view, false
		}
		view.ManifestID = candidate.Manifest.ID
		view.SnapshotCount = len(candidate.Snapshots)
		view.Snapshots = candidate.Snapshots
	}
	if job.ErrorRef != "" {
		data, err := deps.Payloads.Get(c.Request.Context(), tenantID(c), job.ErrorRef)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "read source sync error", "sync_id", job.Request.SyncID, "error", err)
		} else {
			view.Error = json.RawMessage(data)
		}
	}
	return view, true
}

func resumeSync(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		syncID, ok := syncIDParam(c)
		if !ok {
			return
		}
		job, err := deps.Sources.LoadSync(c.Request.Context(), source.SyncRequest{TenantID: tenantID(c), SyncID: syncID})
		if errors.Is(err, source.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "sync_not_found", "sync not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load source sync for resume", "sync_id", syncID, "error", err)
			errorJSON(c, http.StatusInternalServerError, "sync_load_failed", "failed to load the synchronization job")
			return
		}
		if job.Status == "failed" || job.Status == "cancelled" {
			errorJSON(c, http.StatusConflict, "sync_terminal", "a terminal sync cannot be resumed; start a new sync")
			return
		}
		if job.Status == "succeeded" {
			view, ok := syncViewWithPayload(c, deps, logger, job)
			if !ok {
				return
			}
			c.JSON(http.StatusOK, view)
			return
		}
		if err := scheduleSync(c, deps, job); err != nil {
			logger.ErrorContext(c.Request.Context(), "resume source workflow", "sync_id", syncID, "error", err)
			errorJSON(c, http.StatusBadGateway, "workflow_start_failed", "failed to resume the synchronization Workflow")
			return
		}
		c.JSON(http.StatusAccepted, viewOfSync(job))
	}
}

func scheduleSync(c *gin.Context, deps Dependencies, job source.SyncJob) error {
	_, err := deps.Workflows.ExecuteWorkflow(c.Request.Context(), sdkclient.StartWorkflowOptions{
		ID: job.WorkflowID, TaskQueue: deps.SourceTaskQueue,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, source.WorkflowName, source.WorkflowInput{Request: job.Request})
	return err
}

func viewOfSync(job source.SyncJob) syncView {
	return syncView{TenantID: job.Request.TenantID, RepositoryID: job.Request.RepositoryID,
		SyncID: job.Request.SyncID, WorkflowID: job.WorkflowID, Status: job.Status,
		ResultRef: job.ResultRef, ErrorRef: job.ErrorRef}
}

func repositoryID(c *gin.Context) (string, bool) {
	raw := strings.TrimSpace(c.Param("repository_id"))
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		errorJSON(c, http.StatusBadRequest, "invalid_repository_id", "repository_id must be a nonzero UUID")
		return "", false
	}
	return id.String(), true
}

func syncIDParam(c *gin.Context) (string, bool) {
	raw := strings.TrimSpace(c.Param("sync_id"))
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		errorJSON(c, http.StatusBadRequest, "invalid_sync_id", "sync_id must be a nonzero UUID")
		return "", false
	}
	return id.String(), true
}

func offsetParam(c *gin.Context) (int, bool) {
	raw := strings.TrimSpace(c.Query("offset"))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		errorJSON(c, http.StatusBadRequest, "invalid_offset", "offset must be a non-negative integer")
		return 0, false
	}
	return value, true
}
