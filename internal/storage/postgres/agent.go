package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"sentinel/internal/agent"
)

type AgentStore struct {
	pool *pgxpool.Pool
}

var _ agent.RuntimeStore = (*AgentStore)(nil)
var _ agent.ResourceStore = (*AgentStore)(nil)

func NewAgentStore(pool *pgxpool.Pool) *AgentStore { return &AgentStore{pool: pool} }

func (s *AgentStore) LoadAgent(ctx context.Context, tenantID, agentID string) (agent.AgentRecord, error) {
	var record agent.AgentRecord
	err := s.pool.QueryRow(ctx, `SELECT a.tenant_id::text, a.session_id::text, a.agent_id::text,
		a.task_object_id::text, a.workflow_id, COALESCE(a.parent_agent_id::text,''),
		COALESCE(a.parent_tool_execution_id::text,''), a.status, COALESCE(a.result_object_id::text,''),
		h.row_version, h.context_version, h.last_event_sequence, h.context_event_sequence,
		COALESCE(h.snapshot_record_id::text,''), COALESCE(c.object_id::text,''), h.next_step_id
		FROM agents a JOIN agent_head h ON h.agent_id=a.agent_id AND h.tenant_id=a.tenant_id
		LEFT JOIN context_records c ON c.context_record_id=h.snapshot_record_id AND c.tenant_id=h.tenant_id
		WHERE a.tenant_id=$1 AND a.agent_id=$2`, tenantID, agentID).
		Scan(&record.Input.TenantID, &record.Input.SessionID, &record.Input.AgentID, &record.Input.TaskRef,
			&record.WorkflowID, &record.ParentAgentID, &record.ParentToolExecutionID, &record.Status, &record.ResultRef,
			&record.Head.RowVersion, &record.Head.ContextVersion, &record.Head.LastEventSequence,
			&record.Head.ContextEventSequence, &record.Head.SnapshotRecordID, &record.Head.SnapshotRef, &record.Head.NextStepID)
	if errors.Is(err, pgx.ErrNoRows) {
		return record, agent.ErrNotFound
	}
	if err != nil {
		return record, err
	}
	record.Head.TenantID, record.Head.AgentID = tenantID, agentID
	record.Resources, err = s.ListAgentBindings(ctx, tenantID, agentID)
	return record, err
}

