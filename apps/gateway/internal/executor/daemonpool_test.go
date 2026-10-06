package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

// The pool tests re-exec this test binary as the fake daemon (TestHelperDaemon in
// daemonrunner_test.go). A job's job.input.sleep_ms makes it take that long and
// the fake reports its process id, slot and work window, which is how the tests
// tell overlapping jobs from serialized ones and which daemon ran what.

type jobWindow struct {
	pid        float64
	slot       string
	start, end float64
}

func windowOf(t *testing.T, events []jobstore.WorkerEvent) jobWindow {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("job returned no events")
	}
	data := events[0].Data
	pid, _ := data["pid"].(float64)
	slot, _ := data["slot"].(string)
	start, _ := data["start_us"].(float64)
	end, _ := data["end_us"].(float64)
	if pid == 0 || start == 0 || end < start {
		t.Fatalf("event carries no work window: %+v", data)
	}
	return jobWindow{pid: pid, slot: slot, start: start, end: end}
}

func overlaps(a, b jobWindow) bool { return a.start < b.end && b.start < a.end }

func assertNoOverlap(t *testing.T, windows []jobWindow) {
	t.Helper()
	sorted := slices.Clone(windows)
	slices.SortFunc(sorted, func(a, b jobWindow) int { return int(a.start - b.start) })
	for i := 1; i < len(sorted); i++ {
		if overlaps(sorted[i-1], sorted[i]) {
			t.Fatalf("jobs overlapped: %+v and %+v", sorted[i-1], sorted[i])
		}
	}
}

type helperPoolState struct {
	mu   sync.Mutex
	cmds []*exec.Cmd // every daemon the pool started, in start order
}

func (s *helperPoolState) spawned() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cmds)
}

// newHelperPool builds a pool whose slots are fake daemons. tune adjusts it
// before first use.
func newHelperPool(t *testing.T, size int, tune func(*DaemonPool)) (*DaemonPool, *helperPoolState) {
	t.Helper()
	t.Setenv("UBAG_REMOTE_BROWSER_ENDPOINT", "") // the identity key is derived from the target/profile
	state := &helperPoolState{}
	pool := &DaemonPool{
		Size:         size,
		MaxRuntime:   time.Minute,
		MaxWait:      10 * time.Second,
		MaxQueue:     16,
		DrainTimeout: 5 * time.Second,
	}
	pool.newSlotCommand = func(slot int) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperDaemon")
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_DAEMON=1", "GO_HELPER_SLOT="+strconv.Itoa(slot))
		state.mu.Lock()
		state.cmds = append(state.cmds, cmd)
		state.mu.Unlock()
		return cmd
	}
	if tune != nil {
		tune(pool)
	}
	t.Cleanup(pool.Close)
	return pool, state
}

