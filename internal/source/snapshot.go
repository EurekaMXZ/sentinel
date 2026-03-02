// Package source defines externally imported Git versions and read-only snapshots.
package source

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound           = errors.New("source snapshot not found")
	ErrNotReady           = errors.New("source snapshot is not ready")
	ErrRepositoryMismatch = errors.New("source snapshots belong to different repositories")
)

type SnapshotStatus string

const (
	SnapshotPending SnapshotStatus = "pending"
	SnapshotReady   SnapshotStatus = "ready"
	SnapshotFailed  SnapshotStatus = "failed"
)

// Snapshot identity is unique by repository ID and immutable commit SHA.
type Snapshot struct {
	ID                string         `json:"source_snapshot_id"`
	RepositoryID      string         `json:"repository_id"`
	CommitSHA         string         `json:"commit_sha"`
	TreeSHA           string         `json:"tree_sha"`
	StorageManifestID string         `json:"storage_manifest_id"`
	Status            SnapshotStatus `json:"status"`
	CreatedAt         time.Time      `json:"created_at"`
}

// Page cursors must bind both the snapshot and all query parameters.
type Page struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

type VersionPage struct {
	Versions   []Snapshot `json:"versions"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

type SnapshotStore interface {
	Get(ctx context.Context, snapshotID string) (Snapshot, error)
	ListReady(ctx context.Context, repositoryID string, page Page) (VersionPage, error)
	// PublishReady requires a verified durable archive and a transactional index update.
	PublishReady(ctx context.Context, operationID string, snapshot Snapshot) error
}
