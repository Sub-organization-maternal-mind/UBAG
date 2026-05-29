package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

func TestFileSpoolDispatcherWritesDispatchEnvelope(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job := sampleJob()

	receipt, err := dispatcher.EnqueueJob(context.Background(), job)
	if err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}
	if receipt.Backend != "file" || receipt.QueueName != "jobs" || receipt.MessageID != job.ID {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}

	raw, err := os.ReadFile(filepath.Join(dispatcher.pendingDir(), job.ID+".json"))
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	var envelope DispatchEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.APIVersion != "2026-05-22" || envelope.JobID != job.ID || envelope.TraceID != job.TraceID {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	if envelope.Job.Target != "mock" || envelope.Job.Input["prompt"] != "hello" {
		t.Fatalf("unexpected job payload: %#v", envelope.Job)
	}
}

func TestFileSpoolDispatcherIsIdempotentForSameJobID(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job := sampleJob()

	first, err := dispatcher.EnqueueJob(context.Background(), job)
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	second, err := dispatcher.EnqueueJob(context.Background(), job)
	if err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	if first.MessageID != second.MessageID {
		t.Fatalf("message id changed: %#v %#v", first, second)
	}
	entries, err := os.ReadDir(dispatcher.pendingDir())
	if err != nil {
		t.Fatalf("read pending: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("pending json count = %d, want 1", count)
	}
}

func TestFileSpoolDispatcherStats(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	dispatcher.now = func() time.Time {
		return time.Date(2026, 5, 23, 12, 0, 10, 0, time.UTC)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), sampleJob()); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	stats, err := dispatcher.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats returned error: %v", err)
	}
	if stats.DepthByState["queued"] != 1 {
		t.Fatalf("queued depth = %d, want 1", stats.DepthByState["queued"])
	}
}

func TestFileSpoolDispatcherLeasesAndCompletesEnvelope(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job := sampleJob()
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	lease, ok, err := dispatcher.LeaseNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("LeaseNext ok=%v err=%v", ok, err)
	}
	if lease.JobID != job.ID || lease.Envelope.JobID != job.ID {
		t.Fatalf("unexpected lease: %#v", lease)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.pendingDir(), job.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("pending envelope still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(lease.Path); err != nil {
		t.Fatalf("leased envelope missing: %v", err)
	}

	stats, err := dispatcher.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats returned error: %v", err)
	}
	if stats.DepthByState["queued"] != 0 || stats.DepthByState["assigned"] != 1 {
		t.Fatalf("unexpected stats after lease: %#v", stats.DepthByState)
	}

	if err := dispatcher.CompleteLease(context.Background(), lease); err != nil {
		t.Fatalf("CompleteLease returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.doneDir(), filepath.Base(lease.Path))); err != nil {
		t.Fatalf("done envelope missing: %v", err)
	}
}

func TestFileSpoolDispatcherRetryMovesLeaseBackToPending(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job := sampleJob()
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	lease, ok, err := dispatcher.LeaseNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("LeaseNext ok=%v err=%v", ok, err)
	}
	if err := dispatcher.RetryLease(context.Background(), lease); err != nil {
		t.Fatalf("RetryLease returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.pendingDir(), job.ID+".json")); err != nil {
		t.Fatalf("pending envelope missing after retry: %v", err)
	}
	if _, err := os.Stat(lease.Path); !os.IsNotExist(err) {
		t.Fatalf("leased envelope still exists or stat failed: %v", err)
	}

	retried, ok, err := dispatcher.LeaseNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("re-lease ok=%v err=%v", ok, err)
	}
	if retried.JobID != job.ID {
		t.Fatalf("retried lease job id = %q, want %q", retried.JobID, job.ID)
	}
}

func TestFileSpoolDispatcherCancelMovesPendingEnvelope(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job := sampleJob()
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := dispatcher.CancelJob(context.Background(), job, "caller_cancelled"); err != nil {
		t.Fatalf("CancelJob returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.pendingDir(), job.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("pending envelope still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.cancelledDir(), job.ID+".json")); err != nil {
		t.Fatalf("cancelled envelope missing: %v", err)
	}
}

func TestFileSpoolDispatcherCancelWritesMarkerWhenNoEnvelopeExists(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job := sampleJob()

	if err := dispatcher.CancelJob(context.Background(), job, "operator requested"); err != nil {
		t.Fatalf("CancelJob returned error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dispatcher.cancelledDir(), job.ID+".json"))
	if err != nil {
		t.Fatalf("read cancellation marker: %v", err)
	}
	var marker map[string]any
	if err := json.Unmarshal(raw, &marker); err != nil {
		t.Fatalf("decode marker: %v", err)
	}
	if marker["job_id"] != job.ID || marker["reason"] != "operator requested" {
		t.Fatalf("unexpected marker: %#v", marker)
	}
}

func TestFileSpoolDispatcherDoesNotEnqueueAfterCancellationMarker(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job := sampleJob()
	if err := dispatcher.CancelJob(context.Background(), job, "pre_cancel"); err != nil {
		t.Fatalf("CancelJob returned error: %v", err)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.pendingDir(), job.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("pending envelope exists after cancelled enqueue or stat failed: %v", err)
	}
	stats, err := dispatcher.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats returned error: %v", err)
	}
	if stats.DepthByState["queued"] != 0 || stats.DepthByState["cancelled"] != 1 {
		t.Fatalf("unexpected stats after cancelled enqueue: %#v", stats.DepthByState)
	}
}

func TestFileSpoolDispatcherReadyFailsWithoutDirectory(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher("")
	if err := dispatcher.Ready(context.Background()); err == nil {
		t.Fatal("Ready returned nil, want error")
	}
}

func sampleJob() jobstore.Job {
	return jobstore.Job{
		ID:             "job_000000000001",
		APIVersion:     "2026-05-22",
		TenantID:       "tenant_edge",
		AppID:          "app_default",
		IdempotencyKey: "idem_000000000001",
		Target:         "mock",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		Status:         jobstore.StatusQueued,
		TraceID:        "trace_fixture",
		CreatedAt:      time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
	}
}