func (p *DaemonPool) testCounts() (busy, waiting int) {
	p.init()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.countsLocked()
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// poolEnv is a job for an identity (the target) that takes sleepMS to run.
func poolEnv(id, target string, sleepMS int) DispatchEnvelope {
	env := daemonTestEnvelope()
	env.JobID = id
	env.Job.Target = target
	if sleepMS > 0 {
		env.Job.Input = map[string]any{"sleep_ms": sleepMS}
	}
	return env
}

type poolResult struct {
	events []jobstore.WorkerEvent
	err    error
}

func runAsync(pool *DaemonPool, ctx context.Context, env DispatchEnvelope) <-chan poolResult {
	out := make(chan poolResult, 1)
	go func() {
		events, err := pool.RunWorker(ctx, env)
		out <- poolResult{events: events, err: err}
	}()
	return out
}

func mustWindow(t *testing.T, ch <-chan poolResult) jobWindow {
	t.Helper()
	r := <-ch
	if r.err != nil {
		t.Fatalf("job failed: %v", r.err)
	}
	return windowOf(t, r.events)
}

var _ StreamingWorkerRunner = (*DaemonPool)(nil)
var _ WorkerRunner = (*DaemonPool)(nil)

// UBAG_WORKER_POOL_SIZE=1 must stay exactly today's behaviour: one daemon, one
// job at a time, whatever the identities.
func TestDaemonPoolOfOneSerializesConcurrentJobs(t *testing.T) {
	pool, state := newHelperPool(t, 1, func(p *DaemonPool) { p.MaxQueue = 8 })

	var results []<-chan poolResult
	for i, target := range []string{"chatgpt_web", "gemini_web", "deepseek_web", "mistral_lechat"} {
		results = append(results, runAsync(pool, context.Background(), poolEnv(fmt.Sprintf("job_pool_%d", i), target, 60)))
	}
	var windows []jobWindow
	for _, ch := range results {
		windows = append(windows, mustWindow(t, ch))
	}
	assertNoOverlap(t, windows)
	if got := state.spawned(); got != 1 {
		t.Fatalf("spawned %d daemons, want 1", got)
	}
}

// The point of the pool: two different identities (two provider accounts) run at
// the same time, each on its own isolated daemon process.
func TestDaemonPoolOverlapsJobsOfDistinctIdentities(t *testing.T) {
	pool, state := newHelperPool(t, 2, nil)

	a := runAsync(pool, context.Background(), poolEnv("job_pool_a", "chatgpt_web", 800))
	b := runAsync(pool, context.Background(), poolEnv("job_pool_b", "gemini_web", 800))
	wa, wb := mustWindow(t, a), mustWindow(t, b)

	if !overlaps(wa, wb) {
		t.Fatalf("distinct identities should have run concurrently: %+v vs %+v", wa, wb)
	}
	if wa.pid == wb.pid || wa.slot == wb.slot {
		t.Fatalf("each job must run in its own slot process: %+v vs %+v", wa, wb)
	}
	if got := state.spawned(); got != 2 {
		t.Fatalf("spawned %d daemons, want 2", got)
	}
}

// One provider account, one job at a time -- however many slots are free.
func TestDaemonPoolSerializesJobsOfOneIdentity(t *testing.T) {
	pool, state := newHelperPool(t, 2, nil)

	a := runAsync(pool, context.Background(), poolEnv("job_pool_a", "chatgpt_web", 300))
	b := runAsync(pool, context.Background(), poolEnv("job_pool_b", "chatgpt_web", 300))
	wa, wb := mustWindow(t, a), mustWindow(t, b)

	assertNoOverlap(t, []jobWindow{wa, wb})
	if wa.pid != wb.pid {
		t.Fatalf("the waiting job should reuse the identity's warm slot, got pids %v and %v", wa.pid, wb.pid)
	}
	if got := state.spawned(); got != 1 {
		t.Fatalf("spawned %d daemons, want 1 (the second slot was never needed)", got)
	}
}

// The identity gate is per physical session: two tenants mapped to one profile
// directory serialize, two profile directories do not.
func TestDaemonPoolGatesOnThePhysicalSessionNotTheTenant(t *testing.T) {
	pool, _ := newHelperPool(t, 2, nil)
	job := func(id, tenant, dir string) DispatchEnvelope {
		env := poolEnv(id, "chatgpt_web", 300)
		env.TenantID = tenant
		env.Job.Options = map[string]any{"user_data_dir": dir}
		return env
	}

	same1 := runAsync(pool, context.Background(), job("job_pool_1", "tenant_a", "profiles/acct1"))
	same2 := runAsync(pool, context.Background(), job("job_pool_2", "tenant_b", "profiles/./acct1"))
	assertNoOverlap(t, []jobWindow{mustWindow(t, same1), mustWindow(t, same2)})

	one := runAsync(pool, context.Background(), job("job_pool_3", "tenant_a", "profiles/acct1"))
	two := runAsync(pool, context.Background(), job("job_pool_4", "tenant_a", "profiles/acct2"))
	if w1, w2 := mustWindow(t, one), mustWindow(t, two); !overlaps(w1, w2) {
		t.Fatalf("two profile directories are two identities and should overlap: %+v vs %+v", w1, w2)
	}
}

// A job waiting for a busy identity must not hold up a job of a free identity
// that arrived after it.
func TestDaemonPoolBusyIdentityDoesNotBlockAFreeIdentity(t *testing.T) {
	pool, _ := newHelperPool(t, 2, nil)

	first := runAsync(pool, context.Background(), poolEnv("job_pool_a1", "chatgpt_web", 900))
	waitUntil(t, "first job to hold a slot", func() bool { busy, _ := pool.testCounts(); return busy == 1 })
	second := runAsync(pool, context.Background(), poolEnv("job_pool_a2", "chatgpt_web", 50))
	waitUntil(t, "second job to wait on the busy identity", func() bool { _, waiting := pool.testCounts(); return waiting == 1 })
	other := runAsync(pool, context.Background(), poolEnv("job_pool_b", "gemini_web", 50))

	wb := mustWindow(t, other)
	wa1 := mustWindow(t, first)
	if !(wb.start < wa1.end) {
		t.Fatalf("the free identity waited for the busy one: %+v vs %+v", wb, wa1)
	}
	wa2 := mustWindow(t, second)
	assertNoOverlap(t, []jobWindow{wa1, wa2})
}

// A page stays warm in its slot: the same identity goes back to it. When every
// slot is warm for someone else, the least recently used page is the one evicted.
func TestDaemonPoolRoutesToTheWarmSlotAndEvictsTheLeastRecentlyUsed(t *testing.T) {
	pool, state := newHelperPool(t, 2, nil)
	run := func(id, target string) float64 {
		t.Helper()
		events, err := pool.RunWorker(context.Background(), poolEnv(id, target, 0))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		return windowOf(t, events).pid
	}

	x1 := run("job_pool_x1", "chatgpt_web")
	y1 := run("job_pool_y1", "gemini_web")
	if x1 == y1 {
		t.Fatal("a second identity must start a second slot rather than evict the first warm page")
	}
	if x2 := run("job_pool_x2", "chatgpt_web"); x2 != x1 {
		t.Fatalf("identity X went to pid %v, want its warm slot %v", x2, x1)
	}
	// X is now the most recently used, so a third identity takes over Y's slot.
	if z := run("job_pool_z", "deepseek_web"); z != y1 {
		t.Fatalf("identity Z went to pid %v, want the least recently used slot %v", z, y1)
	}
	if got := state.spawned(); got != 2 {
		t.Fatalf("spawned %d daemons, want 2", got)
	}
}

// A cancelled (or failed, or timed-out) job kills only its own slot's process.
func TestDaemonPoolCancelKillsOnlyItsSlot(t *testing.T) {
	pool, state := newHelperPool(t, 2, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hung := runAsync(pool, ctx, poolEnv("job_daemon_hang", "chatgpt_web", 0))
	waitUntil(t, "hung job to hold a slot", func() bool { busy, _ := pool.testCounts(); return busy == 1 })
	other := runAsync(pool, context.Background(), poolEnv("job_pool_y", "gemini_web", 900))
	waitUntil(t, "second job to hold a slot", func() bool { busy, _ := pool.testCounts(); return busy == 2 })

	cancel()
	if r := <-hung; !errors.Is(r.err, context.Canceled) {
		t.Fatalf("cancelled job error = %v, want context canceled", r.err)
	}
	wy := mustWindow(t, other) // would fail with "ended without a terminal marker" had its daemon been killed

	again, err := pool.RunWorker(context.Background(), poolEnv("job_pool_y2", "gemini_web", 0))
	if err != nil {
		t.Fatalf("job on the untouched slot: %v", err)
	}
	if got := windowOf(t, again).pid; got != wy.pid {
		t.Fatalf("the other slot's daemon was replaced (pid %v -> %v); its warm page was lost", wy.pid, got)
	}
	if got := state.spawned(); got != 2 {
		t.Fatalf("spawned %d daemons before the cancelled slot was reused, want 2", got)
	}
	if _, err := pool.RunWorker(context.Background(), poolEnv("job_pool_x2", "chatgpt_web", 0)); err != nil {
		t.Fatalf("the cancelled slot must restart on its next job: %v", err)
	}
	if got := state.spawned(); got != 3 {
		t.Fatalf("spawned %d daemons, want 3 (one replacement for the cancelled slot)", got)
	}
}

// Saturation is a bounded, typed, retryable overload -- not an unbounded wait and
// not a job failure.
func TestDaemonPoolSaturationReturnsABoundedOverload(t *testing.T) {
	assertOverload := func(t *testing.T, err error, reason string, retryAfter time.Duration) {
		t.Helper()
		if !errors.Is(err, ErrPoolOverloaded) {
			t.Fatalf("error = %v, want ErrPoolOverloaded", err)
		}
		var overload *PoolOverloadError
		if !errors.As(err, &overload) || overload.Reason != reason || overload.RetryAfter != retryAfter {
			t.Fatalf("overload = %+v, want reason %q retry-after %v", overload, reason, retryAfter)
		}
	}
	tune := func(p *DaemonPool) { p.MaxWait = 150 * time.Millisecond; p.RetryAfter = 3 * time.Second }

	t.Run("every slot busy", func(t *testing.T) {
		pool, _ := newHelperPool(t, 2, tune)
		a := runAsync(pool, context.Background(), poolEnv("job_pool_a", "chatgpt_web", 1200))
		b := runAsync(pool, context.Background(), poolEnv("job_pool_b", "gemini_web", 1200))
		waitUntil(t, "both slots busy", func() bool { busy, _ := pool.testCounts(); return busy == 2 })

		begin := time.Now()
		_, err := pool.RunWorker(context.Background(), poolEnv("job_pool_c", "deepseek_web", 0))
		assertOverload(t, err, "wait_expired", 3*time.Second)
		if waited := time.Since(begin); waited < 140*time.Millisecond || waited > time.Second {
			t.Fatalf("overload after %v, want about MaxWait (150ms)", waited)
		}
		mustWindow(t, a) // the refusal did not disturb the running jobs
		mustWindow(t, b)
	})

	t.Run("busy identity with a free slot", func(t *testing.T) {
		pool, _ := newHelperPool(t, 2, tune)
		a := runAsync(pool, context.Background(), poolEnv("job_pool_a", "chatgpt_web", 1200))
		waitUntil(t, "a slot busy", func() bool { busy, _ := pool.testCounts(); return busy == 1 })

		_, err := pool.RunWorker(context.Background(), poolEnv("job_pool_a2", "chatgpt_web", 0))
		assertOverload(t, err, "wait_expired", 3*time.Second)
		mustWindow(t, a)
	})

	t.Run("queue full is refused at once", func(t *testing.T) {
		pool, _ := newHelperPool(t, 1, func(p *DaemonPool) { p.MaxQueue = 1; p.RetryAfter = time.Second })
		a := runAsync(pool, context.Background(), poolEnv("job_pool_a", "chatgpt_web", 900))
		waitUntil(t, "slot busy", func() bool { busy, _ := pool.testCounts(); return busy == 1 })
		b := runAsync(pool, context.Background(), poolEnv("job_pool_b", "gemini_web", 0))
		waitUntil(t, "one job waiting", func() bool { _, waiting := pool.testCounts(); return waiting == 1 })

		begin := time.Now()
		_, err := pool.RunWorker(context.Background(), poolEnv("job_pool_c", "deepseek_web", 0))
		assertOverload(t, err, "queue_full", time.Second)
		if time.Since(begin) > 400*time.Millisecond {
			t.Fatalf("a full queue must refuse immediately, took %v", time.Since(begin))
		}
		mustWindow(t, a)
		mustWindow(t, b) // the queued job still gets its turn
	})
}

// With the strict-submit boundary on, a placement refusal never reached a daemon:
// it reads as not-submitted, and the overload stays matchable so the consumer
// still retries the lease after a delay instead of failing the job.
func TestDaemonPoolOverloadStaysRetryableUnderStrictSubmit(t *testing.T) {
	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "1")
	pool, _ := newHelperPool(t, 1, func(p *DaemonPool) { p.MaxWait = 100 * time.Millisecond })
	a := runAsync(pool, context.Background(), poolEnv("job_pool_a", "chatgpt_web", 600))
	waitUntil(t, "slot busy", func() bool { busy, _ := pool.testCounts(); return busy == 1 })

	_, err := pool.RunWorker(context.Background(), poolEnv("job_pool_b", "chatgpt_web", 0))
	if !errors.Is(err, ErrPoolOverloaded) || !errors.Is(err, ErrNotSubmitted) || errors.Is(err, ErrAmbiguous) {
		t.Fatalf("error = %v, want overloaded + not-submitted", err)
	}
	mustWindow(t, a)
}

// A cancelled waiter leaves the queue; it never occupies a slot or strands one.
func TestDaemonPoolCancelledWaiterLeavesTheQueue(t *testing.T) {
	pool, _ := newHelperPool(t, 1, nil)
	a := runAsync(pool, context.Background(), poolEnv("job_pool_a", "chatgpt_web", 400))
	waitUntil(t, "slot busy", func() bool { busy, _ := pool.testCounts(); return busy == 1 })

	ctx, cancel := context.WithCancel(context.Background())
	waiter := runAsync(pool, ctx, poolEnv("job_pool_b", "gemini_web", 0))
	waitUntil(t, "one job waiting", func() bool { _, waiting := pool.testCounts(); return waiting == 1 })
	cancel()
	if r := <-waiter; !errors.Is(r.err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v, want context canceled", r.err)
	}
	if _, waiting := pool.testCounts(); waiting != 0 {
		t.Fatalf("%d waiters left in the queue", waiting)
	}
	mustWindow(t, a)
	if _, err := pool.RunWorker(context.Background(), poolEnv("job_pool_c", "gemini_web", 0)); err != nil {
		t.Fatalf("the slot must be free again: %v", err)
	}
}

// The run timeout belongs to the run, not to the wait for a slot.
func TestDaemonPoolRunTimeoutStartsAfterTheSlotIsAcquired(t *testing.T) {
	pool, _ := newHelperPool(t, 1, nil)
	// Spawn the daemon under the generous default so process start-up does not
	// eat the tight budget below.
	if _, err := pool.RunWorker(context.Background(), poolEnv("job_pool_warm", "chatgpt_web", 0)); err != nil {
		t.Fatalf("warm-up job: %v", err)
	}
	pool.MaxRuntime = 700 * time.Millisecond

	first := runAsync(pool, context.Background(), poolEnv("job_pool_a", "chatgpt_web", 500))
	waitUntil(t, "slot busy", func() bool { busy, _ := pool.testCounts(); return busy == 1 })
	// Waits ~400ms for the identity, then runs 400ms: 800ms in all is past the
	// 700ms MaxRuntime, but the run itself is inside it.
	second := runAsync(pool, context.Background(), poolEnv("job_pool_b", "chatgpt_web", 400))
	mustWindow(t, first)
	mustWindow(t, second)
}

// Shutdown is a drain: the worker is asked to exit by stdin EOF (so it closes its
// own warm pages) rather than being killed, and nothing starts after Close.
func TestDaemonPoolCloseDrainsSlotsGracefully(t *testing.T) {
	pool, state := newHelperPool(t, 2, nil)
	a := runAsync(pool, context.Background(), poolEnv("job_pool_a", "chatgpt_web", 100))
	b := runAsync(pool, context.Background(), poolEnv("job_pool_b", "gemini_web", 100))
	mustWindow(t, a)
	mustWindow(t, b)

	pool.Close()
	pool.Close() // idempotent

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.cmds) != 2 {
		t.Fatalf("started %d daemons, want 2", len(state.cmds))
	}
	for i, cmd := range state.cmds {
		if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 0 {
			t.Fatalf("daemon %d was killed instead of drained: %v", i, cmd.ProcessState)
		}
	}
	if _, err := pool.RunWorker(context.Background(), poolEnv("job_pool_c", "chatgpt_web", 0)); !errors.Is(err, errDaemonPoolClosed) {
		t.Fatalf("job after Close error = %v, want the pool-closed error", err)
	}
}

