package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

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

func (b ResourceBinding) Validate() error {
	if strings.TrimSpace(b.BindingName) == "" || len(b.BindingName) > 128 {
		return errors.New("invalid resource binding name")
	}
	validID := func(value string) bool {
		id, err := uuid.Parse(value)
		return err == nil && id != uuid.Nil
	}
	valid := false
	switch b.Kind {
	case ResourceSourceRepository:
		valid = validID(b.RepositoryID) && b.SourceSnapshotID == "" && b.ObjectID == ""
	case ResourceSourceSnapshot:
		valid = validID(b.RepositoryID) && validID(b.SourceSnapshotID) && b.ObjectID == ""
	case ResourceObject:
		valid = validID(b.ObjectID) && b.RepositoryID == "" && b.SourceSnapshotID == ""
	}
	if !valid {
		return errors.New("invalid resource reference shape")
	}
	return nil
}

// CanAccess checks whether a resource is inside a server-assigned set. It is
// used for an Agent's own inputs and, with a smaller scope, for delegation.
func CanAccess(resources []ResourceBinding, resource ResourceBinding) bool {
	for _, allowed := range resources {
		switch allowed.Kind {
		case ResourceSourceRepository:
			if resource.RepositoryID == allowed.RepositoryID &&
				(resource.Kind == ResourceSourceRepository || resource.Kind == ResourceSourceSnapshot) {
				return true
			}
		case ResourceSourceSnapshot:
			if resource.Kind == ResourceSourceSnapshot && resource.RepositoryID == allowed.RepositoryID && resource.SourceSnapshotID == allowed.SourceSnapshotID {
				return true
			}
		case ResourceObject:
			if resource.Kind == ResourceObject && resource.ObjectID == allowed.ObjectID {
				return true
			}
		}
	}
	return false
}

// CanAccessRepository checks repository-scoped tools such as version listing.
// A snapshot binding implicitly identifies its repository.
func CanAccessRepository(resources []ResourceBinding, repositoryID string) bool {
	for _, binding := range resources {
		if binding.RepositoryID == repositoryID &&
			(binding.Kind == ResourceSourceRepository || binding.Kind == ResourceSourceSnapshot) {
			return true
		}
	}
	return false
}

// CanDelegate checks a server-assigned scope separately from the Agent's inputs.
func CanDelegate(scope []ResourceBinding, resource ResourceBinding) bool {
	return CanAccess(scope, resource)
}

// ResourceStore is read through Activities. Agent bindings are persisted explicitly
// at creation; a child does not implicitly inherit its parent's bindings.
type ResourceStore interface {
	ListSessionBindings(ctx context.Context, tenantID, sessionID string) ([]ResourceBinding, error)
	ListAgentBindings(ctx context.Context, tenantID, agentID string) ([]ResourceBinding, error)
}
