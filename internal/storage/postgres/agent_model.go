package postgres

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"sentinel/internal/agent"
)

const selectStep = `SELECT e.llm_execution_id::text,e.operation_id,e.context_version,e.model,
	e.request_object_id::text,COALESCE(e.response_object_id::text,''),e.status,
	i.request_hash,COALESCE(i.lease_owner,''),i.fencing_token,
	COALESCE(e.provider_request_id,''),COALESCE(e.input_tokens,0),COALESCE(e.output_tokens,0)
	FROM llm_executions e JOIN idempotency_records i ON i.tenant_id=e.tenant_id AND i.operation_id=e.operation_id
	WHERE e.tenant_id=$1 AND e.agent_id=$2 AND e.step_id=$3`

func scanStep(row pgx.Row, input agent.StepInput) (agent.StepRecord, error) {
	record := agent.StepRecord{Input: input}
	err := row.Scan(&record.ID, &record.OperationID, &record.ContextVersion, &record.Model,
		&record.RequestRef, &record.ResponseRef, &record.Status, &record.RequestHash,
		&record.LeaseOwner, &record.FencingToken, &record.ProviderRequestID, &record.InputTokens, &record.OutputTokens)
	if errors.Is(err, pgx.ErrNoRows) {
		err = agent.ErrNotFound
	}
	return record, err
}

func (s *AgentStore) LoadStep(ctx context.Context, input agent.StepInput) (agent.StepRecord, error) {
	return scanStep(s.pool.QueryRow(ctx, selectStep, input.Session.TenantID, input.Session.AgentID, input.StepID), input)
}

func (s *AgentStore) PrepareStep(ctx context.Context, record agent.StepRecord) (agent.StepRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return record, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	input := record.Input
	var version, nextStep int64
	var status string
	err = tx.QueryRow(ctx, `SELECT h.context_version,h.next_step_id,a.status FROM agent_head h
		JOIN agents a ON a.tenant_id=h.tenant_id AND a.agent_id=h.agent_id
		WHERE h.tenant_id=$1 AND h.agent_id=$2 AND a.session_id=$3 FOR UPDATE OF h,a`,
		input.Session.TenantID, input.Session.AgentID, input.Session.SessionID).Scan(&version, &nextStep, &status)
	if err != nil {
		return record, err
	}
	existing, err := scanStep(tx.QueryRow(ctx, selectStep, input.Session.TenantID, input.Session.AgentID, input.StepID), input)
	if err == nil {
		if !bytes.Equal(existing.RequestHash, record.RequestHash) {
			return record, agent.ErrConflict
		}
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, agent.ErrNotFound) {
		return record, err
	}
	if status != "running" || nextStep != input.StepID || version != record.ContextVersion {
		return record, agent.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO idempotency_records (tenant_id,operation_id,operation_type,request_hash)
		VALUES ($1,$2,'llm',$3)`, input.Session.TenantID, record.OperationID, record.RequestHash)
	if err != nil {
		return record, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO llm_executions (llm_execution_id,tenant_id,agent_id,step_id,
		operation_id,context_version,model,request_object_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, record.ID,
		input.Session.TenantID, input.Session.AgentID, input.StepID, record.OperationID, record.ContextVersion, record.Model, record.RequestRef)
	if err != nil {
		return record, err
	}
	record.Status = "prepared"
	return record, tx.Commit(ctx)
}

func (s *AgentStore) ClaimStep(ctx context.Context, input agent.StepInput, owner string, lease time.Duration) (agent.StepRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return agent.StepRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var agentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM agents WHERE tenant_id=$1 AND agent_id=$2 AND session_id=$3 FOR UPDATE`,
		input.Session.TenantID, input.Session.AgentID, input.Session.SessionID).Scan(&agentStatus)
	if err != nil {
		return agent.StepRecord{}, err
	}
	if agentStatus != "running" {
		return agent.StepRecord{}, agent.ErrTerminal
	}
	// Every model mutation locks the execution before its idempotency record.
	record, err := scanStep(tx.QueryRow(ctx, selectStep+` FOR UPDATE OF e`,
		input.Session.TenantID, input.Session.AgentID, input.StepID), input)
	if err != nil {
		return record, err
	}
	if record.ResponseRef != "" {
		return record, tx.Commit(ctx)
	}
	var status string
	var active bool
	err = tx.QueryRow(ctx, `SELECT status,COALESCE(lease_until>now(),false) FROM idempotency_records
		WHERE tenant_id=$1 AND operation_id=$2 FOR UPDATE`, input.Session.TenantID, record.OperationID).Scan(&status, &active)
	if err != nil {
		return record, err
	}
	if status == "uncertain" {
		return record, agent.ErrUncertain
	}
	if status == "failed" {
		return record, agent.ErrTerminal
	}
	if status == "running" {
		if active {
			return record, agent.ErrBusy
		}
		_, err = tx.Exec(ctx, `UPDATE idempotency_records SET status='uncertain',lease_owner=NULL,lease_until=NULL,updated_at=now()
			WHERE tenant_id=$1 AND operation_id=$2`, input.Session.TenantID, record.OperationID)
		if err != nil {
			return record, err
		}
		_, err = tx.Exec(ctx, `UPDATE llm_executions SET status='uncertain',finished_at=now(),updated_at=now()
			WHERE tenant_id=$1 AND agent_id=$2 AND step_id=$3`, input.Session.TenantID, input.Session.AgentID, input.StepID)
		if err != nil {
			return record, err
		}
		if err := tx.Commit(ctx); err != nil {
			return record, err
		}
		return record, agent.ErrUncertain
	}
	if status != "pending" || owner == "" || lease <= 0 {
		return record, agent.ErrConflict
	}
	err = tx.QueryRow(ctx, `UPDATE idempotency_records SET status='running',lease_owner=$3,
		lease_until=now()+($4 * interval '1 millisecond'),fencing_token=fencing_token+1,
		attempt_count=attempt_count+1,updated_at=now() WHERE tenant_id=$1 AND operation_id=$2 RETURNING fencing_token`,
		input.Session.TenantID, record.OperationID, owner, lease.Milliseconds()).Scan(&record.FencingToken)
	if err != nil {
		return record, err
	}
	_, err = tx.Exec(ctx, `UPDATE llm_executions SET status='running',started_at=COALESCE(started_at,now()),updated_at=now()
		WHERE tenant_id=$1 AND agent_id=$2 AND step_id=$3`, input.Session.TenantID, input.Session.AgentID, input.StepID)
	if err != nil {
		return record, err
	}
	record.Status, record.LeaseOwner = "running", owner
	return record, tx.Commit(ctx)
}

