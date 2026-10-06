package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/jobs"
)

const (
	// maxDaemonPoolSize bounds Size no matter what is configured: every slot is a
	// whole Python + Playwright driver process. The operator-facing ceiling (a lower
	// number, until the P0.13 lab measurements exist) is enforced at startup by the
	// serve package; this is only the structural backstop.
	maxDaemonPoolSize = 32

	defaultPoolMaxWait      = 30 * time.Second
	defaultPoolRetryAfter   = 2 * time.Second
	defaultPoolDrainTimeout = 5 * time.Second
)

// ErrPoolOverloaded is the typed, retryable saturation signal of a DaemonPool: no
// slot (or, for an identity that is already busy, no turn on it) became available
// within the bounded wait, or the placement queue was full. The job did NOT reach
// a worker. The consumer answers it with a delayed lease Retry (ADR-0011), never a
// job failure and never an immediate re-queue.
var ErrPoolOverloaded = errors.New("worker pool overloaded")

var errDaemonPoolClosed = errors.New("worker daemon pool is closed")

// PoolOverloadError carries why a placement was refused and how long the caller
// should hold the job back before retrying. errors.Is(err, ErrPoolOverloaded).
type PoolOverloadError struct {
	// Reason is "queue_full" (refused at once) or "wait_expired" (waited MaxWait).
	Reason     string
	RetryAfter time.Duration
}

func (e *PoolOverloadError) Error() string { return "worker pool overloaded: " + e.Reason }

func (e *PoolOverloadError) Unwrap() error { return ErrPoolOverloaded }

// DaemonPool runs jobs on N isolated warm-daemon slots (UBAG_WORKER_POOL_SIZE)
// instead of one daemon behind one mutex.
//
//   - Each slot is its own DaemonWorkerRunner: a separate worker process speaking
//     the unchanged one-job-at-a-time stdio protocol. A failure, cancel or deadline
//     kills only that slot's process; the others keep their warm pages.
//   - Identity gate: at most ONE active job per physical browser session (the
//     tenant-free key of ubag_worker/live/identity_lock.py). Two jobs for one
//     provider account never overlap, whatever the slot count. The worker's own
//     flock (slot mode) is the cross-process backstop for keys Go cannot derive
//     exactly.
//   - Warm affinity: a job goes to the idle slot whose page for its identity is
//     still warm; otherwise to a live cold slot, then a never-started slot, and
//     only then does it evict the least recently used warm page.
//   - Bounded wait: a job that cannot be placed waits at most MaxWait (and the
//     placement queue is bounded by MaxQueue), then fails with ErrPoolOverloaded.
//   - The job's run timeout starts only after a slot is held.
//
// A pool of one slot behaves like a DaemonWorkerRunner, which is what the gateway
// still builds by default; the pool is used only when UBAG_WORKER_POOL_SIZE > 1.
type DaemonPool struct {
	Python     string
	Script     string
	MaxRuntime time.Duration
	Artifacts  artifacts.ArtifactStore

	// Size is the number of slots; <= 0 means 1 and it is capped at 32.
	Size int
	// MaxWait is the longest a job waits to be placed before ErrPoolOverloaded.
	// <= 0 means 30s. It is deliberately NOT tied to MaxRuntime (25 minutes in
	// production): a wait that long would pin a consumer worker and make the
	// "bounded" overload unreachable in practice.
	MaxWait time.Duration
	// MaxQueue bounds how many jobs may wait to be placed at once; one more is
	// refused immediately. <= 0 means Size (the consumer runs about Size jobs).
	MaxQueue int
	// RetryAfter is the hold-back hint carried by every overload error. <= 0
	// means 2s.
	RetryAfter time.Duration
	// DrainTimeout bounds how long Close lets a slot exit on its own (stdin EOF)
	// before it is killed. <= 0 means 5s.
	DrainTimeout time.Duration

	// newSlotCommand builds a slot's daemon process; tests re-exec the test binary.
	newSlotCommand func(slot int) *exec.Cmd

	initOnce sync.Once
	done     chan struct{}
	inflight sync.WaitGroup // slots currently handed out (acquire -> release)

	mu      sync.Mutex
	slots   []*poolSlot
	active  map[string]*poolSlot // identity key -> the slot running it (the gate)
	waiters []*poolWaiter        // FIFO; unplaced requests only
	closed  bool
}

