// Package s3 contains MinIO/S3 adapters for immutable payloads and Git archives.
package s3

import (
	"errors"
	"fmt"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"sentinel/internal/platform/config"
)

// New creates a client without making network requests or creating buckets.
func New(cfg config.S3) (*minio.Client, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("S3_ENDPOINT and S3_BUCKET are required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("S3_ACCESS_KEY and S3_SECRET_KEY are required")
	}
	c, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseTLS,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	return c, nil
}
