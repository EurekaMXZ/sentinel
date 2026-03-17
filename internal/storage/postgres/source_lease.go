package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"sentinel/internal/source"
)

func (s *SourceStore) ClaimSync(ctx context.Context, request source.SyncRequest, owner string, lease time.Duration) (source.SyncJob, source.Repository, error) {
	if owner == "" || lease < time.Second {
		return source.SyncJob{}, source.Repository{}, errors.New("invalid source lease")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return source.SyncJob{}, source.Repository{}, err
	}
	defer tx.Rollback(ctx)
	repo, err := scanRepository(tx.QueryRow(ctx, `SELECT `+repositoryColumns+` FROM source_repositories
		WHERE tenant_id=$1 AND repository_id=$2 FOR UPDATE`, request.TenantID, request.RepositoryID))
	if err != nil {
		return source.SyncJob{}, repo, err
	}
	job, err := loadSync(ctx, tx, request, true)
	if err != nil {
		return job, repo, err
	}
	if job.Status == "succeeded" {
		return job, repo, tx.Commit(ctx)
	}
	if job.Status == "failed" || job.Status == "cancelled" || repo.Status != "active" {
		return job, repo, source.ErrTerminal
	}
	var activeID, activeOperation string
	var live bool
	err = tx.QueryRow(ctx, `SELECT j.sync_id::text,j.operation_id,COALESCE(i.lease_until>clock_timestamp(),false)
		FROM source_sync_jobs j LEFT JOIN idempotency_records i ON i.tenant_id=j.tenant_id AND i.operation_id=j.operation_id
		WHERE j.tenant_id=$1 AND j.repository_id=$2 AND j.status IN ('running','archiving','publishing')`,
		request.TenantID, request.RepositoryID).Scan(&activeID, &activeOperation, &live)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return job, repo, err
	}
	if live {
		return job, repo, source.ErrBusy
	}
	if activeID != "" && activeID != request.SyncID {
		// Requeue an abandoned sync. Its old worker loses publication rights even
		// if it is still fetching when another process takes over this repository.
		if _, err := tx.Exec(ctx, `UPDATE source_sync_jobs SET status='queued',updated_at=now()
			WHERE tenant_id=$1 AND sync_id=$2`, request.TenantID, activeID); err != nil {
			return job, repo, err
		}
		if _, err := tx.Exec(ctx, `UPDATE idempotency_records SET status='pending',lease_owner=NULL,lease_until=NULL,updated_at=now()
			WHERE tenant_id=$1 AND operation_id=$2`, request.TenantID, activeOperation); err != nil {
			return job, repo, err
		}
	}
	err = tx.QueryRow(ctx, `UPDATE idempotency_records SET status='running',lease_owner=$3,
		lease_until=clock_timestamp()+$4*interval '1 millisecond',fencing_token=fencing_token+1,
		attempt_count=attempt_count+1,updated_at=now() WHERE tenant_id=$1 AND operation_id=$2
		RETURNING fencing_token`, request.TenantID, request.OperationID, owner, lease.Milliseconds()).Scan(&job.Fence)
	if err != nil {
		return job, repo, err
	}
	_, err = tx.Exec(ctx, `UPDATE source_sync_jobs SET status=CASE WHEN result_object_id IS NULL THEN 'running' ELSE 'publishing' END,
		started_at=COALESCE(started_at,now()),attempt_count=attempt_count+1,updated_at=now()
		WHERE tenant_id=$1 AND sync_id=$2`, request.TenantID, request.SyncID)
	if err != nil {
		return job, repo, err
	}
	job.Owner, job.Status = owner, "running"
	return job, repo, tx.Commit(ctx)
}

func (s *SourceStore) RenewSync(ctx context.Context, j source.SyncJob, lease time.Duration) error {
	result, err := s.pool.Exec(ctx, `UPDATE idempotency_records SET lease_until=clock_timestamp()+$5*interval '1 millisecond',updated_at=now()
		WHERE tenant_id=$1 AND operation_id=$2 AND status='running' AND lease_owner=$3 AND fencing_token=$4
		AND lease_until>clock_timestamp()`, j.Request.TenantID, j.Request.OperationID, j.Owner, j.Fence, lease.Milliseconds())
	if err == nil && result.RowsAffected() != 1 {
		return source.ErrLeaseLost
	}
	return err
}

// lockSource always locks repository before job/operation. Publication checks
// the lease again inside the transaction, independently of heartbeat timing.
func lockSource(ctx context.Context, tx pgx.Tx, j source.SyncJob) (source.Repository, source.SyncJob, error) {
	r := j.Request
	repo, err := scanRepository(tx.QueryRow(ctx, `SELECT `+repositoryColumns+` FROM source_repositories
		WHERE tenant_id=$1 AND repository_id=$2 FOR UPDATE`, r.TenantID, r.RepositoryID))
	if err != nil {
		return repo, j, err
	}
	current, err := loadSync(ctx, tx, r, true)
	if err != nil {
		return repo, current, err
	}
	var live bool
	err = tx.QueryRow(ctx, `SELECT status='running' AND lease_until>clock_timestamp() FROM idempotency_records
		WHERE tenant_id=$1 AND operation_id=$2`, r.TenantID, r.OperationID).Scan(&live)
	if err == nil && (!live || current.Owner != j.Owner || current.Fence != j.Fence || repo.Status != "active") {
		err = source.ErrLeaseLost
	}
	return repo, current, err
}

func (s *SourceStore) FinishSync(ctx context.Context, request source.SyncRequest, status, errorRef string) error {
	if status != "failed" && status != "cancelled" {
		return errors.New("invalid terminal sync status")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := scanRepository(tx.QueryRow(ctx, `SELECT `+repositoryColumns+` FROM source_repositories
		WHERE tenant_id=$1 AND repository_id=$2 FOR UPDATE`, request.TenantID, request.RepositoryID)); err != nil {
		return err
	}
	job, err := loadSync(ctx, tx, request, true)
	if err != nil {
		return err
	}
	if job.Status == "succeeded" || job.Status == "failed" || job.Status == "cancelled" {
		return tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `UPDATE source_sync_jobs SET status=$3,error_object_id=NULLIF($4,'')::uuid,
		finished_at=now(),updated_at=now() WHERE tenant_id=$1 AND sync_id=$2`, request.TenantID, request.SyncID, status, errorRef)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE idempotency_records SET status='failed',lease_owner=NULL,lease_until=NULL,updated_at=now()
			WHERE tenant_id=$1 AND operation_id=$2`, request.TenantID, request.OperationID)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