type poolSlot struct {
	id     int
	runner *DaemonWorkerRunner

	// The fields below are guarded by DaemonPool.mu.
	busy bool
	// warmKey is the identity this slot's daemon holds a warm page for, "" when it
	// is cold. A slot is pinned to at most one warm key: the worker evicts the page
	// of any other key before it runs a job.
	warmKey  string
	spawned  bool // a daemon was started and has not been discarded since
	lastUsed time.Time
}

type poolWaiter struct {
	key      string
	ready    chan *poolSlot // buffered; written once, under DaemonPool.mu
	assigned bool
}

// slotOutcome is what a finished (or abandoned) placement did to its slot.
type slotOutcome int

const (
	// slotUntouched: the job never reached the daemon, so the slot is as it was.
	slotUntouched slotOutcome = iota
	// slotWarm: the job ended cleanly; the daemon holds the identity's page warm.
	slotWarm
	// slotCold: the job failed or was cancelled; its daemon was discarded.
	slotCold
)

func (p *DaemonPool) init() {
	p.initOnce.Do(func() {
		n := p.size()
		p.done = make(chan struct{})
		p.active = make(map[string]*poolSlot, n)
		p.slots = make([]*poolSlot, n)
		for i := range p.slots {
			runner := &DaemonWorkerRunner{
				Python:     p.Python,
				Script:     p.Script,
				MaxRuntime: p.MaxRuntime,
				Artifacts:  p.Artifacts,
				slotMode:   true,
				slotID:     i,
				poolSize:   n,
			}
			if p.newSlotCommand != nil {
				slot := i
				runner.newCommand = func() *exec.Cmd { return p.newSlotCommand(slot) }
			}
			p.slots[i] = &poolSlot{id: i, runner: runner}
		}
	})
}

func (p *DaemonPool) size() int {
	switch {
	case p.Size <= 0:
		return 1
	case p.Size > maxDaemonPoolSize:
		return maxDaemonPoolSize
	}
	return p.Size
}

func (p *DaemonPool) maxRuntime() time.Duration {
	if p.MaxRuntime <= 0 {
		return defaultWorkerMaxRuntime
	}
	return p.MaxRuntime
}

func (p *DaemonPool) maxWait() time.Duration {
	if p.MaxWait > 0 {
		return p.MaxWait
	}
	return defaultPoolMaxWait
}

func (p *DaemonPool) maxQueue() int {
	if p.MaxQueue > 0 {
		return p.MaxQueue
	}
	return p.size()
}

func (p *DaemonPool) retryAfter() time.Duration {
	if p.RetryAfter > 0 {
		return p.RetryAfter
	}
	return defaultPoolRetryAfter
}

// RunWorker implements WorkerRunner.
func (p *DaemonPool) RunWorker(
	ctx context.Context, envelope DispatchEnvelope,
) ([]jobs.WorkerEvent, error) {
	var events []jobs.WorkerEvent
	if err := p.run(ctx, envelope, batchDaemonJob(&events)); err != nil {
		return nil, err
	}
	return events, nil
}

// StreamWorker implements StreamingWorkerRunner.
func (p *DaemonPool) StreamWorker(
	ctx context.Context, envelope DispatchEnvelope, sink EventSink,
) error {
	return p.run(ctx, envelope, streamingDaemonJob(sink))
}

