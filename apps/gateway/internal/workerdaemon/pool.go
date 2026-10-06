package workerdaemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os/exec"
	"slices"
	"sync"
	"time"
)

const (
	// MaxPoolSize bounds Size no matter what is configured: every slot is a whole
	// Python + Playwright driver process. The operator-facing ceiling (a lower
	// number, until the P0.13 lab measurements exist) is enforced at startup by the
	// serve package and by the Helper Node; this is only the structural backstop.
	MaxPoolSize = 32

	defaultMaxWait      = 30 * time.Second
	defaultRetryAfter   = 2 * time.Second
	defaultDrainTimeout = 5 * time.Second
)

// ErrOverloaded is the typed, retryable saturation signal of a Pool: no slot (or,
// for an identity that is already busy, no turn on it) became available within
// the bounded wait, or the placement queue was full. The job did NOT reach a
// worker. The primary's consumer answers it with a delayed lease Retry
// (ADR-0011), never a job failure and never an immediate re-queue.
var ErrOverloaded = errors.New("worker pool overloaded")

// ErrClosed: the pool was closed; nothing may start a daemon any more.
var ErrClosed = errors.New("worker daemon pool is closed")

// OverloadError carries why a placement was refused and how long the caller
// should hold the job back before retrying. errors.Is(err, ErrOverloaded).
type OverloadError struct {
	// Reason is "queue_full" (refused at once) or "wait_expired" (waited MaxWait).
	Reason     string
	RetryAfter time.Duration
}

func (e *OverloadError) Error() string { return "worker pool overloaded: " + e.Reason }

// Unwrap lets errors.Is(err, ErrOverloaded) match.
func (e *OverloadError) Unwrap() error { return ErrOverloaded }

// IdentityKey is the tenant-free key of one physical browser session: the SHA-256
// of "<where>\n<target>". The primary builds where from the CDP endpoint or the
// profile directory; a Helper Node builds it from its opaque profile ref.
func IdentityKey(where, target string) string {
	sum := sha256.Sum256([]byte(where + "\n" + target))
	return hex.EncodeToString(sum[:])
}

// Pool runs jobs on N isolated warm-daemon slots instead of one daemon behind one
// mutex.
//
//   - Each slot is its own Process: a separate worker process speaking the
//     unchanged one-job-at-a-time stdio protocol. A failure, cancel or deadline
//     kills only that slot's process; the others keep their warm pages.
//   - Identity gate: at most ONE active job per physical browser session key.
//     Two jobs for one provider account never overlap, whatever the slot count.
//     The worker's own flock (slot mode) is the cross-process backstop for keys
//     Go cannot derive exactly.
//   - Warm affinity: a job goes to the idle slot whose page for its identity is
//     still warm; otherwise to a live cold slot, then a never-started slot, and
//     only then does it evict the least recently used warm page.
//   - Bounded wait: a job that cannot be placed waits at most MaxWait (and the
//     placement queue is bounded by MaxQueue), then fails with ErrOverloaded.
//
// Set the exported fields before the first use.
type Pool struct {
	Python string
	Script string
	// Env builds each worker's environment (nil: the local allowlist).
	Env func() []string

	// Size is the number of slots; <= 0 means 1 and it is capped at MaxPoolSize.
	Size int
	// MaxWait is the longest a job waits to be placed before ErrOverloaded.
	// <= 0 means 30s. It is deliberately NOT tied to a job's run timeout (25
	// minutes in production): a wait that long would pin a consumer worker and make
	// the "bounded" overload unreachable in practice.
	MaxWait time.Duration
	// MaxQueue bounds how many jobs may wait to be placed at once; one more is
	// refused immediately. <= 0 means Size.
	MaxQueue int
	// RetryAfter is the hold-back hint carried by every overload error. <= 0
	// means 2s.
	RetryAfter time.Duration
	// DrainTimeout bounds how long Close lets a slot exit on its own (stdin EOF)
	// before it is killed. <= 0 means 5s.
	DrainTimeout time.Duration

	// NewSlotCommand builds a slot's daemon process; tests re-exec the test binary.
	NewSlotCommand func(slot int) *exec.Cmd

	initOnce sync.Once
	done     chan struct{}
	inflight sync.WaitGroup // slots currently handed out (acquire -> release)

	mu      sync.Mutex
	slots   []*Slot
	active  map[string]*Slot // identity key -> the slot running it (the gate)
	waiters []*waiter        // FIFO; unplaced requests only
	closed  bool
}

// Slot is one pool slot: a Process plus the placement state the pool keeps for it.
type Slot struct {
	id   int
	proc *Process

	// touched records that the holder started a job on the daemon; it is only
	// touched by whoever holds the slot.
	touched bool

	// The fields below are guarded by Pool.mu.
	busy bool
	// warmKey is the identity this slot's daemon holds a warm page for, "" when it
	// is cold. A slot is pinned to at most one warm key: the worker evicts the page
	// of any other key before it runs a job.
	warmKey  string
	spawned  bool // a daemon was started and has not been discarded since
	lastUsed time.Time
}

