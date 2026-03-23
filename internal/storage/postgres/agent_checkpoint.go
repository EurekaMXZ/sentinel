package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"sentinel/internal/agent"
)

// CommitStep is the only operation that advances the recovery cursor. Bodies
// have already been verified in object storage before this transaction starts.
func (s *AgentStore) CommitStep(ctx context.Context, checkpoint agent.Checkpoint) (agent.AgentRecord, error) {
	input := checkpoint.Step
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var rowVersion, version, nextStep, lastSequence int64
	var status, sessionID string
	err = tx.QueryRow(ctx, `SELECT h.row_version,h.context_version,h.next_step_id,h.last_event_sequence,a.status,a.session_id::text
		FROM agent_head h JOIN agents a ON a.tenant_id=h.tenant_id AND a.agent_id=h.agent_id
		WHERE h.tenant_id=$1 AND h.agent_id=$2 FOR UPDATE OF h,a`, input.Session.TenantID, input.Session.AgentID).
		Scan(&rowVersion, &version, &nextStep, &lastSequence, &status, &sessionID)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	step, err := scanStep(tx.QueryRow(ctx, selectStep+` FOR UPDATE OF e`, input.Session.TenantID, input.Session.AgentID, input.StepID), input)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	if sessionID != input.Session.SessionID {
		return agent.AgentRecord{}, agent.ErrConflict
	}
	if step.Status == "succeeded" {
		if err := tx.Commit(ctx); err != nil {
			return agent.AgentRecord{}, err
		}
		return s.LoadAgent(ctx, input.Session.TenantID, input.Session.AgentID)
	}
	if status != "running" || step.Status != "response_saved" || nextStep != input.StepID ||
		rowVersion != checkpoint.ExpectedHead.RowVersion || version != checkpoint.ExpectedHead.ContextVersion ||
		lastSequence != checkpoint.ExpectedHead.LastEventSequence || step.ContextVersion != version {
		return agent.AgentRecord{}, agent.ErrConflict
	}
	var toolCount, finishedCount int
	err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status IN ('succeeded','failed') AND result_object_id IS NOT NULL)
		FROM tool_executions WHERE tenant_id=$1 AND agent_id=$2 AND step_id=$3`, input.Session.TenantID, input.Session.AgentID, input.StepID).
		Scan(&toolCount, &finishedCount)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	if toolCount != checkpoint.ToolCount || finishedCount != toolCount || (checkpoint.ResultRef != "") != (toolCount == 0) {
		return agent.AgentRecord{}, errors.New("checkpoint has incomplete tool results")
	}
	if len(checkpoint.Events) < 2 || checkpoint.ContextRecordID == "" || checkpoint.ContextRef == "" || checkpoint.SegmentRef == "" {
		return agent.AgentRecord{}, errors.New("incomplete checkpoint")
	}
	for ordinal, event := range checkpoint.Events {
		if event.TenantID != input.Session.TenantID || event.AgentID != input.Session.AgentID || event.Sequence != lastSequence+int64(ordinal)+1 {
			return agent.AgentRecord{}, errors.New("invalid checkpoint event sequence")
		}
		_, err = tx.Exec(ctx, `INSERT INTO event_index (event_id,tenant_id,agent_id,sequence,event_type,operation_id,
			segment_object_id,record_ordinal,payload_object_id,repository_id,source_snapshot_id,base_snapshot_id,target_snapshot_id,created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid,NULLIF($10,'')::uuid,NULLIF($11,'')::uuid,
			NULLIF($12,'')::uuid,NULLIF($13,'')::uuid,$14)`, event.EventID, event.TenantID, event.AgentID, event.Sequence,
			event.Type, event.OperationID, checkpoint.SegmentRef, ordinal, event.PayloadRef, event.RepositoryID,
			event.SourceSnapshotID, event.BaseSnapshotID, event.TargetSnapshotID, event.CreatedAt)
		if err != nil {
			return agent.AgentRecord{}, err
		}
	}
	through := lastSequence + int64(len(checkpoint.Events))
	_, err = tx.Exec(ctx, `INSERT INTO context_records (context_record_id,tenant_id,agent_id,context_version,kind,object_id,
		from_event_sequence,through_event_sequence) VALUES ($1,$2,$3,$4,'snapshot',$5,$6,$7)`, checkpoint.ContextRecordID,
		input.Session.TenantID, input.Session.AgentID, version+1, checkpoint.ContextRef, lastSequence+1, through)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	for _, ref := range checkpoint.SourceRefs {
		if ref.RepositoryID == "" || ref.SourceSnapshotID == "" ||
			(ref.Role != "evidence" && ref.Role != "diff_base" && ref.Role != "diff_target") {
			return agent.AgentRecord{}, errors.New("invalid checkpoint source reference")
		}
		_, err = tx.Exec(ctx, `INSERT INTO context_source_refs
			(tenant_id,repository_id,context_record_id,source_snapshot_id,role)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, input.Session.TenantID,
			ref.RepositoryID, checkpoint.ContextRecordID, ref.SourceSnapshotID, ref.Role)
		if err != nil {
			return agent.AgentRecord{}, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE agent_head SET row_version=row_version+1,context_version=context_version+1,
		last_event_sequence=$3,context_event_sequence=$3,snapshot_record_id=$4,next_step_id=$5,updated_at=now()
		WHERE tenant_id=$1 AND agent_id=$2`, input.Session.TenantID, input.Session.AgentID, through, checkpoint.ContextRecordID, input.StepID+1)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE llm_executions SET status='succeeded',finished_at=now(),updated_at=now()
		WHERE tenant_id=$1 AND agent_id=$2 AND step_id=$3`, input.Session.TenantID, input.Session.AgentID, input.StepID)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	if checkpoint.ResultRef != "" {
		_, err = tx.Exec(ctx, `UPDATE agents SET status='completed',result_object_id=$3,finished_at=now(),updated_at=now()
			WHERE tenant_id=$1 AND agent_id=$2`, input.Session.TenantID, input.Session.AgentID, checkpoint.ResultRef)
		if err != nil {
			return agent.AgentRecord{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE agent_sessions SET status='completed',finished_at=now(),updated_at=now()
			WHERE tenant_id=$1 AND session_id=$2 AND EXISTS (SELECT 1 FROM agents WHERE tenant_id=$1 AND agent_id=$3 AND parent_agent_id IS NULL)`,
			input.Session.TenantID, input.Session.SessionID, input.Session.AgentID)
		if err != nil {
			return agent.AgentRecord{}, err
		}
	}
	outboxID, err := uuid.NewV7()
	if err != nil {
		return agent.AgentRecord{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (outbox_id,tenant_id,topic,aggregate_type,aggregate_id,dedupe_key,payload_object_id)
		VALUES ($1,$2,'agent.context.updated','agent',$3,$4,$5)`, outboxID.String(), input.Session.TenantID,
		input.Session.AgentID, fmt.Sprintf("checkpoint:%s:%d", input.Session.AgentID, input.StepID), checkpoint.ContextRef)
	if err != nil {
		return agent.AgentRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.AgentRecord{}, err
	}
	return s.LoadAgent(ctx, input.Session.TenantID, input.Session.AgentID)
}
