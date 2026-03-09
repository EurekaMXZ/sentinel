//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

func assertResourceMigration(t *testing.T, pool *pgxpool.Pool, f fixture) {
	t.Helper()
	checks := []struct {
		name, query string
		args        []any
		want        int
	}{
		{"legacy session binding", `SELECT count(*) FROM session_resource_bindings b
			JOIN agent_sessions s USING (tenant_id, session_id)
			WHERE b.session_id = $1 AND b.binding_name = 'default_source' AND b.resource_kind = 'source_snapshot'
			AND b.repository_id = $2 AND b.source_snapshot_id = $3 AND b.created_at = s.created_at`,
			[]any{f.session, f.repository, f.snapshot}, 1},
		{"legacy parent and child bindings", `SELECT count(*) FROM agent_resource_bindings b
			JOIN agents a USING (tenant_id, agent_id)
			WHERE a.session_id = $1 AND b.binding_name = 'default_source' AND b.resource_kind = 'source_snapshot'
			AND b.repository_id = $2 AND b.source_snapshot_id = $3 AND b.created_at = a.created_at`,
			[]any{f.session, f.repository, f.snapshot}, 2},
		{"runtime source columns removed", `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'public'
			AND table_name IN ('agent_sessions', 'agents', 'agent_head', 'context_records', 'llm_executions', 'compaction_jobs')
			AND column_name IN ('repository_id', 'source_snapshot_id', 'default_source_snapshot_id')`, nil, 0},
		{"context evidence preserved", `SELECT count(*) FROM context_source_refs
			WHERE context_record_id = $1 AND repository_id = $2 AND source_snapshot_id = $3 AND role = 'default'`,
			[]any{f.contextRecord, f.repository, f.snapshot}, 1},
		{"event source preserved", `SELECT count(*) FROM event_index
			WHERE event_id = $1 AND repository_id = $2 AND source_snapshot_id = $3`,
			[]any{f.event, f.repository, f.snapshot}, 1},
		{"tool source preserved", `SELECT count(*) FROM tool_executions
			WHERE tool_execution_id = $1 AND repository_id = $2 AND source_snapshot_id = $3`,
			[]any{f.tool, f.repository, f.snapshot}, 1},
	}
	for _, check := range checks {
		var count int
		if err := pool.QueryRow(t.Context(), check.query, check.args...).Scan(&count); err != nil || count != check.want {
			t.Fatalf("%s: count=%d want=%d err=%v", check.name, count, check.want, err)
		}
	}
}

