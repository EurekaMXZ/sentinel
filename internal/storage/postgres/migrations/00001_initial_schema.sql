-- +goose Up
-- PostgreSQL 17+. IDs are application-generated UUIDv7; there is no UUID default.
-- Bodies live in object storage. Foreign keys bind metadata to its tenant/repository.

CREATE TABLE stored_objects (
    object_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    kind text NOT NULL CHECK (btrim(kind) <> ''),
    bucket text NOT NULL CHECK (btrim(bucket) <> ''),
    object_key text NOT NULL CHECK (object_key <> ''),
    version_id text,
    checksum_sha256 bytea CHECK (octet_length(checksum_sha256) = 32),
    size_bytes bigint CHECK (size_bytes >= 0),
    codec text NOT NULL DEFAULT 'identity' CHECK (codec IN ('identity', 'gzip', 'zstd')),
    media_type text NOT NULL DEFAULT 'application/octet-stream',
    status text NOT NULL DEFAULT 'uploading'
        CHECK (status IN ('uploading', 'verified', 'failed', 'deleting', 'deleted')),
    verified_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, object_id),
    UNIQUE (bucket, object_key),
    CONSTRAINT stored_objects_verified_metadata CHECK (
        status <> 'verified' OR
        (checksum_sha256 IS NOT NULL AND size_bytes IS NOT NULL AND verified_at IS NOT NULL)
    )
);

CREATE INDEX stored_objects_inventory_idx ON stored_objects (tenant_id, kind, created_at, object_id);

CREATE TABLE source_repositories (
    repository_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    remote_url text NOT NULL CHECK (btrim(remote_url) <> ''),
    credential_ref text,
    tracked_refs text[] NOT NULL DEFAULT ARRAY['refs/heads/*'],
    history_policy jsonb NOT NULL DEFAULT '{"mode":"all"}'::jsonb
        CHECK (jsonb_typeof(history_policy) = 'object'),
    object_format text NOT NULL DEFAULT 'sha1' CHECK (object_format IN ('sha1', 'sha256')),
    archive_head_id uuid,
    archive_version bigint NOT NULL DEFAULT 0 CHECK (archive_version >= 0),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'archived')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, repository_id),
    CONSTRAINT source_repositories_refs CHECK (
        cardinality(tracked_refs) > 0 AND array_position(tracked_refs, NULL) IS NULL
    ),
    CONSTRAINT source_repositories_archive_version CHECK (
        (archive_head_id IS NULL AND archive_version = 0) OR
        (archive_head_id IS NOT NULL AND archive_version > 0)
    )
);

CREATE TABLE idempotency_records (
    tenant_id uuid NOT NULL,
    operation_id text NOT NULL CHECK (btrim(operation_id) <> ''),
    operation_type text NOT NULL CHECK (btrim(operation_type) <> ''),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'uncertain')),
    result_object_id uuid,
    lease_owner text,
    lease_until timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, operation_id),
    FOREIGN KEY (tenant_id, result_object_id) REFERENCES stored_objects (tenant_id, object_id),
    CONSTRAINT idempotency_records_lease CHECK (
        (lease_owner IS NULL) = (lease_until IS NULL) AND
        (status = 'running') = (lease_owner IS NOT NULL) AND
        (status <> 'running' OR (btrim(lease_owner) <> '' AND fencing_token > 0))
    )
);

CREATE INDEX idempotency_records_lease_idx ON idempotency_records (lease_until, tenant_id, operation_id)
    WHERE status = 'running';

