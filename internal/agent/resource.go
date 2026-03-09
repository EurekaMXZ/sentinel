package agent

import "context"

type ResourceKind string

const (
	ResourceSourceRepository ResourceKind = "source_repository"
	ResourceSourceSnapshot   ResourceKind = "source_snapshot"
	ResourceObject           ResourceKind = "object"
)

// ResourceBinding names an optional task input. A snapshot includes its repository;
// an object has no repository. Bindings describe context, not access permissions.
type ResourceBinding struct {
	BindingName      string       `json:"binding_name"`
	Kind             ResourceKind `json:"resource_kind"`
	RepositoryID     string       `json:"repository_id,omitempty"`
	SourceSnapshotID string       `json:"source_snapshot_id,omitempty"`
	ObjectID         string       `json:"object_id,omitempty"`
}

// ResourceStore is read through Activities. Agent bindings are persisted explicitly
// at creation; a child does not implicitly inherit its parent's bindings.
type ResourceStore interface {
	ListSessionBindings(ctx context.Context, tenantID, sessionID string) ([]ResourceBinding, error)
	ListAgentBindings(ctx context.Context, tenantID, agentID string) ([]ResourceBinding, error)
}
