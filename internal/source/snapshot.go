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
	ErrPathNotFound       = errors.New("source path not found")
	ErrNotFile            = errors.New("source path is not a file")
	ErrBinaryFile         = errors.New("source file is binary")
	ErrLineRange          = errors.New("invalid source line range")
	ErrFileTooLarge       = errors.New("source file exceeds the read limit")
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
	RepositoryID string     `json:"repository_id"`
	Versions     []Snapshot `json:"versions"`
	NextCursor   string     `json:"next_cursor,omitempty"`
}

type SnapshotStore interface {
	Get(ctx context.Context, snapshotID string) (Snapshot, error)
	ListReady(ctx context.Context, repositoryID string, page Page) (VersionPage, error)
	// PublishReady requires a verified durable archive and a transactional index update.
	PublishReady(ctx context.Context, operationID string, snapshot Snapshot) error
}

// ReadStore is used by the Source Worker to resolve tenant-scoped versions for
// read-only tools. It never exposes objects from another tenant or repository.
type ReadStore interface {
	LoadRepository(ctx context.Context, tenantID, repositoryID string) (Repository, error)
	LoadSnapshot(ctx context.Context, tenantID, repositoryID, snapshotID string) (Snapshot, error)
	ListReadySnapshots(ctx context.Context, tenantID, repositoryID string, offset, limit int) ([]Snapshot, bool, error)
}
