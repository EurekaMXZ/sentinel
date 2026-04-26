package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"sentinel/internal/agent"
	"sentinel/internal/control"
	"sentinel/internal/source"
)

type createProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type updateProjectRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
}

type addProjectRepositoryRequest struct {
	RepositoryID string `json:"repository_id"`
	IsPrimary    bool   `json:"is_primary"`
}

type createAnalysisRequest struct {
	RepositoryID   string `json:"repository_id"`
	SnapshotID     string `json:"snapshot_id"`
	Title          string `json:"title"`
	Task           string `json:"task"`
	ExpectedOutput string `json:"expected_output"`
	MaxSteps       *int64 `json:"max_steps"`
	MaxChildDepth  *int   `json:"max_child_depth"`
}

func createProject(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		store, ok := controlStore(deps, c)
		if !ok {
			return
		}
		var request createProjectRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			errorJSON(c, http.StatusBadRequest, "invalid_request", "request body must be valid JSON")
			return
		}
		id, err := uuid.NewV7()
		if err != nil {
			errorJSON(c, http.StatusInternalServerError, "project_create_failed", "failed to allocate a project ID")
			return
		}
		project := control.Project{ID: id.String(), TenantID: tenantID(c),
			Name: strings.TrimSpace(request.Name), Description: request.Description, Status: "active"}
		project, err = store.CreateProject(c.Request.Context(), project)
		if err != nil {
			writeControlError(c, logger, err, "create project")
			return
		}
		c.JSON(http.StatusCreated, project)
	}
}

func listProjects(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		store, ok := controlStore(deps, c)
		if !ok {
			return
		}
		limit, ok := pageLimit(c, 50, 200)
		if !ok {
			return
		}
		projects, err := store.ListProjects(c.Request.Context(), tenantID(c), limit)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "list projects", "error", err)
			errorJSON(c, http.StatusInternalServerError, "project_list_failed", "failed to list projects")
			return
		}
		c.JSON(http.StatusOK, gin.H{"projects": projects})
	}
}

func getProject(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		store, ok := controlStore(deps, c)
		if !ok {
			return
		}
		id, ok := projectID(c)
		if !ok {
			return
		}
		project, err := store.LoadProject(c.Request.Context(), tenantID(c), id)
		if errors.Is(err, control.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load project", "project_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "project_load_failed", "failed to load the project")
			return
		}
		c.JSON(http.StatusOK, project)
	}
}

func updateProject(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		store, ok := controlStore(deps, c)
		if !ok {
			return
		}
		id, ok := projectID(c)
		if !ok {
			return
		}
		project, err := store.LoadProject(c.Request.Context(), tenantID(c), id)
		if errors.Is(err, control.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load project for update", "project_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "project_load_failed", "failed to load the project")
			return
		}
		var request updateProjectRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			errorJSON(c, http.StatusBadRequest, "invalid_request", "request body must be valid JSON")
			return
		}
		if request.Name != nil {
			project.Name = strings.TrimSpace(*request.Name)
		}
		if request.Description != nil {
			project.Description = *request.Description
		}
		if request.Status != nil {
			project.Status = strings.TrimSpace(*request.Status)
		}
		project, err = store.UpdateProject(c.Request.Context(), project)
		if err != nil {
			writeControlError(c, logger, err, "update project")
			return
		}
		c.JSON(http.StatusOK, project)
	}
}

func addProjectRepository(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		store, ok := controlStore(deps, c)
		if !ok {
			return
		}
		projectIDValue, ok := projectID(c)
		if !ok {
			return
		}
		var request addProjectRepositoryRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			errorJSON(c, http.StatusBadRequest, "invalid_request", "request body must be valid JSON")
			return
		}
		if !source.ValidID(request.RepositoryID) {
			errorJSON(c, http.StatusBadRequest, "invalid_repository_id", "repository_id must be a nonzero UUID")
			return
		}
		binding, err := store.AddProjectRepository(c.Request.Context(), tenantID(c), projectIDValue,
			request.RepositoryID, request.IsPrimary)
		if err != nil {
			writeControlError(c, logger, err, "bind project repository")
			return
		}
		c.JSON(http.StatusCreated, binding)
	}
}

