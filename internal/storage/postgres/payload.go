package postgres

import (
	"bytes"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"sentinel/internal/payload"
)

type PayloadIndex struct {
	pool *pgxpool.Pool
}

func NewPayloadIndex(pool *pgxpool.Pool) *PayloadIndex { return &PayloadIndex{pool: pool} }

func (s *PayloadIndex) Register(ctx context.Context, object payload.Object) (payload.Object, error) {
	_, err := s.pool.Exec(ctx, `INSERT INTO stored_objects
		(object_id, tenant_id, kind, bucket, object_key, version_id, checksum_sha256,
		 size_bytes, codec, media_type, status, verified_at)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,'application/json','verified',now())
		ON CONFLICT (bucket, object_key) DO NOTHING`, object.ID, object.TenantID, object.Kind,
		object.Bucket, object.Key, object.Version, object.Checksum, object.Size, object.Codec)
	if err != nil {
		return payload.Object{}, err
	}
	stored, err := scanObject(s.pool.QueryRow(ctx, `SELECT object_id::text, tenant_id::text, kind, bucket,
		object_key, COALESCE(version_id,''), checksum_sha256, size_bytes, codec FROM stored_objects
		WHERE bucket=$1 AND object_key=$2 AND tenant_id=$3 AND status='verified'`, object.Bucket, object.Key, object.TenantID))
	if err != nil {
		return payload.Object{}, err
	}
	if stored.Kind != object.Kind || stored.Size != object.Size || stored.Codec != object.Codec ||
		!bytes.Equal(stored.Checksum, object.Checksum) {
		return payload.Object{}, errors.New("immutable payload metadata conflict")
	}
	return stored, nil
}

func (s *PayloadIndex) Load(ctx context.Context, tenantID, ref string) (payload.Object, error) {
	return scanObject(s.pool.QueryRow(ctx, `SELECT object_id::text, tenant_id::text, kind, bucket,
		object_key, COALESCE(version_id,''), checksum_sha256, size_bytes, codec FROM stored_objects
		WHERE tenant_id=$1 AND object_id=$2 AND status='verified'`, tenantID, ref))
}

func scanObject(row pgx.Row) (payload.Object, error) {
	var object payload.Object
	err := row.Scan(&object.ID, &object.TenantID, &object.Kind, &object.Bucket,
		&object.Key, &object.Version, &object.Checksum, &object.Size, &object.Codec)
	if errors.Is(err, pgx.ErrNoRows) {
		err = payload.ErrNotFound
	}
	return object, err
}