func TestDaemonPoolCloseWakesWaitingJobsAndLetsActiveOnesFinish(t *testing.T) {
	pool, _ := newHelperPool(t, 1, nil)
	active := runAsync(pool, context.Background(), poolEnv("job_pool_a", "chatgpt_web", 400))
	waitUntil(t, "slot busy", func() bool { busy, _ := pool.testCounts(); return busy == 1 })
	waiter := runAsync(pool, context.Background(), poolEnv("job_pool_b", "gemini_web", 0))
	waitUntil(t, "one job waiting", func() bool { _, waiting := pool.testCounts(); return waiting == 1 })

	closed := make(chan struct{})
	go func() { pool.Close(); close(closed) }()

	if r := <-waiter; !errors.Is(r.err, errDaemonPoolClosed) {
		t.Fatalf("waiting job error = %v, want the pool-closed error", r.err)
	}
	mustWindow(t, active) // Close waited for the running job instead of killing it
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return")
	}
}

// Each slot is told which slot it is and how many there are, so the worker can
// scope its page registry, take the identity lock and reap orphaned registries.
func TestDaemonPoolSlotsAreSpawnedWithTheirOwnSlotEnv(t *testing.T) {
	lastEnv := func(env []string, key string) (string, bool) {
		value, found := "", false
		for _, item := range env {
			if k, v, ok := strings.Cut(item, "="); ok && k == key {
				value, found = v, true // duplicates: the last one wins, as in os/exec
			}
		}
		return value, found
	}
	t.Setenv("UBAG_WORKER_SLOT_ID", "9") // a stray gateway value must not renumber a slot
	t.Setenv("UBAG_WORKER_POOL_SIZE", "7")

	pool := &DaemonPool{Size: 3, Python: os.Args[0], Script: "run_worker_daemon.py"}
	pool.init()
	if len(pool.slots) != 3 {
		t.Fatalf("pool has %d slots, want 3", len(pool.slots))
	}
	for i, slot := range pool.slots {
		env := slot.runner.buildCommand().Env
		if got, _ := lastEnv(env, "UBAG_WORKER_SLOT_ID"); got != strconv.Itoa(i) {
			t.Fatalf("slot %d UBAG_WORKER_SLOT_ID = %q", i, got)
		}
		if got, _ := lastEnv(env, "UBAG_WORKER_POOL_SIZE"); got != "3" {
			t.Fatalf("slot %d UBAG_WORKER_POOL_SIZE = %q, want 3", i, got)
		}
	}

	// The single legacy daemon never gets pool env: only the pool sets it.
	legacy := (&DaemonWorkerRunner{Python: os.Args[0], Script: "run_worker_daemon.py"}).buildCommand().Env
	if got, found := lastEnv(legacy, "UBAG_WORKER_POOL_SIZE"); found {
		t.Fatalf("legacy daemon got UBAG_WORKER_POOL_SIZE=%q", got)
	}
}

