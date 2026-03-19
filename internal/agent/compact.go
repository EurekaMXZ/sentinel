package agent

import (
	"fmt"

	"github.com/google/uuid"
)

// SourceRef identifies immutable evidence retained by a compacted Context.
// References are validated against the Agent's delegation scope or existing
// context evidence; they never grant access by themselves.
type SourceRef struct {
	RepositoryID     string `json:"repository_id"`
	SourceSnapshotID string `json:"source_snapshot_id"`
}

func (r SourceRef) Valid() bool {
	validID := func(value string) bool {
		id, err := uuid.Parse(value)
		return err == nil && id != uuid.Nil
	}
	return validID(r.RepositoryID) && validID(r.SourceSnapshotID)
}

// CompactSummary is the model-visible state retained after compaction. The
// server retains task instructions, tool policy, and resource bindings itself.
type CompactSummary struct {
	TaskRequirements  []string    `json:"task_requirements"`
	Constraints       []string    `json:"constraints"`
	Decisions         []string    `json:"decisions"`
	CurrentState      []string    `json:"current_state"`
	OpenItems         []string    `json:"open_items"`
	Errors            []string    `json:"errors"`
	ChildAgentResults []string    `json:"child_agent_results"`
	ToolArtifactRefs  []string    `json:"tool_artifact_refs"`
	SourceRefs        []SourceRef `json:"source_refs"`
}

type CompactInput struct {
	Session    SessionInput `json:"session"`
	RowVersion int64        `json:"row_version"`
}

// CompactRecord is the durable compaction operation for one Agent head version.
type CompactRecord struct {
	Input                SessionInput
	ID                   string
	OperationID          string
	SourceRowVersion     int64
	SourceContextVersion int64
	SourceEventSequence  int64
	RequestRef           string
	ResponseRef          string
	CandidateRef         string
	Status               string
	LeaseOwner           string
	FencingToken         int64
	RequestHash          []byte
}

type CompactPublication struct {
	Record          CompactRecord
	ContextRecordID string
	SnapshotRef     string
	SourceRefs      []SourceRef
}

// CompactOperation is stable across retries and Continue-As-New. A new head
// version uses a new operation, while a stale candidate is marked separately.
func CompactOperation(agentID string, rowVersion int64) string {
	return fmt.Sprintf("%s:%d:compact", agentID, rowVersion)
}
