package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"sentinel/internal/control"
)

type ControlStore struct{ pool *pgxpool.Pool }

func NewControlStore(pool *pgxpool.Pool) *ControlStore { return &ControlStore{pool: pool} }

var _ control.Store = (*ControlStore)(nil)

const projectColumns = `p.project_id::text,p.tenant_id::text,p.name,p.description,p.status,
	(SELECT count(*) FROM project_repositories pr
		WHERE pr.tenant_id=p.tenant_id AND pr.project_id=p.project_id),
	(SELECT count(*) FROM analyses a
		WHERE a.tenant_id=p.tenant_id AND a.project_id=p.project_id),
	p.created_at,p.updated_at`

func scanProject(row pgx.Row) (control.Project, error) {
	var project control.Project
	var repositories, analyses int64
	err := row.Scan(&project.ID, &project.TenantID, &project.Name, &project.Description,
		&project.Status, &repositories, &analyses, &project.CreatedAt, &project.UpdatedAt)
	project.RepositoryCount, project.AnalysisCount = int(repositories), int(analyses)
	return project, controlStoreError(err)
}

func controlStoreError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return control.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return errors.Join(control.ErrConflict, err)
		case "23503", "23514":
			return errors.Join(control.ErrInvalid, err)
		}
	}
	return err
}

