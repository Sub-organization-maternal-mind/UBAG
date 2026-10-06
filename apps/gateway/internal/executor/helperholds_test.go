package executor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// pickerFunc adapts a function to HelperPicker.
type pickerFunc func(req HelperPickRequest) (HelperPlacement, error)

func (f pickerFunc) Pick(_ context.Context, req HelperPickRequest) (HelperPlacement, error) {
	return f(req)
}

// holdRig is a real file spool, a real memory job store and a consumer whose
// picker is scripted. Whatever is not placed runs on a local runner that counts.
type holdRig struct {
	f        *remoteFixture
	spool    *FileSpoolDispatcher
	consumer *WorkerConsumer
	local    atomic.Int32
	cur      atomic.Int32
	peak     atomic.Int32
}

func newHoldRig(t *testing.T, pick pickerFunc, mut func(*WorkerConsumer)) *holdRig {
	t.Helper()
	g := &holdRig{spool: NewFileSpoolDispatcher(t.TempDir())}
	g.f = newRemoteFixture(t, func(c *RemoteConfig) { c.Picker = pick })
	g.consumer = &WorkerConsumer{
		Spool: g.spool, Jobs: g.f.store, Remote: g.f.runner, PollInterval: 5 * time.Millisecond,
		Runner: WorkerRunFunc(func(_ context.Context, env DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			n := g.cur.Add(1)
			defer g.cur.Add(-1)
			for p := g.peak.Load(); n > p && !g.peak.CompareAndSwap(p, n); p = g.peak.Load() {
			}
			time.Sleep(15 * time.Millisecond)
			g.local.Add(1)
			return completedEvents(env), nil
		}),
	}
	if mut != nil {
		mut(g.consumer)
	}
	return g
}

// enqueue creates a job and puts it on the spool (job ids are sequential, so the
// spool serves jobs in the order they are enqueued).
func (g *holdRig) enqueue(t *testing.T) jobstore.Job {
	t.Helper()
	job, _ := g.f.newJob()
	if _, err := g.spool.EnqueueJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	return job
}

// run starts the consumer; stop cancels it and waits for Run to return.
func (g *holdRig) run(t *testing.T) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- g.consumer.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("Run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func (g *holdRig) completed(id string) bool {
	return g.f.job(id).Status == jobstore.StatusCompleted
}

// A busy identity at the head of the queue must neither pin the only worker nor
// starve the job behind it, and must be placed again once per delay, not once per
// poll (the file spool re-queues a retry instantly and returns it to the head).
func TestPlacerConsumerBusyIdentityNeitherBlocksARunnableJobNorSpins(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	var aID atomic.Value
	var picksA atomic.Int32
	g := newHoldRig(t, func(req HelperPickRequest) (HelperPlacement, error) {
		if id, _ := aID.Load().(string); req.JobID == id {
			picksA.Add(1)
			if busy.Load() {
				return HelperPlacement{}, &HelperRetryError{Reason: "identity_busy", RetryAfter: 100 * time.Millisecond}
			}
		}
		return HelperPlacement{}, ErrNoHelper // everything else runs here
	}, func(c *WorkerConsumer) { c.PoolSize, c.AsyncHolds = 1, 8 })
	a := g.enqueue(t) // the busy identity's job is the oldest
	b := g.enqueue(t) // a runnable job behind it
	aID.Store(a.ID)
	stop := g.run(t)

	waitFor(t, 5*time.Second, "the runnable job behind the busy identity to complete", func() bool { return g.completed(b.ID) })
	if jobstore.TerminalStatus(g.f.job(a.ID).Status) {
		t.Fatal("the busy identity's job must still be waiting")
	}
	before := picksA.Load()
	time.Sleep(500 * time.Millisecond)
	if n := picksA.Load() - before; n < 2 || n > 9 {
		t.Fatalf("the held job was placed %d times in 500 ms with a 100 ms delay: want about one per delay (a spin is hundreds)", n)
	}

	busy.Store(false) // the identity frees up
	waitFor(t, 5*time.Second, "the held job to run once its identity is free", func() bool { return g.completed(a.ID) })
	stop()
	if g.consumer.held.Load() != 0 {
		t.Fatalf("%d held leases leaked", g.consumer.held.Load())
	}
}

// Without async holds a refused placement keeps ADR-0011's synchronous behaviour,
// which on the file spool starves the job behind it (the retried job returns to the
// head of the queue). This is the fallback past the AsyncHolds bound, and the
// reason the bound exists.
func TestPlacerConsumerSynchronousHoldStarvesTheJobBehindIt(t *testing.T) {
	var aID atomic.Value
	g := newHoldRig(t, func(req HelperPickRequest) (HelperPlacement, error) {
		if id, _ := aID.Load().(string); req.JobID == id {
			return HelperPlacement{}, &HelperRetryError{Reason: "identity_busy", RetryAfter: 50 * time.Millisecond}
		}
		return HelperPlacement{}, ErrNoHelper
	}, func(c *WorkerConsumer) { c.PoolSize = 1 }) // AsyncHolds unset
	a := g.enqueue(t)
	b := g.enqueue(t)
	aID.Store(a.ID)
	g.run(t)
	time.Sleep(600 * time.Millisecond)
	if g.completed(b.ID) {
		t.Fatal("the job behind a synchronously held one ran: the starvation this slice removes is not reproduced")
	}
}