func removeProjectRepository(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		store, ok := controlStore(deps, c)
		if !ok {
			return
		}
		projectIDValue, ok := projectID(c)
		if !ok {
			return
		}
		repositoryIDValue, ok := repositoryID(c)
		if !ok {
			return
		}
		err := store.RemoveProjectRepository(c.Request.Context(), tenantID(c), projectIDValue, repositoryIDValue)
		if errors.Is(err, control.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "project_repository_not_found", "repository is not bound to this project")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "unbind project repository", "project_id", projectIDValue,
				"repository_id", repositoryIDValue, "error", err)
			errorJSON(c, http.StatusInternalServerError, "project_repository_remove_failed", "failed to unbind the repository")
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func listProjectAnalyses(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		store, ok := controlStore(deps, c)
		if !ok {
			return
		}
		projectIDValue, ok := projectID(c)
		if !ok {
			return
		}
		if _, err := store.LoadProject(c.Request.Context(), tenantID(c), projectIDValue); errors.Is(err, control.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "project_not_found", "project not found")
			return
		} else if err != nil {
			logger.ErrorContext(c.Request.Context(), "load project for analyses", "project_id", projectIDValue, "error", err)
			errorJSON(c, http.StatusInternalServerError, "project_load_failed", "failed to load the project")
			return
		}
		limit, ok := pageLimit(c, 50, 200)
		if !ok {
			return
		}
		analyses, err := store.ListProjectAnalyses(c.Request.Context(), tenantID(c), projectIDValue, limit)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "list project analyses", "project_id", projectIDValue, "error", err)
			errorJSON(c, http.StatusInternalServerError, "analysis_list_failed", "failed to list analyses")
			return
		}
		c.JSON(http.StatusOK, gin.H{"analyses": analyses})
	}
}

