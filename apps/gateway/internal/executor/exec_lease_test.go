package executor

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/topology"

	_ "modernc.org/sqlite"
)

func execLeaseBackend(t *testing.T) *topology.SQLTokenBackend {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "exec.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	b := topology.NewSQLiteTokenBackend(db)
	if err := b.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	return b
}

func queuedJob(t *testing.T, store jobstore.Store, key string) (jobstore.Job, DispatchEnvelope) {
	t.Helper()
	job, err := store.Create(t.Context(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", IdempotencyKey: key,
		Target: "mock", CommandType: "submit", Input: map[string]any{"prompt": "hi"}, TraceID: "trace_" + key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return job, EnvelopeFromJob(job)
}

func completedEvents(envelope DispatchEnvelope) []jobstore.WorkerEvent {
	return []jobstore.WorkerEvent{
		{EventID: "e1_" + envelope.JobID, JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "running", Sequence: 1, TraceID: envelope.TraceID, Data: map[string]any{"status": "running"}},
		{EventID: "e2_" + envelope.JobID, JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "completed", Sequence: 2, TraceID: envelope.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "ok"}}},
	}
}

// Two consumers share one execution-lease backend (two replicas). The same
// job delivered to both while the first is still running must reach the
// provider ONCE: the duplicate delivery is nacked, not run.
func TestDuplicateDeliveryIsNotRunWhileFirstHoldsTheExecutionLease(t *testing.T) {
	backend := execLeaseBackend(t)
	store := jobstore.NewMemoryStore()
	job, envelope := queuedJob(t, store, "dup_delivery_key_0001")

	var runs atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	newConsumer := func(lease *fakeWorkerLease, blocking bool) *WorkerConsumer {
		return &WorkerConsumer{
			Queue:      fakeWorkerQueue{lease: lease},
			Jobs:       store,
			ExecLeases: backend,
			Runner: WorkerRunFunc(func(ctx context.Context, env DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
				runs.Add(1)
				if blocking {
					close(entered)
					<-release
				}
				return completedEvents(env), nil
			}),
		}
	}
	leaseA := &fakeWorkerLease{jobID: job.ID, leaseID: "a", envelope: envelope}
	leaseB := &fakeWorkerLease{jobID: job.ID, leaseID: "b", envelope: envelope}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = newConsumer(leaseA, true).RunOnce(context.Background())
	}()
	<-entered

	processed, err := newConsumer(leaseB, false).RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("duplicate delivery: processed=%v err=%v", processed, err)
	}
	if !leaseB.retried || leaseB.completed || leaseB.failed {
		t.Fatalf("duplicate must be nacked for redelivery, got %+v", leaseB)
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("provider reached %d times, want exactly once", got)
	}
	close(release)
	<-done
	if !leaseA.completed {
		t.Fatalf("first delivery should complete: %+v", leaseA)
	}
}

// If the execution lease is lost while a job runs (the holder was presumed
// dead), the local run is cancelled instead of racing a second worker.
func TestLostExecutionLeaseCancelsTheRun(t *testing.T) {
	backend := execLeaseBackend(t)
	store := jobstore.NewMemoryStore()
	job, envelope := queuedJob(t, store, "lost_lease_key_00001")

	cancelled := make(chan struct{})
	entered := make(chan struct{})
	consumer := &WorkerConsumer{
		Queue:             fakeWorkerQueue{lease: &fakeWorkerLease{jobID: job.ID, leaseID: "a", envelope: envelope}},
		Jobs:              store,
		ExecLeases:        backend,
		HeartbeatInterval: 20 * time.Millisecond,
		Runner: WorkerRunFunc(func(ctx context.Context, env DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			close(entered)
			select {
			case <-ctx.Done():
				close(cancelled)
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return completedEvents(env), nil
			}
		}),
	}
	go func() { _, _ = consumer.RunOnce(context.Background()) }()
	<-entered

	// The lease expires and is swept (as it would be after a stall longer
	// than its TTL): the holder's next heartbeat must find it gone.
	time.Sleep(60 * time.Millisecond)
	if n, err := backend.SweepExpired(t.Context(), time.Now().UTC().Add(10*time.Minute)); err != nil || n != 1 {
		t.Fatalf("sweep n=%d err=%v", n, err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("run was not cancelled after the execution lease was lost")
	}
}

// While a job runs, the heartbeat renews the lease so it outlives its TTL
// window without a second consumer being able to take it.
func TestHeartbeatKeepsExecutionLeaseAlive(t *testing.T) {
	backend := execLeaseBackend(t)
	now := time.Now().UTC()
	token, ok, err := backend.AcquireToken(t.Context(), []topology.Lane{{Key: "exec:j", Cap: 1}}, 50*time.Millisecond, now)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	if err := backend.RenewToken(t.Context(), token, time.Minute, now.Add(10*time.Millisecond)); err != nil {
		t.Fatalf("renew: %v", err)
	}
	later := now.Add(10 * time.Second)
	if _, ok, _ := backend.AcquireToken(t.Context(), []topology.Lane{{Key: "exec:j", Cap: 1}}, time.Minute, later); ok {
		t.Fatal("a renewed lease must still block a second holder")
	}
	// An expired, unrenewed lease is lost.
	if err := backend.RenewToken(t.Context(), token, time.Minute, now.Add(2*time.Minute)); err == nil {
		t.Fatal("renewing an expired token must report it lost")
	}
}
