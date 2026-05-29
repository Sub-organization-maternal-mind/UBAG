package artifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const defaultBucket = "ubag-artifacts"

// MinIOArtifactStore stores artifact bytes in a MinIO (or S3-compatible)
// bucket and tracks metadata in an in-process map or a Postgres table.
//
// Environment variables consumed by NewMinIOArtifactStoreFromEnv:
//
//	UBAG_MINIO_ENDPOINT    – host:port of the MinIO server (required)
//	UBAG_MINIO_ACCESS_KEY  – MinIO access key (required)
//	UBAG_MINIO_SECRET_KEY  – MinIO secret key (required)
//	UBAG_MINIO_BUCKET      – bucket name (default ubag-artifacts)
//	UBAG_MINIO_USE_SSL     – "true" to enable TLS (default false)
type MinIOArtifactStore struct {
	client *minio.Client
	bucket string
	meta   ArtifactMeta
}

// ArtifactMeta tracks artifact metadata independently of blob storage so the
// implementation can be swapped between memory and Postgres.
type ArtifactMeta interface {
	Ready(ctx context.Context) error
	Put(ctx context.Context, rec ArtifactRecord) error
	Get(ctx context.Context, jobID, key string) (ArtifactRecord, error)
	List(ctx context.Context, jobID string) ([]ArtifactRecord, error)
	Delete(ctx context.Context, jobID, key string) error
}

// NewMinIOArtifactStore constructs a MinIOArtifactStore.
// meta may be nil; a MemoryArtifactMeta will be used in that case.
func NewMinIOArtifactStore(endpoint, accessKey, secretKey, bucket string, useSSL bool, meta ArtifactMeta) (*MinIOArtifactStore, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("minio: endpoint is required")
	}
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("minio: access key and secret key are required")
	}
	if bucket == "" {
		bucket = defaultBucket
	}
	if meta == nil {
		meta = NewMemoryArtifactMeta()
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio: client init failed: %w", err)
	}

	return &MinIOArtifactStore{
		client: client,
		bucket: bucket,
		meta:   meta,
	}, nil
}

// Ready checks connectivity to MinIO and ensures the bucket exists.
func (s *MinIOArtifactStore) Ready(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("minio: bucket check failed: %w", err)
	}
	if !exists {
		if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
			// Ignore AlreadyOwnedByYou / BucketAlreadyExists races.
			if merr, ok := err.(minio.ErrorResponse); ok &&
				(merr.Code == "BucketAlreadyOwnedByYou" || merr.Code == "BucketAlreadyExists") {
				return s.meta.Ready(ctx)
			}
			return fmt.Errorf("minio: bucket create failed: %w", err)
		}
	}
	if err := s.meta.Ready(ctx); err != nil {
		return fmt.Errorf("minio: metadata readiness failed: %w", err)
	}
	return nil
}

// PutArtifact uploads bytes to MinIO and records metadata.
func (s *MinIOArtifactStore) PutArtifact(ctx context.Context, jobID, key, contentType string, r io.Reader, sizeBytes int64) (ArtifactRecord, error) {
	mapKey, err := makeArtifactMapKey(jobID, key)
	if err != nil {
		return ArtifactRecord{}, err
	}
	objectKey := minioObjectKey(mapKey.jobID, mapKey.key)

	// Tee through SHA-256 hash while uploading.
	h := sha256.New()
	tee := io.TeeReader(r, h)

	old, oldErr := s.meta.Get(ctx, mapKey.jobID, mapKey.key)
	if oldErr != nil && !IsNotFound(oldErr) {
		return ArtifactRecord{}, oldErr
	}

	info, err := s.client.PutObject(ctx, s.bucket, objectKey, tee, sizeBytes, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return ArtifactRecord{}, fmt.Errorf("minio: put object failed: %w", err)
	}

	rec := ArtifactRecord{
		JobID:       mapKey.jobID,
		Key:         mapKey.key,
		Bucket:      s.bucket,
		ObjectKey:   objectKey,
		ContentType: contentType,
		SizeBytes:   info.Size,
		Checksum:    hex.EncodeToString(h.Sum(nil)),
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.meta.Put(ctx, rec); err != nil {
		_ = s.client.RemoveObject(ctx, s.bucket, objectKey, minio.RemoveObjectOptions{})
		return ArtifactRecord{}, fmt.Errorf("minio: metadata put failed: %w", err)
	}
	if oldErr == nil && old.ObjectKey != "" && old.ObjectKey != objectKey {
		_ = s.client.RemoveObject(ctx, s.bucket, old.ObjectKey, minio.RemoveObjectOptions{})
	}
	return rec, nil
}

// GetArtifact downloads artifact bytes from MinIO.
func (s *MinIOArtifactStore) GetArtifact(ctx context.Context, jobID, key string) (io.ReadCloser, ArtifactRecord, error) {
	mapKey, err := makeArtifactMapKey(jobID, key)
	if err != nil {
		return nil, ArtifactRecord{}, err
	}
	rec, err := s.meta.Get(ctx, mapKey.jobID, mapKey.key)
	if err != nil {
		return nil, ArtifactRecord{}, err
	}

	objectKey := rec.ObjectKey
	if objectKey == "" {
		objectKey = legacyMinioObjectKey(mapKey.jobID, mapKey.key)
	}
	obj, err := s.client.GetObject(ctx, s.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, ArtifactRecord{}, fmt.Errorf("minio: get object failed: %w", err)
	}
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if isMinIONotFound(err) {
			return nil, ArtifactRecord{}, &ErrArtifactNotFound{JobID: mapKey.jobID, Key: mapKey.key}
		}
		return nil, ArtifactRecord{}, fmt.Errorf("minio: stat object failed: %w", err)
	}
	return obj, rec, nil
}