func testResourceFreeRuntime(t *testing.T, pool *pgxpool.Pool, f fixture) {
	tx := beginTx(t, pool)
	session, parent, child, tool, record := newID(t), newID(t), newID(t), newID(t), newID(t)
	task := insertObject(t, tx, f.tenant, "task")
	mustExec(t, tx, `INSERT INTO agent_sessions (session_id, tenant_id, task_object_id)
		VALUES ($1, $2, $3)`, session, f.tenant, task)
	mustExec(t, tx, `INSERT INTO agents (agent_id, tenant_id, session_id, task_object_id, workflow_id)
		VALUES ($1, $2, $3, $4, $5)`, parent, f.tenant, session, task, "agent/"+parent)
	mustExec(t, tx, `INSERT INTO context_records
		(context_record_id, tenant_id, agent_id, context_version, kind, object_id, from_event_sequence, through_event_sequence)
		VALUES ($1, $2, $3, 1, 'snapshot', $4, 0, 0)`,
		record, f.tenant, parent, insertObject(t, tx, f.tenant, "context_snapshot"))
	mustExec(t, tx, `INSERT INTO agent_head (agent_id, tenant_id, context_version, snapshot_record_id)
		VALUES ($1, $2, 1, $3)`, parent, f.tenant, record)
	mustExec(t, tx, `INSERT INTO llm_executions
		(llm_execution_id, tenant_id, agent_id, step_id, operation_id, context_version, model, request_object_id)
		VALUES ($1, $2, $3, 1, $4, 1, 'test-model', $5)`,
		newID(t), f.tenant, parent, "llm/"+parent, insertObject(t, tx, f.tenant, "llm_request"))
	mustExec(t, tx, `INSERT INTO tool_executions
		(tool_execution_id, tenant_id, agent_id, step_id, tool_call_id, tool_name, operation_id, arguments_object_id)
		VALUES ($1, $2, $3, 1, 'call-1', 'call_child_agent', $4, $5)`,
		tool, f.tenant, parent, "tool/"+tool, insertObject(t, tx, f.tenant, "tool_arguments"))
	mustExec(t, tx, `INSERT INTO agents
		(agent_id, tenant_id, session_id, parent_agent_id, parent_tool_execution_id, task_object_id, workflow_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, child, f.tenant, session, parent, tool,
		insertObject(t, tx, f.tenant, "child_task"), "agent/"+child)
	mustExec(t, tx, `INSERT INTO agent_head (agent_id, tenant_id) VALUES ($1, $2)`, child, f.tenant)
	mustExec(t, tx, `INSERT INTO llm_executions
		(llm_execution_id, tenant_id, agent_id, step_id, operation_id, context_version, model, request_object_id)
		VALUES ($1, $2, $3, 1, $4, 0, 'test-model', $5)`,
		newID(t), f.tenant, child, "llm/"+child, insertObject(t, tx, f.tenant, "llm_request"))
	mustExec(t, tx, `INSERT INTO event_index
		(event_id, tenant_id, agent_id, sequence, event_type, operation_id, segment_object_id, record_ordinal)
		VALUES ($1, $2, $3, 1, 'model_request', $4, $5, 0)`,
		newID(t), f.tenant, parent, "llm/"+parent, insertObject(t, tx, f.tenant, "event_segment"))
	mustExec(t, tx, `INSERT INTO compaction_jobs
		(compaction_id, tenant_id, agent_id, operation_id, source_row_version, source_context_version, source_event_sequence)
		VALUES ($1, $2, $3, $4, 0, 1, 0)`, newID(t), f.tenant, parent, "compact/"+parent)
	var count int
	err := tx.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM session_resource_bindings WHERE tenant_id = $1 AND session_id = $2) +
		(SELECT count(*) FROM agent_resource_bindings WHERE tenant_id = $1 AND agent_id IN ($3, $4))`,
		f.tenant, session, parent, child).Scan(&count)
	if err != nil || count != 0 {
		t.Fatalf("resource-free runtime: bindings=%d err=%v", count, err)
	}
}

