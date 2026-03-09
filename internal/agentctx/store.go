// Package agentctx owns Agent context snapshots, deltas, and compaction.
package agentctx

import "context"

// Head references the current Context and recovery cursors, independently of resources.
type Head struct {
	TenantID             string `json:"tenant_id"`
	AgentID              string `json:"agent_id"`
	RowVersion           int64  `json:"row_version"`
	ContextVersion       int64  `json:"context_version"`
	LastEventSequence    int64  `json:"last_event_sequence"`
	ContextEventSequence int64  `json:"context_event_sequence"`
	SnapshotRecordID     string `json:"snapshot_record_id"`
	SnapshotRef          string `json:"snapshot_ref"`
	NextStepID           int64  `json:"next_step_id"`
}

// Store persists metadata; Context payloads are stored in MinIO/S3.
type Store interface {
	LoadHead(ctx context.Context, tenantID, agentID string) (Head, error)
	// CompareAndSwap compares RowVersion, including changes that only advance
	// event/step cursors. ContextVersion advances only when Context changes.
	CompareAndSwap(ctx context.Context, expectedRowVersion int64, next Head) error
}
