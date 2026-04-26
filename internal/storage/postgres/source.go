package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"sentinel/internal/source"
)

type SourceStore struct{ pool *pgxpool.Pool }

func NewSourceStore(pool *pgxpool.Pool) *SourceStore { return &SourceStore{pool: pool} }

var _ source.ReadStore = (*SourceStore)(nil)

const repositoryColumns = `repository_id::text, tenant_id::text, remote_url, COALESCE(credential_ref,''),
	tracked_refs, object_format, COALESCE(archive_head_id::text,''), archive_version, status`

func scanRepository(row pgx.Row) (source.Repository, error) {
	var r source.Repository
	err := row.Scan(&r.ID, &r.TenantID, &r.RemoteURL, &r.CredentialRef, &r.TrackedRefs,
		&r.ObjectFormat, &r.ArchiveHeadID, &r.ArchiveVersion, &r.Status)
	return r, sourceError(err)
}

func sourceError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return source.ErrNotFound
	}
	return err
}

func (s *SourceStore) CreateRepository(ctx context.Context, r source.Repository) (source.Repository, error) {
	if err := source.ValidateRepository(r); err != nil {
		return source.Repository{}, err
	}
	repository, err := scanRepository(s.pool.QueryRow(ctx, `INSERT INTO source_repositories
		(repository_id,tenant_id,remote_url,credential_ref,tracked_refs,object_format)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6) RETURNING `+repositoryColumns,
		r.ID, r.TenantID, r.RemoteURL, r.CredentialRef, r.TrackedRefs, r.ObjectFormat))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return source.Repository{}, source.ErrConflict
	}
	return repository, err
}

func (s *SourceStore) UpdateRepository(ctx context.Context, r source.Repository) (source.Repository, error) {
	if err := source.ValidateRepository(r); err != nil {
		return source.Repository{}, err
	}
	return scanRepository(s.pool.QueryRow(ctx, `UPDATE source_repositories
		SET tracked_refs=$3,credential_ref=NULLIF($4,''),status=$5,updated_at=now()
		WHERE tenant_id=$1 AND repository_id=$2 RETURNING `+repositoryColumns,
		r.TenantID, r.ID, r.TrackedRefs, r.CredentialRef, r.Status))
}

