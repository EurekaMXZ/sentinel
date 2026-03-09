-- +goose Up
-- Freeze runtime writes while moving the legacy source bindings. Source storage
-- remains independent; each source operation still records immutable snapshot IDs.
LOCK TABLE agent_sessions, agents, context_records, agent_head, context_source_refs,
    llm_executions, tool_executions, event_index, compaction_jobs IN ACCESS EXCLUSIVE MODE;

ALTER TABLE agent_sessions ADD CONSTRAINT agent_sessions_tenant_session_key UNIQUE (tenant_id, session_id);
ALTER TABLE context_records ADD CONSTRAINT context_records_tenant_record_key UNIQUE (tenant_id, context_record_id);

CREATE TABLE session_resource_bindings (
    tenant_id uuid NOT NULL,
    session_id uuid NOT NULL,
    binding_name text NOT NULL CHECK (btrim(binding_name) <> ''),
    resource_kind text NOT NULL CHECK (resource_kind IN ('source_repository', 'source_snapshot', 'object')),
    repository_id uuid,
    source_snapshot_id uuid,
    object_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, session_id, binding_name),
    FOREIGN KEY (tenant_id, session_id) REFERENCES agent_sessions (tenant_id, session_id),
    FOREIGN KEY (tenant_id, repository_id) REFERENCES source_repositories (tenant_id, repository_id),
    FOREIGN KEY (tenant_id, repository_id, source_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    FOREIGN KEY (tenant_id, object_id) REFERENCES stored_objects (tenant_id, object_id),
    CONSTRAINT session_resource_bindings_shape CHECK (
        (resource_kind = 'source_repository' AND repository_id IS NOT NULL AND source_snapshot_id IS NULL AND object_id IS NULL) OR
        (resource_kind = 'source_snapshot' AND repository_id IS NOT NULL AND source_snapshot_id IS NOT NULL AND object_id IS NULL) OR
        (resource_kind = 'object' AND repository_id IS NULL AND source_snapshot_id IS NULL AND object_id IS NOT NULL)
    )
);

CREATE INDEX session_resource_bindings_repository_idx
    ON session_resource_bindings (tenant_id, repository_id, source_snapshot_id) WHERE repository_id IS NOT NULL;
CREATE INDEX session_resource_bindings_object_idx
    ON session_resource_bindings (tenant_id, object_id) WHERE object_id IS NOT NULL;

CREATE TABLE agent_resource_bindings (
    tenant_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    binding_name text NOT NULL CHECK (btrim(binding_name) <> ''),
    resource_kind text NOT NULL CHECK (resource_kind IN ('source_repository', 'source_snapshot', 'object')),
    repository_id uuid,
    source_snapshot_id uuid,
    object_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, agent_id, binding_name),
    FOREIGN KEY (tenant_id, agent_id) REFERENCES agents (tenant_id, agent_id),
    FOREIGN KEY (tenant_id, repository_id) REFERENCES source_repositories (tenant_id, repository_id),
    FOREIGN KEY (tenant_id, repository_id, source_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id),
    FOREIGN KEY (tenant_id, object_id) REFERENCES stored_objects (tenant_id, object_id),
    CONSTRAINT agent_resource_bindings_shape CHECK (
        (resource_kind = 'source_repository' AND repository_id IS NOT NULL AND source_snapshot_id IS NULL AND object_id IS NULL) OR
        (resource_kind = 'source_snapshot' AND repository_id IS NOT NULL AND source_snapshot_id IS NOT NULL AND object_id IS NULL) OR
        (resource_kind = 'object' AND repository_id IS NULL AND source_snapshot_id IS NULL AND object_id IS NOT NULL)
    )
);

CREATE INDEX agent_resource_bindings_repository_idx
    ON agent_resource_bindings (tenant_id, repository_id, source_snapshot_id) WHERE repository_id IS NOT NULL;
CREATE INDEX agent_resource_bindings_object_idx
    ON agent_resource_bindings (tenant_id, object_id) WHERE object_id IS NOT NULL;

INSERT INTO session_resource_bindings
    (tenant_id, session_id, binding_name, resource_kind, repository_id, source_snapshot_id, created_at)
SELECT tenant_id, session_id, 'default_source', 'source_snapshot', repository_id, default_source_snapshot_id, created_at
FROM agent_sessions;

INSERT INTO agent_resource_bindings
    (tenant_id, agent_id, binding_name, resource_kind, repository_id, source_snapshot_id, created_at)
