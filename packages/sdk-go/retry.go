package ubag

import "math"

type RetryPolicy struct {
	MaxAttempts int
	BaseDelayMS int64
	MaxDelayMS  int64
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelayMS: 1000, MaxDelayMS: 60000}
}

const jitterFraction = 0.3

// ComputeBackoff returns the delay in milliseconds before attempt `attempt`
// (0-based) using exponential backoff with +/-30% full jitter. `rand` is
// injectable for tests (use math/rand.Float64 in production).
func ComputeBackoff(p RetryPolicy, attempt int, rand func() float64) int64 {
	exp := math.Pow(2, float64(attempt)) * float64(p.BaseDelayMS)
	base := math.Min(exp, float64(p.MaxDelayMS))
	lo := base * (1 - jitterFraction)
	hi := base * (1 + jitterFraction)
	d := lo + rand()*(hi-lo)
	if d < 0 {
		d = 0
	}
	return int64(d)
}

func ShouldRetry(p RetryPolicy, attempt int, retryable bool) bool {
	return retryable && attempt < p.MaxAttempts-1
}
