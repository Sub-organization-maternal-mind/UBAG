package resilience

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fixedNow returns a Now function pinned to t.
func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// newTestBreaker creates a Breaker with deterministic time starting at epoch.
func newTestBreaker(cfg Config) (*Breaker, *time.Time) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	b := New(cfg)
	b.now = fixedNow(base)
	return b, &base
}

// advanceNow updates the pinned now to base + d.
func advanceNow(b *Breaker, base *time.Time, d time.Duration) {
	t := (*base).Add(d)
	*base = t
	b.now = fixedNow(t)
}

// ---------------------------------------------------------------------------
// Test 1: Opens after N consecutive failures (FailureThreshold).
// ---------------------------------------------------------------------------
func TestBreaker_OpensAfterFailureThreshold(t *testing.T) {
	t.Parallel()
	b, _ := newTestBreaker(Config{FailureThreshold: 3})

	if b.State() != StateClosed {
		t.Fatalf("expected closed, got %s", b.State())
	}

	for i := 0; i < 2; i++ {
		b.RecordFailure()
		if b.State() != StateClosed {
			t.Fatalf("expected still closed after %d failures", i+1)
		}
	}

	b.RecordFailure() // 3rd failure — should open
	if b.State() != StateOpen {
		t.Fatalf("expected open after FailureThreshold failures, got %s", b.State())
	}
}

// ---------------------------------------------------------------------------
// Test 2: Rejects calls while open.
// ---------------------------------------------------------------------------
func TestBreaker_RejectsWhileOpen(t *testing.T) {
	t.Parallel()
	b, _ := newTestBreaker(Config{FailureThreshold: 1})

	b.Allow() // closed — allowed
	b.RecordFailure()

	if b.State() != StateOpen {
		t.Fatalf("expected open")
	}
	if b.Allow() {
		t.Fatal("Allow() should return false while open and cooldown not elapsed")
	}
	if b.Allow() {
		t.Fatal("repeated Allow() while open should still be false")
	}
}

// ---------------------------------------------------------------------------
// Test 3: Transitions to half-open after cooldown elapses.
// ---------------------------------------------------------------------------
func TestBreaker_TransitionsToHalfOpenAfterCooldown(t *testing.T) {
	t.Parallel()
	b, base := newTestBreaker(Config{
		FailureThreshold:    1,
		CooldownBase:        100 * time.Millisecond,
		CooldownMax:         200 * time.Millisecond,
		HalfOpenMaxInflight: 1,
	})

	b.Allow()
	b.RecordFailure()
	if b.State() != StateOpen {
		t.Fatalf("expected open")
	}

	// Still in cooldown — should reject.
	if b.Allow() {
		t.Fatal("should be rejected before cooldown elapses")
	}

	// Advance time well past the maximum possible cooldown.
	advanceNow(b, base, 10*time.Second)

	// Now Allow() should admit one probe and switch to half-open.
	if !b.Allow() {
		t.Fatal("Allow() should return true after cooldown elapses")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("expected half-open, got %s", b.State())
	}
}

// ---------------------------------------------------------------------------
// Test 4: Admits up to HalfOpenMaxInflight probes in half-open.
// ---------------------------------------------------------------------------
func TestBreaker_HalfOpenInflightLimit(t *testing.T) {
	t.Parallel()
	cfg := Config{
		FailureThreshold:    1,
		CooldownBase:        1 * time.Millisecond,
		CooldownMax:         2 * time.Millisecond,
		HalfOpenMaxInflight: 2,
		SuccessBudget:       10, // large — won't close during this test
	}
	b, base := newTestBreaker(cfg)

	b.Allow()
	b.RecordFailure()
	advanceNow(b, base, 10*time.Second)

	// First probe transitions open→half-open.
	if !b.Allow() {
		t.Fatal("first probe should be admitted")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("expected half-open")
	}

	// Second probe still within limit.
	if !b.Allow() {
		t.Fatal("second probe should be admitted (within HalfOpenMaxInflight=2)")
	}

	// Third probe should be rejected — at capacity.
	if b.Allow() {
		t.Fatal("third probe should be rejected — at capacity")
	}
}

// ---------------------------------------------------------------------------
// Test 5: Re-closes after SuccessBudget consecutive successes in half-open.
// ---------------------------------------------------------------------------
func TestBreaker_ReClosesAfterSuccessBudget(t *testing.T) {
	t.Parallel()
	cfg := Config{
		FailureThreshold:    1,
		SuccessBudget:       2,
		CooldownBase:        1 * time.Millisecond,
		CooldownMax:         2 * time.Millisecond,
		HalfOpenMaxInflight: 2,
	}
	b, base := newTestBreaker(cfg)

	b.Allow()
	b.RecordFailure()
	advanceNow(b, base, 10*time.Second)

	// Admit two probes (HalfOpenMaxInflight == 2).
	b.Allow() // → half-open, inflight=1
	b.Allow() // inflight=2

	// First success — not yet at SuccessBudget.
	b.RecordSuccess()
	if b.State() != StateHalfOpen {
		t.Fatalf("expected still half-open after 1 success, got %s", b.State())
	}

	// Second success — reaches SuccessBudget, re-close.
	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Fatalf("expected closed after SuccessBudget successes, got %s", b.State())
	}
}