SELECT tenant_id, agent_id, 'default_source', 'source_snapshot', repository_id, source_snapshot_id, created_at
FROM agents;

ALTER TABLE context_source_refs
    DROP CONSTRAINT context_source_refs_tenant_id_repository_id_context_record_fkey,
    ADD CONSTRAINT context_source_refs_context_fk FOREIGN KEY (tenant_id, context_record_id)
        REFERENCES context_records (tenant_id, context_record_id);
DROP INDEX context_source_refs_one_default_idx;
CREATE UNIQUE INDEX context_source_refs_one_default_idx
    ON context_source_refs (tenant_id, context_record_id, repository_id) WHERE role = 'default';

ALTER TABLE agent_head
    DROP COLUMN repository_id,
    DROP COLUMN source_snapshot_id,
    ADD CONSTRAINT agent_head_agent_fk FOREIGN KEY (tenant_id, agent_id) REFERENCES agents (tenant_id, agent_id);
ALTER TABLE context_records
    DROP COLUMN repository_id,
    ADD CONSTRAINT context_records_agent_fk FOREIGN KEY (tenant_id, agent_id) REFERENCES agents (tenant_id, agent_id);
ALTER TABLE llm_executions
    DROP COLUMN repository_id,
    ADD CONSTRAINT llm_executions_agent_fk FOREIGN KEY (tenant_id, agent_id) REFERENCES agents (tenant_id, agent_id);
ALTER TABLE compaction_jobs
    DROP COLUMN repository_id,
    ADD CONSTRAINT compaction_jobs_agent_fk FOREIGN KEY (tenant_id, agent_id) REFERENCES agents (tenant_id, agent_id);

ALTER TABLE tool_executions
    DROP CONSTRAINT tool_executions_tenant_id_repository_id_agent_id_fkey,
    ALTER COLUMN repository_id DROP NOT NULL,
    ADD CONSTRAINT tool_executions_agent_fk FOREIGN KEY (tenant_id, agent_id) REFERENCES agents (tenant_id, agent_id),
    ADD CONSTRAINT tool_executions_repository_fk FOREIGN KEY (tenant_id, repository_id)
        REFERENCES source_repositories (tenant_id, repository_id),
    ADD CONSTRAINT tool_executions_source_repository CHECK (
        repository_id IS NOT NULL OR
        (source_snapshot_id IS NULL AND base_snapshot_id IS NULL AND target_snapshot_id IS NULL)
    );
ALTER TABLE event_index
    DROP CONSTRAINT event_index_tenant_id_repository_id_agent_id_fkey,
    ALTER COLUMN repository_id DROP NOT NULL,
    ADD CONSTRAINT event_index_agent_fk FOREIGN KEY (tenant_id, agent_id) REFERENCES agents (tenant_id, agent_id),
    ADD CONSTRAINT event_index_repository_fk FOREIGN KEY (tenant_id, repository_id)
        REFERENCES source_repositories (tenant_id, repository_id),
    ADD CONSTRAINT event_index_source_repository CHECK (
        repository_id IS NOT NULL OR
        (source_snapshot_id IS NULL AND base_snapshot_id IS NULL AND target_snapshot_id IS NULL)
    );

ALTER TABLE agents
    DROP COLUMN repository_id,
    DROP COLUMN source_snapshot_id,
    ADD CONSTRAINT agents_session_fk FOREIGN KEY (tenant_id, session_id)
        REFERENCES agent_sessions (tenant_id, session_id);
ALTER TABLE agent_sessions DROP COLUMN repository_id, DROP COLUMN default_source_snapshot_id;
CREATE INDEX agent_sessions_history_idx ON agent_sessions (tenant_id, created_at DESC, session_id DESC);

