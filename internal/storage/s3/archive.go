package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"

	"sentinel/internal/payload"
	"sentinel/internal/source"
)

type ArchiveStore struct {
	client   *minio.Client
	bucket   string
	index    payload.Index
	maxBytes int64
}

func NewArchiveStore(client *minio.Client, bucket string, index payload.Index, maxBytes int64) *ArchiveStore {
	return &ArchiveStore{client: client, bucket: bucket, index: index, maxBytes: maxBytes}
}

func (s *ArchiveStore) Put(ctx context.Context, tenant, repository, path string) (source.ArchiveObject, error) {
	if !source.ValidID(tenant) || !source.ValidID(repository) {
		return source.ArchiveObject{}, errors.New("invalid archive identity")
	}
	file, err := os.Open(path)
	if err != nil {
		return source.ArchiveObject{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, s.maxBytes+1))
	if err != nil || size <= 0 || size > s.maxBytes {
		return source.ArchiveObject{}, errors.New("bundle is empty, unreadable, or exceeds size limit")
	}
	checksum := hash.Sum(nil)
	key := "git/" + tenant + "/" + repository + "/bundles/" + hex.EncodeToString(checksum) + ".bundle"
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return source.ArchiveObject{}, err
	}
	info, err := s.client.PutObject(ctx, s.bucket, key, file, size,
		minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: true})
	if err != nil {
		return source.ArchiveObject{}, fmt.Errorf("upload Git bundle: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return source.ArchiveObject{}, err
	}
	object := payload.Object{ID: id.String(), TenantID: tenant, Kind: "git_bundle", Bucket: s.bucket,
		Key: key, Version: info.VersionID, Checksum: checksum, Size: size, Codec: "identity", MediaType: "application/octet-stream"}
	// Verify uploaded bytes before making them available to a manifest transaction.
	if err := s.download(ctx, object, io.Discard); err != nil {
		return source.ArchiveObject{}, err
	}
	saved, err := s.index.Register(ctx, object)
	if err != nil {
		return source.ArchiveObject{}, err
	}
	return source.ArchiveObject{Ref: saved.ID, Checksum: hex.EncodeToString(saved.Checksum), Size: saved.Size}, nil
}

func (s *ArchiveStore) Get(ctx context.Context, tenant, ref, path string) error {
	object, err := s.index.Load(ctx, tenant, ref)
	if err != nil {
		return err
	}
	if object.TenantID != tenant || object.Kind != "git_bundle" || object.Codec != "identity" {
		return errors.New("object is not a tenant Git bundle")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = s.download(ctx, object, file)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

func (s *ArchiveStore) download(ctx context.Context, object payload.Object, target io.Writer) error {
	if object.Size <= 0 || object.Size > s.maxBytes || len(object.Checksum) != sha256.Size {
		return errors.New("invalid Git bundle size or checksum")
	}
	reader, err := s.client.GetObject(ctx, object.Bucket, object.Key, minio.GetObjectOptions{VersionID: object.Version})
	if err != nil {
		return err
	}
	defer reader.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(target, hash), io.LimitReader(reader, object.Size+1))
	if err != nil {
		return err
	}
	if size != object.Size || !bytes.Equal(hash.Sum(nil), object.Checksum) {
		return errors.New("Git bundle integrity verification failed")
	}
	return nil
}
