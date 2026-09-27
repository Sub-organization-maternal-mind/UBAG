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
	if stats.LiveDepth != 1 || stats.TotalDepth != 1 {
		t.Fatalf("live/total depth = %d/%d, want 1/1", stats.LiveDepth, stats.TotalDepth)
	}
}

func TestFileSpoolDispatcherStatsLiveVsTotal(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job := sampleJob()
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	next := sampleJob()
	next.ID = "job_000000000002"
	if _, err := dispatcher.EnqueueJob(context.Background(), next); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}

	lease, ok, err := dispatcher.LeaseNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("LeaseNext ok=%v err=%v", ok, err)
	}
	if err := dispatcher.CompleteLease(context.Background(), lease); err != nil {
		t.Fatalf("CompleteLease: %v", err)
	}

	stats, err := dispatcher.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.LiveDepth != 1 {
		t.Fatalf("live depth = %d, want 1 (queued only)", stats.LiveDepth)
	}
	if stats.TotalDepth != 2 {
		t.Fatalf("total depth = %d, want 2 (queued + completed)", stats.TotalDepth)
	}
}

func TestFileSpoolDispatcherRetentionSweep(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	dispatcher.SetRetention(SpoolRetentionConfig{TTL: time.Hour, MaxCount: 2})

	oldTime := time.Now().Add(-3 * time.Hour)
	freshTime := time.Now()

	// Three terminal entries: two old (past TTL), one fresh. MaxCount=2 would
	// also force the newest old entry out.
	writeSpoolFile(t, dispatcher.doneDir(), "job_000000000001.json", oldTime)
	writeSpoolFile(t, dispatcher.doneDir(), "job_000000000002.json", oldTime)
	writeSpoolFile(t, dispatcher.doneDir(), "job_000000000003.json", freshTime)
	// A non-terminal pending envelope must never be touched.
	writeSpoolFile(t, dispatcher.pendingDir(), "job_000000000009.json", oldTime)

	removed, err := dispatcher.SweepRetention(time.Now())
	if err != nil {
		t.Fatalf("SweepRetention: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (two oldest past TTL)", removed)
	}
	for _, name := range []string{"job_000000000001.json", "job_000000000002.json"} {
		if _, err := os.Stat(filepath.Join(dispatcher.doneDir(), name)); !os.IsNotExist(err) {
			t.Fatalf("%s should have been swept (stat err=%v)", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dispatcher.doneDir(), "job_000000000003.json")); err != nil {
		t.Fatalf("fresh envelope should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.pendingDir(), "job_000000000009.json")); err != nil {
		t.Fatalf("pending envelope must never be swept: %v", err)
	}
}

func TestFileSpoolDispatcherRetentionMaxCountEvictsOldest(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	dispatcher.SetRetention(SpoolRetentionConfig{TTL: 0, MaxCount: 2})

	now := time.Now()
	writeSpoolFile(t, dispatcher.doneDir(), "job_000000000001.json", now.Add(-3*time.Hour))
	writeSpoolFile(t, dispatcher.cancelledDir(), "job_000000000002.json", now.Add(-2*time.Hour))
	writeSpoolFile(t, dispatcher.failedDir(), "job_000000000003.json", now.Add(-1*time.Hour))

	removed, err := dispatcher.SweepRetention(now)
	if err != nil {
		t.Fatalf("SweepRetention: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (oldest beyond max-count)", removed)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.doneDir(), "job_000000000001.json")); !os.IsNotExist(err) {
		t.Fatalf("oldest terminal envelope should have been evicted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.cancelledDir(), "job_000000000002.json")); err != nil {
		t.Fatalf("second-oldest should remain within max-count: %v", err)
	}
}

func TestFileSpoolDispatcherRetentionDisabled(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	writeSpoolFile(t, dispatcher.doneDir(), "job_000000000001.json", time.Now().Add(-48*time.Hour))

	removed, err := dispatcher.SweepRetention(time.Now())
	if err != nil {
		t.Fatalf("SweepRetention: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 (retention disabled)", removed)
	}
}

func writeSpoolFile(t *testing.T, dir, name string, modified time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

// TestRenameNoOverwriteIsRealCAS verifies the destination-protecting CAS: a
// concurrent creator that loses the race must not clobber the winner's file
// (os.Rename would silently overwrite; os.Link fails with EEXIST).
func TestRenameNoOverwriteIsRealCAS(t *testing.T) {
	dir := t.TempDir()

	writeFile := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	winner := writeFile("winner.tmp", "winner")
	target := filepath.Join(dir, "target.json")
	if err := renameNoOverwrite(winner, target); err != nil {
		t.Fatalf("first move: %v", err)
	}
	if _, err := os.Stat(winner); !os.IsNotExist(err) {
		t.Fatalf("source should be gone after move: %v", err)
	}

	// A second mover racing for the same destination must lose cleanly.
	loser := writeFile("loser.tmp", "loser")
	if err := renameNoOverwrite(loser, target); err != nil {
		t.Fatalf("second move (should dedupe, not error): %v", err)
	}
	if _, err := os.Stat(loser); !os.IsNotExist(err) {
		t.Fatalf("loser source should be dropped: %v", err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(raw) != "winner" {
		t.Fatalf("target was overwritten by the losing mover: %q", string(raw))
	}
}

// TestFileSpoolEnqueueNotifiesLeaseLoop verifies the enqueue path drops a wake
// token that LeaseNext consumers can select on, and that a cap-1 channel with
// an unobserved token never blocks the enqueue path.
func TestFileSpoolEnqueueNotifiesLeaseLoop(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	notify := dispatcher.EnqueueNotify()
	if notify == nil {
		t.Fatal("EnqueueNotify returned nil")
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), sampleJob()); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-notify:
	default:
		t.Fatal("enqueue did not drop a wake token")
	}

	// A second enqueue while a token is still pending must not block (cap-1,
	// non-blocking send). Use a distinct job ID: a same-ID enqueue dedupes
	// before reaching the notify.
	next := sampleJob()
	next.ID = "job_000000000002"
	if _, err := dispatcher.EnqueueJob(context.Background(), next); err != nil {
		t.Fatalf("second enqueue: %v", err)
	}

	// An idempotent re-enqueue of an already-spooled job is NOT a fresh
	// enqueue and must not notify... but a token left over from the second
	// enqueue is still observable.
	select {
	case <-notify:
	default:
		t.Fatal("second enqueue did not leave a wake token")
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

func TestFileSpoolDispatcherRecoversOrphanLeases(t *testing.T) {
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job := sampleJob()
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// Simulate a gateway crash mid-RunOnce: the pending envelope was leased
	// (renamed with a lease-id suffix) and the process died before resolving it.
	leasedName := filepath.Join(dispatcher.leasedDir(), job.ID+".lease-1.json")
	if err := os.Rename(filepath.Join(dispatcher.pendingDir(), job.ID+".json"), leasedName); err != nil {
		t.Fatalf("simulate lease: %v", err)
	}

	recovered, err := dispatcher.RecoverOrphanLeases()
	if err != nil {
		t.Fatalf("RecoverOrphanLeases: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered lease, got %d", recovered)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.pendingDir(), job.ID+".json")); err != nil {
		t.Fatalf("envelope not back in pending: %v", err)
	}
	if entries, _ := os.ReadDir(dispatcher.leasedDir()); len(entries) != 0 {
		t.Fatalf("leased dir not empty: %d entries", len(entries))
	}

	// A stranded lease whose pending envelope already exists is a stale
	// duplicate — it must be parked in cancelled/, not duplicated into pending.
	if err := os.MkdirAll(dispatcher.pendingDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dispatcher.pendingDir(), job.ID+".json"), []byte(`{"fresh":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leasedName, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err = dispatcher.RecoverOrphanLeases()
	if err != nil {
		t.Fatalf("RecoverOrphanLeases (duplicate): %v", err)
	}
	if recovered != 0 {
		t.Fatalf("duplicate lease must not count as recovered, got %d", recovered)
	}
	if entries, _ := os.ReadDir(dispatcher.cancelledDir()); len(entries) != 1 {
		t.Fatalf("duplicate lease must be parked in cancelled/, found %d", len(entries))
	}
}
