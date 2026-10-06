package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// spoolClock is an injectable clock for the file-spool lease tests. It starts
// at the real time so the lease files' real mtimes are not "in the future".
type spoolClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *spoolClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *spoolClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func leasedSpool(t *testing.T, ttl time.Duration) (*FileSpoolDispatcher, *spoolClock, FileSpoolLease) {
	t.Helper()
	clock := &spoolClock{t: time.Now().UTC()}
	d := NewFileSpoolDispatcher(t.TempDir())
	d.now = clock.Now
	d.SetLeaseTTL(ttl)
	enqueueSpoolJobs(t, d, "job_000000000001")
	lease, ok, err := d.LeaseNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("LeaseNext ok=%v err=%v", ok, err)
	}
	return d, clock, lease
}

func TestFileSpoolReclaimExpiredLeaseAfterTTL(t *testing.T) {
	d, clock, lease := leasedSpool(t, 30*time.Second)

	clock.Advance(29 * time.Second)
	if n, err := d.ReclaimExpiredLeases(); err != nil || n != 0 {
		t.Fatalf("before expiry reclaimed=%d err=%v, want 0", n, err)
	}
	clock.Advance(2 * time.Second)
	if n, err := d.ReclaimExpiredLeases(); err != nil || n != 1 {
		t.Fatalf("after expiry reclaimed=%d err=%v, want 1", n, err)
	}
	if _, err := os.Stat(filepath.Join(d.pendingDir(), lease.JobID+".json")); err != nil {
		t.Fatalf("envelope not back in pending: %v", err)
	}
	// The envelope is leasable again by another consumer.
	if _, ok, err := d.LeaseNext(context.Background()); err != nil || !ok {
		t.Fatalf("re-lease ok=%v err=%v", ok, err)
	}
}

func TestFileSpoolRenewKeepsLeaseAliveAndReportsLoss(t *testing.T) {
	d, clock, lease := leasedSpool(t, 30*time.Second)

	clock.Advance(25 * time.Second)
	if err := d.RenewLease(context.Background(), lease); err != nil {
		t.Fatalf("RenewLease: %v", err)
	}
	clock.Advance(25 * time.Second) // 50 s since claim, 25 s since renewal
	if n, _ := d.ReclaimExpiredLeases(); n != 0 {
		t.Fatalf("renewed lease reclaimed (n=%d)", n)
	}
	clock.Advance(10 * time.Second) // 35 s since renewal
	if n, _ := d.ReclaimExpiredLeases(); n != 1 {
		t.Fatalf("lapsed lease not reclaimed (n=%d)", n)
	}
	// The slow holder learns it lost the lease on its next renewal.
	if err := d.RenewLease(context.Background(), lease); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("RenewLease after reclaim = %v, want ErrLeaseLost", err)
	}
}

func TestFileSpoolLeaseTTLZeroIsLegacy(t *testing.T) {
	d, clock, _ := leasedSpool(t, 0)
	clock.Advance(24 * time.Hour)
	if n, err := d.ReclaimExpiredLeases(); err != nil || n != 0 {
		t.Fatalf("ttl 0 reclaimed=%d err=%v, want no-op", n, err)
	}
	// Renewal is a no-op too, even for a path that no longer exists.
	if err := d.RenewLease(context.Background(), FileSpoolLease{Path: filepath.Join(t.TempDir(), "gone.json")}); err != nil {
		t.Fatalf("ttl 0 RenewLease = %v, want nil", err)
	}
	// Startup recovery is unchanged: it still reclaims every stranded lease.
	if n, err := d.RecoverOrphanLeases(); err != nil || n != 1 {
		t.Fatalf("RecoverOrphanLeases = %d, %v, want 1", n, err)
	}
}

func TestFileSpoolFreshLeaseOfOldEnvelopeIsNotExpired(t *testing.T) {
	// Rename keeps the pending file's old mtime; the lease time in the file
	// name must keep a just-claimed lease alive.
	clock := &spoolClock{t: time.Now().UTC()}
	d := NewFileSpoolDispatcher(t.TempDir())
	d.now = clock.Now
	d.SetLeaseTTL(30 * time.Second)
	enqueueSpoolJobs(t, d, "job_000000000001")
	old := clock.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(d.pendingDir(), "job_000000000001.json"), old, old); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.LeaseNext(context.Background()); err != nil || !ok {
		t.Fatalf("LeaseNext ok=%v err=%v", ok, err)
	}
	if n, _ := d.ReclaimExpiredLeases(); n != 0 {
		t.Fatalf("just-claimed lease of an old envelope reclaimed (n=%d)", n)
	}
}