// run places the job on a slot (identity gate + affinity), runs it there, and
// frees the slot. Attachments are materialized only once a slot is held, so an
// overloaded job that is retried later does not pay for them again.
func (p *DaemonPool) run(ctx context.Context, envelope DispatchEnvelope, job daemonJobFunc) (err error) {
	// Same typed submission boundary as DaemonWorkerRunner.runJob
	// (UBAG_WORKER_STRICT_SUBMIT; flag off: untouched). A placement refusal never
	// reached a daemon, so with the flag on it reads as ErrNotSubmitted while
	// ErrPoolOverloaded stays in the error chain.
	var submitted atomic.Bool
	defer func() { err = classifySubmission(err, submitted.Load()) }()

	p.init()
	maxRuntime := p.maxRuntime()
	key := daemonIdentityKey(envelope)
	slot, err := p.acquire(ctx, key)
	if err != nil {
		return err
	}
	outcome := slotCold // a panic or an early return leaves the slot cold, never wrongly warm
	defer func() { p.release(slot, key, outcome) }()

	cleanup, err := materializeDaemonAttachments(ctx, p.Artifacts, &envelope, maxRuntime)
	if err != nil {
		outcome = slotUntouched
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	if err := slot.runner.runExclusive(ctx, envelope, maxRuntime, &submitted, job); err != nil {
		return err
	}
	outcome = slotWarm
	return nil
}

// acquire reserves a slot for key, waiting (bounded) for a free slot and for the
// identity's current holder.
func (p *DaemonPool) acquire(ctx context.Context, key string) (*poolSlot, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errDaemonPoolClosed
	}
	w := &poolWaiter{key: key, ready: make(chan *poolSlot, 1)}
	p.waiters = append(p.waiters, w)
	p.dispatchLocked()
	if w.assigned {
		slot := <-w.ready
		p.mu.Unlock()
		return slot, nil
	}
	if len(p.waiters) > p.maxQueue() {
		p.removeWaiterLocked(w)
		busy, waiting := p.countsLocked()
		p.mu.Unlock()
		return nil, p.overload("queue_full", busy, waiting)
	}
	p.mu.Unlock()

	timer := time.NewTimer(p.maxWait())
	defer timer.Stop()
	var cause error
	select {
	case slot := <-w.ready:
		return slot, nil
	case <-timer.C:
	case <-ctx.Done():
		cause = ctx.Err()
	case <-p.done:
		cause = errDaemonPoolClosed
	}

	p.mu.Lock()
	if w.assigned {
		// Placed in the same instant. A timeout just loses the race: run the job.
		// A cancel or shutdown hands the untouched slot straight back.
		slot := <-w.ready
		if cause != nil {
			p.releaseLocked(slot, key, slotUntouched)
			p.mu.Unlock()
			return nil, cause
		}
		p.mu.Unlock()
		return slot, nil
	}
	p.removeWaiterLocked(w)
	busy, waiting := p.countsLocked()
	p.mu.Unlock()
	if cause != nil {
		return nil, cause
	}
	return nil, p.overload("wait_expired", busy, waiting)
}

func (p *DaemonPool) overload(reason string, busy, waiting int) error {
	slog.Warn("worker daemon pool saturated",
		"reason", reason, "slots", len(p.slots), "busy", busy, "waiting", waiting)
	return &PoolOverloadError{Reason: reason, RetryAfter: p.retryAfter()}
}

func (p *DaemonPool) release(slot *poolSlot, key string, outcome slotOutcome) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.releaseLocked(slot, key, outcome)
}

func (p *DaemonPool) releaseLocked(slot *poolSlot, key string, outcome slotOutcome) {
	if p.active[key] == slot {
		delete(p.active, key)
	}
	slot.busy = false
	switch outcome {
	case slotWarm:
		slot.warmKey, slot.spawned, slot.lastUsed = key, true, time.Now()
	case slotCold:
		slot.warmKey, slot.spawned, slot.lastUsed = "", false, time.Now()
	}
	p.inflight.Done()
	p.dispatchLocked()
}

// dispatchLocked hands free slots to waiting requests in arrival order. A request
// whose identity is busy is skipped, not blocked on: a later request for a free
// identity may take a free slot ahead of it.
func (p *DaemonPool) dispatchLocked() {
	if p.closed {
		return // waiters wake on p.done; nothing may start a daemon after Close
	}
	kept := p.waiters[:0]
	for _, w := range p.waiters {
		if p.active[w.key] != nil {
			kept = append(kept, w)
			continue
		}
		slot := p.pickSlotLocked(w.key)
		if slot == nil {
			kept = append(kept, w)
			continue
		}
		slot.busy = true
		p.active[w.key] = slot
		p.inflight.Add(1) // balanced by releaseLocked; Close waits on it
		w.assigned = true
		w.ready <- slot
	}
	clear(p.waiters[len(kept):])
	p.waiters = kept
}

