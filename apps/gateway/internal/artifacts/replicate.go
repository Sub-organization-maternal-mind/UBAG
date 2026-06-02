package artifacts

import (
	"bytes"
	"context"
	"io"
	"log"
	"sync"
)

// MirrorWork is a pending mirror operation.
type MirrorWork struct {
	JobID       string
	Key         string
	ContentType string
	SizeBytes   int64
	Data        []byte // snapshot of the artifact bytes for async delivery
}

// ReplicatingStore wraps a home-region ArtifactStore and enqueues mirror
// operations to one or more remote ArtifactStore instances. Mirror writes
// are best-effort and non-blocking: a failure to mirror never causes
// PutArtifact to fail.
type ReplicatingStore struct {
	home    ArtifactStore
	mirrors []ArtifactStore
	queue   chan MirrorWork
	wg      sync.WaitGroup
}

// NewReplicatingStore wraps home and fans out to mirrors asynchronously.
// bufferSize bounds the in-memory mirror queue; extra operations are dropped
// with a log message when the queue is full.
func NewReplicatingStore(home ArtifactStore, mirrors []ArtifactStore, bufferSize int) *ReplicatingStore {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	return &ReplicatingStore{
		home:    home,
		mirrors: mirrors,
		queue:   make(chan MirrorWork, bufferSize),
	}
}

// Start launches the background mirror worker goroutine. Call once.
func (r *ReplicatingStore) Start() {
	r.wg.Add(1)
	go r.worker()
}

// Stop drains the queue and waits for the mirror worker to finish.
func (r *ReplicatingStore) Stop(_ context.Context) {
	close(r.queue)
	r.wg.Wait()
}

// worker reads MirrorWork items from the queue and writes each one to every
// remote store. Errors are logged but do not stop the worker.
func (r *ReplicatingStore) worker() {
	defer r.wg.Done()
	for work := range r.queue {
		for _, mirror := range r.mirrors {
			rd := bytes.NewReader(work.Data)
			if _, err := mirror.PutArtifact(context.Background(), work.JobID, work.Key, work.ContentType, rd, work.SizeBytes); err != nil {
				log.Printf("replicate: mirror put failed job=%s key=%s: %v", work.JobID, work.Key, err)
			}
		}
	}
}

// Ready returns the home store's readiness. Remote stores are not consulted.
func (r *ReplicatingStore) Ready(ctx context.Context) error {
	return r.home.Ready(ctx)
}

// PutArtifact writes to the home store and enqueues a mirror operation.
// The mirror enqueue is non-blocking; if the queue is full the mirror for
// this item is silently dropped.
func (r *ReplicatingStore) PutArtifact(ctx context.Context, jobID, key, contentType string, reader io.Reader, sizeBytes int64) (ArtifactRecord, error) {
	// Buffer the bytes so we can pass them to the home store AND snapshot a
	// copy for async mirroring without requiring the caller's reader to be
	// re-readable.
	data, err := io.ReadAll(reader)
	if err != nil {
		return ArtifactRecord{}, err
	}

	rec, err := r.home.PutArtifact(ctx, jobID, key, contentType, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ArtifactRecord{}, err
	}

	if len(r.mirrors) > 0 {
		work := MirrorWork{
			JobID:       jobID,
			Key:         key,
			ContentType: contentType,
			SizeBytes:   int64(len(data)),
			Data:        data,
		}
		select {
		case r.queue <- work:
		default:
			log.Printf("replicate: mirror queue full, dropping job=%s key=%s", jobID, key)
		}
	}

	return rec, nil
}

// GetArtifact delegates to the home store. Remote stores are not consulted.
func (r *ReplicatingStore) GetArtifact(ctx context.Context, jobID, key string) (io.ReadCloser, ArtifactRecord, error) {
	return r.home.GetArtifact(ctx, jobID, key)
}

// ListArtifacts delegates to the home store.
func (r *ReplicatingStore) ListArtifacts(ctx context.Context, jobID string) ([]ArtifactRecord, error) {
	return r.home.ListArtifacts(ctx, jobID)
}

// DeleteArtifact delegates to the home store.
func (r *ReplicatingStore) DeleteArtifact(ctx context.Context, jobID, key string) error {
	return r.home.DeleteArtifact(ctx, jobID, key)
}
