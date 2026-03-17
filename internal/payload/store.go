// Package payload defines immutable, tenant-scoped runtime payload storage.
package payload

import (
	"context"
	"errors"
)

const MaxBytes = 16 << 20

var ErrNotFound = errors.New("verified payload not found")

// References are stored_objects UUIDs, not arbitrary URLs or object keys.
type Store interface {
	Put(ctx context.Context, tenantID, kind string, data []byte) (string, error)
	Get(ctx context.Context, tenantID, ref string) ([]byte, error)
}

type Object struct {
	ID        string
	TenantID  string
	Kind      string
	Bucket    string
	Key       string
	Version   string
	Checksum  []byte
	Size      int64
	Codec     string
	MediaType string
}

type Index interface {
	Register(context.Context, Object) (Object, error)
	Load(ctx context.Context, tenantID, ref string) (Object, error)
}
