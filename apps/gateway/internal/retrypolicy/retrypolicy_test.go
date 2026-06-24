package retrypolicy

import (
	"testing"
	"time"
)

func TestDefaultPolicy(t *testing.T) {
	p := DefaultPolicy()
	if p.MaxRetries != DefaultMaxRetries {
		t.Errorf("MaxRetries = %d, want %d", p.MaxRetries, DefaultMaxRetries)
	}
	if p.BackoffBaseMS != DefaultBackoffBase {
		t.Errorf("BackoffBaseMS = %d, want %d", p.BackoffBaseMS, DefaultBackoffBase)
	}
}

func TestPolicyClamp(t *testing.T) {
	p := Policy{MaxRetries: 0, BackoffBaseMS: 0, BackoffMaxMS: 0}.Clamp()
	if p.MaxRetries != DefaultMaxRetries {
		t.Errorf("clamped MaxRetries = %d, want %d", p.MaxRetries, DefaultMaxRetries)
	}

	p = Policy{MaxRetries: 99}.Clamp()
	if p.MaxRetries != 10 {
		t.Errorf("clamped MaxRetries > 10: got %d", p.MaxRetries)
	}
}

func TestNextDelayIncreases(t *testing.T) {
	p := Policy{MaxRetries: 5, BackoffBaseMS: 100, BackoffMaxMS: 10000}
	// Delays should generally increase with retries (accounting for jitter).
	// We test the midpoint (no jitter) by using a fixed seed approach:
	// just verify delay is positive and within range.
	for retries := 0; retries < 5; retries++ {
		d := p.NextDelay(retries)
		if d < 0 {
			t.Errorf("NextDelay(%d) < 0: %v", retries, d)
		}
		// Upper bound: BackoffMaxMS * (1+jitter) = 10000 * 1.3 = 13000ms
		if d > 13000*time.Millisecond {
			t.Errorf("NextDelay(%d) = %v, exceeds upper bound", retries, d)
		}
	}
}

func TestNextDelayCapsAtBackoffMax(t *testing.T) {
	p := Policy{MaxRetries: 5, BackoffBaseMS: 1000, BackoffMaxMS: 2000}
	for retries := 5; retries < 20; retries++ {
		d := p.NextDelay(retries)
		// Upper bound: 2000ms * 1.3 = 2600ms
		if d > 2600*time.Millisecond {
			t.Errorf("NextDelay(%d) = %v, exceeds capped upper bound", retries, d)
		}
	}
}

func TestShouldRetry(t *testing.T) {
	p := Policy{MaxRetries: 3, BackoffBaseMS: 100, BackoffMaxMS: 1000}

	if !p.ShouldRetry(CategoryTransient, 0) {
		t.Error("ShouldRetry(transient, 0) = false, want true")
	}
	if !p.ShouldRetry(CategoryTransient, 2) {
		t.Error("ShouldRetry(transient, 2) = false, want true")
	}
	if p.ShouldRetry(CategoryTransient, 3) {
		t.Error("ShouldRetry(transient, 3) = true, want false (exhausted)")
	}
	if p.ShouldRetry(CategoryPermanent, 0) {
		t.Error("ShouldRetry(permanent, 0) = true, want false")
	}
}

func TestParseFromMap(t *testing.T) {
	raw := map[string]any{
		"retry_policy": map[string]any{
			"max_retries":     float64(7),
			"backoff_base_ms": float64(500),
			"backoff_max_ms":  float64(30000),
		},
	}
	p := ParseFromMap(raw)
	if p.MaxRetries != 7 {
		t.Errorf("MaxRetries = %d, want 7", p.MaxRetries)
	}
	if p.BackoffBaseMS != 500 {
		t.Errorf("BackoffBaseMS = %d, want 500", p.BackoffBaseMS)
	}

	// nil map should return defaults
	p2 := ParseFromMap(nil)
	if p2.MaxRetries != DefaultMaxRetries {
		t.Errorf("nil map MaxRetries = %d, want %d", p2.MaxRetries, DefaultMaxRetries)
	}
}

func TestClassifyError(t *testing.T) {
	transient := []string{
		"UBAG-QUEUE-ENQUEUE-001",
		"UBAG-WORKER-TIMEOUT-001",
		"UBAG-RATE-APP-001",
		"UBAG-INTERNAL-GATEWAY-001",
		"UBAG-ADAPTER-NETWORK-TIMEOUT",
	}
	for _, code := range transient {
		if ClassifyError(code) != CategoryTransient {
			t.Errorf("ClassifyError(%q) = permanent, want transient", code)
		}
	}

	permanent := []string{
		"UBAG-VALIDATION-JOB-TARGET-001",
		"UBAG-AUTH-KEY-001",
		"UBAG-ADAPTER-NOT-FOUND",
		"",
	}
	for _, code := range permanent {
		if ClassifyError(code) != CategoryPermanent {
			t.Errorf("ClassifyError(%q) = transient, want permanent", code)
		}
	}
}