func testMultipleResources(t *testing.T, pool *pgxpool.Pool, f fixture) {
	tx := beginTx(t, pool)
	mustExec(t, tx, `INSERT INTO agent_resource_bindings
		(tenant_id, agent_id, binding_name, resource_kind, repository_id, source_snapshot_id, object_id) VALUES
		($1, $2, 'history', 'source_repository', $3, NULL, NULL),
		($1, $2, 'previous', 'source_snapshot', $3, $4, NULL),
		($1, $2, 'other_source', 'source_snapshot', $5, $6, NULL),
		($1, $2, 'brief', 'object', NULL, NULL, $7)`,
		f.tenant, f.agent, f.repository, f.extraSnapshot, f.otherRepository, f.otherRepositorySnapshot, f.deltaObject)
	// These are metadata constraints; source readiness is validated by Activities.
	mustExec(t, tx, `UPDATE agent_resource_bindings SET repository_id = $1, source_snapshot_id = $2
		WHERE tenant_id = $3 AND agent_id = $4`, f.otherRepository, f.otherRepositorySnapshot, f.tenant, f.childAgent)
	mustExec(t, tx, `INSERT INTO session_resource_bindings
		(tenant_id, session_id, binding_name, resource_kind, object_id) VALUES ($1, $2, 'brief', 'object', $3)`,
		f.tenant, f.session, f.deltaObject)
	mustExec(t, tx, `INSERT INTO context_source_refs
		(tenant_id, repository_id, context_record_id, source_snapshot_id, role)
		VALUES ($1, $2, $3, $4, 'default')`, f.tenant, f.otherRepository, f.contextRecord, f.otherRepositorySnapshot)
	mustExec(t, tx, `UPDATE tool_executions SET tool_name = 'source_read_file', repository_id = $1, source_snapshot_id = $2
		WHERE tool_execution_id = $3`, f.otherRepository, f.otherRepositorySnapshot, f.tool)
	mustExec(t, tx, `UPDATE event_index SET repository_id = $1, source_snapshot_id = $2
		WHERE event_id = $3`, f.otherRepository, f.otherRepositorySnapshot, f.event)
	var parentCount, childCount, defaultCount int
	err := tx.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM agent_resource_bindings WHERE tenant_id = $1 AND agent_id = $2),
		(SELECT count(*) FROM agent_resource_bindings WHERE tenant_id = $1 AND agent_id = $3),
		(SELECT count(*) FROM context_source_refs WHERE tenant_id = $1 AND context_record_id = $4 AND role = 'default')`,
		f.tenant, f.agent, f.childAgent, f.contextRecord).Scan(&parentCount, &childCount, &defaultCount)
	if err != nil || parentCount != 5 || childCount != 1 || defaultCount != 2 {
		t.Fatalf("independent resources: parent=%d child=%d context defaults=%d err=%v", parentCount, childCount, defaultCount, err)
	}
}

func testResourceConstraints(t *testing.T, pool *pgxpool.Pool, f fixture) {
	tests := []struct {
		name, code, query string
		args              []any
	}{
		{"unique agent binding name", "23505", `INSERT INTO agent_resource_bindings
			(tenant_id, agent_id, binding_name, resource_kind, repository_id, source_snapshot_id)
			SELECT tenant_id, agent_id, binding_name, resource_kind, repository_id, source_snapshot_id
			FROM agent_resource_bindings WHERE agent_id = $1`, []any{f.agent}},
		{"agent snapshot requires repository", "23514", `UPDATE agent_resource_bindings SET repository_id = NULL WHERE agent_id = $1`,
			[]any{f.agent}},
		{"session snapshot requires snapshot ID", "23514", `UPDATE session_resource_bindings SET source_snapshot_id = NULL WHERE session_id = $1`,
			[]any{f.session}},
		{"source binding excludes object", "23514", `UPDATE session_resource_bindings SET object_id = $1 WHERE session_id = $2`,
			[]any{f.contextObject, f.session}},
		{"object binding requires object ID", "23514", `INSERT INTO agent_resource_bindings
			(tenant_id, agent_id, binding_name, resource_kind) VALUES ($1, $2, 'empty', 'object')`, []any{f.tenant, f.agent}},
		{"repository binding requires real repository", "23503", `INSERT INTO agent_resource_bindings
			(tenant_id, agent_id, binding_name, resource_kind, repository_id) VALUES ($1, $2, 'missing', 'source_repository', $3)`,
			[]any{f.tenant, f.agent, newID(t)}},
		{"agent snapshot belongs to repository", "23503", `UPDATE agent_resource_bindings SET source_snapshot_id = $1 WHERE agent_id = $2`,
			[]any{f.otherRepositorySnapshot, f.agent}},
		{"agent object belongs to tenant", "23503", `INSERT INTO agent_resource_bindings
			(tenant_id, agent_id, binding_name, resource_kind, object_id) VALUES ($1, $2, 'foreign', 'object', $3)`,
			[]any{f.tenant, f.agent, f.otherTenantObject}},
		{"session object belongs to tenant", "23503", `INSERT INTO session_resource_bindings
			(tenant_id, session_id, binding_name, resource_kind, object_id) VALUES ($1, $2, 'foreign', 'object', $3)`,
			[]any{f.tenant, f.session, f.otherTenantObject}},
		{"agent binding belongs to tenant", "23503", `INSERT INTO agent_resource_bindings
			(tenant_id, agent_id, binding_name, resource_kind, object_id)
			SELECT tenant_id, $1, 'foreign', 'object', object_id FROM stored_objects WHERE object_id = $2`,
			[]any{f.agent, f.otherTenantObject}},
		{"session binding belongs to tenant", "23503", `INSERT INTO session_resource_bindings
			(tenant_id, session_id, binding_name, resource_kind, object_id)
			SELECT tenant_id, $1, 'foreign', 'object', object_id FROM stored_objects WHERE object_id = $2`,
			[]any{f.session, f.otherTenantObject}},
		{"generic agent belongs to session tenant", "23503", `INSERT INTO agents
			(agent_id, tenant_id, session_id, task_object_id, workflow_id)
			SELECT $1, tenant_id, $2, object_id, $3 FROM stored_objects WHERE object_id = $4`,
			[]any{newID(t), f.session, "foreign/" + f.agent, f.otherTenantObject}},
		{"tool snapshot requires repository", "23514", `UPDATE tool_executions SET repository_id = NULL WHERE tool_execution_id = $1`,
			[]any{f.tool}},
		{"event snapshot requires repository", "23514", `UPDATE event_index SET repository_id = NULL WHERE event_id = $1`,
			[]any{f.event}},
		{"repository-only tool checks repository", "23503", `UPDATE tool_executions SET repository_id = $1,
			source_snapshot_id = NULL WHERE tool_execution_id = $2`, []any{newID(t), f.tool}},
		{"repository-only event checks repository", "23503", `UPDATE event_index SET repository_id = $1,
			source_snapshot_id = NULL WHERE event_id = $2`, []any{newID(t), f.event}},
		{"context evidence checks repository", "23503", `UPDATE context_source_refs SET source_snapshot_id = $1
			WHERE context_record_id = $2`, []any{f.otherRepositorySnapshot, f.contextRecord}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expectPGError(t, beginTx(t, pool), tt.code, tt.query, tt.args...)
		})
	}
}

func TestMigrationDowngradeGuards(t *testing.T) {
	tests := []struct {
		name, query string
		args        func(fixture) []any
	}{
		{"resource-free child", `DELETE FROM agent_resource_bindings WHERE agent_id = $1`,
			func(f fixture) []any { return []any{f.childAgent} }},
		{"multiple resources", `INSERT INTO agent_resource_bindings
			(tenant_id, agent_id, binding_name, resource_kind, object_id) VALUES ($1, $2, 'brief', 'object', $3)`,
			func(f fixture) []any { return []any{f.tenant, f.agent, f.contextObject} }},
		{"different binding name", `UPDATE agent_resource_bindings SET binding_name = 'selected_source' WHERE agent_id = $1`,
			func(f fixture) []any { return []any{f.childAgent} }},
		{"cross-repository child", `UPDATE agent_resource_bindings SET repository_id = $1, source_snapshot_id = $2 WHERE agent_id = $3`,
			func(f fixture) []any { return []any{f.otherRepository, f.otherRepositorySnapshot, f.childAgent} }},
		{"cross-repository context evidence", `INSERT INTO context_source_refs
			(tenant_id, repository_id, context_record_id, source_snapshot_id, role) VALUES ($1, $2, $3, $4, 'evidence')`,
			func(f fixture) []any {
				return []any{f.tenant, f.otherRepository, f.contextRecord, f.otherRepositorySnapshot}
			}},
		{"cross-repository tool", `UPDATE tool_executions SET repository_id = $1, source_snapshot_id = $2 WHERE tool_execution_id = $3`,
			func(f fixture) []any { return []any{f.otherRepository, f.otherRepositorySnapshot, f.tool} }},
		{"cross-repository event", `UPDATE event_index SET repository_id = $1, source_snapshot_id = $2 WHERE event_id = $3`,
			func(f fixture) []any { return []any{f.otherRepository, f.otherRepositorySnapshot, f.event} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			dsn := isolatedDatabase(t)
			provider := openProvider(t, ctx, dsn)
			if _, err := provider.UpTo(ctx, 1); err != nil {
				t.Fatal(err)
			}
			pool, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			tx := beginTx(t, pool)
			f := insertLegacyFixture(t, tx)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Up(ctx); err != nil {
				t.Fatal(err)
			}
			tx = beginTx(t, pool)
			mustExec(t, tx, tt.query, tt.args(f)...)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			_, err = provider.Down(ctx)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || !strings.Contains(pgErr.Message, "cannot restore schema v1") {
				t.Fatalf("incompatible downgrade: got %v, want explicit V1 compatibility error", err)
			}
			assertStatus(t, ctx, provider, goose.StateApplied, goose.StateApplied)
			assertTableCount(t, ctx, pool, 19)
		})
	}
}