-- +goose Down
-- V1 cannot represent resource-free or multiple-resource Agents. Refuse such a
-- downgrade transactionally instead of inventing a source or discarding bindings.
LOCK TABLE agent_sessions, agents, session_resource_bindings, agent_resource_bindings,
    context_records, agent_head, context_source_refs, llm_executions, tool_executions,
    event_index, compaction_jobs IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM agent_sessions s
        WHERE (SELECT count(*) FROM session_resource_bindings b
               WHERE b.tenant_id = s.tenant_id AND b.session_id = s.session_id) <> 1
           OR NOT EXISTS (
               SELECT 1 FROM session_resource_bindings b
               WHERE b.tenant_id = s.tenant_id AND b.session_id = s.session_id
                 AND b.binding_name = 'default_source' AND b.resource_kind = 'source_snapshot'
           )
    ) OR EXISTS (
        SELECT 1 FROM agents a
        WHERE (SELECT count(*) FROM agent_resource_bindings b
               WHERE b.tenant_id = a.tenant_id AND b.agent_id = a.agent_id) <> 1
           OR NOT EXISTS (
               SELECT 1 FROM agent_resource_bindings b
               WHERE b.tenant_id = a.tenant_id AND b.agent_id = a.agent_id
                 AND b.binding_name = 'default_source' AND b.resource_kind = 'source_snapshot'
           )
    ) THEN
        RAISE EXCEPTION 'cannot restore schema v1: each session and Agent must have only a default_source snapshot binding';
    END IF;

    IF EXISTS (
        SELECT 1 FROM agents a
        JOIN agent_resource_bindings ab ON ab.tenant_id = a.tenant_id AND ab.agent_id = a.agent_id
        JOIN session_resource_bindings sb ON sb.tenant_id = a.tenant_id AND sb.session_id = a.session_id
        WHERE ab.repository_id <> sb.repository_id
    ) OR EXISTS (
        SELECT 1 FROM context_source_refs r
        JOIN context_records c ON c.tenant_id = r.tenant_id AND c.context_record_id = r.context_record_id
        JOIN agent_resource_bindings b ON b.tenant_id = c.tenant_id AND b.agent_id = c.agent_id
        WHERE r.repository_id <> b.repository_id
    ) OR EXISTS (
        SELECT 1 FROM tool_executions t
        JOIN agent_resource_bindings b ON b.tenant_id = t.tenant_id AND b.agent_id = t.agent_id
        WHERE t.repository_id IS NOT NULL AND t.repository_id <> b.repository_id
    ) OR EXISTS (
        SELECT 1 FROM event_index e
        JOIN agent_resource_bindings b ON b.tenant_id = e.tenant_id AND b.agent_id = e.agent_id
        WHERE e.repository_id IS NOT NULL AND e.repository_id <> b.repository_id
    ) THEN
        RAISE EXCEPTION 'cannot restore schema v1: cross-repository Agent resources or source evidence exist';
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE agent_sessions ADD COLUMN repository_id uuid, ADD COLUMN default_source_snapshot_id uuid;
ALTER TABLE agents ADD COLUMN repository_id uuid, ADD COLUMN source_snapshot_id uuid;
ALTER TABLE context_records ADD COLUMN repository_id uuid;
ALTER TABLE agent_head ADD COLUMN repository_id uuid, ADD COLUMN source_snapshot_id uuid;
ALTER TABLE llm_executions ADD COLUMN repository_id uuid;
ALTER TABLE compaction_jobs ADD COLUMN repository_id uuid;

UPDATE agent_sessions s SET repository_id = b.repository_id, default_source_snapshot_id = b.source_snapshot_id
FROM session_resource_bindings b WHERE b.tenant_id = s.tenant_id AND b.session_id = s.session_id;
UPDATE agents a SET repository_id = b.repository_id, source_snapshot_id = b.source_snapshot_id
FROM agent_resource_bindings b WHERE b.tenant_id = a.tenant_id AND b.agent_id = a.agent_id;
UPDATE context_records c SET repository_id = a.repository_id
FROM agents a WHERE a.tenant_id = c.tenant_id AND a.agent_id = c.agent_id;
UPDATE agent_head h SET repository_id = a.repository_id, source_snapshot_id = a.source_snapshot_id
FROM agents a WHERE a.tenant_id = h.tenant_id AND a.agent_id = h.agent_id;
UPDATE llm_executions l SET repository_id = a.repository_id
FROM agents a WHERE a.tenant_id = l.tenant_id AND a.agent_id = l.agent_id;
UPDATE compaction_jobs c SET repository_id = a.repository_id
FROM agents a WHERE a.tenant_id = c.tenant_id AND a.agent_id = c.agent_id;
UPDATE tool_executions t SET repository_id = a.repository_id
FROM agents a WHERE a.tenant_id = t.tenant_id AND a.agent_id = t.agent_id AND t.repository_id IS NULL;
UPDATE event_index e SET repository_id = a.repository_id
FROM agents a WHERE a.tenant_id = e.tenant_id AND a.agent_id = e.agent_id AND e.repository_id IS NULL;

