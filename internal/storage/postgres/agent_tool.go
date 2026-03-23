package postgres

import (
	"context"
	"crypto/sha256"
	"errors"

	"github.com/jackc/pgx/v5"

	"sentinel/internal/agent"
)

const selectTool = `SELECT tool_execution_id::text,operation_id,tool_name,arguments_object_id::text,
	COALESCE(result_object_id::text,''),status,COALESCE(repository_id::text,''),
	COALESCE(source_snapshot_id::text,''),COALESCE(base_snapshot_id::text,''),COALESCE(target_snapshot_id::text,'')
	FROM tool_executions WHERE tenant_id=$1 AND agent_id=$2 AND step_id=$3 AND tool_call_id=$4`

func scanTool(row pgx.Row, input agent.ToolInput) (agent.ToolRecord, error) {
	record := agent.ToolRecord{Input: input}
	err := row.Scan(&record.ID, &record.OperationID, &record.Input.Call.Name, &record.Input.Call.ArgumentsRef,
		&record.ResultRef, &record.Status, &record.Source.RepositoryID, &record.Source.SourceSnapshotID,
		&record.Source.BaseSnapshotID, &record.Source.TargetSnapshotID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = agent.ErrNotFound
	}
	return record, err
}

func (s *AgentStore) LoadTool(ctx context.Context, input agent.ToolInput) (agent.ToolRecord, error) {
	return scanTool(s.pool.QueryRow(ctx, selectTool, input.Step.Session.TenantID,
		input.Step.Session.AgentID, input.Step.StepID, input.Call.CallID), input)
}

func (s *AgentStore) PrepareTool(ctx context.Context, record agent.ToolRecord) (agent.ToolRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return record, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	input := record.Input
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM llm_executions WHERE tenant_id=$1 AND agent_id=$2 AND step_id=$3 FOR UPDATE`,
		input.Step.Session.TenantID, input.Step.Session.AgentID, input.Step.StepID).Scan(&status)
	if err != nil {
		return record, err
	}
	existing, err := scanTool(tx.QueryRow(ctx, selectTool, input.Step.Session.TenantID,
		input.Step.Session.AgentID, input.Step.StepID, input.Call.CallID), input)
	if err == nil {
		if existing.Input.Call.Name != input.Call.Name || existing.Input.Call.ArgumentsRef != input.Call.ArgumentsRef {
			return record, agent.ErrConflict
		}
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, agent.ErrNotFound) {
		return record, err
	}
	if status != "response_saved" {
		return record, agent.ErrConflict
	}
	hash := sha256.Sum256([]byte(input.Call.Name + ":" + input.Call.ArgumentsRef))
	_, err = tx.Exec(ctx, `INSERT INTO idempotency_records (tenant_id,operation_id,operation_type,request_hash)
		VALUES ($1,$2,'tool',$3)`, input.Step.Session.TenantID, record.OperationID, hash[:])
	if err != nil {
		return record, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO tool_executions (tool_execution_id,tenant_id,agent_id,step_id,
		tool_call_id,tool_name,operation_id,arguments_object_id,repository_id,source_snapshot_id,base_snapshot_id,target_snapshot_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid,NULLIF($10,'')::uuid,NULLIF($11,'')::uuid,NULLIF($12,'')::uuid)`, record.ID,
		input.Step.Session.TenantID, input.Step.Session.AgentID, input.Step.StepID,
		input.Call.CallID, input.Call.Name, record.OperationID, input.Call.ArgumentsRef,
		record.Source.RepositoryID, record.Source.SourceSnapshotID, record.Source.BaseSnapshotID, record.Source.TargetSnapshotID)
	if err != nil {
		return record, err
	}
	record.Status = "queued"
	return record, tx.Commit(ctx)
}

func (s *AgentStore) SaveToolResult(ctx context.Context, record agent.ToolRecord) (agent.ToolRecord, error) {
	if record.Status != "succeeded" && record.Status != "failed" {
		return record, errors.New("invalid completed tool status")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return record, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	input := record.Input
	existing, err := scanTool(tx.QueryRow(ctx, selectTool+` FOR UPDATE`, input.Step.Session.TenantID,
		input.Step.Session.AgentID, input.Step.StepID, input.Call.CallID), input)
	if err != nil {
		return record, err
	}
	if existing.ResultRef != "" {
		return existing, tx.Commit(ctx)
	}
	if existing.ID != record.ID || existing.OperationID != record.OperationID {
		return record, agent.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE tool_executions SET status=$3,result_object_id=$4,
		started_at=COALESCE(started_at,now()),finished_at=now(),updated_at=now()
		WHERE tenant_id=$1 AND tool_execution_id=$2`, input.Step.Session.TenantID, record.ID, record.Status, record.ResultRef)
	if err != nil {
		return record, err
	}
	_, err = tx.Exec(ctx, `UPDATE idempotency_records SET status=$3,result_object_id=$4,updated_at=now()
		WHERE tenant_id=$1 AND operation_id=$2`, input.Step.Session.TenantID, record.OperationID, record.Status, record.ResultRef)
	if err != nil {
		return record, err
	}
	return record, tx.Commit(ctx)
}
