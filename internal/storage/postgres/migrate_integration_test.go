//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"sentinel/internal/platform/config"
	"sentinel/internal/storage/postgres"
)

// Tests create and drop a uniquely named database. The supplied user needs CREATEDB.
func TestMigrations(t *testing.T) {
	dsn := isolatedDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	provider := openProvider(t, ctx, dsn)
	assertStatus(t, ctx, provider, goose.StatePending, goose.StatePending, goose.StatePending)

	otherProvider := openProvider(t, ctx, dsn)
	completed := make(chan error, 2)
	for _, p := range []*goose.Provider{provider, otherProvider} {
		go func() {
			_, err := p.Up(ctx)
			completed <- err
		}()
	}
	for range 2 {
		if err := <-completed; err != nil {
			t.Errorf("concurrent up: %v", err)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	assertStatus(t, ctx, provider, goose.StateApplied, goose.StateApplied, goose.StateApplied)
	if results, err := provider.Up(ctx); err != nil || len(results) != 0 {
		t.Fatalf("repeated up: results=%v, err=%v", results, err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	assertTableCount(t, ctx, pool, 23)
	// An empty installation can return to V1, which lets us insert real legacy
	// rows before testing the data migration instead of only testing empty DDL.
	result, err := provider.Down(ctx)
	if err != nil || result == nil || result.Source.Version != 3 {
		t.Fatalf("empty down from v3: result=%v, err=%v", result, err)
	}
	assertStatus(t, ctx, provider, goose.StateApplied, goose.StateApplied, goose.StatePending)
	assertTableCount(t, ctx, pool, 19)
	result, err = provider.Down(ctx)
	if err != nil || result == nil || result.Source.Version != 2 {
		t.Fatalf("empty down from v2: result=%v, err=%v", result, err)
	}
	assertStatus(t, ctx, provider, goose.StateApplied, goose.StatePending, goose.StatePending)
	assertTableCount(t, ctx, pool, 17)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := insertLegacyFixture(t, tx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if results, err := provider.UpTo(ctx, 2); err != nil || len(results) != 1 || results[0].Source.Version != 2 {
		t.Fatalf("legacy upgrade: results=%v, err=%v", results, err)
	}
	assertResourceMigration(t, pool, f)
	t.Run("constraints", func(t *testing.T) { testConstraints(t, pool, f) })
	t.Run("resource constraints", func(t *testing.T) { testResourceConstraints(t, pool, f) })
	t.Run("resource-free parent and child", func(t *testing.T) { testResourceFreeRuntime(t, pool, f) })
	t.Run("independent multiple resources", func(t *testing.T) { testMultipleResources(t, pool, f) })
	t.Run("head CAS", func(t *testing.T) { testHeadCAS(t, pool, f) })
	t.Run("outbox concurrent readers", func(t *testing.T) { testOutboxLocks(t, pool, f) })

	result, err = provider.Down(ctx)
	if err != nil || result == nil || result.Source.Version != 2 {
		t.Fatalf("down: result=%v, err=%v", result, err)
	}
	assertStatus(t, ctx, provider, goose.StateApplied, goose.StatePending, goose.StatePending)
	assertTableCount(t, ctx, pool, 17)
	var repository, sessionSnapshot, agentSnapshot, headSnapshot string
	err = pool.QueryRow(ctx, `SELECT s.repository_id::text, s.default_source_snapshot_id::text,
		a.source_snapshot_id::text, h.source_snapshot_id::text
		FROM agent_sessions s JOIN agents a ON a.session_id = s.session_id
		JOIN agent_head h ON h.agent_id = a.agent_id WHERE a.agent_id = $1`, f.agent).
		Scan(&repository, &sessionSnapshot, &agentSnapshot, &headSnapshot)
	if err != nil || repository != f.repository || sessionSnapshot != f.snapshot || agentSnapshot != f.snapshot || headSnapshot != f.snapshot {
		t.Fatalf("legacy bindings restored: repo=%s session=%s agent=%s head=%s err=%v", repository, sessionSnapshot, agentSnapshot, headSnapshot, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("upgrade after legacy downgrade: %v", err)
	}
	assertResourceMigration(t, pool, f)
	if results, err := provider.DownTo(ctx, 0); err != nil || len(results) != 3 {
		t.Fatalf("full down: results=%v, err=%v", results, err)
	}
	assertStatus(t, ctx, provider, goose.StatePending, goose.StatePending, goose.StatePending)
	assertTableCount(t, ctx, pool, 0)
	if _, err := provider.Down(ctx); !errors.Is(err, goose.ErrNoNextVersion) {
		t.Fatalf("down at version zero: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	assertStatus(t, ctx, provider, goose.StateApplied, goose.StateApplied, goose.StateApplied)
	assertTableCount(t, ctx, pool, 23)
}

func isolatedDatabase(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("SENTINEL_TEST_POSTGRES_DSN")
	if raw == "" {
		t.Skip("set SENTINEL_TEST_POSTGRES_DSN to a PostgreSQL test server URI")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("SENTINEL_TEST_POSTGRES_DSN must be a postgres:// or postgresql:// URI")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	databaseName := "sentinel_schema_test_" + strings.ReplaceAll(newID(t), "-", "")
	identifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatalf("create isolated database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+identifier); err != nil {
			t.Errorf("drop isolated database %s: %v", databaseName, err)
		}
	})
	parsed.Path = "/" + databaseName
	return parsed.String()
}

func openProvider(t *testing.T, ctx context.Context, dsn string) *goose.Provider {
	t.Helper()
	provider, err := postgres.OpenMigrations(ctx, config.Postgres{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	})
	return provider
}

func assertStatus(t *testing.T, ctx context.Context, p *goose.Provider, want ...goose.State) {
	t.Helper()
	statuses, err := p.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != len(want) {
		t.Fatalf("migration count: got %d, want %d", len(statuses), len(want))
	}
	for i, state := range want {
		if statuses[i].Source.Version != int64(i+1) || statuses[i].State != state {
			t.Fatalf("migration status: got %+v, want version %d %s", statuses[i], i+1, state)
		}
	}
}

func assertTableCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()
	var count int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
		AND table_name <> 'sentinel_schema_version'`).Scan(&count)
	if err != nil || count != want {
		t.Fatalf("business table count: got %d, want %d, err=%v", count, want, err)
	}
}

type fixture struct {
	tenant, repository, sync, manifest, snapshot, session, agent string
	contextRecord, childAgent, childContextRecord, tool, event   string
	compaction, outbox, contextObject, deltaObject, segment      string
	otherTenantObject, otherRepository, otherRepositorySnapshot  string
	extraSnapshot                                                string
}

func insertLegacyFixture(t *testing.T, tx pgx.Tx) fixture {
	t.Helper()
	f := fixture{
		tenant: newID(t), repository: newID(t), sync: newID(t), manifest: newID(t),
		snapshot: newID(t), session: newID(t), agent: newID(t), contextRecord: newID(t),
		childAgent: newID(t), childContextRecord: newID(t), tool: newID(t), event: newID(t),
		compaction: newID(t), outbox: newID(t), extraSnapshot: newID(t),
	}
	f.contextObject = insertObject(t, tx, f.tenant, "context_snapshot")
	f.deltaObject = insertObject(t, tx, f.tenant, "context_delta")
	f.segment = insertObject(t, tx, f.tenant, "event_segment")
	f.otherTenantObject = insertObject(t, tx, newID(t), "artifact")
	configObject := insertObject(t, tx, f.tenant, "sync_config")
	bundleObject := insertObject(t, tx, f.tenant, "git_bundle")
	manifestObject := insertObject(t, tx, f.tenant, "git_manifest")
	taskObject := insertObject(t, tx, f.tenant, "task")
	requestObject := insertObject(t, tx, f.tenant, "llm_request")
	argumentsObject := insertObject(t, tx, f.tenant, "tool_arguments")
	commit, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
	mustExec(t, tx, `INSERT INTO source_repositories (repository_id, tenant_id, remote_url)
		VALUES ($1, $2, 'https://example.test/repository.git')`, f.repository, f.tenant)
	mustExec(t, tx, `INSERT INTO source_sync_jobs
		(sync_id, tenant_id, repository_id, operation_id, config_snapshot_object_id, expected_archive_version, workflow_id)
		VALUES ($1, $2, $3, $4, $5, 0, $6)`, f.sync, f.tenant, f.repository, "sync/"+f.sync, configObject, "source/"+f.sync)
	mustExec(t, tx, `INSERT INTO source_ref_observations
		(tenant_id, repository_id, sync_id, ref_name, target_oid, peeled_commit_sha)
		VALUES ($1, $2, $3, 'refs/heads/main', $4, $4)`, f.tenant, f.repository, f.sync, commit)
	mustExec(t, tx, `INSERT INTO source_storage_manifests
		(manifest_id, tenant_id, repository_id, sync_id, operation_id, bundle_kind,
		bundle_object_id, manifest_object_id, status, published_at)
		VALUES ($1, $2, $3, $4, $5, 'base', $6, $7, 'published', now())`,
		f.manifest, f.tenant, f.repository, f.sync, "archive/"+f.sync, bundleObject, manifestObject)
	mustExec(t, tx, `UPDATE source_repositories SET archive_head_id = $1, archive_version = 1
		WHERE repository_id = $2`, f.manifest, f.repository)
	for _, snapshot := range []struct{ id, commit string }{{f.snapshot, commit}, {f.extraSnapshot, strings.Repeat("c", 40)}} {
		mustExec(t, tx, `INSERT INTO source_snapshots
			(snapshot_id, tenant_id, repository_id, commit_sha, tree_sha, storage_manifest_id, status, ready_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'ready', now())`,
			snapshot.id, f.tenant, f.repository, snapshot.commit, tree, f.manifest)
	}
	// SHA-256 repositories are accepted without truncating Git OIDs to SHA-1.
	f.otherRepository = newID(t)
	f.otherRepositorySnapshot = newID(t)
	mustExec(t, tx, `INSERT INTO source_repositories (repository_id, tenant_id, remote_url, object_format)
		VALUES ($1, $2, 'https://example.test/sha256.git', 'sha256')`, f.otherRepository, f.tenant)
	mustExec(t, tx, `INSERT INTO source_snapshots (snapshot_id, tenant_id, repository_id, commit_sha, tree_sha)
		VALUES ($1, $2, $3, $4, $5)`, f.otherRepositorySnapshot, f.tenant, f.otherRepository,
		strings.Repeat("d", 64), strings.Repeat("e", 64))
	mustExec(t, tx, `INSERT INTO agent_sessions
		(session_id, tenant_id, repository_id, default_source_snapshot_id, task_object_id)
		VALUES ($1, $2, $3, $4, $5)`, f.session, f.tenant, f.repository, f.snapshot, taskObject)
	mustExec(t, tx, `INSERT INTO agents
		(agent_id, tenant_id, repository_id, session_id, source_snapshot_id, task_object_id, workflow_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, f.agent, f.tenant, f.repository, f.session, f.snapshot, taskObject, "agent/"+f.agent)
	mustExec(t, tx, `INSERT INTO context_records
		(context_record_id, tenant_id, repository_id, agent_id, context_version, kind, object_id, from_event_sequence, through_event_sequence)
		VALUES ($1, $2, $3, $4, 1, 'snapshot', $5, 0, 0)`, f.contextRecord, f.tenant, f.repository, f.agent, f.contextObject)
	mustExec(t, tx, `INSERT INTO context_source_refs
		(tenant_id, repository_id, context_record_id, source_snapshot_id, role)
		VALUES ($1, $2, $3, $4, 'default')`, f.tenant, f.repository, f.contextRecord, f.snapshot)
	mustExec(t, tx, `INSERT INTO llm_executions
		(llm_execution_id, tenant_id, repository_id, agent_id, step_id, operation_id, context_version, model, request_object_id)
		VALUES ($1, $2, $3, $4, 1, $5, 1, 'test-model', $6)`, newID(t), f.tenant, f.repository, f.agent, "llm/"+f.agent, requestObject)
	mustExec(t, tx, `INSERT INTO tool_executions
		(tool_execution_id, tenant_id, repository_id, agent_id, step_id, tool_call_id, tool_name, operation_id, arguments_object_id, source_snapshot_id)
		VALUES ($1, $2, $3, $4, 1, 'call-1', 'call_child_agent', $5, $6, $7)`,
		f.tool, f.tenant, f.repository, f.agent, "tool/"+f.tool, argumentsObject, f.snapshot)
	mustExec(t, tx, `INSERT INTO agents
		(agent_id, tenant_id, repository_id, session_id, parent_agent_id, parent_tool_execution_id, source_snapshot_id, task_object_id, workflow_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		f.childAgent, f.tenant, f.repository, f.session, f.agent, f.tool, f.snapshot, taskObject, "agent/"+f.childAgent)
	mustExec(t, tx, `INSERT INTO context_records
		(context_record_id, tenant_id, repository_id, agent_id, context_version, kind, object_id, from_event_sequence, through_event_sequence)
		VALUES ($1, $2, $3, $4, 1, 'snapshot', $5, 0, 0)`,
		f.childContextRecord, f.tenant, f.repository, f.childAgent, insertObject(t, tx, f.tenant, "context_snapshot"))
	mustExec(t, tx, `INSERT INTO event_index
		(event_id, tenant_id, repository_id, agent_id, sequence, event_type, operation_id, segment_object_id, record_ordinal, source_snapshot_id)
		VALUES ($1, $2, $3, $4, 1, 'model_request', $5, $6, 0, $7)`,
		f.event, f.tenant, f.repository, f.agent, "llm/"+f.agent, f.segment, f.snapshot)
	mustExec(t, tx, `INSERT INTO agent_head
		(agent_id, tenant_id, repository_id, row_version, context_version, last_event_sequence, snapshot_record_id, source_snapshot_id, next_step_id)
		VALUES ($1, $2, $3, 2, 1, 1, $4, $5, 2)`, f.agent, f.tenant, f.repository, f.contextRecord, f.snapshot)
	mustExec(t, tx, `INSERT INTO compaction_jobs
		(compaction_id, tenant_id, repository_id, agent_id, operation_id, source_row_version, source_context_version, source_event_sequence)
		VALUES ($1, $2, $3, $4, $5, 2, 1, 1)`, f.compaction, f.tenant, f.repository, f.agent, "compact/"+f.agent)
	requestHash := sha256.Sum256([]byte("test-request"))
	mustExec(t, tx, `INSERT INTO idempotency_records (tenant_id, operation_id, operation_type, request_hash)
		VALUES ($1, $2, 'test', $3)`, f.tenant, "test/"+f.agent, requestHash[:])
	mustExec(t, tx, `INSERT INTO outbox
		(outbox_id, tenant_id, topic, aggregate_type, aggregate_id, dedupe_key, payload_object_id)
		VALUES ($1, $2, 'agent.started', 'agent', $3, $4, $5)`,
		f.outbox, f.tenant, f.agent, "started/"+f.agent, insertObject(t, tx, f.tenant, "outbox_payload"))
	return f
}

func newID(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

func insertObject(t *testing.T, tx pgx.Tx, tenant, kind string) string {
	t.Helper()
	id := newID(t)
	checksum := sha256.Sum256([]byte(kind))
	mustExec(t, tx, `INSERT INTO stored_objects
		(object_id, tenant_id, kind, bucket, object_key, checksum_sha256, size_bytes, status, verified_at)
		VALUES ($1, $2, $3, 'schema-tests', $4, $5, $6, 'verified', now())`,
		id, tenant, kind, tenant+"/"+id, checksum[:], len(kind))
	return id
}

func mustExec(t *testing.T, tx pgx.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(t.Context(), query, args...); err != nil {
		t.Fatalf("SQL failed: %v\n%s", err, query)
	}
}

func beginTx(t *testing.T, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func testConstraints(t *testing.T, pool *pgxpool.Pool, f fixture) {
	tests := []struct {
		name, code, query string
		args              []any
	}{
		{"cross tenant object", "23503", `UPDATE tool_executions SET result_object_id = $1 WHERE tool_execution_id = $2`,
			[]any{f.otherTenantObject, f.tool}},
		{"cross repository session resource", "23503", `UPDATE session_resource_bindings SET source_snapshot_id = $1 WHERE session_id = $2`,
			[]any{f.otherRepositorySnapshot, f.session}},
		{"cross repository archive head", "23503", `UPDATE source_repositories SET archive_head_id = $1, archive_version = 1
			WHERE repository_id = (SELECT repository_id FROM source_snapshots WHERE snapshot_id = $2)`,
			[]any{f.manifest, f.otherRepositorySnapshot}},
		{"duplicate commit", "23505", `INSERT INTO source_snapshots (snapshot_id, tenant_id, repository_id, commit_sha, tree_sha)
			SELECT $1, tenant_id, repository_id, commit_sha, tree_sha FROM source_snapshots WHERE snapshot_id = $2`,
			[]any{newID(t), f.snapshot}},
		{"ready without manifest", "23514", `UPDATE source_snapshots SET storage_manifest_id = NULL WHERE snapshot_id = $1`,
			[]any{f.snapshot}},
		{"mixed git hash formats", "23514", `UPDATE source_snapshots SET tree_sha = $1 WHERE snapshot_id = $2`,
			[]any{strings.Repeat("f", 64), f.snapshot}},
		{"deleted ref with target", "23514", `UPDATE source_ref_observations SET is_deleted = true WHERE sync_id = $1`,
			[]any{f.sync}},
		{"base manifest with parent", "23514", `UPDATE source_storage_manifests SET parent_manifest_id = $1 WHERE manifest_id = $1`,
			[]any{f.manifest}},
		{"archive version without head", "23514", `UPDATE source_repositories SET archive_head_id = NULL WHERE repository_id = $1`,
			[]any{f.repository}},
		{"one root per session", "23505", `UPDATE agents SET parent_agent_id = NULL, parent_tool_execution_id = NULL WHERE agent_id = $1`,
			[]any{f.childAgent}},
		{"parent tool must be paired", "23514", `UPDATE agents SET parent_tool_execution_id = NULL WHERE agent_id = $1`,
			[]any{f.childAgent}},
		{"head belongs to tenant", "23503", `UPDATE agent_head SET tenant_id = $1 WHERE agent_id = $2`,
			[]any{newID(t), f.agent}},
		{"head context coverage", "23514", `UPDATE agent_head SET context_event_sequence = 2 WHERE agent_id = $1`,
			[]any{f.agent}},
		{"head snapshot belongs to agent", "23503", `UPDATE agent_head SET snapshot_record_id = $1 WHERE agent_id = $2`,
			[]any{f.childContextRecord, f.agent}},
		{"delta base belongs to agent", "23503", `UPDATE context_records SET kind = 'delta', base_snapshot_record_id = $1
			WHERE context_record_id = $2`, []any{f.childContextRecord, f.contextRecord}},
		{"unique context version", "23505", `INSERT INTO context_records
			(context_record_id, tenant_id, agent_id, context_version, kind, object_id, from_event_sequence, through_event_sequence)
			SELECT $1, tenant_id, agent_id, context_version, kind, object_id, from_event_sequence, through_event_sequence
			FROM context_records WHERE context_record_id = $2`, []any{newID(t), f.contextRecord}},
		{"one default source per repository in context", "23505", `INSERT INTO context_source_refs
			(tenant_id, repository_id, context_record_id, source_snapshot_id, role) VALUES ($1, $2, $3, $4, 'default')`,
			[]any{f.tenant, f.repository, f.contextRecord, f.extraSnapshot}},
		{"event sequence deduplication", "23505", `INSERT INTO event_index
			(event_id, tenant_id, repository_id, agent_id, sequence, event_type, operation_id, segment_object_id, record_ordinal)
			SELECT $1, tenant_id, repository_id, agent_id, sequence, event_type, operation_id, segment_object_id, 1
			FROM event_index WHERE event_id = $2`, []any{newID(t), f.event}},
		{"event segment ordinal deduplication", "23505", `INSERT INTO event_index
			(event_id, tenant_id, repository_id, agent_id, sequence, event_type, operation_id, segment_object_id, record_ordinal)
			SELECT $1, tenant_id, repository_id, agent_id, 2, event_type, operation_id, segment_object_id, record_ordinal
			FROM event_index WHERE event_id = $2`, []any{newID(t), f.event}},
		{"tool call deduplication", "23505", `INSERT INTO tool_executions
			(tool_execution_id, tenant_id, repository_id, agent_id, step_id, tool_call_id, tool_name, operation_id, arguments_object_id)
			SELECT $1, tenant_id, repository_id, agent_id, step_id, tool_call_id, tool_name, $2, arguments_object_id
			FROM tool_executions WHERE tool_execution_id = $3`, []any{newID(t), "duplicate/" + f.tool, f.tool}},
		{"saved model response requires object", "23514", `UPDATE llm_executions SET status = 'response_saved' WHERE agent_id = $1`,
			[]any{f.agent}},
		{"diff requires two snapshots", "23514", `UPDATE tool_executions SET base_snapshot_id = $1 WHERE tool_execution_id = $2`,
			[]any{f.snapshot, f.tool}},
		{"diff uses one repository", "23503", `UPDATE tool_executions SET base_snapshot_id = $1, target_snapshot_id = $2
			WHERE tool_execution_id = $3`, []any{f.snapshot, f.otherRepositorySnapshot, f.tool}},
		{"compact publication requires saved output", "23514", `UPDATE compaction_jobs SET status = 'published' WHERE compaction_id = $1`,
			[]any{f.compaction}},
		{"idempotency hash is SHA256", "23514", `UPDATE idempotency_records SET request_hash = $1 WHERE tenant_id = $2`,
			[]any{[]byte{1}, f.tenant}},
		{"running operation requires lease", "23514", `UPDATE idempotency_records SET status = 'running' WHERE tenant_id = $1`,
			[]any{f.tenant}},
		{"outbox processing requires lease", "23514", `UPDATE outbox SET status = 'processing' WHERE outbox_id = $1`,
			[]any{f.outbox}},
		{"published outbox requires timestamp", "23514", `UPDATE outbox SET status = 'published' WHERE outbox_id = $1`,
			[]any{f.outbox}},
		{"verified object requires checksum", "23514", `UPDATE stored_objects SET checksum_sha256 = NULL WHERE object_id = $1`,
			[]any{f.contextObject}},
		{"immutable object key is unique", "23505", `INSERT INTO stored_objects (object_id, tenant_id, kind, bucket, object_key)
			SELECT $1, tenant_id, kind, bucket, object_key FROM stored_objects WHERE object_id = $2`,
			[]any{newID(t), f.contextObject}},
		{"retained archive cannot be deleted", "23503", `DELETE FROM source_storage_manifests WHERE manifest_id = $1`,
			[]any{f.manifest}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := beginTx(t, pool)
			expectPGError(t, tx, tt.code, tt.query, tt.args...)
		})
	}
	t.Run("one active source sync", func(t *testing.T) {
		tx := beginTx(t, pool)
		mustExec(t, tx, `UPDATE source_sync_jobs SET status = 'running', started_at = now() WHERE sync_id = $1`, f.sync)
		expectPGError(t, tx, "23505", `INSERT INTO source_sync_jobs
			(sync_id, tenant_id, repository_id, operation_id, config_snapshot_object_id, expected_archive_version, workflow_id, status, started_at)
			SELECT $1, tenant_id, repository_id, $2, config_snapshot_object_id, expected_archive_version, $3, 'running', now()
			FROM source_sync_jobs WHERE sync_id = $4`, newID(t), "second/"+f.sync, "second-workflow/"+f.sync, f.sync)
	})
	t.Run("valid increment and deleted ref", func(t *testing.T) {
		tx := beginTx(t, pool)
		mustExec(t, tx, `INSERT INTO source_storage_manifests
			(manifest_id, tenant_id, repository_id, sync_id, operation_id, parent_manifest_id, bundle_kind,
			bundle_object_id, manifest_object_id, prerequisite_commit_shas)
			VALUES ($1, $2, $3, $4, $5, $6, 'increment', $7, $8, $9)`, newID(t), f.tenant, f.repository,
			f.sync, "increment/"+f.sync, f.manifest, insertObject(t, tx, f.tenant, "git_bundle"),
			insertObject(t, tx, f.tenant, "git_manifest"), []string{strings.Repeat("a", 40)})
		mustExec(t, tx, `INSERT INTO source_ref_observations
			(tenant_id, repository_id, sync_id, ref_name, is_deleted) VALUES ($1, $2, $3, 'refs/heads/deleted', true)`,
			f.tenant, f.repository, f.sync)
		mustExec(t, tx, `INSERT INTO context_records
			(context_record_id, tenant_id, agent_id, context_version, kind, object_id,
			base_snapshot_record_id, from_event_sequence, through_event_sequence)
			VALUES ($1, $2, $3, 2, 'delta', $4, $5, 1, 1)`,
			newID(t), f.tenant, f.agent, f.deltaObject, f.contextRecord)
	})
}

func expectPGError(t *testing.T, tx pgx.Tx, code, query string, args ...any) {
	t.Helper()
	_, err := tx.Exec(t.Context(), query, args...)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("got %v, want PostgreSQL error %s", err, code)
	}
}

func testHeadCAS(t *testing.T, pool *pgxpool.Pool, f fixture) {
	tx := beginTx(t, pool)
	query := `UPDATE agent_head SET row_version = row_version + 1, last_event_sequence = 2, updated_at = now()
		WHERE tenant_id = $1 AND agent_id = $2 AND row_version = 2`
	for _, want := range []int64{1, 0} {
		result, err := tx.Exec(t.Context(), query, f.tenant, f.agent)
		if err != nil || result.RowsAffected() != want {
			t.Fatalf("CAS rows=%d, want %d, err=%v", result.RowsAffected(), want, err)
		}
	}
	var rowVersion, contextVersion, covered, nextStep int64
	err := tx.QueryRow(t.Context(), `SELECT row_version, context_version, context_event_sequence, next_step_id
		FROM agent_head WHERE agent_id = $1`, f.agent).Scan(&rowVersion, &contextVersion, &covered, &nextStep)
	if err != nil || rowVersion != 3 || contextVersion != 1 || covered != 0 || nextStep != 2 {
		t.Fatalf("head changed unexpectedly: row=%d context=%d covered=%d next=%d err=%v", rowVersion, contextVersion, covered, nextStep, err)
	}
}

func testOutboxLocks(t *testing.T, pool *pgxpool.Pool, f fixture) {
	first, second := beginTx(t, pool), beginTx(t, pool)
	query := `SELECT outbox_id FROM outbox WHERE tenant_id = $1 AND status = 'pending'
		AND available_at <= now() ORDER BY available_at, outbox_id LIMIT 1 FOR UPDATE SKIP LOCKED`
	var id string
	if err := first.QueryRow(t.Context(), query, f.tenant).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := second.QueryRow(t.Context(), query, f.tenant).Scan(&id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second reader should skip the locked row, got %v", err)
	}
	mustExec(t, first, `UPDATE outbox SET status = 'processing', lease_owner = 'test-relay',
		lease_until = now() + interval '1 minute', fencing_token = 1, attempt_count = 1 WHERE outbox_id = $1`, f.outbox)
	mustExec(t, first, `UPDATE outbox SET status = 'published', published_at = now(),
		lease_owner = NULL, lease_until = NULL WHERE outbox_id = $1 AND fencing_token = 1`, f.outbox)
}
