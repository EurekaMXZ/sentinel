package source

import "context"

type Repository struct {
	ID            string   `json:"repository_id"`
	TenantID      string   `json:"tenant_id"`
	RemoteURL     string   `json:"remote_url"`
	TrackedRefs   []string `json:"tracked_refs"`
	ArchiveHeadID string   `json:"archive_head_id,omitempty"`
}

// SyncRequest IDs remain stable across retries. Credentials are resolved separately.
type SyncRequest struct {
	RepositoryID string `json:"repository_id"`
	SyncID       string `json:"sync_id"`
	OperationID  string `json:"operation_id"`
}

// SyncResult contains references, never Git bundle data or source file bodies.
type SyncResult struct {
	RepositoryID string `json:"repository_id"`
	ManifestID   string `json:"manifest_id"`
	ResultRef    string `json:"result_ref"`
}

type Synchronizer interface {
	Sync(ctx context.Context, request SyncRequest) (SyncResult, error)
}
