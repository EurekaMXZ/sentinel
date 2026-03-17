package source

import "context"

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

type Synchronizer interface {
	Sync(ctx context.Context, request SyncRequest) (SyncResult, error)
}