ALTER TABLE agent_sessions
    ALTER COLUMN repository_id SET NOT NULL,
    ALTER COLUMN default_source_snapshot_id SET NOT NULL,
    ADD UNIQUE (tenant_id, repository_id, session_id),
    ADD FOREIGN KEY (tenant_id, repository_id, default_source_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id);
ALTER TABLE agents
    ALTER COLUMN repository_id SET NOT NULL,
    ALTER COLUMN source_snapshot_id SET NOT NULL,
    DROP CONSTRAINT agents_session_fk,
    ADD UNIQUE (tenant_id, repository_id, agent_id),
    ADD UNIQUE (tenant_id, repository_id, agent_id, source_snapshot_id),
    ADD FOREIGN KEY (tenant_id, repository_id, session_id)
        REFERENCES agent_sessions (tenant_id, repository_id, session_id),
    ADD FOREIGN KEY (tenant_id, repository_id, source_snapshot_id)
        REFERENCES source_snapshots (tenant_id, repository_id, snapshot_id);
ALTER TABLE context_records
    ALTER COLUMN repository_id SET NOT NULL,
    DROP CONSTRAINT context_records_agent_fk,
    ADD UNIQUE (tenant_id, repository_id, context_record_id),
    ADD FOREIGN KEY (tenant_id, repository_id, agent_id) REFERENCES agents (tenant_id, repository_id, agent_id);
ALTER TABLE agent_head
    ALTER COLUMN repository_id SET NOT NULL,
    ALTER COLUMN source_snapshot_id SET NOT NULL,
    DROP CONSTRAINT agent_head_agent_fk,
    ADD FOREIGN KEY (tenant_id, repository_id, agent_id, source_snapshot_id)
        REFERENCES agents (tenant_id, repository_id, agent_id, source_snapshot_id);
ALTER TABLE llm_executions
    ALTER COLUMN repository_id SET NOT NULL,
    DROP CONSTRAINT llm_executions_agent_fk,
    ADD FOREIGN KEY (tenant_id, repository_id, agent_id) REFERENCES agents (tenant_id, repository_id, agent_id);
ALTER TABLE compaction_jobs
    ALTER COLUMN repository_id SET NOT NULL,
    DROP CONSTRAINT compaction_jobs_agent_fk,
    ADD FOREIGN KEY (tenant_id, repository_id, agent_id) REFERENCES agents (tenant_id, repository_id, agent_id);
ALTER TABLE tool_executions
    ALTER COLUMN repository_id SET NOT NULL,
    DROP CONSTRAINT tool_executions_agent_fk,
    DROP CONSTRAINT tool_executions_repository_fk,
    DROP CONSTRAINT tool_executions_source_repository,
    ADD FOREIGN KEY (tenant_id, repository_id, agent_id) REFERENCES agents (tenant_id, repository_id, agent_id);
ALTER TABLE event_index
    ALTER COLUMN repository_id SET NOT NULL,
    DROP CONSTRAINT event_index_agent_fk,
    DROP CONSTRAINT event_index_repository_fk,
    DROP CONSTRAINT event_index_source_repository,
    ADD FOREIGN KEY (tenant_id, repository_id, agent_id) REFERENCES agents (tenant_id, repository_id, agent_id);
ALTER TABLE context_source_refs
    DROP CONSTRAINT context_source_refs_context_fk,
    ADD CONSTRAINT context_source_refs_tenant_id_repository_id_context_record_fkey
        FOREIGN KEY (tenant_id, repository_id, context_record_id)
        REFERENCES context_records (tenant_id, repository_id, context_record_id);
DROP INDEX context_source_refs_one_default_idx;
CREATE UNIQUE INDEX context_source_refs_one_default_idx
    ON context_source_refs (tenant_id, context_record_id) WHERE role = 'default';

DROP TABLE agent_resource_bindings;
DROP TABLE session_resource_bindings;
ALTER TABLE context_records DROP CONSTRAINT context_records_tenant_record_key;
ALTER TABLE agent_sessions DROP CONSTRAINT agent_sessions_tenant_session_key;
DROP INDEX agent_sessions_history_idx;
CREATE INDEX agent_sessions_history_idx ON agent_sessions (tenant_id, repository_id, created_at DESC, session_id DESC);
CREATE INDEX agent_sessions_snapshot_idx ON agent_sessions (tenant_id, repository_id, default_source_snapshot_id);
CREATE INDEX agents_snapshot_idx ON agents (tenant_id, repository_id, source_snapshot_id);