// pickSlotLocked chooses among the idle slots: warm for this identity, then live
// but cold, then never started, then the least recently used warm slot of another
// identity. Preferring a fresh start over an eviction keeps warm pages alive for
// as long as the pool has room. nil means every slot is busy.
func (p *DaemonPool) pickSlotLocked(key string) *poolSlot {
	var best *poolSlot
	bestRank := 0
	for _, s := range p.slots {
		if s.busy {
			continue
		}
		rank := 4
		switch {
		case s.warmKey == key:
			rank = 1
		case s.spawned && s.warmKey == "":
			rank = 2
		case !s.spawned:
			rank = 3
		}
		if best == nil || rank < bestRank ||
			(rank == 4 && bestRank == 4 && s.lastUsed.Before(best.lastUsed)) {
			best, bestRank = s, rank
		}
	}
	return best
}

func (p *DaemonPool) removeWaiterLocked(w *poolWaiter) {
	p.waiters = slices.DeleteFunc(p.waiters, func(other *poolWaiter) bool { return other == w })
}

func (p *DaemonPool) countsLocked() (busy, waiting int) {
	for _, s := range p.slots {
		if s.busy {
			busy++
		}
	}
	return busy, len(p.waiters)
}

// Close drains the pool (gateway shutdown): new and waiting jobs are refused, and
// every slot finishes its active job and is then asked to exit by closing its
// stdin (the worker closes its warm pages itself), with a kill after DrainTimeout.
func (p *DaemonPool) Close() {
	p.init()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.done)
	p.mu.Unlock()

	// A slot handed out before the flag flipped may still be on its way to its
	// daemon (attachments, spawn). Let it finish so no daemon outlives Close.
	p.inflight.Wait()

	grace := p.DrainTimeout
	if grace <= 0 {
		grace = defaultPoolDrainTimeout
	}
	var wg sync.WaitGroup
	for _, s := range p.slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runner.drain(grace)
		}()
	}
	wg.Wait()
}

// daemonIdentityKey is the identity-gate key: one active job per PHYSICAL browser
// session. It is deliberately tenant-free (two tenants mapped to one provider
// account must serialize) and is the Go twin of physical_session_key in
// ubag_worker/live/identity_lock.py: the CDP endpoint when one is configured,
// else the requested profile directory, plus the target.
//
// The endpoint form hashes to exactly the worker's value. The profile-directory
// form is cleaned but not symlink-resolved, so an alias of the same directory can
// look like two identities here; the worker's flock then serializes them. Jobs
// that carry no profile hint share the target's default profile and therefore one
// key per target.
func daemonIdentityKey(envelope DispatchEnvelope) string {
	where := "dir:" + daemonProfileHint(envelope.Job)
	if endpoint := strings.TrimSpace(os.Getenv("UBAG_REMOTE_BROWSER_ENDPOINT")); endpoint != "" {
		where = "cdp:" + strings.ToLower(strings.TrimRight(endpoint, "/"))
	}
	sum := sha256.Sum256([]byte(where + "\n" + envelope.Job.Target))
	return hex.EncodeToString(sum[:])
}

// daemonProfileHint is the profile directory a job asks for, using the same
// fields and precedence as the worker's _resolve_user_data_dir (options, then
// context); "" means the target's default profile.
func daemonProfileHint(job DispatchJob) string {
	for _, source := range []map[string]any{job.Options, job.Context} {
		for _, field := range []string{"user_data_dir", "profile_dir", "profile_path"} {
			if value, ok := source[field].(string); ok && strings.TrimSpace(value) != "" {
				return path.Clean(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
			}
		}
	}
	return ""
}
