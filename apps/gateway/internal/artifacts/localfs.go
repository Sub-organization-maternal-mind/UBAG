package artifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LocalFSArtifactStore stores artifact bytes on the local filesystem under a
// root directory and tracks metadata via an ArtifactMeta implementation
// (in-memory or SQLite). It mirrors the MinIOArtifactStore method set and
// semantics: object keys are derived from sanitized job/key components plus a
// random token so re-uploads never collide, and metadata is the source of
// truth for which blob backs a given (job, key) pair.
type LocalFSArtifactStore struct {
	root string
	meta ArtifactMeta
}

// NewLocalFSArtifactStore constructs a LocalFSArtifactStore rooted at rootDir.
// meta may be nil; a MemoryArtifactMeta is used in that case.
func NewLocalFSArtifactStore(rootDir string, meta ArtifactMeta) (ArtifactStore, error) {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return nil, fmt.Errorf("localfs: root directory is required")
	}
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("localfs: resolve root directory: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("localfs: create root directory: %w", err)
	}
	if meta == nil {
		meta = NewMemoryArtifactMeta()
	}
	return &LocalFSArtifactStore{root: abs, meta: meta}, nil
}

// Ready ensures the root directory is writable and the metadata store is ready.
func (s *LocalFSArtifactStore) Ready(ctx context.Context) error {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return fmt.Errorf("localfs: root directory not writable: %w", err)
	}
	if err := s.meta.Ready(ctx); err != nil {
		return fmt.Errorf("localfs: metadata readiness failed: %w", err)
	}
	return nil
}

// PutArtifact writes bytes to disk and records metadata.
func (s *LocalFSArtifactStore) PutArtifact(ctx context.Context, jobID, key, contentType string, r io.Reader, sizeBytes int64) (ArtifactRecord, error) {
	mapKey, err := makeArtifactMapKey(jobID, key)
	if err != nil {
		return ArtifactRecord{}, err
	}
	if err := rejectUnsafeArtifactComponent(mapKey.jobID); err != nil {
		return ArtifactRecord{}, err
	}
	if err := rejectUnsafeArtifactComponent(mapKey.key); err != nil {
		return ArtifactRecord{}, err
	}
	objectKey := localFSObjectKey(mapKey.jobID, mapKey.key)
	fullPath, err := s.resolve(objectKey)
	if err != nil {
		return ArtifactRecord{}, err
	}

	old, oldErr := s.meta.Get(ctx, mapKey.jobID, mapKey.key)
	if oldErr != nil && !IsNotFound(oldErr) {
		return ArtifactRecord{}, oldErr
	}

	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return ArtifactRecord{}, fmt.Errorf("localfs: create object directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(fullPath), ".upload-*")
	if err != nil {
		return ArtifactRecord{}, fmt.Errorf("localfs: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	h := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, h), r)
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpName)
		return ArtifactRecord{}, fmt.Errorf("localfs: write object: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return ArtifactRecord{}, fmt.Errorf("localfs: close object: %w", closeErr)
	}
	if err := os.Rename(tmpName, fullPath); err != nil {
		_ = os.Remove(tmpName)
		return ArtifactRecord{}, fmt.Errorf("localfs: finalize object: %w", err)
	}

	rec := ArtifactRecord{
		JobID:       mapKey.jobID,
		Key:         mapKey.key,
		Bucket:      "localfs",
		ObjectKey:   objectKey,
		ContentType: contentType,
		SizeBytes:   written,
		Checksum:    hex.EncodeToString(h.Sum(nil)),
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.meta.Put(ctx, rec); err != nil {
		_ = os.Remove(fullPath)
		return ArtifactRecord{}, fmt.Errorf("localfs: metadata put failed: %w", err)
	}
	if oldErr == nil && old.ObjectKey != "" && old.ObjectKey != objectKey {
		if oldPath, resolveErr := s.resolve(old.ObjectKey); resolveErr == nil {
			_ = os.Remove(oldPath)
		}
	}
	return rec, nil
}

// GetArtifact opens the artifact bytes from disk.
func (s *LocalFSArtifactStore) GetArtifact(ctx context.Context, jobID, key string) (io.ReadCloser, ArtifactRecord, error) {
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
		objectKey = legacyLocalFSObjectKey(mapKey.jobID, mapKey.key)
	}
	fullPath, err := s.resolve(objectKey)
	if err != nil {
		return nil, ArtifactRecord{}, err
	}
	file, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ArtifactRecord{}, &ErrArtifactNotFound{JobID: mapKey.jobID, Key: mapKey.key}
		}
		return nil, ArtifactRecord{}, fmt.Errorf("localfs: open object: %w", err)
	}
	return file, rec, nil
}

