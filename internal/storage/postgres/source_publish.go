package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"sentinel/internal/source"
)

func verifiedSourceObject(ctx context.Context, tx pgx.Tx, tenant, ref, kind string) error {
	var verified bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stored_objects WHERE tenant_id=$1 AND object_id=$2
		AND kind=$3 AND status='verified')`, tenant, ref, kind).Scan(&verified)
	if err == nil && !verified {
		return errors.New("source object is not verified for this tenant and purpose")
	}
	return err
}

func (s *SourceStore) SaveCandidate(ctx context.Context, j source.SyncJob, c source.Candidate, ref string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	repo, _, err := lockSource(ctx, tx, j)
	if err != nil {
		return err
	}
	if c.Request != j.Request || c.ArchiveAttempt != j.Fence {
		return source.ErrConflict
	}
	if err := source.ValidateCandidate(c, repo); err != nil {
		return err
	}
	if err := verifiedSourceObject(ctx, tx, j.Request.TenantID, ref, "source-result"); err != nil {
		return err
	}
	if c.NewArchive {
		for _, object := range []struct{ ref, kind string }{{c.Manifest.BundleRef, "git_bundle"}, {c.ManifestRef, "git-manifest"}} {
			if err := verifiedSourceObject(ctx, tx, j.Request.TenantID, object.ref, object.kind); err != nil {
				return err
			}
		}
		m := c.Manifest
		prerequisites := m.PrerequisiteCommitSHAs
		if prerequisites == nil {
			prerequisites = []string{}
		}
		_, err = tx.Exec(ctx, `INSERT INTO source_storage_manifests
			(manifest_id,tenant_id,repository_id,sync_id,operation_id,archive_attempt,parent_manifest_id,bundle_kind,
			 bundle_object_id,manifest_object_id,prerequisite_commit_shas,status,created_at)
			VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,$8,$9,$10,$11,'verified',$12) ON CONFLICT (manifest_id) DO NOTHING`,
			m.ID, j.Request.TenantID, repo.ID, j.Request.SyncID, j.Request.OperationID, c.ArchiveAttempt,
			m.ParentManifestID, m.BundleKind, m.BundleRef, c.ManifestRef, prerequisites, m.CreatedAt)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE source_sync_jobs SET status='publishing',result_object_id=$3,
		expected_archive_version=$4,updated_at=now() WHERE tenant_id=$1 AND sync_id=$2`,
		j.Request.TenantID, j.Request.SyncID, ref, c.ExpectedArchiveVersion)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *SourceStore) Publish(ctx context.Context, j source.SyncJob, c source.Candidate, ref string) (source.SyncResult, error) {
	result := source.SyncResult{RepositoryID: j.Request.RepositoryID, SyncID: j.Request.SyncID,
		ManifestID: c.Manifest.ID, ResultRef: ref, SnapshotCount: len(c.Snapshots)}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	repo, current, err := lockSource(ctx, tx, j)
	if err != nil {
		return result, err
	}
	if current.Status != "publishing" || current.ResultRef != ref || c.Request != j.Request {
		return result, source.ErrConflict
	}
	if err := source.ValidateCandidate(c, repo); err != nil {
		return result, err
	}
	if c.NewArchive {
		tag, err := tx.Exec(ctx, `UPDATE source_storage_manifests SET status='published',published_at=clock_timestamp(),updated_at=now()
			WHERE tenant_id=$1 AND repository_id=$2 AND manifest_id=$3 AND sync_id=$4 AND status='verified'
			AND archive_attempt=$5 AND bundle_object_id=$6 AND manifest_object_id=$7
			AND parent_manifest_id IS NOT DISTINCT FROM NULLIF($8,'')::uuid`,
			j.Request.TenantID, repo.ID, c.Manifest.ID, j.Request.SyncID, c.ArchiveAttempt, c.Manifest.BundleRef, c.ManifestRef, repo.ArchiveHeadID)
		if err != nil {
			return result, err
		}
		if tag.RowsAffected() != 1 {
			return result, source.ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE source_repositories SET archive_head_id=$3,archive_version=archive_version+1,updated_at=now()
			WHERE tenant_id=$1 AND repository_id=$2 AND archive_version=$4`, j.Request.TenantID, repo.ID, c.Manifest.ID, c.ExpectedArchiveVersion)
		if err != nil {
			return result, err
		}
	}
	batch := &pgx.Batch{}
	for _, snapshot := range c.Snapshots {
		batch.Queue(`INSERT INTO source_snapshots (snapshot_id,tenant_id,repository_id,commit_sha,tree_sha,storage_manifest_id,status,ready_at,created_at)
			VALUES ($1,$2,$3,$4,$5,$6,'ready',clock_timestamp(),$7) ON CONFLICT (tenant_id,repository_id,commit_sha)
			DO UPDATE SET storage_manifest_id=EXCLUDED.storage_manifest_id,status='ready',ready_at=EXCLUDED.ready_at,updated_at=now()
			WHERE source_snapshots.status<>'ready' AND source_snapshots.tree_sha=EXCLUDED.tree_sha`,
			snapshot.ID, j.Request.TenantID, repo.ID, snapshot.CommitSHA, snapshot.TreeSHA, c.Manifest.ID, snapshot.CreatedAt)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return result, err
	}
	names := make([]string, 0, len(c.Refs))
	batch = &pgx.Batch{}
	for _, observation := range c.Refs {
		names = append(names, observation.Name)
		batch.Queue(`INSERT INTO source_ref_observations (tenant_id,repository_id,sync_id,ref_name,target_oid,peeled_commit_sha,observed_at)
			VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),clock_timestamp())`,
			j.Request.TenantID, repo.ID, j.Request.SyncID, observation.Name, observation.TargetOID, observation.CommitSHA)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO source_ref_observations (tenant_id,repository_id,sync_id,ref_name,is_deleted,observed_at)
		SELECT $1,$2,$3,ref_name,true,clock_timestamp() FROM
		(SELECT DISTINCT ON (ref_name) ref_name,is_deleted FROM source_ref_observations
		 WHERE tenant_id=$1 AND repository_id=$2 ORDER BY ref_name,observed_at DESC,sync_id DESC) latest
		WHERE NOT is_deleted AND NOT (ref_name=ANY($4::text[]))`, j.Request.TenantID, repo.ID, j.Request.SyncID, names)
	if err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `UPDATE source_sync_jobs SET status='succeeded',finished_at=clock_timestamp(),updated_at=now()
		WHERE tenant_id=$1 AND sync_id=$2`, j.Request.TenantID, j.Request.SyncID)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE idempotency_records SET status='succeeded',result_object_id=$3,lease_owner=NULL,lease_until=NULL,updated_at=now()
			WHERE tenant_id=$1 AND operation_id=$2`, j.Request.TenantID, j.Request.OperationID, ref)
	}
	if err != nil {
		return result, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (outbox_id,tenant_id,topic,aggregate_type,aggregate_id,dedupe_key,payload_object_id)
		VALUES ($1,$2,'source.synced','source_repository',$3,$4,$5) ON CONFLICT (tenant_id,dedupe_key) DO NOTHING`,
		id.String(), j.Request.TenantID, repo.ID, j.Request.OperationID+"/published", ref)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
