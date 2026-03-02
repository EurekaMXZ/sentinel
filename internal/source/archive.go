package source

import (
	"context"
	"time"
)

type BundleKind string

const (
	BundleBase      BundleKind = "base"
	BundleIncrement BundleKind = "increment"
)

// Manifest dependencies describe archive recovery, independently of Git commit parents.
type Manifest struct {
	ID                     string     `json:"manifest_id"`
	RepositoryID           string     `json:"repository_id"`
	ParentManifestID       string     `json:"parent_manifest_id,omitempty"`
	BundleKind             BundleKind `json:"bundle_kind"`
	BundleRef              string     `json:"bundle_ref"`
	BundleChecksum         string     `json:"bundle_checksum"`
	PrerequisiteCommitSHAs []string   `json:"prerequisite_commit_shas"`
	OperationID            string     `json:"operation_id"`
	CreatedAt              time.Time  `json:"created_at"`
}

// Restorer verifies bundle checksums and prerequisites before populating a local bare cache.
type Restorer interface {
	Restore(ctx context.Context, snapshot Snapshot) error
}
