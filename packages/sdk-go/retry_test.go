package ubag

import "testing"

func TestComputeBackoffIncreases(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 5, BaseDelayMS: 1000, MaxDelayMS: 60000}
	d0 := ComputeBackoff(p, 0, func() float64 { return 0.5 })
	d1 := ComputeBackoff(p, 1, func() float64 { return 0.5 })
	if d1 <= d0 {
		t.Fatalf("expected delay to grow: d0=%d d1=%d", d0, d1)
	}
}

func TestComputeBackoffCaps(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 10, BaseDelayMS: 1000, MaxDelayMS: 2000}
	d := ComputeBackoff(p, 9, func() float64 { return 1.0 })
	if d > int64(float64(2000)*1.3) {
		t.Fatalf("delay %d exceeds cap", d)
	}
}

func TestShouldRetry(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 3}
	if !ShouldRetry(p, 0, true) {
		t.Fatal("expected retry on attempt 0")
	}
	if ShouldRetry(p, 2, true) {
		t.Fatal("expected no retry once budget exhausted")
	}
	if ShouldRetry(p, 0, false) {
		t.Fatal("expected no retry for non-retryable")
	}
}
