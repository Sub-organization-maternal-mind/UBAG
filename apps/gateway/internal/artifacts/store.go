// Package artifacts provides a gateway-owned store for job artifact metadata
// and object bytes.  The default implementation keeps everything in memory; the
// MinIO implementation stores bytes in MinIO/S3-compatible object storage and
// metadata either in memory or in Postgres.
package artifacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ArtifactRecord describes a single artifact stored for a job.
type ArtifactRecord struct {
	JobID       string    `json:"job_id"`
	Key         string    `json:"key"`
	Bucket      string    `json:"-"`
	ObjectKey   string    `json:"-"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	Checksum    string    `json:"checksum,omitempty"` // hex-encoded SHA-256
	CreatedAt   time.Time `json:"created_at"`
}

// ArtifactStore is the gateway-owned backend for per-job artifact blobs.
type ArtifactStore interface {
	// Ready returns a non-nil error when the store is unavailable.
	Ready(ctx context.Context) error

	// PutArtifact uploads artifact bytes and records metadata.
	// key must be non-empty and is scoped to jobID.
	PutArtifact(ctx context.Context, jobID, key, contentType string, r io.Reader, sizeBytes int64) (ArtifactRecord, error)

	// GetArtifact returns a reader for artifact bytes and the associated metadata.
	// The caller must close the returned reader.
	GetArtifact(ctx context.Context, jobID, key string) (io.ReadCloser, ArtifactRecord, error)

	// ListArtifacts returns all artifact records for a job, newest first.
	ListArtifacts(ctx context.Context, jobID string) ([]ArtifactRecord, error)

	// DeleteArtifact removes artifact bytes and metadata.
	DeleteArtifact(ctx context.Context, jobID, key string) error
}

// ErrArtifactNotFound is returned when the requested artifact does not exist.
type ErrArtifactNotFound struct {
	JobID string
	Key   string
}

func (e *ErrArtifactNotFound) Error() string {
	return fmt.Sprintf("artifact not found: job=%s key=%s", e.JobID, e.Key)
}

// ErrArtifactInvalid is returned when a caller supplies an invalid job/key.
type ErrArtifactInvalid struct {
	Field string
}

func (e *ErrArtifactInvalid) Error() string {
	return fmt.Sprintf("invalid artifact %s", e.Field)
}

// IsNotFound reports whether err is an ErrArtifactNotFound.
func IsNotFound(err error) bool {
	var target *ErrArtifactNotFound
	return errors.As(err, &target)
}

// IsInvalid reports whether err is an ErrArtifactInvalid.
func IsInvalid(err error) bool {
	var target *ErrArtifactInvalid
	return errors.As(err, &target)
}

type artifactMapKey struct {
	jobID string
	key   string
}

func makeArtifactMapKey(jobID, key string) (artifactMapKey, error) {
	jobID = strings.TrimSpace(jobID)
	key = strings.TrimSpace(key)
	if jobID == "" {
		return artifactMapKey{}, &ErrArtifactInvalid{Field: "job_id"}
	}
	if key == "" {
		return artifactMapKey{}, &ErrArtifactInvalid{Field: "key"}
	}
	return artifactMapKey{jobID: jobID, key: key}, nil
}
