package resilience

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A half-open window whose probe result is never recorded used to stay wedged
// forever: Allow() admits the first probe (inflight = 1), and because nothing
// decremented inflight, every later Allow() hit
// `if b.inflight >= b.cfg.HalfOpenMaxInflight { return false }` and was refused
// for the lifetime of the process. This reproduces that and asserts the
// HalfOpenProbeTimeout re-arm releases it.
func TestBreaker_HalfOpenRecoversFromUnrecordedProbe(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	b := New(Config{
		FailureThreshold:     1,
		SuccessBudget:        1,
		CooldownBase:         time.Second,
		CooldownMax:          time.Second,
		HalfOpenMaxInflight:  1,
		HalfOpenProbeTimeout: 10 * time.Second,
	})
	b.now = func() time.Time { return now }

	// Trip it.
	if !b.Allow() {
		t.Fatal("closed breaker must admit")
	}
	b.RecordFailure()
	if got := b.State(); got != StateOpen {
		t.Fatalf("expected open, got %s", got)
	}

	// Cooldown elapses -> half-open with one probe in flight, and the caller
	// never records a result (the bug: a 3xx/4xx path recorded nothing).
	now = now.Add(2 * time.Second)
	if !b.Allow() {
		t.Fatal("half-open must admit the first probe")
	}
	if got := b.State(); got != StateHalfOpen {
		t.Fatalf("expected half-open, got %s", got)
	}

	// Still inside the probe window: correctly refuses a second concurrent probe.
	if b.Allow() {
		t.Fatal("half-open must refuse a probe while one is in flight")
	}

	// Probe never resolves. After the timeout the window re-arms and a new
	// probe is admitted instead of the breaker staying shut forever.
	now = now.Add(11 * time.Second)
	if !b.Allow() {
		t.Fatal("half-open must re-arm after the probe timeout, not stay wedged")
	}

	// And a normal recorded result still works afterwards.
	b.RecordSuccess()
	if got := b.State(); got != StateClosed {
		t.Errorf("expected closed after re-armed probe succeeded, got %s", got)
	}
}

// With the re-arm disabled the wedge must still be observable, proving the
// timeout is what fixes it rather than the test being vacuous.
func TestBreaker_HalfOpenWedgesWithoutProbeTimeout(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	b := &Breaker{
		cfg: Config{
			FailureThreshold:    1,
			SuccessBudget:       1,
			CooldownBase:        time.Second,
			CooldownMax:         time.Second,
			HalfOpenMaxInflight: 1,
			// HalfOpenProbeTimeout deliberately zero.
		},
		now: func() time.Time { return now },
	}

	b.Allow()
	b.RecordFailure()
	now = now.Add(2 * time.Second)
	if !b.Allow() {
		t.Fatal("expected first half-open probe")
	}
	// Never recorded.
	now = now.Add(time.Hour)
	if b.Allow() {
		t.Fatal("without a probe timeout the breaker is expected to stay wedged")
	}
}

// Concurrent Allow calls must never exceed HalfOpenMaxInflight within a live
// window, and the timeout must not double-count in-flight probes.
func TestBreaker_HalfOpenRespectsInflightCapUnderConcurrency(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	b := New(Config{
		FailureThreshold:     1,
		SuccessBudget:        1,
		CooldownBase:         time.Second,
		CooldownMax:          time.Second,
		HalfOpenMaxInflight:  1,
		HalfOpenProbeTimeout: time.Hour,
	})
	b.now = func() time.Time { return now }
	b.Allow()
	b.RecordFailure()
	now = now.Add(2 * time.Second)

	var wg sync.WaitGroup
	var admitted atomic.Int64
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Allow() {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := admitted.Load(); got != 1 {
		t.Errorf("expected exactly 1 admitted probe, got %d", got)
	}
}
