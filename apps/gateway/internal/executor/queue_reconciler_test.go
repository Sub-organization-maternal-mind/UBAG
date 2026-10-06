package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// createThenEnqueue mirrors the HTTP handler's non-atomic sequence: the job row
// is written, then the job is enqueued. crashBetween injects a process crash in
// the window between the two steps (the enqueue never happens).
func createThenEnqueue(t *testing.T, store jobstore.Store, d Dispatcher, crashBetween bool) jobstore.Job {
	t.Helper()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a",
		Target: "mock", CommandType: "submit",
		Input: map[string]any{"prompt": "hi"}, TraceID: "trace_rec",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if crashBetween {
		return job
	}
	if _, err := d.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	return job
}

func pendingExists(d *FileSpoolDispatcher, jobID string) bool {
	_, err := os.Stat(filepath.Join(d.pendingDir(), jobID+".json"))
	return err == nil
}

func TestQueuedJobReconcilerRecoversCrashBetweenCreateAndEnqueue(t *testing.T) {
	store := jobstore.NewMemoryStore()
	spool := NewFileSpoolDispatcher(t.TempDir())

	stranded := createThenEnqueue(t, store, spool, true) // crash before enqueue
	healthy := createThenEnqueue(t, store, spool, false) // normal path
	if pendingExists(spool, stranded.ID) || !pendingExists(spool, healthy.ID) {
		t.Fatal("precondition: only the healthy job should be spooled")
	}

	rec := &QueuedJobReconciler{
		Jobs: store, Dispatcher: spool, MinAge: time.Minute,
		Now: func() time.Time { return stranded.CreatedAt.Add(5 * time.Minute) },
	}
	n, err := rec.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-enqueued = %d, want 1", n)
	}
	if !pendingExists(spool, stranded.ID) {
		t.Fatal("stranded job was not re-enqueued")
	}
	// Idempotent: a second sweep (or a restart) finds nothing to do.
	if n, _ := rec.SweepOnce(context.Background()); n != 0 {
		t.Fatalf("second sweep re-enqueued %d, want 0", n)
	}
	// The recovered job is leasable by a worker.
	lease, ok, err := spool.LeaseNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("LeaseNext ok=%v err=%v", ok, err)
	}
	if lease.Envelope.JobID != stranded.ID && lease.Envelope.JobID != healthy.ID {
		t.Fatalf("unexpected leased job %q", lease.Envelope.JobID)
	}
}

func TestQueuedJobReconcilerSkipsYoungAndAlreadyHandledJobs(t *testing.T) {
	store := jobstore.NewMemoryStore()
	spool := NewFileSpoolDispatcher(t.TempDir())

	young := createThenEnqueue(t, store, spool, true)
	rec := &QueuedJobReconciler{
		Jobs: store, Dispatcher: spool, MinAge: time.Minute,
		Now: func() time.Time { return young.CreatedAt.Add(10 * time.Second) },
	}
	if n, _ := rec.SweepOnce(context.Background()); n != 0 || pendingExists(spool, young.ID) {
		t.Fatalf("young job must be left to its live handler (n=%d)", n)
	}

	// A job that is leased or already finished in the spool must not be
	// re-enqueued even if its row still reads queued.
	leased := createThenEnqueue(t, store, spool, false)
	if _, ok, err := spool.LeaseNext(context.Background()); err != nil || !ok {
		t.Fatalf("LeaseNext ok=%v err=%v", ok, err)
	}
	done := createThenEnqueue(t, store, spool, false)
	lease, ok, err := spool.LeaseNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("LeaseNext ok=%v err=%v", ok, err)
	}
	if err := spool.CompleteLease(context.Background(), lease); err != nil {
		t.Fatalf("CompleteLease: %v", err)
	}
	rec.Now = func() time.Time { return young.CreatedAt.Add(time.Hour) }
	n, err := rec.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 1 { // only `young`, now old enough
		t.Fatalf("re-enqueued = %d, want 1", n)
	}
	if pendingExists(spool, leased.ID) || pendingExists(spool, done.ID) {
		t.Fatal("leased/finished jobs must not be re-enqueued")
	}
}

type failingEnqueueSpool struct {
	*FileSpoolDispatcher
	failID string
}

func (f failingEnqueueSpool) EnqueueJob(ctx context.Context, job jobstore.Job) (Receipt, error) {
	if job.ID == f.failID {
		return Receipt{}, errors.New("boom")
	}
	return f.FileSpoolDispatcher.EnqueueJob(ctx, job)
}

func TestQueuedJobReconcilerEnqueueFailureDoesNotBlockOthers(t *testing.T) {
	store := jobstore.NewMemoryStore()
	spool := NewFileSpoolDispatcher(t.TempDir())
	bad := createThenEnqueue(t, store, spool, true)
	good := createThenEnqueue(t, store, spool, true)

	rec := &QueuedJobReconciler{
		Jobs: store, Dispatcher: failingEnqueueSpool{spool, bad.ID}, MinAge: time.Minute,
		Now: func() time.Time { return good.CreatedAt.Add(time.Hour) },
	}
	n, err := rec.SweepOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("SweepOnce n=%d err=%v, want 1,nil", n, err)
	}
	if pendingExists(spool, bad.ID) || !pendingExists(spool, good.ID) {
		t.Fatal("expected only the good job to be re-enqueued")
	}
}

func TestQueuedJobReconcilerNoopWithoutPresenceChecker(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job := createThenEnqueue(t, store, NewNoopDispatcher(), true)
	rec := &QueuedJobReconciler{
		Jobs: store, Dispatcher: NewNoopDispatcher(), MinAge: time.Minute,
		Now: func() time.Time { return job.CreatedAt.Add(time.Hour) },
	}
	if n, err := rec.SweepOnce(context.Background()); n != 0 || err != nil {
		t.Fatalf("SweepOnce n=%d err=%v, want 0,nil (no presence check, no re-publish)", n, err)
	}
}

func TestQueuedJobReconcilerRunSweepsAtStartup(t *testing.T) {
	store := jobstore.NewMemoryStore()
	spool := NewFileSpoolDispatcher(t.TempDir())
	job := createThenEnqueue(t, store, spool, true)

	ctx, cancel := context.WithCancel(context.Background())
	rec := &QueuedJobReconciler{
		Jobs: store, Dispatcher: spool, MinAge: time.Minute, Interval: time.Hour,
		Now: func() time.Time { return job.CreatedAt.Add(time.Hour) },
	}
	done := make(chan error, 1)
	go func() { done <- rec.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for !pendingExists(spool, job.ID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	if !pendingExists(spool, job.ID) {
		t.Fatal("startup sweep did not re-enqueue the stranded job")
	}
}
