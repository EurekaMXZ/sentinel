package source

import (
	"context"
	"errors"
	"time"
)

const (
	WorkflowName   = "sentinel.SourceSyncWorkflow"
	SyncActivity   = "sentinel.source.Sync"
	FinishActivity = "sentinel.source.Finish"
)

var (
	ErrBusy      = errors.New("repository synchronization is busy")
	ErrConflict  = errors.New("source state changed")
	ErrLeaseLost = errors.New("source synchronization lease lost")
	ErrTerminal  = errors.New("source synchronization is terminal")
)

type RefObservation struct {
	Name      string `json:"ref_name"`
	TargetOID string `json:"target_oid"`
	CommitSHA string `json:"peeled_commit_sha,omitempty"`
}

type Commit struct {
	SHA     string `json:"commit_sha"`
	TreeSHA string `json:"tree_sha"`
}

type GitState struct {
	Roots   []string `json:"roots"`
	Commits []Commit `json:"commits"`
}

type BundleInfo struct {
	Empty         bool
	Prerequisites []string
}

// Each attempt uses its own bare workspace. A lost lease cannot corrupt another
// worker's cache; only a fenced database transaction can publish its result.
type Workspace interface {
	Directory() string
	Import(context.Context, string) error
	Fetch(context.Context, string, []string, string) ([]RefObservation, error)
	State(context.Context) (GitState, error)
	Bundle(context.Context, string, []string) (BundleInfo, error)
	Verify(context.Context) error
	// Read-only operations resolve immutable commits; offsets paginate results.
	HasCommit(context.Context, string) (bool, error)
	ListFiles(context.Context, string, string, int, int) ([]FileEntry, bool, error)
	ReadFile(context.Context, string, string, int, int, int, int) (string, int, int, bool, error)
	Search(context.Context, string, string, string, int, int, int) ([]SearchMatch, bool, bool, error)
	Diff(context.Context, string, string, string, int, int, int) (string, int, bool, bool, error)
	Close() error
}

type Git interface {
	Open(context.Context, string, string, string) (Workspace, error)
}

type ArchiveObject struct {
	Ref      string `json:"object_id"`
	Checksum string `json:"checksum_sha256"`
	Size     int64  `json:"size_bytes"`
}

// Bundle storage streams files instead of using the small runtime payload limit.
type ArchiveStore interface {
	Put(context.Context, string, string, string) (ArchiveObject, error)
	Get(context.Context, string, string, string) error
}

type ArchiveDocument struct {
	Version      int      `json:"version"`
	TenantID     string   `json:"tenant_id"`
	Manifest     Manifest `json:"manifest"`
	ObjectFormat string   `json:"object_format"`
	Roots        []string `json:"roots"`
}

// Candidate is immutable and is saved before publication. A retry can reuse it
// without contacting GitHub if the repository archive head has not advanced.
type Candidate struct {
	Version                int              `json:"version"`
	Request                SyncRequest      `json:"request"`
	ExpectedArchiveVersion int64            `json:"expected_archive_version"`
	ParentManifestID       string           `json:"parent_manifest_id,omitempty"`
	Manifest               Manifest         `json:"manifest"`
	ManifestRef            string           `json:"manifest_object_id,omitempty"`
	ArchiveAttempt         int64            `json:"archive_attempt"`
	NewArchive             bool             `json:"new_archive"`
	Refs                   []RefObservation `json:"refs"`
	Snapshots              []Snapshot       `json:"snapshots"`
}

type SyncJob struct {
	Request                SyncRequest
	ConfigRef              string
	WorkflowID             string
	Status                 string
	ExpectedArchiveVersion int64
	ResultRef              string
	ErrorRef               string
	Owner                  string
	Fence                  int64
	RequestHash            []byte
}

type ManifestRecord struct {
	Manifest  Manifest
	ObjectRef string
}

type SyncStore interface {
	CreateRepository(context.Context, Repository) (Repository, error)
	LoadRepository(context.Context, string, string) (Repository, error)
	CreateSync(context.Context, SyncJob) (SyncJob, error)
	LoadSync(context.Context, SyncRequest) (SyncJob, error)
	ClaimSync(context.Context, SyncRequest, string, time.Duration) (SyncJob, Repository, error)
	RenewSync(context.Context, SyncJob, time.Duration) error
	SaveCandidate(context.Context, SyncJob, Candidate, string) error
	Publish(context.Context, SyncJob, Candidate, string) (SyncResult, error)
	FinishSync(context.Context, SyncRequest, string, string) error
	LoadManifest(context.Context, string, string, string) (ManifestRecord, error)
	KnownCommits(context.Context, string, string) (map[string]Snapshot, error)
}

type WorkflowInput struct {
	Request SyncRequest `json:"request"`
}

type FinishInput struct {
	Request SyncRequest `json:"request"`
	Status  string      `json:"status"`
}

func WorkflowID(tenantID, syncID string) string { return "source/" + tenantID + "/" + syncID }

func OperationID(repositoryID, syncID string) string { return "source/" + repositoryID + "/" + syncID }
