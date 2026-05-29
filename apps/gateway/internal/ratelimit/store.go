// Package ratelimit implements a configurable, race-safe rate limiter for the
// UBAG gateway. It uses a fixed-window algorithm with pluggable storage so the
// same policy logic can run against an in-process map (single node), SQLite
// (small/edge deployments) or Postgres (clustered deployments).
//
// The caller composes an opaque rate-limit key (typically tenantID+appID+action)
// and asks the limiter to Allow a request with a given cost. The limiter returns
// a Decision describing whether the request is permitted plus the headers the
// HTTP layer needs to emit (limit, remaining, reset, retry-after).
package ratelimit

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Decision is the result of evaluating a single Allow call. It carries enough
// information for the HTTP layer to populate X-RateLimit-* headers and, when the
// request is denied, a Retry-After header.
type Decision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	ResetAt    time.Time
	RetryAfter time.Duration
}

// Policy describes a fixed-window quota. Limit requests are permitted per Window.
// Burst, when greater than zero, adds extra capacity on top of Limit within the
// same window to absorb short spikes (the token-bucket-style variant).
type Policy struct {
	Limit  int
	Window time.Duration
	Burst  int
}

// Capacity returns the effective number of requests permitted within a single
// window, including any configured burst allowance.
func (p Policy) Capacity() int {
	capacity := p.Limit + p.Burst
	if capacity < 0 {
		return 0
	}
	return capacity
}

// Valid reports whether the policy can be enforced. A policy with a non-positive
// limit or window is treated as "unlimited" by the limiter rather than an error.
func (p Policy) Valid() bool {
	return p.Limit > 0 && p.Window > 0
}

// Store is the pluggable backend for fixed-window counters. Implementations must
// be safe for concurrent use. Increment adds cost to the counter identified by
// (key, windowStart) and returns the resulting total for that window. Because
// each window has a distinct windowStart, counters reset implicitly when a new
// window begins; implementations may prune expired windows opportunistically.
type Store interface {
	Increment(ctx context.Context, key string, windowStart time.Time, window time.Duration, cost int) (int, error)
	// Peek returns the current count for (key, windowStart) without mutating it.
	// It returns 0 when no counter exists for that window.
	Peek(ctx context.Context, key string, windowStart time.Time) (int, error)
}

// Limiter is the contract used by the HTTP middleware. key is an opaque
// caller-built string; cost defaults to 1 when non-positive.
type Limiter interface {
	Allow(ctx context.Context, key string, cost int) (Decision, error)
}

// WindowLimiter is a fixed-window, store-backed Limiter. It is safe for
// concurrent use as long as the underlying Store is. The now field is injectable
// so tests can advance the clock deterministically.
type WindowLimiter struct {
	store  Store
	policy Policy
	now    func() time.Time
}

// New builds a WindowLimiter that applies policy to every Allow call. Use
// AllowPolicy to evaluate a per-request policy resolved elsewhere.
func New(store Store, policy Policy) *WindowLimiter {
	return &WindowLimiter{store: store, policy: policy, now: time.Now}
}

// SetClock overrides the time source. Intended for tests.
func (l *WindowLimiter) SetClock(now func() time.Time) {
	if now != nil {
		l.now = now
	}
}

// Allow evaluates the limiter's default policy for key.
func (l *WindowLimiter) Allow(ctx context.Context, key string, cost int) (Decision, error) {
	return l.AllowPolicy(ctx, key, l.policy, cost)
}

// AllowPolicy evaluates an explicit policy for key. This is the entry point the
// middleware uses after resolving the policy for the request's action.
func (l *WindowLimiter) AllowPolicy(ctx context.Context, key string, policy Policy, cost int) (Decision, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Decision{}, fmt.Errorf("ratelimit: key is required")
	}
	if cost <= 0 {
		cost = 1
	}
	now := l.now().UTC()
	if !policy.Valid() {
		// Unlimited policy: always allow, never expose a meaningful reset.
		return Decision{Allowed: true, Limit: 0, Remaining: 0, ResetAt: now}, nil
	}
	windowStart := WindowStart(now, policy.Window)
	count, err := l.store.Increment(ctx, key, windowStart, policy.Window, cost)
	if err != nil {
		return Decision{}, err
	}
	return decide(policy, count, windowStart, now), nil
}

// WindowStart aligns now to the start of its fixed window.
func WindowStart(now time.Time, window time.Duration) time.Time {
	if window <= 0 {
		return now.UTC()
	}
	return now.UTC().Truncate(window)
}

func decide(policy Policy, count int, windowStart time.Time, now time.Time) Decision {
	capacity := policy.Capacity()
	resetAt := windowStart.Add(policy.Window)
	remaining := capacity - count
	if remaining < 0 {
		remaining = 0
	}
	allowed := count <= capacity
	decision := Decision{
		Allowed:   allowed,
		Limit:     capacity,
		Remaining: remaining,
		ResetAt:   resetAt,
	}
	if !allowed {
		decision.Remaining = 0
		retryAfter := resetAt.Sub(now)
		if retryAfter < 0 {
			retryAfter = 0
		}
		decision.RetryAfter = retryAfter
	}
	return decision
}
