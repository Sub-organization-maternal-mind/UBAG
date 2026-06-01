package resilience

import (
	"sync"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/retrypolicy"
)

// State represents the circuit breaker state.
type State int

const (
	StateClosed   State = iota // Normal operation — calls pass through.
	StateOpen                  // Tripped — calls are rejected.
	StateHalfOpen              // Probing — limited calls allowed.
)

// String returns a human-readable label for the state.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Config holds circuit breaker tuning parameters.
type Config struct {
	// FailureThreshold is the number of consecutive failures required to open the breaker.
	FailureThreshold int
	// SuccessBudget is the number of consecutive successes in half-open needed to re-close.
	SuccessBudget int
	// CooldownBase is the base duration for exponential backoff between opens.
	CooldownBase time.Duration
	// CooldownMax caps the computed cooldown duration.
	CooldownMax time.Duration
	// HalfOpenMaxInflight is the maximum number of concurrent probes allowed in half-open.
	HalfOpenMaxInflight int
}

// DefaultConfig returns a Config with sensible production defaults.
func DefaultConfig() Config {
	return Config{
		FailureThreshold:    5,
		SuccessBudget:       2,
		CooldownBase:        5 * time.Second,
		CooldownMax:         60 * time.Second,
		HalfOpenMaxInflight: 1,
	}
}

// Breaker is a concurrency-safe circuit breaker.
type Breaker struct {
	cfg Config

	mu              sync.Mutex
	state           State
	failures        int // consecutive failures in closed
	successes       int // consecutive successes in half-open
	inflight        int // current probes in half-open
	openCount       int // cumulative consecutive opens (for cooldown growth)
	openedAt        time.Time
	cooldownForOpen time.Duration

	// Now is called to get the current time. Defaults to time.Now.
	// Settable in tests to control time.
	Now func() time.Time
}

// New constructs a Breaker. Zero-value Config fields fall back to DefaultConfig values.
func New(cfg Config) *Breaker {
	def := DefaultConfig()
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = def.FailureThreshold
	}
	if cfg.SuccessBudget <= 0 {
		cfg.SuccessBudget = def.SuccessBudget
	}
	if cfg.CooldownBase <= 0 {
		cfg.CooldownBase = def.CooldownBase
	}
	if cfg.CooldownMax <= 0 {
		cfg.CooldownMax = def.CooldownMax
	}
	if cfg.HalfOpenMaxInflight <= 0 {
		cfg.HalfOpenMaxInflight = def.HalfOpenMaxInflight
	}
	return &Breaker{
		cfg: cfg,
		Now: time.Now,
	}
}

// cooldown returns the computed cooldown for the given consecutive open count
// using the same exponential-backoff formula as retrypolicy.
func (b *Breaker) cooldown(openCount int) time.Duration {
	p := retrypolicy.Policy{
		MaxRetries:    10,
		BackoffBaseMS: int(b.cfg.CooldownBase.Milliseconds()),
		BackoffMaxMS:  int(b.cfg.CooldownMax.Milliseconds()),
	}
	return p.NextDelay(openCount)
}

// Allow reports whether the next call should be executed.
//
// Closed: always true.
// Open: false, unless the cooldown has elapsed — in that case transition to
// half-open and admit the first probe.
// Half-open: true until HalfOpenMaxInflight probes are in-flight; false thereafter.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return true

	case StateOpen:
		now := b.Now()
		if now.Before(b.openedAt.Add(b.cooldownForOpen)) {
			return false
		}
		// Cooldown elapsed — transition to half-open and admit one probe.
		b.state = StateHalfOpen
		b.inflight = 1
		b.successes = 0
		return true

	case StateHalfOpen:
		if b.inflight >= b.cfg.HalfOpenMaxInflight {
			return false
		}
		b.inflight++
		return true
	}
	return false
}

// RecordSuccess records a successful call result.
//
// Closed: resets the consecutive-failure counter.
// Half-open: increments the consecutive-success counter; re-closes when the
// SuccessBudget is reached and resets openCount.
// Open: no-op.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		b.failures = 0

	case StateHalfOpen:
		if b.inflight > 0 {
			b.inflight--
		}
		b.successes++
		if b.successes >= b.cfg.SuccessBudget {
			b.state = StateClosed
			b.failures = 0
			b.successes = 0
			b.inflight = 0
			b.openCount = 0
		}

	case StateOpen:
		// no-op
	}
}

// RecordFailure records a failed call result.
//
// Closed: increments consecutive failures; opens the breaker when the
// FailureThreshold is reached.
// Half-open: immediately re-opens the breaker (openCount increments again).
// Open: no-op.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		b.failures++
		if b.failures >= b.cfg.FailureThreshold {
			b.doOpen()
		}

	case StateHalfOpen:
		b.inflight = 0
		b.successes = 0
		b.doOpen()

	case StateOpen:
		// no-op
	}
}

// doOpen transitions to StateOpen and computes the cooldown. Must be called with mu held.
func (b *Breaker) doOpen() {
	b.openCount++
	b.state = StateOpen
	b.openedAt = b.Now()
	b.cooldownForOpen = b.cooldown(b.openCount)
}

// State returns the current breaker state (for metrics / snapshots).
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// CooldownRemaining returns how long until the breaker may transition to
// half-open. Returns 0 if the breaker is not open or the cooldown has elapsed.
func (b *Breaker) CooldownRemaining() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state != StateOpen {
		return 0
	}
	remaining := b.openedAt.Add(b.cooldownForOpen).Sub(b.Now())
	if remaining < 0 {
		return 0
	}
	return remaining
}
