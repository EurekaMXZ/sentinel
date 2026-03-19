package postgres

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"sentinel/internal/agent"
)

const selectCompact = `SELECT c.compaction_id::text,c.operation_id,c.source_row_version,c.source_context_version,
	c.source_event_sequence,COALESCE(c.request_object_id::text,''),COALESCE(c.response_object_id::text,''),
	COALESCE(c.candidate_snapshot_object_id::text,''),c.status,COALESCE(i.lease_owner,''),i.fencing_token,i.request_hash
	FROM compaction_jobs c JOIN idempotency_records i ON i.tenant_id=c.tenant_id AND i.operation_id=c.operation_id
	WHERE c.tenant_id=$1 AND c.agent_id=$2 AND c.source_row_version=$3`

func scanCompact(row pgx.Row, input agent.CompactInput) (agent.CompactRecord, error) {
	record := agent.CompactRecord{Input: input.Session}
	err := row.Scan(&record.ID, &record.OperationID, &record.SourceRowVersion, &record.SourceContextVersion,
		&record.SourceEventSequence, &record.RequestRef, &record.ResponseRef, &record.CandidateRef,
		&record.Status, &record.LeaseOwner, &record.FencingToken, &record.RequestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		err = agent.ErrNotFound
	}
	return record, err
}

func (s *AgentStore) LoadCompact(ctx context.Context, input agent.CompactInput) (agent.CompactRecord, error) {
	return scanCompact(s.pool.QueryRow(ctx, selectCompact, input.Session.TenantID, input.Session.AgentID, input.RowVersion), input)
}

