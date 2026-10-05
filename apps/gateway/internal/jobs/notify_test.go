package jobs

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func recvWithin(t *testing.T, ch <-chan struct{}, d time.Duration) bool {
	t.Helper()
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

func TestEventHubNotifyWakesOnlyMatchingJob(t *testing.T) {
	h := newEventHub()
	a, cancelA := h.subscribe("job_a")
	defer cancelA()
	b, cancelB := h.subscribe("job_b")
	defer cancelB()

	h.notify("job_a")
	if !recvWithin(t, a, 10*time.Millisecond) {
		t.Fatal("job_a waiter was not woken within 10ms")
	}
	if recvWithin(t, b, 20*time.Millisecond) {
		t.Fatal("job_b waiter was woken by a job_a notify")
	}
}

func TestEventHubNotifyIsLossyAndNonBlocking(t *testing.T) {
	h := newEventHub()
	ch, cancel := h.subscribe("job_a")
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ { // nobody is draining: must never block
			h.notify("job_a")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notify blocked on a full waiter channel")
	}
	if len(ch) != 1 {
		t.Fatalf("pending wakes = %d, want exactly 1 (cap-1 channel)", len(ch))
	}
	var nilHub *eventHub
	nilHub.notify("job_a") // flag off: must be a no-op, not a panic
}

func TestEventHubThousandWaitersAcrossHundredJobs(t *testing.T) {
	h := newEventHub()
	const jobs, perJob = 100, 10
	chans := make([][]<-chan struct{}, jobs)
	for j := 0; j < jobs; j++ {
		for w := 0; w < perJob; w++ {
			ch, cancel := h.subscribe(fmt.Sprintf("job_%03d", j))
			defer cancel()
			chans[j] = append(chans[j], ch)
		}
	}
	h.notify("job_042")
	for j := 0; j < jobs; j++ {
		for w, ch := range chans[j] {
			woke := len(ch) == 1
			if want := j == 42; woke != want {
				t.Fatalf("job %d waiter %d woke=%v, want %v", j, w, woke, want)
			}
		}
	}
}

func TestEventHubUnsubscribeLeavesNothingBehind(t *testing.T) {
	h := newEventHub()
	before := runtime.NumGoroutine()
	var cancels []func()
	for i := 0; i < 200; i++ {
		_, cancel := h.subscribe(fmt.Sprintf("job_%d", i%10))
		cancels = append(cancels, cancel)
	}
	for _, cancel := range cancels {
		cancel()
		cancel() // idempotent
	}
	h.mu.Lock()
	left := len(h.waiters)
	h.mu.Unlock()
	if left != 0 {
		t.Fatalf("hub retained %d job entries after every waiter unsubscribed", left)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutines %d -> %d: hub must not spawn any", before, after)
	}
}

// A WaitEvents caller that is cancelled must unregister and end its goroutine.
func TestWaitEventsLoopCancelLeaksNothing(t *testing.T) {
	h := newEventHub()
	before := runtime.NumGoroutine()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, found, err := waitEventsLoop(ctx, h, time.Hour, time.Hour, "job_x", func() ([]Event, bool, error) { return nil, true, nil })
			if !found || !errors.Is(err, context.Canceled) {
				t.Errorf("found=%v err=%v, want (true, Canceled)", found, err)
			}
		}()
		time.AfterFunc(30*time.Millisecond, cancel)
	}
	wg.Wait()
	h.mu.Lock()
	left := len(h.waiters)
	h.mu.Unlock()
	if left != 0 {
		t.Fatalf("hub retained %d job entries after cancel", left)
	}
	time.Sleep(20 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutines %d -> %d after cancelled waiters", before, after)
	}
}

// An event committed between subscribe and the first read must not be lost:
// the wake is already queued, so the waiter re-reads at once instead of waiting
// a full fallback interval.
func TestWaitEventsLoopWakeBeforeFirstReadIsNotLost(t *testing.T) {
	h := newEventHub()
	var reads atomic.Int32
	read := func() ([]Event, bool, error) {
		if reads.Add(1) == 1 {
			h.notify("job_x") // commit lands after subscribe, before/at the first read
			return nil, true, nil
		}
		return []Event{{Sequence: 2}}, true, nil
	}
	start := time.Now()
	events, _, err := waitEventsLoop(context.Background(), h, time.Hour, time.Hour, "job_x", read)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("woke after %v, want a prompt re-read", d)
	}
}

