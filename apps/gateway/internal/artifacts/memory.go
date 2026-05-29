package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryArtifactStore stores artifact bytes and metadata entirely in process.
// It is safe for concurrent use and is the default when no external store is
// configured.
type MemoryArtifactStore struct {
	mu      sync.RWMutex
	blobs   map[artifactMapKey][]byte         // key -> bytes
	records map[artifactMapKey]ArtifactRecord // key -> metadata
}

func NewMemoryArtifactStore() *MemoryArtifactStore {
	return &MemoryArtifactStore{
		blobs:   make(map[artifactMapKey][]byte),
		records: make(map[artifactMapKey]ArtifactRecord),
	}
}

func (s *MemoryArtifactStore) Ready(_ context.Context) error {
	return nil
}

func (s *MemoryArtifactStore) PutArtifact(_ context.Context, jobID, key, contentType string, r io.Reader, sizeBytes int64) (ArtifactRecord, error) {
	mapKey, err := makeArtifactMapKey(jobID, key)
	if err != nil {
		return ArtifactRecord{}, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return ArtifactRecord{}, err
	}

	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])

	rec := ArtifactRecord{
		JobID:       mapKey.jobID,
		Key:         mapKey.key,
		Bucket:      "memory",
		ContentType: contentType,
		SizeBytes:   int64(len(data)),
		Checksum:    checksum,
		CreatedAt:   time.Now().UTC(),
	}

	s.mu.Lock()
	s.blobs[mapKey] = data
	s.records[mapKey] = rec
	s.mu.Unlock()

	return rec, nil
}

func (s *MemoryArtifactStore) GetArtifact(_ context.Context, jobID, key string) (io.ReadCloser, ArtifactRecord, error) {
	mapKey, err := makeArtifactMapKey(jobID, key)
	if err != nil {
		return nil, ArtifactRecord{}, err
	}
	s.mu.RLock()
	data, ok := s.blobs[mapKey]
	rec := s.records[mapKey]
	s.mu.RUnlock()

	if !ok {
		return nil, ArtifactRecord{}, &ErrArtifactNotFound{JobID: mapKey.jobID, Key: mapKey.key}
	}
	return io.NopCloser(bytes.NewReader(data)), rec, nil
}

func (s *MemoryArtifactStore) ListArtifacts(_ context.Context, jobID string) ([]ArtifactRecord, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, &ErrArtifactInvalid{Field: "job_id"}
	}
	s.mu.RLock()
	var result []ArtifactRecord
	for _, rec := range s.records {
		if rec.JobID == jobID {
			result = append(result, rec)
		}
	}
	s.mu.RUnlock()

	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].Key < result[j].Key
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (s *MemoryArtifactStore) DeleteArtifact(_ context.Context, jobID, key string) error {
	mapKey, err := makeArtifactMapKey(jobID, key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if _, ok := s.records[mapKey]; !ok {
		s.mu.Unlock()
		return &ErrArtifactNotFound{JobID: mapKey.jobID, Key: mapKey.key}
	}
	delete(s.blobs, mapKey)
	delete(s.records, mapKey)
	s.mu.Unlock()
	return nil
}