func createAnalysis(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		store, ok := controlStore(deps, c)
		if !ok {
			return
		}
		if deps.SourceAdmin == nil {
			errorJSON(c, http.StatusServiceUnavailable, "analysis_disabled", "source target lookup is not configured")
			return
		}
		projectIDValue, ok := projectID(c)
		if !ok {
			return
		}
		project, err := store.LoadProject(c.Request.Context(), tenantID(c), projectIDValue)
		if errors.Is(err, control.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load project for analysis", "project_id", projectIDValue, "error", err)
			errorJSON(c, http.StatusInternalServerError, "project_load_failed", "failed to load the project")
			return
		}
		if project.Status != "active" {
			errorJSON(c, http.StatusConflict, "project_archived", "cannot create an analysis in an archived project")
			return
		}
		var request createAnalysisRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			errorJSON(c, http.StatusBadRequest, "invalid_request", "request body must be valid JSON")
			return
		}
		if !source.ValidID(request.RepositoryID) {
			errorJSON(c, http.StatusBadRequest, "invalid_repository_id", "repository_id must be a nonzero UUID")
			return
		}
		bound := false
		for _, repository := range project.Repositories {
			if repository.RepositoryID == request.RepositoryID {
				bound = true
				break
			}
		}
		if !bound {
			errorJSON(c, http.StatusBadRequest, "repository_not_bound", "repository is not bound to this project")
			return
		}
		if strings.TrimSpace(request.Task) == "" || len(request.Task) > 128<<10 {
			errorJSON(c, http.StatusBadRequest, "invalid_task", "task is required and limited to 128 KiB")
			return
		}
		if len(request.ExpectedOutput) > 32<<10 {
			errorJSON(c, http.StatusBadRequest, "invalid_expected_output", "expected_output is limited to 32 KiB")
			return
		}
		repository, err := deps.Sources.LoadRepository(c.Request.Context(), tenantID(c), request.RepositoryID)
		if errors.Is(err, source.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "repository_not_found", "repository not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load repository for analysis", "repository_id", request.RepositoryID, "error", err)
			errorJSON(c, http.StatusInternalServerError, "repository_load_failed", "failed to load the repository")
			return
		}
		if repository.Status != "active" {
			errorJSON(c, http.StatusConflict, "repository_not_active", "repository is not active")
			return
		}
		snapshot, ok := selectAnalysisSnapshot(c, deps, request.SnapshotID, request.RepositoryID)
		if !ok {
			return
		}
		title := strings.TrimSpace(request.Title)
		if title == "" {
			title = defaultAnalysisTitle(repository.RemoteURL, snapshot.CommitSHA)
		}
		if len(title) > 300 {
			errorJSON(c, http.StatusBadRequest, "invalid_title", "analysis title is limited to 300 bytes")
			return
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
		task := agent.NewRootTask(request.Task, "", deps.Model, limits, agent.DefaultTools(), deps.SourceTaskQueue)
		task.ExpectedOutput = request.ExpectedOutput
		resources := []agent.ResourceBinding{{BindingName: "source", Kind: agent.ResourceSourceSnapshot,
			RepositoryID: request.RepositoryID, SourceSnapshotID: snapshot.ID}}
		if err := deps.AgentResources.ValidateResources(c.Request.Context(), tenantID(c), resources); err != nil {
			errorJSON(c, http.StatusBadRequest, "resource_unavailable", err.Error())
			return
		}
		root, err := deps.AgentTasks.CreateRoot(c.Request.Context(), tenantID(c), task, resources)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "create analysis Agent", "project_id", projectIDValue, "error", err)
			errorJSON(c, http.StatusInternalServerError, "agent_create_failed", "failed to persist the analysis Agent")
			return
		}
		analysisID, err := uuid.NewV7()
		if err != nil {
			errorJSON(c, http.StatusInternalServerError, "analysis_create_failed", "failed to allocate an analysis ID")
			return
		}
		targetID, err := uuid.NewV7()
		if err != nil {
			errorJSON(c, http.StatusInternalServerError, "analysis_create_failed", "failed to allocate a target ID")
			return
		}
		target := &control.Target{ID: targetID.String(), Kind: "snapshot", CommitSHA: snapshot.CommitSHA,
			SourceSnapshotID: snapshot.ID, Status: "ready"}
		analysis := control.Analysis{ID: analysisID.String(), TenantID: tenantID(c), ProjectID: projectIDValue,
			RepositoryID: request.RepositoryID, SessionID: root.Input.SessionID, Title: title,
			Trigger: "manual", Status: root.Status, AgentID: root.Input.AgentID, WorkflowID: root.WorkflowID,
			Target: target}
		analysis, err = store.CreateAnalysis(c.Request.Context(), analysis)
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "create analysis", "project_id", projectIDValue, "error", err)
			errorJSON(c, http.StatusInternalServerError, "analysis_create_failed", "failed to persist the analysis")
			return
		}
		if err := scheduleAgent(c, deps, root); err != nil {
			logger.ErrorContext(c.Request.Context(), "schedule analysis Agent", "project_id", projectIDValue,
				"analysis_id", analysis.ID, "error", err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "analysis was persisted but its Agent Workflow was not started; resume with the agent ID",
				"code": "workflow_start_failed", "analysis_id": analysis.ID, "agent_id": root.Input.AgentID,
				"project_id": projectIDValue})
			return
		}
		c.JSON(http.StatusAccepted, analysis)
	}
}

func getAnalysis(deps Dependencies, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		store, ok := controlStore(deps, c)
		if !ok {
			return
		}
		id, ok := analysisID(c)
		if !ok {
			return
		}
		analysis, err := store.LoadAnalysis(c.Request.Context(), tenantID(c), id)
		if errors.Is(err, control.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "analysis_not_found", "analysis not found")
			return
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "load analysis", "analysis_id", id, "error", err)
			errorJSON(c, http.StatusInternalServerError, "analysis_load_failed", "failed to load the analysis")
			return
		}
		c.JSON(http.StatusOK, analysis)
	}
}