// Idle waiters read at the fallback cadence with the hub on, versus the legacy
// interval with it off (the per-waiter query count is the DB-load proxy).
func TestWaitEventsLoopIdleReadsFollowFallbackCadence(t *testing.T) {
	count := func(h *eventHub, fallback, interval time.Duration) int32 {
		var reads atomic.Int32
		ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer cancel()
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, _ = waitEventsLoop(ctx, h, fallback, interval, "job_idle", func() ([]Event, bool, error) {
					reads.Add(1)
					return nil, true, nil
				})
			}()
		}
		wg.Wait()
		return reads.Load()
	}
	legacy := count(nil, 0, 10*time.Millisecond)
	hub := count(newEventHub(), 200*time.Millisecond, 10*time.Millisecond)
	// 20 waiters x 600ms: legacy ~20*60 reads; hub ~20*(1+600/200..240) <= ~100.
	if hub > 120 || legacy < 5*hub {
		t.Fatalf("idle reads legacy=%d hub=%d, want hub at fallback cadence (<=120) and >=5x fewer", legacy, hub)
	}
}

func TestSQLiteStoreWaitEventsContractWithWakeHub(t *testing.T) {
	store := newScheduledTestSQLiteStore(t)
	store.EnableEventNotify(2 * time.Second)
	waitEventsContract(t, store, "tenant_wait_events")
}

// With a long fallback the only way a waiter can return promptly is the commit
// notification, so this proves the write sites notify.
func TestSQLiteStoreWakeHubDeliversWithoutPolling(t *testing.T) {
	store := newScheduledTestSQLiteStore(t)
	store.EnableEventNotify(time.Hour)
	job := waitEventsTestJob(t, store, "tenant_wake")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := startWait(ctx, store, job.ID, 1, 10)
	time.Sleep(100 * time.Millisecond) // waiter is parked on the hub
	start := time.Now()
	applyWaitEventsWorkerEvent(t, store, job, "wake_evt_"+job.ID, "running", 2, map[string]any{"status": "running"})
	select {
	case r := <-result:
		if r.err != nil || len(r.events) != 1 || r.events[0].Type != "running" {
			t.Fatalf("WaitEvents = %#v", r)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("woke after %v; a 1h fallback means the notify did not arrive", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter was not woken by the commit notification")
	}

	// UpdateStatus and TransitionStatus are event-writing sites too.
	next := waitEventsTestJob(t, store, "tenant_wake")
	result = startWait(ctx, store, next.ID, 1, 10)
	time.Sleep(100 * time.Millisecond)
	if _, ok, err := store.TransitionStatus(ctx, next.ID, StatusQueued, StatusAssigned); err != nil || !ok {
		t.Fatalf("TransitionStatus ok=%v err=%v", ok, err)
	}
	select {
	case r := <-result:
		if r.err != nil || len(r.events) != 1 {
			t.Fatalf("WaitEvents after transition = %#v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TransitionStatus did not notify")
	}
}

// A write the hub never hears about (another gateway on a shared database) is
// still picked up, by the fallback poll.
func TestSQLiteStoreDroppedNotifyRecoveredByFallback(t *testing.T) {
	store := newScheduledTestSQLiteStore(t)
	store.EnableEventNotify(100 * time.Millisecond)
	silent := &SQLiteStore{db: store.db, now: store.now, waitInterval: store.waitInterval} // no hub

	job := waitEventsTestJob(t, store, "tenant_fallback")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := startWait(ctx, store, job.ID, 1, 10)
	time.Sleep(50 * time.Millisecond)
	applyWaitEventsWorkerEvent(t, silent, job, "silent_evt_"+job.ID, "running", 2, map[string]any{"status": "running"})
	select {
	case r := <-result:
		if r.err != nil || len(r.events) != 1 || r.events[0].Type != "running" {
			t.Fatalf("WaitEvents = %#v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fallback poll did not recover the un-notified event")
	}
}

func TestParseEventNotify(t *testing.T) {
	for mode, want := range map[string][2]bool{
		"": {false, true}, "off": {false, true}, "local": {true, true},
		"postgres": {false, false}, "bogus": {false, false},
	} {
		on, ok := ParseEventNotify(mode)
		if on != want[0] || ok != want[1] {
			t.Errorf("ParseEventNotify(%q) = (%v,%v), want (%v,%v)", mode, on, ok, want[0], want[1])
		}
	}
}