func TestFileSpoolRunLeaseReclaimerSweeps(t *testing.T) {
	d, clock, _ := leasedSpool(t, 30*time.Second)
	clock.Advance(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.RunLeaseReclaimer(ctx, 10*time.Millisecond) }()
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(d.pendingDir(), "job_000000000001.json")); err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatal("reclaimer did not return the expired lease to pending")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestNATSWorkerLeaseHeartbeatSendsInProgress(t *testing.T) {
	msg := &fakeNATSMsg{}
	var beater LeaseHeartbeater = natsWorkerLease{msg: msg, envelope: DispatchEnvelope{JobID: "job_hb"}}
	for i := 0; i < 3; i++ {
		if err := beater.Heartbeat(context.Background()); err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
	}
	if msg.inProgress != 3 {
		t.Fatalf("InProgress calls = %d, want 3", msg.inProgress)
	}
}

// heartbeatLease is a fakeWorkerLease whose Heartbeat reports a lost queue lease.
type heartbeatLease struct {
	*fakeWorkerLease
	beats atomic.Int32
	err   error
}

func (l *heartbeatLease) Heartbeat(context.Context) error {
	l.beats.Add(1)
	return l.err
}

// leaseQueue serves one WorkerLease of any type (fakeWorkerQueue is typed to *fakeWorkerLease).
type leaseQueue struct{ lease WorkerLease }

func (q leaseQueue) Ready(context.Context) error { return nil }
func (q leaseQueue) LeaseNext(context.Context) (WorkerLease, bool, error) {
	return q.lease, true, nil
}

func TestConsumerCancelsRunWhenQueueLeaseIsLost(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job, envelope := queuedJob(t, store, "lease_lost_key_0001")
	lease := &heartbeatLease{
		fakeWorkerLease: &fakeWorkerLease{jobID: job.ID, leaseID: "a", envelope: envelope},
		err:             ErrLeaseLost,
	}
	cancelled := make(chan struct{})
	consumer := &WorkerConsumer{
		Queue:             leaseQueue{lease: lease},
		Jobs:              store,
		HeartbeatInterval: 10 * time.Millisecond,
		Runner: WorkerRunFunc(func(ctx context.Context, env DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			select {
			case <-ctx.Done():
				close(cancelled)
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return completedEvents(env), nil
			}
		}),
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = consumer.RunOnce(context.Background()) }()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("run not cancelled after ErrLeaseLost")
	}
	<-done
	if lease.completed || lease.failed {
		t.Fatalf("lost lease must not be completed/failed: completed=%v failed=%v", lease.completed, lease.failed)
	}
}

func TestReaperTimeoutStopsRunningWorkerAndClosesLease(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job, envelope := queuedJob(t, store, "reaper_stop_key_0001")
	lease := &fakeWorkerLease{jobID: job.ID, leaseID: "a", envelope: envelope}
	entered, cancelled := make(chan struct{}), make(chan struct{})
	consumer := &WorkerConsumer{
		Queue: fakeWorkerQueue{lease: lease},
		Jobs:  store,
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
	done := make(chan struct{})
	go func() { defer close(done); _, _ = consumer.RunOnce(context.Background()) }()
	<-entered

	rp := &StaleJobReaper{
		Jobs:        store,
		MaxLifetime: time.Minute,
		Now:         func() time.Time { return time.Now().Add(time.Hour) },
	}
	if n, err := rp.SweepOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("SweepOnce = %d, %v, want 1", n, err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("reaped job's worker was not stopped")
	}
	<-done
	got, _, _ := store.Get(context.Background(), job.ID)
	if got.Status != jobstore.StatusTimedOut {
		t.Fatalf("status = %s, want timed_out (not overwritten by the cancelled run)", got.Status)
	}
	if !lease.completed || lease.retried {
		t.Fatalf("lease completed=%v retried=%v, want completed and not re-queued", lease.completed, lease.retried)
	}
}

func TestReaperExpiresLapsedAttemptsFirst(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job := reaperTestJob(t, store, nil, nil)
	if _, err := store.BeginAttempt(context.Background(), jobstore.BeginAttemptRequest{
		JobID: job.ID, AttemptID: "att_reap1", NodeID: "node_a", TTL: time.Millisecond,
	}); err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	rp := &StaleJobReaper{Jobs: store, Attempts: store, MaxLifetime: time.Hour}
	if _, err := rp.SweepOnce(context.Background()); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	attempts, err := store.ListAttempts(context.Background(), job.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("ListAttempts = %v, %v", attempts, err)
	}
	if attempts[0].State != jobstore.AttemptExpired {
		t.Fatalf("attempt state = %q, want expired (fence closed)", attempts[0].State)
	}
}
