package s3

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"

	"sentinel/internal/payload"
)

// PayloadStore stores immutable gzip objects and verifies their indexed checksum
// on every read. The bucket must be provisioned separately.
type PayloadStore struct {
	client *minio.Client
	bucket string
	index  payload.Index
}

func NewPayloadStore(client *minio.Client, bucket string, index payload.Index) *PayloadStore {
	return &PayloadStore{client: client, bucket: bucket, index: index}
}

func (s *PayloadStore) Put(ctx context.Context, tenantID, kind string, data []byte) (string, error) {
	if id, err := uuid.Parse(tenantID); err != nil || id == uuid.Nil {
		return "", errors.New("invalid payload tenant ID")
	}
	if len(data) > payload.MaxBytes || kind == "" || strings.ContainsAny(kind, "/\\.\x00") {
		return "", errors.New("invalid payload kind or payload exceeds size limit")
	}
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	encoded := buffer.Bytes()
	checksum := sha256.Sum256(encoded)
	key := "runtime/" + tenantID + "/" + kind + "/" + hex.EncodeToString(checksum[:]) + ".gz"
	info, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(encoded), int64(len(encoded)),
		minio.PutObjectOptions{ContentType: "application/gzip"})
	if err != nil {
		return "", fmt.Errorf("upload %s payload: %w", kind, err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	object, err := s.index.Register(ctx, payload.Object{ID: id.String(), TenantID: tenantID,
		Kind: kind, Bucket: s.bucket, Key: key, Version: info.VersionID,
		Checksum: checksum[:], Size: int64(len(encoded)), Codec: "gzip"})
	if err != nil {
		return "", fmt.Errorf("index %s payload: %w", kind, err)
	}
	return object.ID, nil
}

func (s *PayloadStore) Get(ctx context.Context, tenantID, ref string) ([]byte, error) {
	object, err := s.index.Load(ctx, tenantID, ref)
	if err != nil {
		return nil, err
	}
	if object.Size > payload.MaxBytes+(1<<20) || object.Size < 0 {
		return nil, errors.New("stored payload exceeds size limit")
	}
	reader, err := s.client.GetObject(ctx, object.Bucket, object.Key, minio.GetObjectOptions{VersionID: object.Version})
	if err != nil {
		return nil, fmt.Errorf("open payload: %w", err)
	}
	defer reader.Close()
	encoded, err := io.ReadAll(io.LimitReader(reader, object.Size+1))
	if err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	checksum := sha256.Sum256(encoded)
	if int64(len(encoded)) != object.Size || !bytes.Equal(checksum[:], object.Checksum) {
		return nil, errors.New("payload integrity verification failed")
	}
	var decoded io.Reader = bytes.NewReader(encoded)
	if object.Codec == "gzip" {
		gzipReader, err := gzip.NewReader(decoded)
		if err != nil {
			return nil, fmt.Errorf("open gzip payload: %w", err)
		}
		defer gzipReader.Close()
		decoded = gzipReader
	} else if object.Codec != "identity" {
		return nil, fmt.Errorf("unsupported runtime payload codec %q", object.Codec)
	}
	data, err := io.ReadAll(io.LimitReader(decoded, payload.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	if len(data) > payload.MaxBytes {
		return nil, errors.New("decoded payload exceeds size limit")
	}
	return data, nil
}