func (s *AgentStore) PrepareCompact(ctx context.Context, record agent.CompactRecord) (agent.CompactRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return record, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var rowVersion, contextVersion, lastSequence int64
	var status string
	err = tx.QueryRow(ctx, `SELECT h.row_version,h.context_version,h.last_event_sequence,a.status
		FROM agent_head h JOIN agents a ON a.tenant_id=h.tenant_id AND a.agent_id=h.agent_id
		WHERE h.tenant_id=$1 AND h.agent_id=$2 AND a.session_id=$3 FOR UPDATE OF h,a`,
		record.Input.TenantID, record.Input.AgentID, record.Input.SessionID).
		Scan(&rowVersion, &contextVersion, &lastSequence, &status)
	if err != nil {
		return record, err
	}
	if status != "running" {
		return record, agent.ErrTerminal
	}
	if rowVersion != record.SourceRowVersion || contextVersion != record.SourceContextVersion || lastSequence != record.SourceEventSequence {
		return record, agent.ErrConflict
	}
	input := agent.CompactInput{Session: record.Input, RowVersion: record.SourceRowVersion}
	existing, err := scanCompact(tx.QueryRow(ctx, selectCompact+` FOR UPDATE OF c,i`,
		record.Input.TenantID, record.Input.AgentID, record.SourceRowVersion), input)
	if err == nil {
		if existing.OperationID != record.OperationID || !bytes.Equal(existing.RequestHash, record.RequestHash) {
			return record, agent.ErrConflict
		}
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, agent.ErrNotFound) {
		return record, err
	}
	if err := verifiedAgentObject(ctx, tx, record.Input.TenantID, record.RequestRef, "compact-request"); err != nil {
		return record, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO idempotency_records (tenant_id,operation_id,operation_type,request_hash)
		VALUES ($1,$2,'compact',$3)`, record.Input.TenantID, record.OperationID, record.RequestHash)
	if err != nil {
		return record, err
	}
	_, err = tx.Exec(ctx, `UPDATE compaction_jobs SET status='stale',finished_at=now(),updated_at=now()
		WHERE tenant_id=$1 AND agent_id=$2 AND source_row_version<$3 AND status IN ('pending','running','response_saved')`,
		record.Input.TenantID, record.Input.AgentID, record.SourceRowVersion)
	if err != nil {
		return record, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO compaction_jobs
		(compaction_id,tenant_id,agent_id,operation_id,source_row_version,source_context_version,source_event_sequence,request_object_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, record.ID, record.Input.TenantID, record.Input.AgentID,
		record.OperationID, record.SourceRowVersion, record.SourceContextVersion, record.SourceEventSequence, record.RequestRef)
	if err != nil {
		return record, err
	}
	record.Status = "pending"
	return record, tx.Commit(ctx)
}

func (s *AgentStore) ClaimCompact(ctx context.Context, input agent.CompactInput, owner string, lease time.Duration) (agent.CompactRecord, error) {
	if owner == "" || lease <= 0 {
		return agent.CompactRecord{}, errors.New("invalid compaction lease")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return agent.CompactRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var agentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM agents WHERE tenant_id=$1 AND agent_id=$2 AND session_id=$3 FOR UPDATE`,
		input.Session.TenantID, input.Session.AgentID, input.Session.SessionID).Scan(&agentStatus)
	if err != nil {
		return agent.CompactRecord{}, err
	}
	if agentStatus != "running" {
		return agent.CompactRecord{}, agent.ErrTerminal
	}
	var currentRow int64
	err = tx.QueryRow(ctx, `SELECT row_version FROM agent_head WHERE tenant_id=$1 AND agent_id=$2 FOR UPDATE`,
		input.Session.TenantID, input.Session.AgentID).Scan(&currentRow)
	if err != nil {
		return agent.CompactRecord{}, err
	}
	if currentRow != input.RowVersion {
		return agent.CompactRecord{}, agent.ErrConflict
	}
	record, err := scanCompact(tx.QueryRow(ctx, selectCompact+` FOR UPDATE OF c`,
		input.Session.TenantID, input.Session.AgentID, input.RowVersion), input)
	if err != nil {
		return record, err
	}
	if record.Status == "published" || record.Status == "response_saved" {
		return record, tx.Commit(ctx)
	}
	var status string
	var active bool
	err = tx.QueryRow(ctx, `SELECT status,COALESCE(lease_until>clock_timestamp(),false) FROM idempotency_records
		WHERE tenant_id=$1 AND operation_id=$2 FOR UPDATE`, input.Session.TenantID, record.OperationID).Scan(&status, &active)
	if err != nil {
		return record, err
	}
	switch status {
	case "uncertain":
		return record, agent.ErrUncertain
	case "failed":
		return record, agent.ErrTerminal
	case "running":
		if active {
			return record, agent.ErrBusy
		}
		_, err = tx.Exec(ctx, `UPDATE compaction_jobs SET status='uncertain',finished_at=now(),updated_at=now()
			WHERE tenant_id=$1 AND compaction_id=$2 AND status='running'`, input.Session.TenantID, record.ID)
		if err != nil {
			return record, err
		}
		_, err = tx.Exec(ctx, `UPDATE idempotency_records SET status='uncertain',lease_owner=NULL,lease_until=NULL,updated_at=now()
			WHERE tenant_id=$1 AND operation_id=$2`, input.Session.TenantID, record.OperationID)
		if err != nil {
			return record, err
		}
		if err := tx.Commit(ctx); err != nil {
			return record, err
		}
		return record, agent.ErrUncertain
	case "pending":
	default:
		return record, agent.ErrConflict
	}
	err = tx.QueryRow(ctx, `UPDATE idempotency_records SET status='running',lease_owner=$3,
		lease_until=clock_timestamp()+$4*interval '1 millisecond',fencing_token=fencing_token+1,
		attempt_count=attempt_count+1,updated_at=now()
		WHERE tenant_id=$1 AND operation_id=$2 RETURNING fencing_token`,
		input.Session.TenantID, record.OperationID, owner, lease.Milliseconds()).Scan(&record.FencingToken)
	if err != nil {
		return record, err
	}
	_, err = tx.Exec(ctx, `UPDATE compaction_jobs SET status='running',started_at=COALESCE(started_at,now()),updated_at=now()
		WHERE tenant_id=$1 AND compaction_id=$2`, input.Session.TenantID, record.ID)
	if err != nil {
		return record, err
	}
	record.Status, record.LeaseOwner = "running", owner
	return record, tx.Commit(ctx)
}