func (s *AgentStore) SaveResponse(ctx context.Context, record agent.StepRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	input := record.Input
	if err := lockModelExecution(ctx, tx, record); err != nil {
		return err
	}
	changed, err := tx.Exec(ctx, `UPDATE idempotency_records SET status='succeeded',result_object_id=$5,
		lease_owner=NULL,lease_until=NULL,updated_at=now() WHERE tenant_id=$1 AND operation_id=$2
		AND status='running' AND lease_owner=$3 AND fencing_token=$4`, input.Session.TenantID, record.OperationID,
		record.LeaseOwner, record.FencingToken, record.ResponseRef)
	if err != nil {
		return err
	}
	if changed.RowsAffected() != 1 {
		return agent.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE llm_executions SET status='response_saved',response_object_id=$4,
		provider_request_id=NULLIF($5,''),input_tokens=$6,output_tokens=$7,updated_at=now()
		WHERE tenant_id=$1 AND agent_id=$2 AND step_id=$3`, input.Session.TenantID, input.Session.AgentID, input.StepID,
		record.ResponseRef, record.ProviderRequestID, record.InputTokens, record.OutputTokens)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Only a definite rate-limit rejection is reset to pending automatically. Unknown
// transport/server outcomes remain uncertain; an expired lease never sends again.
func (s *AgentStore) ReleaseStep(ctx context.Context, record agent.StepRecord, status, errorRef string) error {
	llmStatus := status
	if status == "pending" {
		llmStatus = "prepared"
	} else if status != "failed" && status != "uncertain" {
		return errors.New("invalid model failure status")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	input := record.Input
	if err := lockModelExecution(ctx, tx, record); err != nil {
		return err
	}
	changed, err := tx.Exec(ctx, `UPDATE idempotency_records SET status=$5,lease_owner=NULL,lease_until=NULL,updated_at=now()
		WHERE tenant_id=$1 AND operation_id=$2 AND status='running' AND lease_owner=$3 AND fencing_token=$4`,
		input.Session.TenantID, record.OperationID, record.LeaseOwner, record.FencingToken, status)
	if err != nil {
		return err
	}
	if changed.RowsAffected() != 1 {
		return agent.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE llm_executions SET status=$4,error_object_id=NULLIF($5,'')::uuid,
		finished_at=CASE WHEN $4='prepared' THEN NULL ELSE now() END,updated_at=now()
		WHERE tenant_id=$1 AND agent_id=$2 AND step_id=$3`, input.Session.TenantID, input.Session.AgentID, input.StepID, llmStatus, errorRef)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func lockModelExecution(ctx context.Context, tx pgx.Tx, record agent.StepRecord) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT llm_execution_id::text FROM llm_executions
		WHERE tenant_id=$1 AND agent_id=$2 AND step_id=$3 AND operation_id=$4 FOR UPDATE`,
		record.Input.Session.TenantID, record.Input.Session.AgentID, record.Input.StepID, record.OperationID).Scan(&id)
	if err != nil {
		return err
	}
	if id != record.ID {
		return agent.ErrConflict
	}
	return nil
}