func (s *SourceStore) ListRefs(ctx context.Context, tenantID, repositoryID string, limit int) ([]source.RefRecord, error) {
	if limit <= 0 || limit > 500 {
		return nil, errors.New("invalid ref page limit")
	}
	rows, err := s.pool.Query(ctx, `WITH latest AS (
			SELECT DISTINCT ON (ref_name) ref_name,target_oid,peeled_commit_sha,observed_at,sync_id,is_deleted
			FROM source_ref_observations
			WHERE tenant_id=$1 AND repository_id=$2
			ORDER BY ref_name,observed_at DESC,sync_id DESC
		)
		SELECT l.ref_name,COALESCE(l.target_oid,''),COALESCE(l.peeled_commit_sha,''),
			l.sync_id::text,l.observed_at,COALESCE(s.snapshot_id::text,''),COALESCE(s.status,'')
		FROM latest l
		LEFT JOIN source_snapshots s ON s.tenant_id=$1 AND s.repository_id=$2
			AND s.commit_sha=l.peeled_commit_sha
		WHERE NOT l.is_deleted
		ORDER BY l.ref_name LIMIT $3`, tenantID, repositoryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make([]source.RefRecord, 0, limit)
	for rows.Next() {
		var ref source.RefRecord
		if err := rows.Scan(&ref.Name, &ref.TargetOID, &ref.CommitSHA, &ref.SyncID,
			&ref.ObservedAt, &ref.SnapshotID, &ref.SnapshotStatus); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (s *SourceStore) ListSyncs(ctx context.Context, tenantID, repositoryID string, limit int) ([]source.SyncRecord, error) {
	if limit <= 0 || limit > 200 {
		return nil, errors.New("invalid sync page limit")
	}
	rows, err := s.pool.Query(ctx, `SELECT sync_id::text,status,trigger_type,created_at,
		started_at,finished_at,COALESCE(result_object_id::text,''),COALESCE(error_object_id::text,'')
		FROM source_sync_jobs
		WHERE tenant_id=$1 AND repository_id=$2
		ORDER BY created_at DESC,sync_id DESC LIMIT $3`, tenantID, repositoryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	syncs := make([]source.SyncRecord, 0, limit)
	for rows.Next() {
		var record source.SyncRecord
		var started, finished pgtype.Timestamptz
		if err := rows.Scan(&record.SyncID, &record.Status, &record.TriggerType, &record.CreatedAt,
			&started, &finished, &record.ResultRef, &record.ErrorRef); err != nil {
			return nil, err
		}
		if started.Valid {
			value := started.Time
			record.StartedAt = &value
		}
		if finished.Valid {
			value := finished.Time
			record.FinishedAt = &value
		}
		syncs = append(syncs, record)
	}
	return syncs, rows.Err()
}

func (s *SourceStore) LoadRepository(ctx context.Context, tenant, repository string) (source.Repository, error) {
	return scanRepository(s.pool.QueryRow(ctx, `SELECT `+repositoryColumns+` FROM source_repositories
		WHERE tenant_id=$1 AND repository_id=$2`, tenant, repository))
}

func (s *SourceStore) ListRepositories(ctx context.Context, tenant string, limit int) ([]source.Repository, error) {
	if limit <= 0 || limit > 200 {
		return nil, errors.New("invalid repository page limit")
	}
	rows, err := s.pool.Query(ctx, `SELECT `+repositoryColumns+` FROM source_repositories
		WHERE tenant_id=$1 ORDER BY created_at DESC,repository_id DESC LIMIT $2`, tenant, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repositories := make([]source.Repository, 0, limit)
	for rows.Next() {
		repository, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

const syncColumns = `j.tenant_id::text,j.repository_id::text,j.sync_id::text,j.operation_id,
	j.config_snapshot_object_id::text,j.workflow_id,j.status,j.expected_archive_version,
	COALESCE(j.result_object_id::text,''),COALESCE(j.error_object_id::text,''),
	COALESCE(i.lease_owner,''),i.fencing_token,i.request_hash`

func scanSync(row pgx.Row) (source.SyncJob, error) {
	var j source.SyncJob
	err := row.Scan(&j.Request.TenantID, &j.Request.RepositoryID, &j.Request.SyncID, &j.Request.OperationID,
		&j.ConfigRef, &j.WorkflowID, &j.Status, &j.ExpectedArchiveVersion,
		&j.ResultRef, &j.ErrorRef, &j.Owner, &j.Fence, &j.RequestHash)
	return j, sourceError(err)
}

func loadSync(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, r source.SyncRequest, lock bool) (source.SyncJob, error) {
	sql := `SELECT ` + syncColumns + ` FROM source_sync_jobs j JOIN idempotency_records i
		ON i.tenant_id=j.tenant_id AND i.operation_id=j.operation_id
		WHERE j.tenant_id=$1 AND j.sync_id=$2 AND ($3='' OR j.repository_id::text=$3)`
	if lock {
		sql += ` FOR UPDATE OF j,i`
	}
	job, err := scanSync(q.QueryRow(ctx, sql, r.TenantID, r.SyncID, r.RepositoryID))
	if err == nil && r.OperationID != "" && r.OperationID != job.Request.OperationID {
		return source.SyncJob{}, source.ErrConflict
	}
	return job, err
}

func (s *SourceStore) LoadSync(ctx context.Context, r source.SyncRequest) (source.SyncJob, error) {
	return loadSync(ctx, s.pool, r, false)
}

func (s *SourceStore) CreateSync(ctx context.Context, j source.SyncJob) (source.SyncJob, error) {
	r := j.Request
	if !source.ValidID(r.TenantID) || !source.ValidID(r.RepositoryID) || !source.ValidID(r.SyncID) ||
		r.OperationID != source.OperationID(r.RepositoryID, r.SyncID) || j.WorkflowID != source.WorkflowID(r.TenantID, r.SyncID) || !source.ValidID(j.ConfigRef) {
		return source.SyncJob{}, errors.New("invalid source sync identity")
	}
	encoded, err := json.Marshal(struct {
		Request source.SyncRequest
		Config  string
	}{r, j.ConfigRef})
	if err != nil {
		return source.SyncJob{}, err
	}
	hash := sha256.Sum256(encoded)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return source.SyncJob{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO source_sync_jobs
		(sync_id,tenant_id,repository_id,operation_id,config_snapshot_object_id,expected_archive_version,workflow_id)
		SELECT $1,$2,$3,$4,o.object_id,r.archive_version,$6 FROM source_repositories r,stored_objects o
		WHERE r.tenant_id=$2 AND r.repository_id=$3 AND r.status='active'
		AND o.tenant_id=$2 AND o.object_id=$5 AND o.status='verified' AND o.kind='source-config'
		ON CONFLICT (sync_id) DO NOTHING`, r.SyncID, r.TenantID, r.RepositoryID, r.OperationID, j.ConfigRef, j.WorkflowID)
	if err != nil {
		return source.SyncJob{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO idempotency_records (tenant_id,operation_id,operation_type,request_hash)
		VALUES ($1,$2,'source_sync',$3) ON CONFLICT (tenant_id,operation_id) DO NOTHING`, r.TenantID, r.OperationID, hash[:])
	if err != nil {
		return source.SyncJob{}, err
	}
	saved, err := loadSync(ctx, tx, r, false)
	if err != nil {
		return source.SyncJob{}, err
	}
	if saved.ConfigRef != j.ConfigRef || string(saved.RequestHash) != string(hash[:]) || saved.WorkflowID != j.WorkflowID {
		return source.SyncJob{}, source.ErrConflict
	}
	return saved, tx.Commit(ctx)
}

func (s *SourceStore) LoadManifest(ctx context.Context, tenant, repository, id string) (source.ManifestRecord, error) {
	var record source.ManifestRecord
	m := &record.Manifest
	var checksum []byte
	err := s.pool.QueryRow(ctx, `SELECT m.manifest_id::text,m.repository_id::text,COALESCE(m.parent_manifest_id::text,''),
		m.bundle_kind,m.bundle_object_id::text,o.checksum_sha256,m.prerequisite_commit_shas,m.operation_id,m.created_at,m.manifest_object_id::text
		FROM source_storage_manifests m JOIN stored_objects o ON o.tenant_id=m.tenant_id AND o.object_id=m.bundle_object_id
		WHERE m.tenant_id=$1 AND m.repository_id=$2 AND m.manifest_id=$3 AND m.status='published' AND o.status='verified'`,
		tenant, repository, id).Scan(&m.ID, &m.RepositoryID, &m.ParentManifestID, &m.BundleKind, &m.BundleRef,
		&checksum, &m.PrerequisiteCommitSHAs, &m.OperationID, &m.CreatedAt, &record.ObjectRef)
	m.BundleChecksum = hex.EncodeToString(checksum)
	return record, sourceError(err)
}

const snapshotColumns = `snapshot_id::text,repository_id::text,commit_sha,tree_sha,
	COALESCE(storage_manifest_id::text,''),status,created_at`

func scanSnapshot(row pgx.Row) (source.Snapshot, error) {
	var snapshot source.Snapshot
	err := row.Scan(&snapshot.ID, &snapshot.RepositoryID, &snapshot.CommitSHA, &snapshot.TreeSHA,
		&snapshot.StorageManifestID, &snapshot.Status, &snapshot.CreatedAt)
	return snapshot, sourceError(err)
}

func (s *SourceStore) LoadSnapshot(ctx context.Context, tenant, repository, snapshotID string) (source.Snapshot, error) {
	return scanSnapshot(s.pool.QueryRow(ctx, `SELECT `+snapshotColumns+` FROM source_snapshots
		WHERE tenant_id=$1 AND repository_id=$2 AND snapshot_id=$3`, tenant, repository, snapshotID))
}

func (s *SourceStore) ListReadySnapshots(ctx context.Context, tenant, repository string, offset, limit int) ([]source.Snapshot, bool, error) {
	if offset < 0 || limit <= 0 {
		return nil, false, errors.New("invalid snapshot page")
	}
	rows, err := s.pool.Query(ctx, `SELECT `+snapshotColumns+` FROM source_snapshots
		WHERE tenant_id=$1 AND repository_id=$2 AND status='ready'
		ORDER BY ready_at DESC,snapshot_id DESC LIMIT $3 OFFSET $4`, tenant, repository, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	snapshots := make([]source.Snapshot, 0, limit)
	for rows.Next() {
		var snapshot source.Snapshot
		if err := rows.Scan(&snapshot.ID, &snapshot.RepositoryID, &snapshot.CommitSHA, &snapshot.TreeSHA,
			&snapshot.StorageManifestID, &snapshot.Status, &snapshot.CreatedAt); err != nil {
			return nil, false, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(snapshots) > limit
	if more {
		snapshots = snapshots[:limit]
	}
	return snapshots, more, nil
}

func (s *SourceStore) KnownCommits(ctx context.Context, tenant, repository string) (map[string]source.Snapshot, error) {
	rows, err := s.pool.Query(ctx, `SELECT snapshot_id::text,repository_id::text,commit_sha,tree_sha,
		COALESCE(storage_manifest_id::text,''),status,created_at FROM source_snapshots WHERE tenant_id=$1 AND repository_id=$2`, tenant, repository)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]source.Snapshot)
	for rows.Next() {
		var snapshot source.Snapshot
		if err := rows.Scan(&snapshot.ID, &snapshot.RepositoryID, &snapshot.CommitSHA, &snapshot.TreeSHA,
			&snapshot.StorageManifestID, &snapshot.Status, &snapshot.CreatedAt); err != nil {
			return nil, err
		}
		result[snapshot.CommitSHA] = snapshot
		if len(result) > 100000 {
			return nil, errors.New("repository exceeds supported snapshot count")
		}
	}
	return result, rows.Err()
}
