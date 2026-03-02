// Package agentctx owns Agent context snapshots, deltas, and compaction.
package agentctx

import "context"

// Head references the current Context separately from its pinned source snapshot.
type Head struct {
	AgentID           string `json:"agent_id"`
	ContextVersion    int64  `json:"context_version"`
	LastEventSequence int64  `json:"last_event_sequence"`
	SnapshotRef       string `json:"snapshot_ref"`
	SourceSnapshotID  string `json:"source_snapshot_id"`
}

// Store persists metadata; Context payloads are stored in MinIO/S3.
type Store interface {
	LoadHead(ctx context.Context, agentID string) (Head, error)
	// CompareAndSwap must reject an update when the persisted version differs.
	CompareAndSwap(ctx context.Context, expectedVersion int64, next Head) error
}