CREATE TABLE source_sync_jobs (
    sync_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    operation_id text NOT NULL CHECK (btrim(operation_id) <> ''),
    trigger_type text NOT NULL DEFAULT 'manual' CHECK (trigger_type IN ('manual', 'schedule', 'webhook')),
    config_snapshot_object_id uuid NOT NULL,
    expected_archive_version bigint NOT NULL CHECK (expected_archive_version >= 0),
    workflow_id text NOT NULL CHECK (btrim(workflow_id) <> ''),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'archiving', 'publishing', 'succeeded', 'failed', 'cancelled')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    result_object_id uuid,
    error_object_id uuid,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, repository_id, sync_id),
    UNIQUE (tenant_id, operation_id),
    UNIQUE (tenant_id, workflow_id),
    FOREIGN KEY (tenant_id, repository_id) REFERENCES source_repositories (tenant_id, repository_id),
    FOREIGN KEY (tenant_id, config_snapshot_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, result_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, error_object_id) REFERENCES stored_objects (tenant_id, object_id),
    CONSTRAINT source_sync_jobs_active_started CHECK (
        status NOT IN ('running', 'archiving', 'publishing') OR started_at IS NOT NULL
    ),
    CONSTRAINT source_sync_jobs_finished CHECK (
        (status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX source_sync_jobs_one_active_idx ON source_sync_jobs (tenant_id, repository_id)
    WHERE status IN ('running', 'archiving', 'publishing');
CREATE INDEX source_sync_jobs_history_idx
    ON source_sync_jobs (tenant_id, repository_id, created_at DESC, sync_id DESC);

CREATE TABLE source_ref_observations (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    sync_id uuid NOT NULL,
    ref_name text NOT NULL CHECK (ref_name LIKE 'refs/%'),
    target_oid text CHECK (target_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    peeled_commit_sha text CHECK (peeled_commit_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    is_deleted boolean NOT NULL DEFAULT false,
    observed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, sync_id, ref_name),
    FOREIGN KEY (tenant_id, repository_id, sync_id)
        REFERENCES source_sync_jobs (tenant_id, repository_id, sync_id),
    CONSTRAINT source_ref_observations_target CHECK (
        (is_deleted AND target_oid IS NULL AND peeled_commit_sha IS NULL) OR
        (NOT is_deleted AND target_oid IS NOT NULL)
    )
);

CREATE INDEX source_ref_observations_history_idx
    ON source_ref_observations (tenant_id, repository_id, ref_name, observed_at DESC);

CREATE TABLE source_storage_manifests (
    manifest_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    sync_id uuid NOT NULL,
    operation_id text NOT NULL CHECK (btrim(operation_id) <> ''),
    archive_attempt integer NOT NULL DEFAULT 1 CHECK (archive_attempt > 0),
    parent_manifest_id uuid,
    bundle_kind text NOT NULL CHECK (bundle_kind IN ('base', 'increment')),
    bundle_object_id uuid NOT NULL,
    manifest_object_id uuid NOT NULL,
    prerequisite_commit_shas text[] NOT NULL DEFAULT '{}'
        CHECK (array_position(prerequisite_commit_shas, NULL) IS NULL),
    status text NOT NULL DEFAULT 'staging'
        CHECK (status IN ('staging', 'verified', 'published', 'failed', 'obsolete', 'deleted')),
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, repository_id, manifest_id),
    UNIQUE (tenant_id, repository_id, operation_id, archive_attempt),
    UNIQUE (tenant_id, manifest_object_id),
    FOREIGN KEY (tenant_id, repository_id, sync_id)
        REFERENCES source_sync_jobs (tenant_id, repository_id, sync_id),
    FOREIGN KEY (tenant_id, repository_id, parent_manifest_id)
        REFERENCES source_storage_manifests (tenant_id, repository_id, manifest_id),
    FOREIGN KEY (tenant_id, bundle_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, manifest_object_id) REFERENCES stored_objects (tenant_id, object_id),
    CONSTRAINT source_storage_manifests_parent CHECK (
        parent_manifest_id IS NULL OR parent_manifest_id <> manifest_id
    ),
    CONSTRAINT source_storage_manifests_kind CHECK (
        (bundle_kind = 'base' AND parent_manifest_id IS NULL AND cardinality(prerequisite_commit_shas) = 0) OR
        (bundle_kind = 'increment' AND parent_manifest_id IS NOT NULL)
    ),
    CONSTRAINT source_storage_manifests_published CHECK (
        status NOT IN ('published', 'obsolete', 'deleted') OR published_at IS NOT NULL
    )
);

CREATE INDEX source_storage_manifests_parent_idx
    ON source_storage_manifests (tenant_id, repository_id, parent_manifest_id)
    WHERE parent_manifest_id IS NOT NULL;
CREATE INDEX source_storage_manifests_sync_idx ON source_storage_manifests (tenant_id, repository_id, sync_id);

ALTER TABLE source_repositories ADD CONSTRAINT source_repositories_archive_head_fk
    FOREIGN KEY (tenant_id, repository_id, archive_head_id)
    REFERENCES source_storage_manifests (tenant_id, repository_id, manifest_id);

CREATE TABLE source_snapshots (
    snapshot_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    commit_sha text NOT NULL CHECK (commit_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    tree_sha text NOT NULL CHECK (tree_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    storage_manifest_id uuid,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'failed')),
    ready_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, repository_id, snapshot_id),
    UNIQUE (tenant_id, repository_id, commit_sha),
    FOREIGN KEY (tenant_id, repository_id) REFERENCES source_repositories (tenant_id, repository_id),
    FOREIGN KEY (tenant_id, repository_id, storage_manifest_id)
        REFERENCES source_storage_manifests (tenant_id, repository_id, manifest_id),
    CONSTRAINT source_snapshots_hash_format CHECK (length(commit_sha) = length(tree_sha)),
    CONSTRAINT source_snapshots_ready CHECK (
        (status = 'ready') = (ready_at IS NOT NULL) AND
        (status <> 'ready' OR storage_manifest_id IS NOT NULL)
    )
);

CREATE INDEX source_snapshots_ready_idx
    ON source_snapshots (tenant_id, repository_id, ready_at DESC, snapshot_id DESC) WHERE status = 'ready';
CREATE INDEX source_snapshots_manifest_idx ON source_snapshots (tenant_id, repository_id, storage_manifest_id)
    WHERE storage_manifest_id IS NOT NULL;

CREATE TABLE agent_sessions (
    session_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    default_source_snapshot_id uuid NOT NULL,
    task_object_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    error_object_id uuid,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, repository_id, session_id),
    FOREIGN KEY (tenant_id, repository_id, default_source_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    FOREIGN KEY (tenant_id, task_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, error_object_id) REFERENCES stored_objects (tenant_id, object_id),
    CONSTRAINT agent_sessions_finished CHECK (
        (status IN ('completed', 'failed', 'cancelled')) = (finished_at IS NOT NULL)
    )
);

CREATE INDEX agent_sessions_history_idx ON agent_sessions (tenant_id, repository_id, created_at DESC, session_id DESC);
CREATE INDEX agent_sessions_snapshot_idx ON agent_sessions (tenant_id, repository_id, default_source_snapshot_id);

CREATE TABLE agents (
    agent_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    session_id uuid NOT NULL,
    parent_agent_id uuid,
    parent_tool_execution_id uuid,
    source_snapshot_id uuid NOT NULL,
    task_object_id uuid NOT NULL,
    workflow_id text NOT NULL CHECK (btrim(workflow_id) <> ''),
    current_run_id text,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'waiting', 'completed', 'failed', 'cancelled')),
    result_object_id uuid,
    error_object_id uuid,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, agent_id),
    UNIQUE (tenant_id, repository_id, agent_id),
    UNIQUE (tenant_id, session_id, agent_id),
    UNIQUE (tenant_id, repository_id, agent_id, source_snapshot_id),
    UNIQUE (tenant_id, parent_tool_execution_id),
    UNIQUE (tenant_id, workflow_id),
    FOREIGN KEY (tenant_id, repository_id, session_id)
        REFERENCES agent_sessions (tenant_id, repository_id, session_id),
    FOREIGN KEY (tenant_id, session_id, parent_agent_id) REFERENCES agents (tenant_id, session_id, agent_id),
    FOREIGN KEY (tenant_id, repository_id, source_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    FOREIGN KEY (tenant_id, task_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, result_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, error_object_id) REFERENCES stored_objects (tenant_id, object_id),
    CONSTRAINT agents_parent CHECK (
        (parent_agent_id IS NULL) = (parent_tool_execution_id IS NULL) AND
        (parent_agent_id IS NULL OR parent_agent_id <> agent_id)
    ),
    CONSTRAINT agents_finished CHECK (
        (status IN ('completed', 'failed', 'cancelled')) = (finished_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX agents_one_root_idx ON agents (tenant_id, session_id) WHERE parent_agent_id IS NULL;
CREATE INDEX agents_parent_idx ON agents (tenant_id, parent_agent_id) WHERE parent_agent_id IS NOT NULL;
CREATE INDEX agents_snapshot_idx ON agents (tenant_id, repository_id, source_snapshot_id);

CREATE TABLE context_records (
    context_record_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    context_version bigint NOT NULL CHECK (context_version > 0),
    kind text NOT NULL CHECK (kind IN ('snapshot', 'delta')),
    object_id uuid NOT NULL,
    base_snapshot_record_id uuid,
    from_event_sequence bigint NOT NULL CHECK (from_event_sequence >= 0),
    through_event_sequence bigint NOT NULL CHECK (through_event_sequence >= from_event_sequence),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, agent_id, context_record_id),
    UNIQUE (tenant_id, repository_id, context_record_id),
    UNIQUE (tenant_id, agent_id, context_version),
    FOREIGN KEY (tenant_id, repository_id, agent_id) REFERENCES agents (tenant_id, repository_id, agent_id),
    FOREIGN KEY (tenant_id, object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, agent_id, base_snapshot_record_id)
        REFERENCES context_records (tenant_id, agent_id, context_record_id),
    CONSTRAINT context_records_base CHECK (
        (kind = 'snapshot' AND base_snapshot_record_id IS NULL) OR
        (kind = 'delta' AND base_snapshot_record_id IS NOT NULL AND base_snapshot_record_id <> context_record_id)
    )
);

CREATE INDEX context_records_base_idx ON context_records (tenant_id, agent_id, base_snapshot_record_id)
    WHERE base_snapshot_record_id IS NOT NULL;

CREATE TABLE agent_head (
    agent_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    row_version bigint NOT NULL DEFAULT 0 CHECK (row_version >= 0),
    context_version bigint NOT NULL DEFAULT 0 CHECK (context_version >= 0),
    last_event_sequence bigint NOT NULL DEFAULT 0 CHECK (last_event_sequence >= 0),
    context_event_sequence bigint NOT NULL DEFAULT 0
        CHECK (context_event_sequence >= 0 AND context_event_sequence <= last_event_sequence),
    snapshot_record_id uuid,
    source_snapshot_id uuid NOT NULL,
    next_step_id bigint NOT NULL DEFAULT 1 CHECK (next_step_id > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, repository_id, agent_id, source_snapshot_id)
        REFERENCES agents (tenant_id, repository_id, agent_id, source_snapshot_id),
    FOREIGN KEY (tenant_id, agent_id, snapshot_record_id)
        REFERENCES context_records (tenant_id, agent_id, context_record_id),
    CONSTRAINT agent_head_context CHECK (
        (context_version = 0 AND snapshot_record_id IS NULL AND context_event_sequence = 0) OR
        (context_version > 0 AND snapshot_record_id IS NOT NULL)
    )
);

CREATE INDEX agent_head_snapshot_idx ON agent_head (tenant_id, agent_id, snapshot_record_id)
    WHERE snapshot_record_id IS NOT NULL;

CREATE TABLE context_source_refs (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    context_record_id uuid NOT NULL,
    source_snapshot_id uuid NOT NULL,
    role text NOT NULL CHECK (role IN ('default', 'evidence', 'diff_base', 'diff_target')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, context_record_id, source_snapshot_id, role),
    FOREIGN KEY (tenant_id, repository_id, context_record_id)
        REFERENCES context_records (tenant_id, repository_id, context_record_id),
    FOREIGN KEY (tenant_id, repository_id, source_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id)
);

CREATE UNIQUE INDEX context_source_refs_one_default_idx
    ON context_source_refs (tenant_id, context_record_id) WHERE role = 'default';
CREATE INDEX context_source_refs_snapshot_idx ON context_source_refs (tenant_id, repository_id, source_snapshot_id);

CREATE TABLE llm_executions (
    llm_execution_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    step_id bigint NOT NULL CHECK (step_id > 0),
    operation_id text NOT NULL CHECK (btrim(operation_id) <> ''),
    context_version bigint NOT NULL CHECK (context_version >= 0),
    model text NOT NULL CHECK (btrim(model) <> ''),
    request_object_id uuid NOT NULL,
    response_object_id uuid,
    provider_request_id text,
    input_tokens bigint CHECK (input_tokens >= 0),
    output_tokens bigint CHECK (output_tokens >= 0),
    status text NOT NULL DEFAULT 'prepared'
        CHECK (status IN ('prepared', 'running', 'response_saved', 'succeeded', 'failed', 'uncertain', 'cancelled')),
    error_object_id uuid,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, agent_id, step_id),
    UNIQUE (tenant_id, operation_id),
    FOREIGN KEY (tenant_id, repository_id, agent_id) REFERENCES agents (tenant_id, repository_id, agent_id),
    FOREIGN KEY (tenant_id, request_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, response_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, error_object_id) REFERENCES stored_objects (tenant_id, object_id),
    CONSTRAINT llm_executions_response CHECK (
        status NOT IN ('response_saved', 'succeeded') OR response_object_id IS NOT NULL
    )
);

CREATE TABLE tool_executions (
    tool_execution_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    step_id bigint NOT NULL CHECK (step_id > 0),
    tool_call_id text NOT NULL CHECK (btrim(tool_call_id) <> ''),
    tool_name text NOT NULL CHECK (btrim(tool_name) <> ''),
    operation_id text NOT NULL CHECK (btrim(operation_id) <> ''),
    arguments_object_id uuid NOT NULL,
    result_object_id uuid,
    source_snapshot_id uuid,
    base_snapshot_id uuid,
    target_snapshot_id uuid,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    error_object_id uuid,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, agent_id, tool_execution_id),
    UNIQUE (tenant_id, agent_id, step_id, tool_call_id),
    UNIQUE (tenant_id, operation_id),
    FOREIGN KEY (tenant_id, repository_id, agent_id) REFERENCES agents (tenant_id, repository_id, agent_id),
    FOREIGN KEY (tenant_id, agent_id, step_id) REFERENCES llm_executions (tenant_id, agent_id, step_id),
    FOREIGN KEY (tenant_id, arguments_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, result_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, error_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, repository_id, source_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    FOREIGN KEY (tenant_id, repository_id, base_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    FOREIGN KEY (tenant_id, repository_id, target_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    CONSTRAINT tool_executions_diff CHECK ((base_snapshot_id IS NULL) = (target_snapshot_id IS NULL)),
    CONSTRAINT tool_executions_result CHECK (status <> 'succeeded' OR result_object_id IS NOT NULL)
);

CREATE INDEX tool_executions_snapshot_idx ON tool_executions (tenant_id, repository_id, source_snapshot_id)
    WHERE source_snapshot_id IS NOT NULL;
CREATE INDEX tool_executions_base_idx ON tool_executions (tenant_id, repository_id, base_snapshot_id)
    WHERE base_snapshot_id IS NOT NULL;
CREATE INDEX tool_executions_target_idx ON tool_executions (tenant_id, repository_id, target_snapshot_id)
    WHERE target_snapshot_id IS NOT NULL;

ALTER TABLE agents ADD CONSTRAINT agents_parent_tool_fk
    FOREIGN KEY (tenant_id, parent_agent_id, parent_tool_execution_id)
    REFERENCES tool_executions (tenant_id, agent_id, tool_execution_id);

CREATE TABLE event_index (
    event_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    event_type text NOT NULL CHECK (btrim(event_type) <> ''),
    operation_id text NOT NULL CHECK (btrim(operation_id) <> ''),
    segment_object_id uuid NOT NULL,
    record_ordinal integer NOT NULL CHECK (record_ordinal >= 0),
    payload_object_id uuid,
    source_snapshot_id uuid,
    base_snapshot_id uuid,
    target_snapshot_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, agent_id, sequence),
    UNIQUE (tenant_id, segment_object_id, record_ordinal),
    FOREIGN KEY (tenant_id, repository_id, agent_id) REFERENCES agents (tenant_id, repository_id, agent_id),
    FOREIGN KEY (tenant_id, segment_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, payload_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, repository_id, source_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    FOREIGN KEY (tenant_id, repository_id, base_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    FOREIGN KEY (tenant_id, repository_id, target_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    CONSTRAINT event_index_diff CHECK ((base_snapshot_id IS NULL) = (target_snapshot_id IS NULL))
);

CREATE INDEX event_index_operation_idx ON event_index (tenant_id, operation_id);
CREATE INDEX event_index_snapshot_idx ON event_index (tenant_id, repository_id, source_snapshot_id)
    WHERE source_snapshot_id IS NOT NULL;
CREATE INDEX event_index_base_idx ON event_index (tenant_id, repository_id, base_snapshot_id)
    WHERE base_snapshot_id IS NOT NULL;
CREATE INDEX event_index_target_idx ON event_index (tenant_id, repository_id, target_snapshot_id)
    WHERE target_snapshot_id IS NOT NULL;

CREATE TABLE compaction_jobs (
    compaction_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    operation_id text NOT NULL CHECK (btrim(operation_id) <> ''),
    source_row_version bigint NOT NULL CHECK (source_row_version >= 0),
    source_context_version bigint NOT NULL CHECK (source_context_version >= 0),
    source_event_sequence bigint NOT NULL CHECK (source_event_sequence >= 0),
    request_object_id uuid,
    response_object_id uuid,
    candidate_snapshot_object_id uuid,
    published_context_record_id uuid,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'response_saved', 'published', 'stale', 'failed', 'uncertain', 'cancelled')),
    error_object_id uuid,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, agent_id, source_row_version),
    UNIQUE (tenant_id, operation_id),
    FOREIGN KEY (tenant_id, repository_id, agent_id) REFERENCES agents (tenant_id, repository_id, agent_id),
    FOREIGN KEY (tenant_id, request_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, response_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, candidate_snapshot_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, error_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, agent_id, published_context_record_id)
        REFERENCES context_records (tenant_id, agent_id, context_record_id),
    CONSTRAINT compaction_jobs_response CHECK (
        status NOT IN ('response_saved', 'published') OR response_object_id IS NOT NULL
    ),
    CONSTRAINT compaction_jobs_publication CHECK (
        (status = 'published') = (published_context_record_id IS NOT NULL) AND
        (status <> 'published' OR (request_object_id IS NOT NULL AND candidate_snapshot_object_id IS NOT NULL))
    )
);

CREATE TABLE outbox (
    outbox_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    topic text NOT NULL CHECK (btrim(topic) <> ''),
    aggregate_type text NOT NULL CHECK (btrim(aggregate_type) <> ''),
    aggregate_id uuid NOT NULL,
    dedupe_key text NOT NULL CHECK (btrim(dedupe_key) <> ''),
    payload_object_id uuid,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'published', 'dead')),
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_until timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    published_at timestamptz,
    error_object_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, dedupe_key),
    FOREIGN KEY (tenant_id, payload_object_id) REFERENCES stored_objects (tenant_id, object_id),
    FOREIGN KEY (tenant_id, error_object_id) REFERENCES stored_objects (tenant_id, object_id),
    CONSTRAINT outbox_lease CHECK (
        (lease_owner IS NULL) = (lease_until IS NULL) AND
        (status = 'processing') = (lease_owner IS NOT NULL) AND
        (status <> 'processing' OR (btrim(lease_owner) <> '' AND fencing_token > 0))
    ),
    CONSTRAINT outbox_publication CHECK ((status = 'published') = (published_at IS NOT NULL))
);

CREATE INDEX outbox_pending_idx ON outbox (available_at, outbox_id) WHERE status = 'pending';
CREATE INDEX outbox_processing_idx ON outbox (lease_until, outbox_id) WHERE status = 'processing';
CREATE INDEX outbox_aggregate_idx ON outbox (tenant_id, aggregate_type, aggregate_id, created_at, outbox_id);

-- +goose Down
-- This initial migration owns all 17 business tables. Rollback deletes their data.
DROP TABLE outbox;
DROP TABLE compaction_jobs;
DROP TABLE event_index;
ALTER TABLE agents DROP CONSTRAINT agents_parent_tool_fk;
DROP TABLE tool_executions;
DROP TABLE llm_executions;
DROP TABLE context_source_refs;
DROP TABLE agent_head;
DROP TABLE context_records;
DROP TABLE agents;
DROP TABLE agent_sessions;
DROP TABLE source_snapshots;
ALTER TABLE source_repositories DROP CONSTRAINT source_repositories_archive_head_fk;
DROP TABLE source_storage_manifests;
DROP TABLE source_ref_observations;
DROP TABLE source_sync_jobs;
DROP TABLE idempotency_records;
DROP TABLE source_repositories;
DROP TABLE stored_objects;
