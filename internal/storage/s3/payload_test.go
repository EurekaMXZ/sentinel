package s3_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"sentinel/internal/payload"
	storage "sentinel/internal/storage/s3"
)

type memoryIndex struct {
	mu      sync.Mutex
	objects map[string]payload.Object
}

func (m *memoryIndex) Register(_ context.Context, object payload.Object) (payload.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, saved := range m.objects {
		if saved.Bucket == object.Bucket && saved.Key == object.Key {
			return saved, nil
		}
	}
	m.objects[object.ID] = object
	return object, nil
}

func (m *memoryIndex) Load(_ context.Context, tenantID, ref string) (payload.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	object, ok := m.objects[ref]
	if !ok || object.TenantID != tenantID {
		return payload.Object{}, payload.ErrNotFound
	}
	return object, nil
}

type objectFixture struct {
	store    *storage.PayloadStore
	index    *memoryIndex
	mu       sync.Mutex
	objects  map[string][]byte
	requests atomic.Int32
}

func newObjectFixture(t *testing.T) *objectFixture {
	t.Helper()
	f := &objectFixture{index: &memoryIndex{objects: make(map[string]payload.Object)}, objects: make(map[string][]byte)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("S3 request was not signed by the SDK")
		}
		if r.URL.Query().Get("versionId") != "" && r.URL.Query().Get("versionId") != "version-1" {
			t.Error("unexpected object version")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			f.objects[r.URL.Path] = data
			w.Header().Set("ETag", `"test-etag"`)
			w.Header().Set("X-Amz-Version-Id", "version-1")
		case http.MethodGet, http.MethodHead:
			data, ok := f.objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/gzip")
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.Header().Set("ETag", `"test-etag"`)
			w.Header().Set("Last-Modified", time.Unix(0, 0).UTC().Format(http.TimeFormat))
			w.Header().Set("X-Amz-Version-Id", "version-1")
			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
		default:
			t.Errorf("unexpected S3 method: %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	client, err := minio.New(strings.TrimPrefix(server.URL, "https://"), &minio.Options{
		Creds: credentials.NewStaticV4("test-access", "test-secret", ""), Secure: true,
		Region: "us-east-1", Transport: server.Client().Transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.store = storage.NewPayloadStore(client, "runtime-test", f.index)
	return f
}

func TestPayloadRoundTripContentAddressingAndTenantIsolation(t *testing.T) {
	f := newObjectFixture(t)
	tenant := uuid.NewString()
	data := []byte(`{"task":"independent agent input"}`)
	ref, err := f.store.Put(t.Context(), tenant, "agent-task", data)
	if err != nil {
		t.Fatal(err)
	}
	object, err := f.index.Load(t.Context(), tenant, ref)
	if err != nil || object.Codec != "gzip" || object.Version != "version-1" || !strings.HasPrefix(object.Key, "runtime/"+tenant+"/agent-task/") {
		t.Fatalf("indexed object=%+v err=%v", object, err)
	}
	got, err := f.store.Get(t.Context(), tenant, ref)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("payload=%s err=%v", got, err)
	}
	if again, err := f.store.Put(t.Context(), tenant, "agent-task", data); err != nil || again != ref {
		t.Fatalf("content-addressed reference changed: %s %v", again, err)
	}
	before := f.requests.Load()
	if _, err := f.store.Get(t.Context(), uuid.NewString(), ref); !errors.Is(err, payload.ErrNotFound) || f.requests.Load() != before {
		t.Fatalf("cross-tenant read reached S3: requests=%d err=%v", f.requests.Load()-before, err)
	}
}

func TestPayloadRejectsCorruptionAndOversizedDecodedBody(t *testing.T) {
	for _, mode := range []string{"checksum", "truncated", "decoded limit", "unsupported codec"} {
		t.Run(mode, func(t *testing.T) {
			f := newObjectFixture(t)
			tenant := uuid.NewString()
			ref, err := f.store.Put(t.Context(), tenant, "agent-context", []byte(`{"version":1}`))
			if err != nil {
				t.Fatal(err)
			}
			f.index.mu.Lock()
			object := f.index.objects[ref]
			f.mu.Lock()
			path := "/" + object.Bucket + "/" + object.Key
			encoded := f.objects[path]
			switch mode {
			case "checksum":
				encoded = append([]byte(nil), encoded...)
				encoded[len(encoded)-1] ^= 1
			case "truncated":
				encoded = encoded[:len(encoded)-1]
			case "decoded limit":
				var buffer bytes.Buffer
				writer := gzip.NewWriter(&buffer)
				_, _ = writer.Write(bytes.Repeat([]byte("x"), payload.MaxBytes+1))
				_ = writer.Close()
				encoded = buffer.Bytes()
				hash := sha256.Sum256(encoded)
				object.Size, object.Checksum = int64(len(encoded)), hash[:]
			case "unsupported codec":
				object.Codec = "zstd"
			}
			f.objects[path] = encoded
			f.index.objects[ref] = object
			f.mu.Unlock()
			f.index.mu.Unlock()
			if _, err := f.store.Get(t.Context(), tenant, ref); err == nil {
				t.Fatal("invalid stored payload was accepted")
			}
		})
	}
}

func TestPayloadRejectsInvalidInputBeforeUpload(t *testing.T) {
	f := newObjectFixture(t)
	for _, test := range []struct {
		tenant string
		kind   string
		data   []byte
	}{
		{"invalid", "agent-task", nil},
		{uuid.Nil.String(), "agent-task", nil},
		{uuid.NewString(), "../agent-task", nil},
		{uuid.NewString(), "agent-task", make([]byte, payload.MaxBytes+1)},
	} {
		if _, err := f.store.Put(t.Context(), test.tenant, test.kind, test.data); err == nil {
			t.Error("invalid payload input was accepted")
		}
	}
	if f.requests.Load() != 0 {
		t.Fatalf("invalid input reached S3: %d requests", f.requests.Load())
	}
}
