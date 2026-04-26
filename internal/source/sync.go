package source

import (
	"context"
	"time"
)

type Repository struct {
	ID             string   `json:"repository_id"`
	TenantID       string   `json:"tenant_id"`
	RemoteURL      string   `json:"remote_url"`
	CredentialRef  string   `json:"credential_ref,omitempty"`
	TrackedRefs    []string `json:"tracked_refs"`
	ObjectFormat   string   `json:"object_format"`
	ArchiveHeadID  string   `json:"archive_head_id,omitempty"`
	ArchiveVersion int64    `json:"archive_version"`
	Status         string   `json:"status"`
}

// SyncRequest IDs remain stable across retries. Credentials are resolved separately.
type SyncRequest struct {
	TenantID     string `json:"tenant_id"`
	RepositoryID string `json:"repository_id"`
	SyncID       string `json:"sync_id"`
	OperationID  string `json:"operation_id"`
}

// SyncResult contains references, never Git bundle data or source file bodies.
type SyncResult struct {
	RepositoryID  string `json:"repository_id"`
	SyncID        string `json:"sync_id"`
	ManifestID    string `json:"manifest_id"`
	ResultRef     string `json:"result_ref"`
	SnapshotCount int    `json:"snapshot_count"`
}

// SyncRecord is the repository-management view of one synchronization job.
type SyncRecord struct {
	SyncID      string     `json:"sync_id"`
	Status      string     `json:"status"`
	TriggerType string     `json:"trigger_type"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	ResultRef   string     `json:"result_ref,omitempty"`
	ErrorRef    string     `json:"error_ref,omitempty"`
}

// RefRecord is the latest observed remote ref and its ready snapshot, if any.
type RefRecord struct {
	Name           string    `json:"ref_name"`
	TargetOID      string    `json:"target_oid,omitempty"`
	CommitSHA      string    `json:"commit_sha,omitempty"`
	SyncID         string    `json:"sync_id"`
	ObservedAt     time.Time `json:"observed_at"`
	SnapshotID     string    `json:"source_snapshot_id,omitempty"`
	SnapshotStatus string    `json:"snapshot_status,omitempty"`
}

type Synchronizer interface {
	Sync(ctx context.Context, request SyncRequest) (SyncResult, error)
}