// ListArtifacts returns metadata for all artifacts stored for jobID.
func (s *LocalFSArtifactStore) ListArtifacts(ctx context.Context, jobID string) ([]ArtifactRecord, error) {
	return s.meta.List(ctx, jobID)
}

// DeleteArtifact removes the object file and its metadata entry.
func (s *LocalFSArtifactStore) DeleteArtifact(ctx context.Context, jobID, key string) error {
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
		objectKey = legacyLocalFSObjectKey(mapKey.jobID, mapKey.key)
	}
	if fullPath, resolveErr := s.resolve(objectKey); resolveErr == nil {
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("localfs: remove object: %w", err)
		}
	}
	return s.meta.Delete(ctx, mapKey.jobID, mapKey.key)
}

// resolve maps an object key to an absolute path and guarantees the result
// stays inside the store root (defense-in-depth against path traversal).
func (s *LocalFSArtifactStore) resolve(objectKey string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(objectKey))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", &ErrArtifactInvalid{Field: "object_key"}
	}
	full := filepath.Join(s.root, clean)
	rel, err := filepath.Rel(s.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", &ErrArtifactInvalid{Field: "object_key"}
	}
	return full, nil
}

// rejectUnsafeArtifactComponent rejects job/key components that could escape
// the store root via path traversal or absolute paths.
func rejectUnsafeArtifactComponent(value string) error {
	if value == "" {
		return &ErrArtifactInvalid{Field: "key"}
	}
	if strings.Contains(value, "..") || filepath.IsAbs(value) || strings.ContainsRune(value, 0) {
		return &ErrArtifactInvalid{Field: "key"}
	}
	return nil
}

func legacyLocalFSObjectKey(jobID, key string) string {
	return url.PathEscape(jobID) + "/" + url.PathEscape(key)
}

func localFSObjectKey(jobID, key string) string {
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return legacyLocalFSObjectKey(jobID, key) + "/" + fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	return legacyLocalFSObjectKey(jobID, key) + "/" + hex.EncodeToString(token[:])
}

// SQLiteArtifactMeta implements ArtifactMeta using the artifact_metadata table
// created by the gateway SQLite schema. It mirrors PostgresArtifactMeta.
type SQLiteArtifactMeta struct {
	db *sql.DB
}

func NewSQLiteArtifactMeta(db *sql.DB) *SQLiteArtifactMeta {
	return &SQLiteArtifactMeta{db: db}
}