// The attempt reconcile gate (P4.18) holds a job whose previous attempt still holds
// its lease; that wait is off the worker too, so it does not block the jobs behind.
type reconcilerFunc func(jobID string) nodes.ReconcilePlan

func (f reconcilerFunc) Reconcile(_ context.Context, jobID string) (nodes.ReconcilePlan, error) {
	return f(jobID), nil
}

func TestPlacerConsumerReconcileGateHoldsWaitOffTheWorkerToo(t *testing.T) {
	var aID atomic.Value
	g := newHoldRig(t, func(HelperPickRequest) (HelperPlacement, error) { return HelperPlacement{}, ErrNoHelper },
		func(c *WorkerConsumer) {
			c.PoolSize, c.AsyncHolds = 1, 8
			c.Reconcile = reconcilerFunc(func(jobID string) nodes.ReconcilePlan {
				if id, _ := aID.Load().(string); jobID == id {
					return nodes.ReconcilePlan{Action: nodes.ReconcileWait, Reason: nodes.ReconcileLeaseHeld, RetryAfter: 100 * time.Millisecond}
				}
				return nodes.ReconcilePlan{Action: nodes.ReconcileRun, Reason: nodes.ReconcileNoAttempt}
			})
		})
	a := g.enqueue(t)
	b := g.enqueue(t)
	aID.Store(a.ID)
	g.run(t)
	waitFor(t, 5*time.Second, "the job behind the reconcile hold to complete", func() bool { return g.completed(b.ID) })
	if jobstore.TerminalStatus(g.f.job(a.ID).Status) {
		t.Fatal("the held job must still be waiting")
	}
}

// At most AsyncHolds leases wait off the worker; the rest wait on it.
func TestPlacerConsumerAsyncHoldsAreBounded(t *testing.T) {
	g := newHoldRig(t, func(HelperPickRequest) (HelperPlacement, error) {
		return HelperPlacement{}, &HelperRetryError{Reason: "no_capacity", RetryAfter: 80 * time.Millisecond}
	}, func(c *WorkerConsumer) { c.PoolSize, c.AsyncHolds = 1, 2 })
	for range 6 {
		g.enqueue(t)
	}
	g.run(t)
	peak := int64(0)
	for end := time.Now().Add(600 * time.Millisecond); time.Now().Before(end); time.Sleep(time.Millisecond) {
		peak = max(peak, g.consumer.held.Load())
	}
	if peak != 2 {
		t.Fatalf("peak held leases = %d, want exactly the bound of 2", peak)
	}
	if g.local.Load() != 0 {
		t.Fatal("a held job ran")
	}
}

// With the pool larger than the local pool, local runs stay bounded by the local
// pool: a job that stays on this gateway takes a local slot or is held.
func TestPlacerConsumerLocalRunsStayBoundedByTheLocalPool(t *testing.T) {
	g := newHoldRig(t, func(HelperPickRequest) (HelperPlacement, error) { return HelperPlacement{}, ErrNoHelper },
		func(c *WorkerConsumer) {
			c.PoolSize, c.AsyncHolds, c.localHoldDelay = 1, 8, 20*time.Millisecond
			c.HelperCapacity = func() int { return 3 }
		})
	if got := g.consumer.workerCount(); got != 4 {
		t.Fatalf("workers = %d, want the local pool (1) plus the helper capacity (3)", got)
	}
	var jobs []jobstore.Job
	for range 6 {
		jobs = append(jobs, g.enqueue(t))
	}
	g.run(t)
	waitFor(t, 10*time.Second, "all six local jobs to complete", func() bool {
		for _, j := range jobs {
			if !g.completed(j.ID) {
				return false
			}
		}
		return true
	})
	if g.peak.Load() != 1 {
		t.Fatalf("%d local runs overlapped with a local pool of 1: helper workers must not raise local concurrency", g.peak.Load())
	}
}

type blockingQueue struct{ active atomic.Int32 }

func (*blockingQueue) Ready(context.Context) error { return nil }
func (q *blockingQueue) LeaseNext(ctx context.Context) (WorkerLease, bool, error) {
	q.active.Add(1)
	defer q.active.Add(-1)
	<-ctx.Done()
	return nil, false, ctx.Err()
}