// ListArtifacts returns metadata for all artifacts stored for jobID.
func (s *MinIOArtifactStore) ListArtifacts(ctx context.Context, jobID string) ([]ArtifactRecord, error) {
	return s.meta.List(ctx, jobID)
}

// DeleteArtifact removes the object from MinIO and its metadata entry.
func (s *MinIOArtifactStore) DeleteArtifact(ctx context.Context, jobID, key string) error {
	mapKey, err := makeArtifactMapKey(jobID, key)
	if err != nil {
		return err
	}
	rec, err := s.meta.Get(ctx, mapKey.jobID, mapKey.key)
	if err != nil {
		return err
	}
	objectKey := rec.ObjectKey
	if objectKey == "" {
		objectKey = legacyMinioObjectKey(mapKey.jobID, mapKey.key)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, objectKey, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("minio: remove object failed: %w", err)
	}
	return s.meta.Delete(ctx, mapKey.jobID, mapKey.key)
}

// MemoryArtifactMeta implements ArtifactMeta entirely in process.
type MemoryArtifactMeta struct {
	mu      sync.RWMutex
	records map[artifactMapKey]ArtifactRecord
}

func NewMemoryArtifactMeta() *MemoryArtifactMeta {
	return &MemoryArtifactMeta{records: make(map[artifactMapKey]ArtifactRecord)}
}

func (m *MemoryArtifactMeta) Ready(_ context.Context) error {
	return nil
}

func (m *MemoryArtifactMeta) Put(_ context.Context, rec ArtifactRecord) error {
	mapKey, err := makeArtifactMapKey(rec.JobID, rec.Key)
	if err != nil {
		return err
	}
	m.mu.Lock()
	rec.JobID = mapKey.jobID
	rec.Key = mapKey.key
	m.records[mapKey] = rec
	m.mu.Unlock()
	return nil
}

func (m *MemoryArtifactMeta) Get(_ context.Context, jobID, key string) (ArtifactRecord, error) {
	mapKey, err := makeArtifactMapKey(jobID, key)
	if err != nil {
		return ArtifactRecord{}, err
	}
	m.mu.RLock()
	rec, ok := m.records[mapKey]
	m.mu.RUnlock()
	if !ok {
		return ArtifactRecord{}, &ErrArtifactNotFound{JobID: mapKey.jobID, Key: mapKey.key}
	}
	return rec, nil
}

func (m *MemoryArtifactMeta) List(_ context.Context, jobID string) ([]ArtifactRecord, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, &ErrArtifactInvalid{Field: "job_id"}
	}
	m.mu.RLock()
	var result []ArtifactRecord
	for _, rec := range m.records {
		if rec.JobID == jobID {
			result = append(result, rec)
		}
	}
	m.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].Key < result[j].Key
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (m *MemoryArtifactMeta) Delete(_ context.Context, jobID, key string) error {
	mapKey, err := makeArtifactMapKey(jobID, key)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if _, ok := m.records[mapKey]; !ok {
		m.mu.Unlock()
		return &ErrArtifactNotFound{JobID: mapKey.jobID, Key: mapKey.key}
	}
	delete(m.records, mapKey)
	m.mu.Unlock()
	return nil
}

func legacyMinioObjectKey(jobID, key string) string {
	return url.PathEscape(jobID) + "/" + url.PathEscape(key)
}

func minioObjectKey(jobID, key string) string {
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return legacyMinioObjectKey(jobID, key) + "/" + fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	return legacyMinioObjectKey(jobID, key) + "/" + hex.EncodeToString(token[:])
}

func isMinIONotFound(err error) bool {
	if merr, ok := err.(minio.ErrorResponse); ok {
		return merr.Code == "NoSuchKey" || merr.Code == "NoSuchBucket" || merr.StatusCode == 404
	}
	return false
}
