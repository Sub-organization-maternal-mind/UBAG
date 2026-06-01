package retrypolicy

import (
	"math"
	"math/rand"
	"time"
)

const (
	DefaultMaxRetries  = 3
	DefaultBackoffBase = 1000 // ms
	DefaultBackoffMax  = 60000 // ms (60s)
	jitterFraction     = 0.30  // ±30% full jitter
)

// Category classifies a failure for routing through the retry policy.
type Category int

const (
	// CategoryTransient indicates a temporary failure that may succeed on retry
	// (network timeout, 503, rate limit, lock contention).
	CategoryTransient Category = iota
	// CategoryPermanent indicates a failure that will not benefit from retry
	// (bad request, schema validation error, missing adapter).
	CategoryPermanent
)

// Policy holds the retry configuration for a single job.
type Policy struct {
	// MaxRetries is the number of retry attempts (not counting the initial try).
	// Range 1–10; defaults to DefaultMaxRetries.
	MaxRetries int
	// BackoffBaseMS is the base delay in milliseconds for exponential backoff.
	BackoffBaseMS int
	// BackoffMaxMS is the cap on the computed delay in milliseconds.
	BackoffMaxMS int
}

// DefaultPolicy returns a Policy with the blueprint §14.2 defaults.
func DefaultPolicy() Policy {
	return Policy{
		MaxRetries:    DefaultMaxRetries,
		BackoffBaseMS: DefaultBackoffBase,
		BackoffMaxMS:  DefaultBackoffMax,
	}
}

// Clamp normalises p to ensure all values are within valid ranges.
func (p Policy) Clamp() Policy {
	if p.MaxRetries < 1 {
		p.MaxRetries = DefaultMaxRetries
	}
	if p.MaxRetries > 10 {
		p.MaxRetries = 10
	}
	if p.BackoffBaseMS <= 0 {
		p.BackoffBaseMS = DefaultBackoffBase
	}
	if p.BackoffMaxMS <= 0 {
		p.BackoffMaxMS = DefaultBackoffMax
	}
	if p.BackoffMaxMS < p.BackoffBaseMS {
		p.BackoffMaxMS = p.BackoffBaseMS
	}
	return p
}

// NextDelay computes the delay before attempt number (retries+1) using
// exponential backoff with full jitter:
//
//	base = min(2^retries * BackoffBaseMS, BackoffMaxMS)
//	delay = base * uniform(1-jitterFraction, 1+jitterFraction)
//
// The returned duration is always ≥ 0.
func (p Policy) NextDelay(retries int) time.Duration {
	p = p.Clamp()
	exp := math.Pow(2, float64(retries))
	base := math.Min(exp*float64(p.BackoffBaseMS), float64(p.BackoffMaxMS))

	lo := base * (1 - jitterFraction)
	hi := base * (1 + jitterFraction)
	jittered := lo + rand.Float64()*(hi-lo) //nolint:gosec — timing jitter, not crypto
	if jittered < 0 {
		jittered = 0
	}
	return time.Duration(jittered) * time.Millisecond
}

// ShouldRetry returns true when the job should be retried given the failure
// category and the number of retries already attempted.
func (p Policy) ShouldRetry(cat Category, retriesSoFar int) bool {
	if cat == CategoryPermanent {
		return false
	}
	return retriesSoFar < p.Clamp().MaxRetries
}

// ParseFromMap extracts a Policy from a raw options map (blueprint §14.2).
// Unknown or invalid values fall back to defaults.
func ParseFromMap(raw map[string]any) Policy {
	p := DefaultPolicy()
	if raw == nil {
		return p
	}
	rp, ok := raw["retry_policy"].(map[string]any)
	if !ok {
		return p
	}
	if v, ok := rp["max_retries"].(float64); ok && v >= 1 {
		p.MaxRetries = int(v)
	}
	if v, ok := rp["backoff_base_ms"].(float64); ok && v > 0 {
		p.BackoffBaseMS = int(v)
	}
	if v, ok := rp["backoff_max_ms"].(float64); ok && v > 0 {
		p.BackoffMaxMS = int(v)
	}
	return p.Clamp()
}

// ClassifyError maps an error code string to a Category.
// Codes starting with transient prefixes are retryable; everything else is permanent.
func ClassifyError(code string) Category {
	for _, prefix := range transientPrefixes {
		if len(code) >= len(prefix) && code[:len(prefix)] == prefix {
			return CategoryTransient
		}
	}
	return CategoryPermanent
}

// transientPrefixes lists error code prefixes that indicate retryable failures.
var transientPrefixes = []string{
	"UBAG-QUEUE-",
	"UBAG-WORKER-TIMEOUT",
	"UBAG-WORKER-RETRYABLE",
	"UBAG-BROWSER-LAUNCH",
	"UBAG-BROWSER-CRASH",
	"UBAG-ADAPTER-NETWORK",
	"UBAG-ADAPTER-TIMEOUT",
	"UBAG-RATE-",
	"UBAG-CACHE-MISS",
	"UBAG-INTERNAL-",
}