// The worker count follows the helper capacity and only grows.
func TestPlacerConsumerPoolFollowsHelperCapacity(t *testing.T) {
	var capacity atomic.Int32
	q := &blockingQueue{}
	c := &WorkerConsumer{
		Queue: q, Jobs: jobstore.NewMemoryStore(), PoolSize: 1, PoolRegrowEvery: 10 * time.Millisecond,
		Runner:         WorkerRunFunc(func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) { return nil, nil }),
		HelperCapacity: func() int { return int(capacity.Load()) },
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	waitFor(t, 5*time.Second, "the local worker", func() bool { return q.active.Load() == 1 })
	capacity.Store(3)
	waitFor(t, 5*time.Second, "the pool to grow to the local pool plus 3 helper slots", func() bool { return q.active.Load() == 4 })
	capacity.Store(1) // a shrink parks nothing and kills nothing
	time.Sleep(60 * time.Millisecond)
	if q.active.Load() != 4 {
		t.Fatalf("workers = %d after the capacity fell, want 4 (grow-only)", q.active.Load())
	}
}

func TestPlacerConsumerWorkerCount(t *testing.T) {
	cases := []struct {
		name     string
		pool     int
		capacity func() int
		want     int
	}{
		{"unset keeps one worker", 0, nil, 1},
		{"legacy pool", 5, nil, 5},
		{"legacy pool is clamped to 32", 100, nil, 32},
		{"capacity adds a worker per helper slot", 1, func() int { return 3 }, 4},
		{"the local pool keeps its clamp", 100, func() int { return 3 }, 35},
		{"a negative capacity adds nothing", 2, func() int { return -5 }, 2},
		{"derived from capacity instead of the clamp of 32", 1, func() int { return 200 }, 201},
		{"bounded at maxFleetWorkers", 1, func() int { return 100000 }, maxFleetWorkers},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &WorkerConsumer{PoolSize: tc.pool, HelperCapacity: tc.capacity}
			if got := c.workerCount(); got != tc.want {
				t.Fatalf("workerCount = %d, want %d", got, tc.want)
			}
		})
	}
}

// A held lease waits off the worker, goes back to the queue after its delay, and
// goes back at once on shutdown.
func TestPlacerConsumerAsyncHoldReturnsTheLeaseAfterTheDelayOrOnShutdown(t *testing.T) {
	hold := func(retryAfter time.Duration) (*remoteFixture, *fakeWorkerLease, *WorkerConsumer) {
		f := newRemoteFixture(t, func(c *RemoteConfig) {
			c.Picker = pickerFunc(func(HelperPickRequest) (HelperPlacement, error) {
				return HelperPlacement{}, &HelperRetryError{Reason: "identity_busy", RetryAfter: retryAfter}
			})
		})
		_, lease := f.newJob()
		c := f.consumer(lease, nil)
		c.AsyncHolds = 1
		return f, lease, c
	}

	t.Run("after the delay", func(t *testing.T) {
		_, lease, c := hold(80 * time.Millisecond)
		begin := time.Now()
		res := awaitRun(t, runOnceAsync(t.Context(), c))
		if !res.processed || res.err != nil {
			t.Fatalf("RunOnce = %+v", res)
		}
		if took := time.Since(begin); took > 60*time.Millisecond {
			t.Fatalf("RunOnce took %v: the worker waited out the hold", took)
		}
		if c.held.Load() != 1 {
			t.Fatalf("held = %d, want 1", c.held.Load())
		}
		c.holdWG.Wait()
		if waited := time.Since(begin); waited < 70*time.Millisecond {
			t.Fatalf("the lease went back after %v: the delay was skipped", waited)
		}
		if !lease.retried || lease.completed || lease.failed || c.held.Load() != 0 {
			t.Fatalf("lease = %+v, held = %d: want retried only", lease, c.held.Load())
		}
	})
	t.Run("at once on shutdown", func(t *testing.T) {
		_, lease, c := hold(25 * time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		if res := awaitRun(t, runOnceAsync(ctx, c)); !res.processed || res.err != nil {
			t.Fatalf("RunOnce = %+v", res)
		}
		cancel()
		done := make(chan struct{})
		go func() { c.holdWG.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("the held lease was not returned on shutdown")
		}
		if !lease.retried {
			t.Fatal("a held job must go back to the queue on shutdown, not stay leased")
		}
	})
	t.Run("a held job never runs and is never failed", func(t *testing.T) {
		f, lease, c := hold(10 * time.Millisecond)
		job := f.job(lease.jobID)
		awaitRun(t, runOnceAsync(t.Context(), c))
		c.holdWG.Wait()
		if f.localRuns.Load() != 0 || f.job(job.ID).Status != jobstore.StatusQueued || len(f.attempts(job.ID)) != 0 {
			t.Fatal("a held job must stay queued, unrun and out of the ledger")
		}
	})
}
