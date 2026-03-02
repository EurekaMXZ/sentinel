// Package agent defines Agent session inputs and durable runtime state.
// Workflow state carries references; model requests and tool payloads belong in storage.
package agent

type SessionInput struct {
	SessionID        string `json:"session_id"`
	AgentID          string `json:"agent_id"`
	RepositoryID     string `json:"repository_id"`
	SourceSnapshotID string `json:"source_snapshot_id"`
	TaskRef          string `json:"task_ref"`
	ContextRef       string `json:"context_ref,omitempty"`
}

type RuntimeState struct {
	SessionID          string `json:"session_id"`
	AgentID            string `json:"agent_id"`
	ContextVersion     int64  `json:"context_version"`
	LastEventSequence  int64  `json:"last_event_sequence"`
	SourceSnapshotID   string `json:"source_snapshot_id"`
	PendingOperationID string `json:"pending_operation_id,omitempty"`
	NextStepID         int64  `json:"next_step_id"`
	StepCount          int    `json:"step_count"`
}

type ContinueState struct {
	AgentID            string `json:"agent_id"`
	ContextVersion     int64  `json:"context_version"`
	LastEventSequence  int64  `json:"last_event_sequence"`
	SourceSnapshotID   string `json:"source_snapshot_id"`
	PendingOperationID string `json:"pending_operation_id,omitempty"`
	NextStepID         int64  `json:"next_step_id"`
}

type Result struct {
	AgentID          string `json:"agent_id"`
	SourceSnapshotID string `json:"source_snapshot_id"`
	ResultRef        string `json:"result_ref"`
}
