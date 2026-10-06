package topology

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserLaneKeyIsTheBrowserNotTheSpelling(t *testing.T) {
	same := [][]string{
		{"http://browser:9222", "http://browser:9222/", "  HTTP://Browser:9222/ ", "ws://browser:9222/devtools/browser/abc"},
		{"127.0.0.1:9222", "http://127.0.0.1:9222"},
	}
	for _, group := range same {
		want := BrowserLaneKey(group[0])
		if want == "" {
			t.Fatalf("%q has no lane", group[0])
		}
		for _, spelling := range group[1:] {
			if got := BrowserLaneKey(spelling); got != want {
				t.Errorf("BrowserLaneKey(%q) = %q, want %q (same browser)", spelling, got, want)
			}
		}
	}
	if BrowserLaneKey("http://browser:9222") == BrowserLaneKey("http://browser:9223") {
		t.Error("two ports are two browsers")
	}
	if BrowserLaneKey("http://browser-a:9222") == BrowserLaneKey("http://browser-b:9222") {
		t.Error("two hosts are two browsers")
	}
	for _, none := range []string{"", "   "} {
		if got := BrowserLaneKey(none); got != "" {
			t.Errorf("BrowserLaneKey(%q) = %q, want no lane", none, got)
		}
	}
}

func TestLaneRegistrationsAreCountedPerKindAndReleasedOnce(t *testing.T) {
	r := NewConcurrencyRegistry()
	ctx := t.Context()
	const lane = "browser:b:1"
	count := func(kind LaneKind) int {
		n, err := r.LaneHolders(ctx, kind, lane)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	j1, _ := r.EnterLane(ctx, LaneJob, lane)
	j2, _ := r.EnterLane(ctx, LaneJob, lane)
	v, _ := r.EnterLane(ctx, LaneVoice, lane)
	if count(LaneJob) != 2 || count(LaneVoice) != 1 {
		t.Fatalf("jobs=%d voice=%d, want 2 and 1", count(LaneJob), count(LaneVoice))
	}
	j1.Release()
	j1.Release() // idempotent: must not steal j2's registration
	if count(LaneJob) != 1 {
		t.Fatalf("jobs=%d after releasing j1 twice, want 1", count(LaneJob))
	}
	j2.Release()
	v.Release()
	if count(LaneJob) != 0 || count(LaneVoice) != 0 {
		t.Fatalf("lane not empty: jobs=%d voice=%d", count(LaneJob), count(LaneVoice))
	}
	if len(r.lanes) != 0 {
		t.Fatalf("empty lanes must not accumulate: %v", r.lanes)
	}
}

func TestLaneRegistryIsNilAndEmptyLaneSafe(t *testing.T) {
	var r *ConcurrencyRegistry
	hold, err := r.EnterLane(t.Context(), LaneJob, "browser:b:1")
	if hold != nil || err != nil {
		t.Fatalf("nil registry: hold=%v err=%v", hold, err)
	}
	if n, err := r.LaneHolders(t.Context(), LaneJob, "browser:b:1"); n != 0 || err != nil {
		t.Fatalf("nil registry holders=%d err=%v", n, err)
	}
	hold.Release() // nil hold
	if err := hold.Renew(t.Context()); err != nil {
		t.Fatalf("nil hold renew: %v", err)
	}

	live := NewConcurrencyRegistry()
	if hold, err := live.EnterLane(t.Context(), LaneVoice, ""); hold != nil || err != nil {
		t.Fatalf("empty lane must register nothing: hold=%v err=%v", hold, err)
	}
}

// Two registries on one database are two replicas: a job registered on one is
// seen by a voice admission on the other, and its release frees the lane.
func TestLaneRegistrationsAreSharedAcrossReplicas(t *testing.T) {
	b, _ := sqliteBackend(t)
	a, c := NewConcurrencyRegistry(), NewConcurrencyRegistry()
	a.UseLaneBackend(b)
	c.UseLaneBackend(b)
	const lane = "browser:b:1"

	job, err := a.EnterLane(t.Context(), LaneJob, lane)
	if err != nil || job == nil {
		t.Fatalf("EnterLane: hold=%v err=%v", job, err)
	}
	if n, err := c.LaneHolders(t.Context(), LaneJob, lane); err != nil || n != 1 {
		t.Fatalf("the other replica sees %d jobs err=%v, want 1", n, err)
	}
	if n, _ := c.LaneHolders(t.Context(), LaneVoice, lane); n != 0 {
		t.Fatalf("a job registration must not read as a voice one: %d", n)
	}
	if err := job.Renew(t.Context()); err != nil {
		t.Fatalf("renewing a live registration: %v", err)
	}
	job.Release()
	if n, _ := c.LaneHolders(t.Context(), LaneJob, lane); n != 0 {
		t.Fatalf("released on replica A, still %d on replica B", n)
	}
}

func TestSharedLaneRegistrationExpiresWhenItsHolderDies(t *testing.T) {
	b, _ := sqliteBackend(t)
	r := NewConcurrencyRegistry()
	r.UseLaneBackend(b)
	hold, err := r.EnterLane(t.Context(), LaneJob, "browser:b:1")
	if err != nil {
		t.Fatal(err)
	}
	key := laneTokenKey(LaneJob, "browser:b:1")
	now := time.Now().UTC()
	if n, _ := b.LaneLive(t.Context(), key, now); n != 1 {
		t.Fatalf("live = %d, want 1", n)
	}
	later := now.Add(laneTokenTTL + time.Second)
	if n, _ := b.LaneLive(t.Context(), key, later); n != 0 {
		t.Fatalf("a registration nobody renewed still counts after its TTL: %d", n)
	}
	// Swept (every replica's admission sweeper does this), the holder learns it
	// lost the lane the next time it renews.
	if n, err := b.SweepExpired(t.Context(), later); err != nil || n != 1 {
		t.Fatalf("swept %d err=%v", n, err)
	}
	if err := hold.Renew(t.Context()); !errors.Is(err, ErrTokenLost) {
		t.Fatalf("renew after expiry = %v, want ErrTokenLost", err)
	}
}

// The exclusion protocol (see lane.go): each side registers, then looks at the
// other, then commits. Whatever the interleaving, a job must never run while a
// voice session is committed, and the reverse. The "store" is a bool standing for
// the voice lease; its read by the job stands for the consumer's probe.
func TestLaneProtocolNeverOverlapsJobAndVoice(t *testing.T) {
	run := func(t *testing.T, a, c *ConcurrencyRegistry, rounds int) {
		const lane = "browser:b:1"
		var storeHeld, jobsRunning atomic.Int32
		var violations atomic.Int32
		var wg sync.WaitGroup

		jobSide := func(r *ConcurrencyRegistry) {
			hold, err := r.EnterLane(t.Context(), LaneJob, lane)
			if err != nil {
				return
			}
			defer hold.Release()
			if n, _ := r.LaneHolders(t.Context(), LaneVoice, lane); n > 0 || storeHeld.Load() > 0 {
				return // held back
			}
			jobsRunning.Add(1)
			if storeHeld.Load() > 0 {
				violations.Add(1)
			}
			time.Sleep(50 * time.Microsecond)
			jobsRunning.Add(-1)
		}
		voiceSide := func(r *ConcurrencyRegistry) {
			intent, err := r.EnterLane(t.Context(), LaneVoice, lane)
			if err != nil {
				return
			}
			if n, _ := r.LaneHolders(t.Context(), LaneJob, lane); n > 0 {
				intent.Release()
				return // queued behind the running job
			}
			storeHeld.Add(1) // Reserve committed
			if jobsRunning.Load() > 0 {
				violations.Add(1)
			}
			intent.Release()
			time.Sleep(50 * time.Microsecond)
			storeHeld.Add(-1) // the session ended
		}
		for i := 0; i < rounds; i++ {
			wg.Add(2)
			j, v := a, c
			if i%2 == 1 {
				j, v = c, a // the other replica plays the job side
			}
			go func() { defer wg.Done(); jobSide(j) }()
			go func() { defer wg.Done(); voiceSide(v) }()
		}
		wg.Wait()
		if n := violations.Load(); n != 0 {
			t.Fatalf("%d overlaps of a running job and a committed voice session", n)
		}
	}
	t.Run("process-local", func(t *testing.T) {
		r := NewConcurrencyRegistry()
		run(t, r, r, 5000)
	})
	t.Run("shared", func(t *testing.T) {
		b, _ := sqliteBackend(t)
		a, c := NewConcurrencyRegistry(), NewConcurrencyRegistry()
		a.UseLaneBackend(b)
		c.UseLaneBackend(b)
		run(t, a, c, 20)
	})
}