// Run runs job on this slot's daemon with the run timeout starting now. It is
// the only way a held slot reaches its process.
func (s *Slot) Run(ctx context.Context, maxRuntime time.Duration, job JobFunc) error {
	s.touched = true
	return s.proc.Run(ctx, maxRuntime, job)
}

type waiter struct {
	key      string
	ready    chan *Slot // buffered; written once, under Pool.mu
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

func (p *Pool) init() {
	p.initOnce.Do(func() {
		n := p.size()
		p.done = make(chan struct{})
		p.active = make(map[string]*Slot, n)
		p.slots = make([]*Slot, n)
		for i := range p.slots {
			proc := &Process{
				Python:   p.Python,
				Script:   p.Script,
				Env:      p.Env,
				SlotMode: true,
				SlotID:   i,
				PoolSize: n,
			}
			if p.NewSlotCommand != nil {
				slot := i
				proc.NewCommand = func() *exec.Cmd { return p.NewSlotCommand(slot) }
			}
			p.slots[i] = &Slot{id: i, proc: proc}
		}
	})
}

func (p *Pool) size() int {
	switch {
	case p.Size <= 0:
		return 1
	case p.Size > MaxPoolSize:
		return MaxPoolSize
	}
	return p.Size
}

// Slots is the number of slots the pool runs.
func (p *Pool) Slots() int { return p.size() }

func (p *Pool) maxWait() time.Duration {
	if p.MaxWait > 0 {
		return p.MaxWait
	}
	return defaultMaxWait
}

func (p *Pool) maxQueue() int {
	if p.MaxQueue > 0 {
		return p.MaxQueue
	}
	return p.size()
}

func (p *Pool) retryAfter() time.Duration {
	if p.RetryAfter > 0 {
		return p.RetryAfter
	}
	return defaultRetryAfter
}

// SlotCommand builds the command slot i would start (tests inspect its env).
func (p *Pool) SlotCommand(i int) *exec.Cmd {
	p.init()
	return p.slots[i].proc.BuildCommand()
}

// Counts reports how many slots are busy and how many placements are waiting.
func (p *Pool) Counts() (busy, waiting int) {
	p.init()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.countsLocked()
}

// Run places key on a slot (identity gate + affinity), calls fn while the slot is
// held, and frees the slot. fn reaches the daemon only through Slot.Run, so work
// done before it (materializing attachments) does not occupy a daemon and an
// overloaded job that is retried later never pays for it.
//
// The slot's warm state afterwards: fn returned nil -> warm for key; fn failed
// after starting a job (or panicked) -> cold, its daemon was discarded; fn failed
// before touching the daemon -> unchanged.
func (p *Pool) Run(ctx context.Context, key string, fn func(*Slot) error) error {
	p.init()
	slot, err := p.acquire(ctx, key)
	if err != nil {
		return err
	}
	outcome := slotCold // a panic or an early return leaves the slot cold, never wrongly warm
	defer func() { p.release(slot, key, outcome) }()

	if err := fn(slot); err != nil {
		if !slot.touched {
			outcome = slotUntouched
		}
		return err
	}
	outcome = slotWarm
	return nil
}

// acquire reserves a slot for key, waiting (bounded) for a free slot and for the
// identity's current holder.
func (p *Pool) acquire(ctx context.Context, key string) (*Slot, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	w := &waiter{key: key, ready: make(chan *Slot, 1)}
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
		cause = ErrClosed
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

func (p *Pool) overload(reason string, busy, waiting int) error {
	slog.Warn("worker daemon pool saturated",
		"reason", reason, "slots", len(p.slots), "busy", busy, "waiting", waiting)
	return &OverloadError{Reason: reason, RetryAfter: p.retryAfter()}
}

func (p *Pool) release(slot *Slot, key string, outcome slotOutcome) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.releaseLocked(slot, key, outcome)
}

func (p *Pool) releaseLocked(slot *Slot, key string, outcome slotOutcome) {
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
func (p *Pool) dispatchLocked() {
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
		slot.touched = false
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
func (p *Pool) pickSlotLocked(key string) *Slot {
	var best *Slot
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

func (p *Pool) removeWaiterLocked(w *waiter) {
	p.waiters = slices.DeleteFunc(p.waiters, func(other *waiter) bool { return other == w })
}

func (p *Pool) countsLocked() (busy, waiting int) {
	for _, s := range p.slots {
		if s.busy {
			busy++
		}
	}
	return busy, len(p.waiters)
}

// Close drains the pool (shutdown): new and waiting jobs are refused, and every
// slot finishes its active job and is then asked to exit by closing its stdin
// (the worker closes its warm pages itself), with a kill after DrainTimeout.
func (p *Pool) Close() {
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
		grace = defaultDrainTimeout
	}
	var wg sync.WaitGroup
	for _, s := range p.slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.proc.Drain(grace)
		}()
	}
	wg.Wait()
}