func (s *ControlStore) CreateProject(ctx context.Context, project control.Project) (control.Project, error) {
	if err := control.ValidateProject(project); err != nil {
		return control.Project{}, err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO projects
		(project_id,tenant_id,name,description,status)
		VALUES ($1,$2,$3,$4,$5)`,
		project.ID, project.TenantID, project.Name, project.Description, project.Status)
	if err != nil {
		return control.Project{}, controlStoreError(err)
	}
	return s.LoadProject(ctx, project.TenantID, project.ID)
}

func (s *ControlStore) LoadProject(ctx context.Context, tenantID, projectID string) (control.Project, error) {
	project, err := scanProject(s.pool.QueryRow(ctx, `SELECT `+projectColumns+`
		FROM projects p WHERE p.tenant_id=$1 AND p.project_id=$2`, tenantID, projectID))
	if err != nil {
		return control.Project{}, err
	}
	project.Repositories, err = s.ListProjectRepositories(ctx, tenantID, projectID)
	if err != nil {
		return control.Project{}, err
	}
	return project, nil
}

func (s *ControlStore) ListProjects(ctx context.Context, tenantID string, limit int) ([]control.Project, error) {
	if limit <= 0 || limit > 200 {
		return nil, errors.New("invalid project page limit")
	}
	rows, err := s.pool.Query(ctx, `SELECT `+projectColumns+`
		FROM projects p WHERE p.tenant_id=$1
		ORDER BY p.created_at DESC,p.project_id DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := make([]control.Project, 0, limit)
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (s *ControlStore) UpdateProject(ctx context.Context, project control.Project) (control.Project, error) {
	if err := control.ValidateProject(project); err != nil {
		return control.Project{}, err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE projects
		SET name=$3,description=$4,status=$5,updated_at=now()
		WHERE tenant_id=$1 AND project_id=$2`,
		project.TenantID, project.ID, project.Name, project.Description, project.Status)
	if err != nil {
		return control.Project{}, controlStoreError(err)
	}
	if tag.RowsAffected() == 0 {
		return control.Project{}, control.ErrNotFound
	}
	return s.LoadProject(ctx, project.TenantID, project.ID)
}

func (s *ControlStore) AddProjectRepository(ctx context.Context, tenantID, projectID, repositoryID string, primary bool) (control.ProjectRepository, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return control.ProjectRepository{}, err
	}
	defer tx.Rollback(ctx)

	var projectStatus, repositoryStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM projects
		WHERE tenant_id=$1 AND project_id=$2 FOR UPDATE`, tenantID, projectID).Scan(&projectStatus); err != nil {
		return control.ProjectRepository{}, controlStoreError(err)
	}
	if projectStatus != "active" {
		return control.ProjectRepository{}, control.ErrConflict
	}
	if err := tx.QueryRow(ctx, `SELECT status FROM source_repositories
		WHERE tenant_id=$1 AND repository_id=$2`, tenantID, repositoryID).Scan(&repositoryStatus); err != nil {
		return control.ProjectRepository{}, controlStoreError(err)
	}
	if repositoryStatus != "active" {
		return control.ProjectRepository{}, control.ErrConflict
	}
	if primary {
		if _, err := tx.Exec(ctx, `UPDATE project_repositories SET is_primary=false
			WHERE tenant_id=$1 AND project_id=$2 AND is_primary`, tenantID, projectID); err != nil {
			return control.ProjectRepository{}, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO project_repositories
		(tenant_id,project_id,repository_id,is_primary)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id,project_id,repository_id)
		DO UPDATE SET is_primary=EXCLUDED.is_primary`,
		tenantID, projectID, repositoryID, primary); err != nil {
		return control.ProjectRepository{}, controlStoreError(err)
	}
	binding, err := scanProjectRepository(tx.QueryRow(ctx, `SELECT `+projectRepositoryColumns+`
		FROM project_repositories pr
		JOIN source_repositories r ON r.tenant_id=pr.tenant_id AND r.repository_id=pr.repository_id
		WHERE pr.tenant_id=$1 AND pr.project_id=$2 AND pr.repository_id=$3`,
		tenantID, projectID, repositoryID))
	if err != nil {
		return control.ProjectRepository{}, err
	}
	return binding, tx.Commit(ctx)
}

func (s *ControlStore) RemoveProjectRepository(ctx context.Context, tenantID, projectID, repositoryID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM project_repositories
		WHERE tenant_id=$1 AND project_id=$2 AND repository_id=$3`,
		tenantID, projectID, repositoryID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return control.ErrNotFound
	}
	return nil
}

const projectRepositoryColumns = `pr.repository_id::text,r.remote_url,r.status,r.tracked_refs,
	r.object_format,pr.is_primary,pr.created_at`

func scanProjectRepository(row pgx.Row) (control.ProjectRepository, error) {
	var binding control.ProjectRepository
	err := row.Scan(&binding.RepositoryID, &binding.RemoteURL, &binding.RepositoryStatus,
		&binding.TrackedRefs, &binding.ObjectFormat, &binding.IsPrimary, &binding.BoundAt)
	return binding, controlStoreError(err)
}

func (s *ControlStore) ListProjectRepositories(ctx context.Context, tenantID, projectID string) ([]control.ProjectRepository, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+projectRepositoryColumns+`
		FROM project_repositories pr
		JOIN source_repositories r ON r.tenant_id=pr.tenant_id AND r.repository_id=pr.repository_id
		WHERE pr.tenant_id=$1 AND pr.project_id=$2
		ORDER BY pr.is_primary DESC,pr.created_at,pr.repository_id`, tenantID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := make([]control.ProjectRepository, 0)
	for rows.Next() {
		binding, err := scanProjectRepository(rows)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

func (s *ControlStore) CreateAnalysis(ctx context.Context, analysis control.Analysis) (control.Analysis, error) {
	if err := control.ValidateAnalysis(analysis); err != nil {
		return control.Analysis{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return control.Analysis{}, err
	}
	defer tx.Rollback(ctx)

	target := analysis.Target
	_, err = tx.Exec(ctx, `INSERT INTO analysis_targets
		(target_id,tenant_id,repository_id,kind,ref_name,commit_sha,base_commit_sha,
		 pull_request_number,source_snapshot_id,base_snapshot_id,status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,
		 NULLIF($9,'')::uuid,NULLIF($10,'')::uuid,$11)`,
		target.ID, analysis.TenantID, analysis.RepositoryID, target.Kind,
		nullable(target.RefName), nullable(target.CommitSHA), nullable(target.BaseCommitSHA),
		nullableInt(target.PullRequestNumber), target.SourceSnapshotID, target.BaseSnapshotID,
		target.Status)
	if err != nil {
		return control.Analysis{}, controlStoreError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO analyses
		(analysis_id,tenant_id,project_id,repository_id,target_id,session_id,title,trigger,external_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''))`,
		analysis.ID, analysis.TenantID, analysis.ProjectID, analysis.RepositoryID,
		target.ID, analysis.SessionID, analysis.Title, analysis.Trigger, analysis.ExternalID)
	if err != nil {
		return control.Analysis{}, controlStoreError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return control.Analysis{}, err
	}
	return s.LoadAnalysis(ctx, analysis.TenantID, analysis.ID)
}

const analysisColumns = `a.analysis_id::text,a.tenant_id::text,a.project_id::text,a.repository_id::text,
	a.session_id::text,a.title,a.trigger,COALESCE(a.external_id,''),
	COALESCE(root.agent_id::text,''),COALESCE(root.workflow_id,''),
	COALESCE(root.status,s.status,'pending'),a.created_at,
	t.target_id::text,t.kind,COALESCE(t.ref_name,''),COALESCE(t.commit_sha,''),
	COALESCE(t.base_commit_sha,''),COALESCE(t.pull_request_number,0),
	COALESCE(t.source_snapshot_id::text,''),COALESCE(t.base_snapshot_id::text,''),t.status`

const analysisFrom = ` FROM analyses a
	JOIN analysis_targets t ON t.tenant_id=a.tenant_id AND t.target_id=a.target_id
	LEFT JOIN agents root ON root.tenant_id=a.tenant_id AND root.session_id=a.session_id
		AND root.parent_agent_id IS NULL
	LEFT JOIN agent_sessions s ON s.tenant_id=a.tenant_id AND s.session_id=a.session_id`

func scanAnalysis(row pgx.Row) (control.Analysis, error) {
	var analysis control.Analysis
	var target control.Target
	var pullRequestNumber int
	err := row.Scan(&analysis.ID, &analysis.TenantID, &analysis.ProjectID, &analysis.RepositoryID,
		&analysis.SessionID, &analysis.Title, &analysis.Trigger, &analysis.ExternalID,
		&analysis.AgentID, &analysis.WorkflowID, &analysis.Status, &analysis.CreatedAt,
		&target.ID, &target.Kind, &target.RefName, &target.CommitSHA, &target.BaseCommitSHA,
		&pullRequestNumber, &target.SourceSnapshotID, &target.BaseSnapshotID, &target.Status)
	target.PullRequestNumber = pullRequestNumber
	analysis.Target = &target
	return analysis, controlStoreError(err)
}

func (s *ControlStore) LoadAnalysis(ctx context.Context, tenantID, analysisID string) (control.Analysis, error) {
	return scanAnalysis(s.pool.QueryRow(ctx, `SELECT `+analysisColumns+analysisFrom+`
		WHERE a.tenant_id=$1 AND a.analysis_id=$2`, tenantID, analysisID))
}

func (s *ControlStore) ListProjectAnalyses(ctx context.Context, tenantID, projectID string, limit int) ([]control.Analysis, error) {
	if limit <= 0 || limit > 200 {
		return nil, errors.New("invalid analysis page limit")
	}
	rows, err := s.pool.Query(ctx, `SELECT `+analysisColumns+analysisFrom+`
		WHERE a.tenant_id=$1 AND a.project_id=$2
		ORDER BY a.created_at DESC,a.analysis_id DESC LIMIT $3`, tenantID, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	analyses := make([]control.Analysis, 0, limit)
	for rows.Next() {
		analysis, err := scanAnalysis(rows)
		if err != nil {
			return nil, err
		}
		analyses = append(analyses, analysis)
	}
	return analyses, rows.Err()
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