func selectAnalysisSnapshot(c *gin.Context, deps Dependencies, snapshotID, repositoryID string) (source.Snapshot, bool) {
	if snapshotID != "" {
		if !source.ValidID(snapshotID) {
			errorJSON(c, http.StatusBadRequest, "invalid_snapshot_id", "snapshot_id must be a nonzero UUID")
			return source.Snapshot{}, false
		}
		snapshot, err := deps.SourceAdmin.LoadSnapshot(c.Request.Context(), tenantID(c), repositoryID, snapshotID)
		if errors.Is(err, source.ErrNotFound) {
			errorJSON(c, http.StatusNotFound, "snapshot_not_found", "source snapshot not found")
			return source.Snapshot{}, false
		}
		if err != nil {
			errorJSON(c, http.StatusInternalServerError, "snapshot_load_failed", "failed to load the source snapshot")
			return source.Snapshot{}, false
		}
		if snapshot.Status != source.SnapshotReady {
			errorJSON(c, http.StatusConflict, "snapshot_not_ready", "source snapshot is not ready")
			return source.Snapshot{}, false
		}
		return snapshot, true
	}
	snapshots, _, err := deps.Sources.ListReadySnapshots(c.Request.Context(), tenantID(c), repositoryID, 0, 1)
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, "snapshot_list_failed", "failed to list source snapshots")
		return source.Snapshot{}, false
	}
	if len(snapshots) == 0 {
		errorJSON(c, http.StatusConflict, "no_ready_snapshot", "repository has no ready source snapshot; run a sync first")
		return source.Snapshot{}, false
	}
	return snapshots[0], true
}

func defaultAnalysisTitle(remoteURL, commitSHA string) string {
	label := repositoryName(remoteURL)
	short := commitSHA
	if len(short) > 12 {
		short = short[:12]
	}
	if label == "" {
		return "analysis @ " + short
	}
	return label + " @ " + short
}

func repositoryName(remoteURL string) string {
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + strings.TrimSuffix(parts[len(parts)-1], ".git")
}

func controlStore(deps Dependencies, c *gin.Context) (control.Store, bool) {
	if deps.Control == nil {
		errorJSON(c, http.StatusServiceUnavailable, "control_disabled", "project and analysis management are not configured")
		return nil, false
	}
	return deps.Control, true
}

func writeControlError(c *gin.Context, logger *slog.Logger, err error, operation string) {
	switch {
	case errors.Is(err, control.ErrNotFound):
		errorJSON(c, http.StatusNotFound, "project_not_found", "project or repository not found")
	case errors.Is(err, control.ErrConflict):
		errorJSON(c, http.StatusConflict, "project_conflict", "project or repository is not in a usable state, or the name already exists")
	case errors.Is(err, control.ErrInvalid):
		errorJSON(c, http.StatusBadRequest, "invalid_project", err.Error())
	default:
		logger.ErrorContext(c.Request.Context(), operation, "error", err)
		errorJSON(c, http.StatusInternalServerError, "control_operation_failed", "failed to complete the operation")
	}
}

func projectID(c *gin.Context) (string, bool) {
	raw := strings.TrimSpace(c.Param("project_id"))
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		errorJSON(c, http.StatusBadRequest, "invalid_project_id", "project_id must be a nonzero UUID")
		return "", false
	}
	return id.String(), true
}

func analysisID(c *gin.Context) (string, bool) {
	raw := strings.TrimSpace(c.Param("analysis_id"))
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		errorJSON(c, http.StatusBadRequest, "invalid_analysis_id", "analysis_id must be a nonzero UUID")
		return "", false
	}
	return id.String(), true
}
