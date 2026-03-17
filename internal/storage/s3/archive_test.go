package s3_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"sentinel/internal/payload"
)

func TestArchiveStreamsBeyondRuntimePayloadLimitAndVerifiesRestore(t *testing.T) {
	f := newObjectFixture(t)
	tenant, repo := uuid.NewString(), uuid.NewString()
	data := bytes.Repeat([]byte("x"), payload.MaxBytes+1)
	path := filepath.Join(t.TempDir(), "large.bundle")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	object, err := f.archives.Put(t.Context(), tenant, repo, path)
	if err != nil || object.Size != int64(len(data)) {
		t.Fatalf("archive=%+v err=%v", object, err)
	}
	if same, err := f.archives.Put(t.Context(), tenant, repo, path); err != nil || same.Ref != object.Ref {
		t.Fatalf("duplicate archive=%+v err=%v", same, err)
	}
	restored := filepath.Join(t.TempDir(), "restored.bundle")
	if err := f.archives.Get(t.Context(), tenant, object.Ref, restored); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(restored)
	if err != nil || !bytes.Equal(data, got) {
		t.Fatalf("restore bytes=%d err=%v", len(got), err)
	}
	before := f.requests.Load()
	if err := f.archives.Get(t.Context(), uuid.NewString(), object.Ref, filepath.Join(t.TempDir(), "forbidden.bundle")); !errors.Is(err, payload.ErrNotFound) || f.requests.Load() != before {
		t.Fatalf("cross-tenant archive=%v", err)
	}
	saved, err := f.index.Load(t.Context(), tenant, object.Ref)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	key := "/" + saved.Bucket + "/" + saved.Key
	f.objects[key][0] ^= 1
	f.mu.Unlock()
	partial := filepath.Join(t.TempDir(), "corrupt.bundle")
	if err := f.archives.Get(t.Context(), tenant, object.Ref, partial); err == nil {
		t.Fatal("corrupt archive accepted")
	}
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial archive retained: %v", err)
	}
}