func (s *AgentStore) SaveCompactResponse(ctx context.Context, record agent.CompactRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockCompact(ctx, tx, record, "running"); err != nil {
		return err
	}
	if err := verifiedAgentObject(ctx, tx, record.Input.TenantID, record.ResponseRef, "compact-response"); err != nil {
		return err
	}
	changed, err := tx.Exec(ctx, `UPDATE idempotency_records SET status='succeeded',result_object_id=$5,
		lease_owner=NULL,lease_until=NULL,updated_at=now()
		WHERE tenant_id=$1 AND operation_id=$2 AND status='running' AND lease_owner=$3 AND fencing_token=$4`,
		record.Input.TenantID, record.OperationID, record.LeaseOwner, record.FencingToken, record.ResponseRef)
	if err != nil {
		return err
	}
	if changed.RowsAffected() != 1 {
		return agent.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE compaction_jobs SET status='response_saved',response_object_id=$3,updated_at=now()
		WHERE tenant_id=$1 AND compaction_id=$2`, record.Input.TenantID, record.ID, record.ResponseRef)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *AgentStore) ReleaseCompact(ctx context.Context, record agent.CompactRecord, status, errorRef string) error {
	if status != "pending" && status != "failed" && status != "uncertain" {
		return errors.New("invalid compaction failure status")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockCompact(ctx, tx, record, "running"); err != nil {
		return err
	}
	if errorRef != "" {
		if err := verifiedAgentObject(ctx, tx, record.Input.TenantID, errorRef, "compact-error"); err != nil {
			return err
		}
	}
	changed, err := tx.Exec(ctx, `UPDATE idempotency_records SET status=$5,lease_owner=NULL,lease_until=NULL,updated_at=now()
		WHERE tenant_id=$1 AND operation_id=$2 AND status='running' AND lease_owner=$3 AND fencing_token=$4`,
		record.Input.TenantID, record.OperationID, record.LeaseOwner, record.FencingToken, status)
	if err != nil {
		return err
	}
	if changed.RowsAffected() != 1 {
		return agent.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE compaction_jobs SET status=$3,error_object_id=NULLIF($4,'')::uuid,
		finished_at=CASE WHEN $3='pending' THEN NULL ELSE now() END,updated_at=now()
		WHERE tenant_id=$1 AND compaction_id=$2`, record.Input.TenantID, record.ID, status, errorRef)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *AgentStore) PublishCompact(ctx context.Context, publication agent.CompactPublication) (agent.AgentRecord, error) {
	record := publication.Record
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var rowVersion, contextVersion, lastSequence int64
	var agentStatus string
	err = tx.QueryRow(ctx, `SELECT h.row_version,h.context_version,h.last_event_sequence,a.status
		FROM agent_head h JOIN agents a ON a.tenant_id=h.tenant_id AND a.agent_id=h.agent_id
		WHERE h.tenant_id=$1 AND h.agent_id=$2 AND a.session_id=$3 FOR UPDATE OF h,a`,
		record.Input.TenantID, record.Input.AgentID, record.Input.SessionID).
		Scan(&rowVersion, &contextVersion, &lastSequence, &agentStatus)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	input := agent.CompactInput{Session: record.Input, RowVersion: record.SourceRowVersion}
	current, err := scanCompact(tx.QueryRow(ctx, selectCompact+` FOR UPDATE OF c,i`,
		record.Input.TenantID, record.Input.AgentID, record.SourceRowVersion), input)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	if current.Status == "published" {
		if err := tx.Commit(ctx); err != nil {
			return agent.AgentRecord{}, err
		}
		return s.LoadAgent(ctx, record.Input.TenantID, record.Input.AgentID)
	}
	if agentStatus != "running" {
		if _, err := tx.Exec(ctx, `UPDATE compaction_jobs SET status='cancelled',finished_at=now(),updated_at=now()
			WHERE tenant_id=$1 AND compaction_id=$2 AND status IN ('pending','running','response_saved')`,
			record.Input.TenantID, current.ID); err != nil {
			return agent.AgentRecord{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return agent.AgentRecord{}, err
		}
		return agent.AgentRecord{}, agent.ErrTerminal
	}
	if rowVersion != current.SourceRowVersion || contextVersion != current.SourceContextVersion || lastSequence != current.SourceEventSequence {
		if _, err := tx.Exec(ctx, `UPDATE compaction_jobs SET status='stale',finished_at=now(),updated_at=now()
			WHERE tenant_id=$1 AND compaction_id=$2 AND status='response_saved'`,
			record.Input.TenantID, current.ID); err != nil {
			return agent.AgentRecord{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return agent.AgentRecord{}, err
		}
		return agent.AgentRecord{}, agent.ErrConflict
	}
	if current.Status != "response_saved" || current.ID != record.ID || current.OperationID != record.OperationID ||
		current.ResponseRef == "" || current.ResponseRef != record.ResponseRef {
		return agent.AgentRecord{}, agent.ErrConflict
	}
	if err := verifiedAgentObject(ctx, tx, record.Input.TenantID, publication.SnapshotRef, "agent-context"); err != nil {
		return agent.AgentRecord{}, err
	}
	fromSequence := int64(0)
	if lastSequence > 0 {
		fromSequence = 1
	}
	_, err = tx.Exec(ctx, `INSERT INTO context_records (context_record_id,tenant_id,agent_id,context_version,kind,object_id,
		from_event_sequence,through_event_sequence) VALUES ($1,$2,$3,$4,'snapshot',$5,$6,$7)`,
		publication.ContextRecordID, record.Input.TenantID, record.Input.AgentID, contextVersion+1,
		publication.SnapshotRef, fromSequence, lastSequence)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	for _, ref := range publication.SourceRefs {
		_, err = tx.Exec(ctx, `INSERT INTO context_source_refs
			(tenant_id,repository_id,context_record_id,source_snapshot_id,role)
			VALUES ($1,$2,$3,$4,'evidence') ON CONFLICT DO NOTHING`,
			record.Input.TenantID, ref.RepositoryID, publication.ContextRecordID, ref.SourceSnapshotID)
		if err != nil {
			return agent.AgentRecord{}, err
		}
	}
	changed, err := tx.Exec(ctx, `UPDATE agent_head SET row_version=row_version+1,context_version=$3,
		snapshot_record_id=$4,updated_at=now()
		WHERE tenant_id=$1 AND agent_id=$2 AND row_version=$5`,
		record.Input.TenantID, record.Input.AgentID, contextVersion+1, publication.ContextRecordID, rowVersion)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	if changed.RowsAffected() != 1 {
		return agent.AgentRecord{}, agent.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE compaction_jobs SET status='published',candidate_snapshot_object_id=$3,
		published_context_record_id=$4,finished_at=now(),updated_at=now()
		WHERE tenant_id=$1 AND compaction_id=$2`, record.Input.TenantID, current.ID,
		publication.SnapshotRef, publication.ContextRecordID)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE idempotency_records SET status='succeeded',result_object_id=$3,updated_at=now()
		WHERE tenant_id=$1 AND operation_id=$2`, record.Input.TenantID, current.OperationID, publication.SnapshotRef)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	outboxID, err := uuid.NewV7()
	if err != nil {
		return agent.AgentRecord{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (outbox_id,tenant_id,topic,aggregate_type,aggregate_id,dedupe_key,payload_object_id)
		VALUES ($1,$2,'agent.context.compacted','agent',$3,$4,$5) ON CONFLICT (tenant_id,dedupe_key) DO NOTHING`,
		outboxID.String(), record.Input.TenantID, record.Input.AgentID,
		"compact:"+record.Input.AgentID+":"+strconv.FormatInt(current.SourceRowVersion, 10), publication.SnapshotRef)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.AgentRecord{}, err
	}
	return s.LoadAgent(ctx, record.Input.TenantID, record.Input.AgentID)
}

func lockCompact(ctx context.Context, tx pgx.Tx, record agent.CompactRecord, status string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT compaction_id::text FROM compaction_jobs
		WHERE tenant_id=$1 AND compaction_id=$2 AND agent_id=$3 AND operation_id=$4 AND status=$5 FOR UPDATE`,
		record.Input.TenantID, record.ID, record.Input.AgentID, record.OperationID, status).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.ErrConflict
		}
		return err
	}
	if id != record.ID {
		return agent.ErrConflict
	}
	return nil
}

func verifiedAgentObject(ctx context.Context, tx pgx.Tx, tenant, ref, kind string) error {
	if id, err := uuid.Parse(ref); err != nil || id == uuid.Nil {
		return errors.New("invalid stored object reference")
	}
	var verified bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stored_objects
		WHERE tenant_id=$1 AND object_id=$2 AND kind=$3 AND status='verified')`, tenant, ref, kind).Scan(&verified)
	if err != nil {
		return err
	}
	if !verified {
		return errors.New("stored object is not verified for this tenant and purpose")
	}
	return nil
}

func (s *AgentStore) CurrentEvidence(ctx context.Context, tenantID, agentID, contextRecordID string) ([]agent.SourceRef, error) {
	if contextRecordID == "" {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT r.repository_id::text,r.source_snapshot_id::text
		FROM context_source_refs r JOIN context_records c
		ON c.tenant_id=r.tenant_id AND c.context_record_id=r.context_record_id
		WHERE r.tenant_id=$1 AND c.agent_id=$2 AND r.context_record_id=$3 ORDER BY 1,2`,
		tenantID, agentID, contextRecordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make([]agent.SourceRef, 0)
	for rows.Next() {
		var ref agent.SourceRef
		if err := rows.Scan(&ref.RepositoryID, &ref.SourceSnapshotID); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (s *AgentStore) ValidateSourceRefs(ctx context.Context, tenantID string, refs []agent.SourceRef) error {
	for _, ref := range refs {
		if !ref.Valid() {
			return errors.New("invalid source reference")
		}
		var ready bool
		err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_snapshots
			WHERE tenant_id=$1 AND repository_id=$2 AND snapshot_id=$3 AND status='ready')`,
			tenantID, ref.RepositoryID, ref.SourceSnapshotID).Scan(&ready)
		if err != nil {
			return err
		}
		if !ready {
			return errors.New("source reference is unavailable")
		}
	}
	return nil
}

func (s *AgentStore) IsCompactedContext(ctx context.Context, tenantID, agentID, contextRecordID string) (bool, error) {
	if contextRecordID == "" {
		return false, nil
	}
	var found bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM compaction_jobs
		WHERE tenant_id=$1 AND agent_id=$2 AND status='published' AND published_context_record_id=$3)`,
		tenantID, agentID, contextRecordID).Scan(&found)
	return found, err
}

func (s *AgentStore) ValidateToolArtifacts(ctx context.Context, tenantID string, refs []string) error {
	for _, ref := range refs {
		if id, err := uuid.Parse(ref); err != nil || id == uuid.Nil {
			return errors.New("invalid tool artifact reference")
		}
		var verified bool
		err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stored_objects
			WHERE tenant_id=$1 AND object_id=$2 AND kind='tool-result' AND status='verified')`, tenantID, ref).Scan(&verified)
		if err != nil {
			return err
		}
		if !verified {
			return errors.New("tool artifact is unavailable")
		}
	}
	return nil
}