func (s *AgentStore) CreateAgent(ctx context.Context, record agent.AgentRecord) (agent.AgentRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return record, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	input := record.Input
	if record.ParentAgentID != "" {
		var parentSession string
		if err := tx.QueryRow(ctx, `SELECT a.session_id::text FROM agents a JOIN tool_executions t
			ON t.tenant_id=a.tenant_id AND t.agent_id=a.agent_id
			WHERE a.tenant_id=$1 AND a.agent_id=$2 AND t.tool_execution_id=$3
			AND a.status IN ('running','waiting') FOR UPDATE OF t`, input.TenantID, record.ParentAgentID, record.ParentToolExecutionID).
			Scan(&parentSession); err != nil {
			return record, err
		}
		if parentSession != input.SessionID {
			return record, errors.New("child session differs from parent")
		}
		var existing string
		err := tx.QueryRow(ctx, `SELECT agent_id::text FROM agents WHERE tenant_id=$1 AND parent_tool_execution_id=$2`,
			input.TenantID, record.ParentToolExecutionID).Scan(&existing)
		if err == nil {
			if err := tx.Commit(ctx); err != nil {
				return record, err
			}
			return s.LoadAgent(ctx, input.TenantID, existing)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return record, err
		}
	}
	if err := validateResources(ctx, tx, input.TenantID, record.Resources); err != nil {
		return record, err
	}
	if record.ParentAgentID == "" {
		if _, err := tx.Exec(ctx, `INSERT INTO agent_sessions (session_id,tenant_id,task_object_id)
			VALUES ($1,$2,$3)`, input.SessionID, input.TenantID, input.TaskRef); err != nil {
			return record, err
		}
		for _, binding := range record.Resources {
			if err := insertBinding(ctx, tx, "session_resource_bindings", "session_id", input.TenantID, input.SessionID, binding); err != nil {
				return record, err
			}
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO agents (agent_id,tenant_id,session_id,parent_agent_id,
		parent_tool_execution_id,task_object_id,workflow_id) VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7)`,
		input.AgentID, input.TenantID, input.SessionID, record.ParentAgentID, record.ParentToolExecutionID, input.TaskRef, record.WorkflowID)
	if err != nil {
		return record, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_head (agent_id,tenant_id) VALUES ($1,$2)`, input.AgentID, input.TenantID); err != nil {
		return record, err
	}
	for _, binding := range record.Resources {
		if err := insertBinding(ctx, tx, "agent_resource_bindings", "agent_id", input.TenantID, input.AgentID, binding); err != nil {
			return record, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return record, err
	}
	return s.LoadAgent(ctx, input.TenantID, input.AgentID)
}

func (s *AgentStore) StartAgent(ctx context.Context, input agent.InitializeInput) (agent.AgentRecord, error) {
	record, err := s.LoadAgent(ctx, input.Session.TenantID, input.Session.AgentID)
	if err != nil {
		return record, err
	}
	if record.Input.SessionID != input.Session.SessionID || record.Input.TaskRef != input.Session.TaskRef || record.WorkflowID != input.WorkflowID {
		return record, errors.New("workflow input differs from the persisted Agent")
	}
	if record.Status == "failed" || record.Status == "cancelled" {
		return record, agent.ErrTerminal
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return record, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	_, err = tx.Exec(ctx, `UPDATE agents SET status='running',current_run_id=$3,
		started_at=COALESCE(started_at,now()),updated_at=now()
		WHERE tenant_id=$1 AND agent_id=$2 AND status IN ('pending','running','waiting')`,
		input.Session.TenantID, input.Session.AgentID, input.RunID)
	if err != nil {
		return record, err
	}
	if record.ParentAgentID == "" {
		if _, err := tx.Exec(ctx, `UPDATE agent_sessions SET status='running',started_at=COALESCE(started_at,now()),updated_at=now()
			WHERE tenant_id=$1 AND session_id=$2 AND status='pending'`, input.Session.TenantID, input.Session.SessionID); err != nil {
			return record, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return record, err
	}
	return s.LoadAgent(ctx, input.Session.TenantID, input.Session.AgentID)
}

func (s *AgentStore) FindChild(ctx context.Context, tenantID, parentToolID string) (agent.AgentRecord, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT agent_id::text FROM agents WHERE tenant_id=$1 AND parent_tool_execution_id=$2`, tenantID, parentToolID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.AgentRecord{}, agent.ErrNotFound
	}
	if err != nil {
		return agent.AgentRecord{}, err
	}
	return s.LoadAgent(ctx, tenantID, id)
}

func (s *AgentStore) ListSessionBindings(ctx context.Context, tenantID, sessionID string) ([]agent.ResourceBinding, error) {
	return s.listBindings(ctx, "session_resource_bindings", "session_id", tenantID, sessionID)
}

func (s *AgentStore) ListAgentBindings(ctx context.Context, tenantID, agentID string) ([]agent.ResourceBinding, error) {
	return s.listBindings(ctx, "agent_resource_bindings", "agent_id", tenantID, agentID)
}

func (s *AgentStore) listBindings(ctx context.Context, table, idColumn, tenantID, id string) ([]agent.ResourceBinding, error) {
	// The table and column names are constants owned by the two callers above.
	rows, err := s.pool.Query(ctx, `SELECT binding_name,resource_kind,COALESCE(repository_id::text,''),
		COALESCE(source_snapshot_id::text,''),COALESCE(object_id::text,'') FROM `+table+
		` WHERE tenant_id=$1 AND `+idColumn+`=$2 ORDER BY binding_name`, tenantID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := make([]agent.ResourceBinding, 0)
	for rows.Next() {
		var binding agent.ResourceBinding
		if err := rows.Scan(&binding.BindingName, &binding.Kind, &binding.RepositoryID, &binding.SourceSnapshotID, &binding.ObjectID); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

type resourceQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *AgentStore) ValidateResources(ctx context.Context, tenantID string, bindings []agent.ResourceBinding) error {
	return validateResources(ctx, s.pool, tenantID, bindings)
}

func validateResources(ctx context.Context, q resourceQuerier, tenantID string, bindings []agent.ResourceBinding) error {
	names := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		if err := binding.Validate(); err != nil {
			return err
		}
		if names[binding.BindingName] {
			return errors.New("duplicate resource binding name")
		}
		names[binding.BindingName] = true
		var valid bool
		var err error
		switch binding.Kind {
		case agent.ResourceSourceRepository:
			err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_repositories WHERE tenant_id=$1 AND repository_id=$2 AND status='active')`,
				tenantID, binding.RepositoryID).Scan(&valid)
		case agent.ResourceSourceSnapshot:
			err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_snapshots WHERE tenant_id=$1 AND repository_id=$2 AND snapshot_id=$3 AND status='ready')`,
				tenantID, binding.RepositoryID, binding.SourceSnapshotID).Scan(&valid)
		case agent.ResourceObject:
			err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stored_objects WHERE tenant_id=$1 AND object_id=$2 AND status='verified')`,
				tenantID, binding.ObjectID).Scan(&valid)
		}
		if err != nil {
			return err
		}
		if !valid {
			return fmt.Errorf("resource %q is unavailable or not ready", binding.BindingName)
		}
	}
	return nil
}

func insertBinding(ctx context.Context, tx pgx.Tx, table, idColumn, tenantID, id string, b agent.ResourceBinding) error {
	_, err := tx.Exec(ctx, `INSERT INTO `+table+` (tenant_id,`+idColumn+`,binding_name,resource_kind,repository_id,source_snapshot_id,object_id)
		VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid)`, tenantID, id, b.BindingName, b.Kind, b.RepositoryID, b.SourceSnapshotID, b.ObjectID)
	return err
}

func (s *AgentStore) FinishAgent(ctx context.Context, input agent.FinishInput, errorRef string) error {
	if input.Status != "failed" && input.Status != "cancelled" {
		return errors.New("invalid terminal Agent status")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	_, err = tx.Exec(ctx, `UPDATE agents SET status=$3,error_object_id=$4,finished_at=now(),updated_at=now()
		WHERE tenant_id=$1 AND agent_id=$2 AND session_id=$5 AND status IN ('pending','running','waiting')`,
		input.Session.TenantID, input.Session.AgentID, input.Status, errorRef, input.Session.SessionID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_sessions SET status=$3,error_object_id=$4,finished_at=now(),updated_at=now()
		WHERE tenant_id=$1 AND session_id=$2 AND status IN ('pending','running') AND EXISTS
		(SELECT 1 FROM agents WHERE tenant_id=$1 AND agent_id=$5 AND session_id=$2 AND parent_agent_id IS NULL AND status=$3)`,
		input.Session.TenantID, input.Session.SessionID, input.Status, errorRef, input.Session.AgentID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