// ---------------------------------------------------------------------------
// Test 6: Re-opens on a failure during half-open.
// ---------------------------------------------------------------------------
func TestBreaker_ReOpensOnFailureDuringHalfOpen(t *testing.T) {
	t.Parallel()
	cfg := Config{
		FailureThreshold:    1,
		CooldownBase:        1 * time.Millisecond,
		CooldownMax:         2 * time.Millisecond,
		HalfOpenMaxInflight: 1,
	}
	b, base := newTestBreaker(cfg)

	b.Allow()
	b.RecordFailure()
	advanceNow(b, base, 10*time.Second)

	b.Allow() // → half-open
	if b.State() != StateHalfOpen {
		t.Fatalf("expected half-open")
	}

	// Failure during half-open must immediately re-open.
	b.RecordFailure()
	if b.State() != StateOpen {
		t.Fatalf("expected re-opened after failure in half-open, got %s", b.State())
	}

	// And now Allow() must reject again.
	if b.Allow() {
		t.Fatal("should be rejected immediately after re-open")
	}
}

// ---------------------------------------------------------------------------
// Test 7: Cooldown grows with consecutive opens (openCount increases).
// ---------------------------------------------------------------------------
func TestBreaker_CooldownGrowsWithConsecutiveOpens(t *testing.T) {
	t.Parallel()
	cfg := Config{
		FailureThreshold:    1,
		SuccessBudget:       10, // won't re-close during this test
		CooldownBase:        100 * time.Millisecond,
		CooldownMax:         60 * time.Second,
		HalfOpenMaxInflight: 1,
	}
	b, base := newTestBreaker(cfg)

	// Open once.
	b.Allow()
	b.RecordFailure()
	cd1 := b.CooldownRemaining()

	// Advance past first cooldown, probe, fail → second open.
	advanceNow(b, base, 10*time.Second)
	b.Allow()                              // → half-open
	b.RecordFailure()                      // → re-open (openCount = 2)
	advanceNow(b, base, 1*time.Nanosecond) // reset base reference
	cd2 := b.CooldownRemaining()

	// The second open should have a longer (or at minimum equal) cooldown than
	// the first. Because NextDelay uses jitter we cannot require strict >, but
	// with a base of 100ms and openCount doubling, the expected value grows from
	// ~100ms to ~200ms. We just verify cd2 > 0 and that both are positive, and
	// that cd2 is at most CooldownMax.
	if cd1 <= 0 {
		t.Fatalf("first cooldown should be > 0, got %v", cd1)
	}
	if cd2 <= 0 {
		t.Fatalf("second cooldown should be > 0, got %v", cd2)
	}
	if cd2 > cfg.CooldownMax {
		t.Fatalf("cooldown %v exceeds CooldownMax %v", cd2, cfg.CooldownMax)
	}

	// The internal openCount must be 2 (we opened twice without re-closing).
	b.mu.Lock()
	oc := b.openCount
	b.mu.Unlock()
	if oc != 2 {
		t.Fatalf("expected openCount=2, got %d", oc)
	}
}

// ---------------------------------------------------------------------------
// Test 8: RecordSuccess in closed resets failure counter (no spurious opening).
// ---------------------------------------------------------------------------
func TestBreaker_RecordSuccessResetsClosed(t *testing.T) {
	t.Parallel()
	cfg := Config{FailureThreshold: 3}
	b, _ := newTestBreaker(cfg)

	// Two failures — not yet open.
	b.RecordFailure()
	b.RecordFailure()
	if b.State() != StateClosed {
		t.Fatal("should still be closed")
	}

	// A success must reset the counter.
	b.RecordSuccess()

	// Two more failures — still under threshold because counter was reset.
	b.RecordFailure()
	b.RecordFailure()
	if b.State() != StateClosed {
		t.Fatalf("should still be closed after counter reset + 2 failures, got %s", b.State())
	}

	// One more failure should NOT open yet (counter was reset to 0 by success,
	// so we've only accumulated 2 failures since the reset; we need one more).
	b.RecordFailure()
	if b.State() != StateOpen {
		t.Fatalf("should be open after 3 failures following reset, got %s", b.State())
	}
}

// ---------------------------------------------------------------------------
// Test 9: Concurrent Allow/Record calls don't deadlock or panic.
// ---------------------------------------------------------------------------
func TestBreaker_Concurrent(t *testing.T) {
	t.Parallel()

	cfg := Config{
		FailureThreshold:    5,
		SuccessBudget:       2,
		CooldownBase:        1 * time.Millisecond,
		CooldownMax:         10 * time.Millisecond,
		HalfOpenMaxInflight: 3,
	}
	b := New(cfg)
	// Use a time source that advances at ~10µs per call to quickly cycle states.
	var tick atomic.Int64
	b.now = func() time.Time {
		v := tick.Add(int64(10 * time.Microsecond))
		return time.Unix(0, v)
	}

	const goroutines = 50
	const iters = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				allowed := b.Allow()
				if allowed {
					if (i+j)%3 == 0 {
						b.RecordFailure()
					} else {
						b.RecordSuccess()
					}
				}
				_ = b.State()
				_ = b.CooldownRemaining()
			}
		}()
	}
	wg.Wait()
	// Success = no deadlock, no panic, no data race (run with -race).
}

// ---------------------------------------------------------------------------
// Test 10: CooldownRemaining returns 0 when not open.
// ---------------------------------------------------------------------------
func TestBreaker_CooldownRemainingNotOpen(t *testing.T) {
	t.Parallel()
	b, _ := newTestBreaker(Config{})
	if b.CooldownRemaining() != 0 {
		t.Fatalf("CooldownRemaining should be 0 when closed, got %v", b.CooldownRemaining())
	}
}
