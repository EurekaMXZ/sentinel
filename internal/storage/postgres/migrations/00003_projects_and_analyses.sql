-- +goose Up
-- Repositories stay tenant-scoped source resources. Projects bind those
-- resources and group analyses; analyses are the control-plane link between a
-- project, an immutable source target and an Agent session.

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM source_repositories
        GROUP BY tenant_id, remote_url
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'duplicate source repository URLs must be merged before applying migration 3';
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX source_repositories_tenant_url_idx
    ON source_repositories (tenant_id, remote_url);

CREATE TABLE projects (
    project_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    name text NOT NULL CHECK (btrim(name) <> '' AND length(name) <= 200),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 4000),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, project_id),
    UNIQUE (tenant_id, name)
);

CREATE INDEX projects_tenant_created_idx
    ON projects (tenant_id, created_at DESC, project_id DESC);

CREATE TABLE project_repositories (
    tenant_id uuid NOT NULL,
    project_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    is_primary boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, project_id, repository_id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, project_id),
    FOREIGN KEY (tenant_id, repository_id) REFERENCES source_repositories (tenant_id, repository_id)
);

CREATE UNIQUE INDEX project_repositories_one_primary_idx
    ON project_repositories (tenant_id, project_id) WHERE is_primary;
CREATE INDEX project_repositories_repository_idx
    ON project_repositories (tenant_id, repository_id, project_id);

CREATE TABLE analysis_targets (
    target_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('snapshot', 'commit', 'ref', 'pull_request')),
    ref_name text,
    commit_sha text CHECK (commit_sha IS NULL OR commit_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    base_commit_sha text CHECK (base_commit_sha IS NULL OR base_commit_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    pull_request_number integer CHECK (pull_request_number IS NULL OR pull_request_number > 0),
    source_snapshot_id uuid,
    base_snapshot_id uuid,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'resolving', 'ready', 'failed')),
    error_object_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, target_id),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES source_repositories (tenant_id, repository_id),
    FOREIGN KEY (tenant_id, repository_id, source_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    FOREIGN KEY (tenant_id, repository_id, base_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    FOREIGN KEY (tenant_id, error_object_id) REFERENCES stored_objects (tenant_id, object_id),
    CONSTRAINT analysis_targets_shape CHECK (
        (kind = 'snapshot' AND source_snapshot_id IS NOT NULL AND commit_sha IS NOT NULL) OR
        (kind = 'commit' AND commit_sha IS NOT NULL) OR
        (kind = 'ref' AND ref_name IS NOT NULL AND btrim(ref_name) <> '') OR
        (kind = 'pull_request' AND pull_request_number IS NOT NULL)
    ),
    CONSTRAINT analysis_targets_ready CHECK (
        status <> 'ready' OR source_snapshot_id IS NOT NULL
    )
);

CREATE INDEX analysis_targets_repository_idx
    ON analysis_targets (tenant_id, repository_id, created_at DESC, target_id DESC);

CREATE TABLE analyses (
    analysis_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    project_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    target_id uuid NOT NULL,
    session_id uuid NOT NULL,
    title text NOT NULL CHECK (btrim(title) <> '' AND length(title) <= 300),
    trigger text NOT NULL DEFAULT 'manual' CHECK (trigger IN ('manual', 'github_actions', 'api')),
    external_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, analysis_id),
    UNIQUE (tenant_id, session_id),
    UNIQUE (tenant_id, external_id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, project_id),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES source_repositories (tenant_id, repository_id),
    FOREIGN KEY (tenant_id, target_id) REFERENCES analysis_targets (tenant_id, target_id),
    FOREIGN KEY (tenant_id, session_id) REFERENCES agent_sessions (tenant_id, session_id)
);

CREATE INDEX analyses_project_idx
    ON analyses (tenant_id, project_id, created_at DESC, analysis_id DESC);
CREATE INDEX analyses_repository_idx
    ON analyses (tenant_id, repository_id, created_at DESC, analysis_id DESC);

-- +goose Down
DROP TABLE analyses;
DROP TABLE analysis_targets;
DROP TABLE project_repositories;
DROP TABLE projects;
DROP INDEX source_repositories_tenant_url_idx;