// The unset knobs are bounded too: a pool must never wait or queue without limit.
func TestDaemonPoolDefaultsAreBounded(t *testing.T) {
	p := &DaemonPool{Size: 3, MaxRuntime: 25 * time.Minute}
	if got := p.maxWait(); got != 30*time.Second {
		t.Fatalf("default wait = %v, want 30s (not the 25m MaxRuntime)", got)
	}
	if got := p.maxQueue(); got != 3 {
		t.Fatalf("default queue bound = %d, want the pool size", got)
	}
	if got := p.retryAfter(); got != 2*time.Second {
		t.Fatalf("default retry-after = %v, want 2s", got)
	}
}

func TestDaemonPoolSizeIsBounded(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 1}, {-3, 1}, {1, 1}, {4, 4}, {32, 32}, {500, 32}} {
		if got := (&DaemonPool{Size: tc.in}).size(); got != tc.want {
			t.Fatalf("Size=%d -> %d slots, want %d", tc.in, got, tc.want)
		}
	}
}

// The gate key is the Go twin of physical_session_key in identity_lock.py. The two
// endpoint vectors below were produced by that Python function.
func TestDaemonPoolIdentityKeyIsPhysicalAndTenantFree(t *testing.T) {
	t.Setenv("UBAG_REMOTE_BROWSER_ENDPOINT", "")
	envFor := func(tenant, target string, options, jobContext map[string]any) DispatchEnvelope {
		return DispatchEnvelope{
			TenantID: tenant,
			Job:      DispatchJob{Target: target, Options: options, Context: jobContext},
		}
	}
	key := func(e DispatchEnvelope) string { return daemonIdentityKey(e) }

	if key(envFor("tenant_a", "chatgpt_web", nil, nil)) != key(envFor("tenant_b", "chatgpt_web", nil, nil)) {
		t.Fatal("two tenants on one default profile are one physical session")
	}
	if key(envFor("t", "chatgpt_web", nil, nil)) == key(envFor("t", "gemini_web", nil, nil)) {
		t.Fatal("two targets are two physical sessions")
	}
	dir1 := map[string]any{"user_data_dir": "profiles/acct1"}
	if key(envFor("t", "chatgpt_web", dir1, nil)) != key(envFor("t", "chatgpt_web", map[string]any{"profile_dir": " profiles\\acct1\\. "}, nil)) {
		t.Fatal("the same directory spelled differently must be one session")
	}
	if key(envFor("t", "chatgpt_web", dir1, nil)) == key(envFor("t", "chatgpt_web", map[string]any{"user_data_dir": "profiles/acct2"}, nil)) {
		t.Fatal("two profile directories are two sessions")
	}
	if key(envFor("t", "chatgpt_web", nil, dir1)) != key(envFor("t", "chatgpt_web", dir1, nil)) {
		t.Fatal("context supplies the profile when options do not")
	}
	if key(envFor("t", "chatgpt_web", dir1, map[string]any{"user_data_dir": "elsewhere"})) != key(envFor("t", "chatgpt_web", dir1, nil)) {
		t.Fatal("options take precedence over context")
	}

	// With a CDP endpoint the browser IS the session: profile hints stop
	// mattering and the hash is exactly the worker's.
	t.Setenv("UBAG_REMOTE_BROWSER_ENDPOINT", "http://127.0.0.1:9222/")
	const pythonVector = "39179df0698eb6b445864dffb0e386b103db2996e09e1abe3762100fd02a92ba"
	if got := key(envFor("t", "chatgpt_web", dir1, nil)); got != pythonVector {
		t.Fatalf("endpoint key = %s, want the Python physical_session_key %s", got, pythonVector)
	}
	t.Setenv("UBAG_REMOTE_BROWSER_ENDPOINT", "  HTTP://Browser:9222 ")
	const pythonVector2 = "a69bd00635aac7a68117cbb607a33b80891530cd974c480da27b9d4f55c0c5eb"
	if got := key(envFor("t", "gemini_web", nil, nil)); got != pythonVector2 {
		t.Fatalf("endpoint key = %s, want the Python physical_session_key %s", got, pythonVector2)
	}
}

