// Package eventlog defines immutable Agent events and their index boundary.
package eventlog

import (
	"context"
	"time"
)

type Event struct {
	EventID          string    `json:"event_id"`
	AgentID          string    `json:"agent_id"`
	OperationID      string    `json:"operation_id"`
	Sequence         int64     `json:"sequence"`
	Type             string    `json:"type"`
	PayloadRef       string    `json:"payload_ref"`
	SourceSnapshotID string    `json:"source_snapshot_id,omitempty"`
	BaseSnapshotID   string    `json:"base_snapshot_id,omitempty"`
	TargetSnapshotID string    `json:"target_snapshot_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// Store must make retries of the same event ID idempotent.
type Store interface {
	Append(ctx context.Context, event Event) error
}