func (m *SQLiteArtifactMeta) Ready(ctx context.Context) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("sqlite artifact meta is not configured")
	}
	if err := m.db.PingContext(ctx); err != nil {
		return err
	}
	var name string
	err := m.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE name = ? LIMIT 1`, "artifact_metadata").Scan(&name)
	if err == sql.ErrNoRows {
		return fmt.Errorf("artifact_metadata table is missing")
	}
	return err
}

func (m *SQLiteArtifactMeta) Put(ctx context.Context, rec ArtifactRecord) error {
	if _, err := makeArtifactMapKey(rec.JobID, rec.Key); err != nil {
		return err
	}
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO artifact_metadata
			(job_id, artifact_key, bucket, object_key, content_type, size_bytes, checksum, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (job_id, artifact_key) DO UPDATE SET
			bucket       = excluded.bucket,
			object_key   = excluded.object_key,
			content_type = excluded.content_type,
			size_bytes   = excluded.size_bytes,
			checksum     = excluded.checksum,
			created_at   = excluded.created_at`,
		rec.JobID,
		rec.Key,
		rec.Bucket,
		rec.ObjectKey,
		rec.ContentType,
		rec.SizeBytes,
		rec.Checksum,
		formatArtifactSQLiteTime(rec.CreatedAt),
	)
	return err
}

func (m *SQLiteArtifactMeta) Get(ctx context.Context, jobID, key string) (ArtifactRecord, error) {
	if _, err := makeArtifactMapKey(jobID, key); err != nil {
		return ArtifactRecord{}, err
	}
	row := m.db.QueryRowContext(ctx, `
		SELECT job_id, artifact_key, bucket, object_key, content_type, size_bytes, checksum, created_at
		FROM artifact_metadata
		WHERE job_id = ? AND artifact_key = ?`, jobID, key)

	var rec ArtifactRecord
	var checksum sql.NullString
	var objectKey sql.NullString
	var createdAt string
	if err := row.Scan(&rec.JobID, &rec.Key, &rec.Bucket, &objectKey, &rec.ContentType, &rec.SizeBytes, &checksum, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return ArtifactRecord{}, &ErrArtifactNotFound{JobID: jobID, Key: key}
		}
		return ArtifactRecord{}, fmt.Errorf("artifact_metadata get: %w", err)
	}
	rec.ObjectKey = objectKey.String
	rec.Checksum = checksum.String
	rec.CreatedAt = parseArtifactSQLiteTime(createdAt)
	return rec, nil
}

func (m *SQLiteArtifactMeta) List(ctx context.Context, jobID string) ([]ArtifactRecord, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, &ErrArtifactInvalid{Field: "job_id"}
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT job_id, artifact_key, bucket, object_key, content_type, size_bytes, checksum, created_at
		FROM artifact_metadata
		WHERE job_id = ?
		ORDER BY created_at DESC, artifact_key ASC`, jobID)
	if err != nil {
		return nil, fmt.Errorf("artifact_metadata list: %w", err)
	}
	defer rows.Close()

	var result []ArtifactRecord
	for rows.Next() {
		var rec ArtifactRecord
		var checksum sql.NullString
		var objectKey sql.NullString
		var createdAt string
		if err := rows.Scan(&rec.JobID, &rec.Key, &rec.Bucket, &objectKey, &rec.ContentType, &rec.SizeBytes, &checksum, &createdAt); err != nil {
			return nil, fmt.Errorf("artifact_metadata list scan: %w", err)
		}
		rec.ObjectKey = objectKey.String
		rec.Checksum = checksum.String
		rec.CreatedAt = parseArtifactSQLiteTime(createdAt)
		result = append(result, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("artifact_metadata list rows: %w", err)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].Key < result[j].Key
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (m *SQLiteArtifactMeta) Delete(ctx context.Context, jobID, key string) error {
	if _, err := makeArtifactMapKey(jobID, key); err != nil {
		return err
	}
	result, err := m.db.ExecContext(ctx,
		`DELETE FROM artifact_metadata WHERE job_id = ? AND artifact_key = ?`,
		jobID, key)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return &ErrArtifactNotFound{JobID: jobID, Key: key}
	}
	return err
}

const artifactSQLiteTimeLayout = "2006-01-02T15:04:05.000Z07:00"

func formatArtifactSQLiteTime(t time.Time) string {
	return t.UTC().Format(artifactSQLiteTimeLayout)
}

func parseArtifactSQLiteTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{artifactSQLiteTimeLayout, time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}