// --- consumer: lease-then-place (ADR-0011) -----------------------------------

// overloadProbeLease records when Retry was called and whether the job's
// execution lease had already been released by then.
type overloadProbeLease struct {
	fakeWorkerLease
	backend         *topology.SQLTokenBackend
	retriedAt       time.Time
	execFreeAtRetry bool
}

func (l *overloadProbeLease) Retry(ctx context.Context) error {
	l.retriedAt = time.Now()
	token, held, err := l.backend.AcquireToken(ctx,
		[]topology.Lane{{Key: "exec:" + l.jobID, Cap: 1}}, time.Minute, time.Now().UTC())
	if err == nil && held {
		l.execFreeAtRetry = true
		_ = l.backend.ReleaseToken(ctx, token)
	}
	return l.fakeWorkerLease.Retry(ctx)
}

// A placement refused after the lease is a nack with a delay: the job is not
// failed, not completed, and not handed straight back.
func TestDaemonPoolConsumerRetriesAnOverloadedPlacementAfterADelay(t *testing.T) {
	backend := execLeaseBackend(t)
	store := jobstore.NewMemoryStore()
	job, envelope := queuedJob(t, store, "overload_delay_key_01")
	lease := &overloadProbeLease{
		fakeWorkerLease: fakeWorkerLease{jobID: job.ID, leaseID: "lease_overload", envelope: envelope},
		backend:         backend,
	}
	consumer := &WorkerConsumer{
		Queue:      singleLeaseQueue{lease: lease},
		Jobs:       store,
		ExecLeases: backend,
		Runner: WorkerRunFunc(func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return nil, &PoolOverloadError{Reason: "wait_expired", RetryAfter: 150 * time.Millisecond}
		}),
	}

	begin := time.Now()
	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	if !lease.retried || lease.completed || lease.failed || lease.cancelled || lease.poisoned {
		t.Fatalf("an overloaded placement must only be retried: %+v", lease.fakeWorkerLease)
	}
	if waited := lease.retriedAt.Sub(begin); waited < 140*time.Millisecond {
		t.Fatalf("retried after %v: the retry must be delayed, not immediate", waited)
	}
	if !lease.execFreeAtRetry {
		t.Fatal("the execution lease must be released before the lease goes back to the queue")
	}
	final, found, err := store.Get(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("Get found=%v err=%v", found, err)
	}
	if jobstore.TerminalStatus(final.Status) {
		t.Fatalf("the job never ran, so it must not be terminal: %s", final.Status)
	}
}

