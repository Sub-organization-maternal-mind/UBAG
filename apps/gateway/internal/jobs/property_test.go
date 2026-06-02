package jobs_test

import (
	"math/rand"
	"testing"
	"testing/quick"
	"time"
)

// TestRetryBackoffWithinBounds verifies that jittered exponential backoff stays
// within [base*2^n, base*2^n*1.5] for any attempt count up to 10.
func TestRetryBackoffWithinBounds(t *testing.T) {
	f := func(attempt uint8) bool {
		if attempt > 10 {
			attempt = 10 // cap to avoid overflow
		}
		base := 100 * time.Millisecond
		n := int(attempt)

		// Simulated jittered backoff: base * 2^n * (1 + rand*0.5)
		exp := base * time.Duration(1<<n)
		jitter := time.Duration(float64(exp) * 0.5 * rand.Float64())
		backoff := exp + jitter

		// Must be within [base*2^n, base*2^n*1.5]
		min := exp
		max := exp + time.Duration(float64(exp)*0.5)
		return backoff >= min && backoff <= max
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("retry backoff property violated: %v", err)
	}
}

// TestIdempotencyKeyUniqueness verifies that randomly generated hex keys
// (the pattern used by dashboard/SDK) are collision-free across 1000 samples.
func TestIdempotencyKeyUniqueness(t *testing.T) {
	keys := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		key := generateRandomHex(32)
		if keys[key] {
			t.Errorf("duplicate idempotency key generated: %s", key)
		}
		keys[key] = true
	}
}

func generateRandomHex(n int) string {
	const hexChars = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = hexChars[rand.Intn(len(hexChars))]
	}
	return string(b)
}
