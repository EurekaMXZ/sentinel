// Package control defines the control-plane grouping of source repositories
// and Agent analyses. Agent execution itself remains resource-based and does
// not depend on projects.
package control

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound = errors.New("control resource not found")
	ErrConflict = errors.New("control resource conflict")
	ErrInvalid  = errors.New("invalid control resource")
)

type Project struct {
	ID              string              `json:"project_id"`
	TenantID        string              `json:"tenant_id"`
	Name            string              `json:"name"`
	Description     string              `json:"description"`
	Status          string              `json:"status"`
	RepositoryCount int                 `json:"repository_count"`
	AnalysisCount   int                 `json:"analysis_count"`
	Repositories    []ProjectRepository `json:"repositories,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
}

type ProjectRepository struct {
	RepositoryID     string    `json:"repository_id"`
	RemoteURL        string    `json:"remote_url"`
	RepositoryStatus string    `json:"repository_status"`
	TrackedRefs      []string  `json:"tracked_refs"`
	ObjectFormat     string    `json:"object_format"`
	IsPrimary        bool      `json:"is_primary"`
	BoundAt          time.Time `json:"bound_at"`
}

// Target pins the immutable source version an analysis was created for. The
// first milestone creates snapshot targets; commit/ref/PR resolution extends
// the same record without changing the Agent runtime.
type Target struct {
	ID                string `json:"target_id"`
	Kind              string `json:"kind"`
	RefName           string `json:"ref_name,omitempty"`
	CommitSHA         string `json:"commit_sha,omitempty"`
	BaseCommitSHA     string `json:"base_commit_sha,omitempty"`
	PullRequestNumber int    `json:"pull_request_number,omitempty"`
	SourceSnapshotID  string `json:"source_snapshot_id,omitempty"`
	BaseSnapshotID    string `json:"base_snapshot_id,omitempty"`
	Status            string `json:"status"`
}

type Analysis struct {
	ID           string    `json:"analysis_id"`
	TenantID     string    `json:"tenant_id"`
	ProjectID    string    `json:"project_id"`
	RepositoryID string    `json:"repository_id"`
	SessionID    string    `json:"session_id"`
	Title        string    `json:"title"`
	Trigger      string    `json:"trigger"`
	ExternalID   string    `json:"external_id,omitempty"`
	Status       string    `json:"status"`
	AgentID      string    `json:"agent_id,omitempty"`
	WorkflowID   string    `json:"workflow_id,omitempty"`
	Target       *Target   `json:"target,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Store interface {
	CreateProject(context.Context, Project) (Project, error)
	ListProjects(context.Context, string, int) ([]Project, error)
	LoadProject(context.Context, string, string) (Project, error)
	UpdateProject(context.Context, Project) (Project, error)
	AddProjectRepository(context.Context, string, string, string, bool) (ProjectRepository, error)
	RemoveProjectRepository(context.Context, string, string, string) error
	ListProjectRepositories(context.Context, string, string) ([]ProjectRepository, error)

	CreateAnalysis(context.Context, Analysis) (Analysis, error)
	ListProjectAnalyses(context.Context, string, string, int) ([]Analysis, error)
	LoadAnalysis(context.Context, string, string) (Analysis, error)
}

func ValidateProject(project Project) error {
	if !validID(project.ID) || !validID(project.TenantID) {
		return invalid("project identity must use nonzero UUIDs")
	}
	name := strings.TrimSpace(project.Name)
	if name == "" || len(name) > 200 {
		return invalid("project name is required and limited to 200 bytes")
	}
	if len(project.Description) > 4000 {
		return invalid("project description is limited to 4000 bytes")
	}
	if project.Status != "active" && project.Status != "archived" {
		return invalid("project status must be active or archived")
	}
	return nil
}

func ValidateTarget(target Target) error {
	if !validID(target.ID) {
		return invalid("target_id must be a nonzero UUID")
	}
	switch target.Kind {
	case "snapshot":
		if !validID(target.SourceSnapshotID) || strings.TrimSpace(target.CommitSHA) == "" {
			return invalid("snapshot targets require source_snapshot_id and commit_sha")
		}
	case "commit":
		if strings.TrimSpace(target.CommitSHA) == "" {
			return invalid("commit targets require commit_sha")
		}
	case "ref":
		if strings.TrimSpace(target.RefName) == "" {
			return invalid("ref targets require ref_name")
		}
	case "pull_request":
		if target.PullRequestNumber <= 0 {
			return invalid("pull_request targets require a positive pull_request_number")
		}
	default:
		return invalid("unsupported analysis target kind")
	}
	if target.Status != "pending" && target.Status != "resolving" &&
		target.Status != "ready" && target.Status != "failed" {
		return invalid("unsupported analysis target status")
	}
	if target.Status == "ready" && !validID(target.SourceSnapshotID) {
		return invalid("ready targets require a source snapshot")
	}
	return nil
}

func ValidateAnalysis(analysis Analysis) error {
	if !validID(analysis.ID) || !validID(analysis.TenantID) || !validID(analysis.ProjectID) ||
		!validID(analysis.RepositoryID) || !validID(analysis.SessionID) {
		return invalid("analysis identity must use nonzero UUIDs")
	}
	if analysis.Target == nil {
		return invalid("analysis target is required")
	}
	if err := ValidateTarget(*analysis.Target); err != nil {
		return err
	}
	if strings.TrimSpace(analysis.Title) == "" || len(analysis.Title) > 300 {
		return invalid("analysis title is required and limited to 300 bytes")
	}
	if analysis.Trigger != "manual" && analysis.Trigger != "github_actions" && analysis.Trigger != "api" {
		return invalid("unsupported analysis trigger")
	}
	return nil
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

func invalid(message string) error {
	return &ValidationError{Message: message}
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil
}