// A saturated pool must not become a lease -> overload -> Retry -> lease spin on
// the file spool, whose Retry re-queues instantly.
func TestDaemonPoolConsumerDoesNotSpinOnASaturatedPool(t *testing.T) {
	store := jobstore.NewMemoryStore()
	spool := NewFileSpoolDispatcher(t.TempDir())
	job, _ := queuedJob(t, store, "overload_spin_key_001")
	if _, err := spool.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	var attempts atomic.Int32
	consumer := WorkerConsumer{
		Spool: spool,
		Jobs:  store,
		Runner: WorkerRunFunc(func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			attempts.Add(1)
			return nil, &PoolOverloadError{Reason: "wait_expired", RetryAfter: 200 * time.Millisecond}
		}),
		PollInterval: 5 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = consumer.Run(ctx)

	if n := attempts.Load(); n < 2 || n > 6 {
		t.Fatalf("%d placement attempts in 1s with a 200ms retry delay, want 2..6 (a busy loop would be hundreds)", n)
	}
	final, found, err := store.Get(context.Background(), job.ID)
	if err != nil || !found || jobstore.TerminalStatus(final.Status) {
		t.Fatalf("the job must still be waiting for a slot: found=%v err=%v job=%+v", found, err, final)
	}
}

// singleLeaseQueue hands out one pre-built lease of any type.
type singleLeaseQueue struct{ lease WorkerLease }

func (singleLeaseQueue) Ready(context.Context) error { return nil }

func (q singleLeaseQueue) LeaseNext(context.Context) (WorkerLease, bool, error) {
	return q.lease, true, nil
}
