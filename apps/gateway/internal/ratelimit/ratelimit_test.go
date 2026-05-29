package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mutableClock is a deterministic, race-safe clock for tests.
type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock(start time.Time) *mutableClock {
	return &mutableClock{now: start.UTC()}
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestLimiter(store Store, policy Policy, clock *mutableClock) *WindowLimiter {
	limiter := New(store, policy)
	limiter.SetClock(clock.Now)
	if setter, ok := store.(interface{ SetClock(func() time.Time) }); ok {
		setter.SetClock(clock.Now)
	}
	return limiter
}

func TestMemoryLimitEnforcement(t *testing.T) {
	clock := newClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	limiter := newTestLimiter(NewMemoryStore(), Policy{Limit: 3, Window: time.Minute}, clock)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		decision, err := limiter.Allow(ctx, "tenant:app:job:create", 1)
		if err != nil {
			t.Fatalf("request %d errored: %v", i, err)
		}
		if !decision.Allowed {
			t.Fatalf("request %d should be allowed", i)
		}
		if decision.Remaining != 3-i {
			t.Fatalf("request %d remaining = %d, want %d", i, decision.Remaining, 3-i)
		}
		if decision.Limit != 3 {
			t.Fatalf("request %d limit = %d, want 3", i, decision.Limit)
		}
	}

	decision, err := limiter.Allow(ctx, "tenant:app:job:create", 1)
	if err != nil {
		t.Fatalf("4th request errored: %v", err)
	}
	if decision.Allowed {
		t.Fatal("4th request should be denied")
	}
	if decision.Remaining != 0 {
		t.Fatalf("denied remaining = %d, want 0", decision.Remaining)
	}
	if decision.RetryAfter <= 0 || decision.RetryAfter > time.Minute {
		t.Fatalf("denied RetryAfter = %v, want (0, 1m]", decision.RetryAfter)
	}
	wantReset := WindowStart(clock.Now(), time.Minute).Add(time.Minute)
	if !decision.ResetAt.Equal(wantReset) {
		t.Fatalf("ResetAt = %v, want %v", decision.ResetAt, wantReset)
	}
}

func TestMemoryWindowReset(t *testing.T) {
	clock := newClock(time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC))
	limiter := newTestLimiter(NewMemoryStore(), Policy{Limit: 2, Window: time.Minute}, clock)
	ctx := context.Background()

	mustAllow(t, limiter, ctx, "k", true)
	mustAllow(t, limiter, ctx, "k", true)
	mustAllow(t, limiter, ctx, "k", false) // exhausted

	// Advance past the end of the current window so a fresh window begins.
	clock.Advance(time.Minute)
	decision, err := limiter.Allow(ctx, "k", 1)
	if err != nil {
		t.Fatalf("post-reset errored: %v", err)
	}
	if !decision.Allowed {
		t.Fatal("request after window reset should be allowed")
	}
	if decision.Remaining != 1 {
		t.Fatalf("post-reset remaining = %d, want 1", decision.Remaining)
	}
}

func TestMemoryPerKeyIsolation(t *testing.T) {
	clock := newClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	limiter := newTestLimiter(NewMemoryStore(), Policy{Limit: 1, Window: time.Minute}, clock)
	ctx := context.Background()

	mustAllow(t, limiter, ctx, "tenant-a", true)
	mustAllow(t, limiter, ctx, "tenant-a", false) // a exhausted
	mustAllow(t, limiter, ctx, "tenant-b", true)   // b independent
}

func TestMemoryCostGreaterThanOne(t *testing.T) {
	clock := newClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	limiter := newTestLimiter(NewMemoryStore(), Policy{Limit: 5, Window: time.Minute}, clock)
	ctx := context.Background()

	decision, err := limiter.Allow(ctx, "k", 4)
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if !decision.Allowed || decision.Remaining != 1 {
		t.Fatalf("cost=4 decision = %+v, want allowed remaining 1", decision)
	}
	decision, _ = limiter.Allow(ctx, "k", 4) // total 8 > 5
	if decision.Allowed {
		t.Fatal("cost overflow should be denied")
	}
}

func TestBurstAddsCapacity(t *testing.T) {
	clock := newClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	limiter := newTestLimiter(NewMemoryStore(), Policy{Limit: 2, Window: time.Minute, Burst: 2}, clock)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		mustAllow(t, limiter, ctx, "k", true)
	}
	mustAllow(t, limiter, ctx, "k", false)
}

func TestUnlimitedPolicyAlwaysAllows(t *testing.T) {
	clock := newClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	limiter := newTestLimiter(NewMemoryStore(), Policy{}, clock)
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		mustAllow(t, limiter, ctx, "k", true)
	}
}

func TestMemoryConcurrentAllowRace(t *testing.T) {
	clock := newClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	const capacity = 100
	limiter := newTestLimiter(NewMemoryStore(), Policy{Limit: capacity, Window: time.Hour}, clock)
	ctx := context.Background()

	const goroutines = 50
	const perGoroutine = 10 // 500 total attempts against capacity 100
	var allowed int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				decision, err := limiter.Allow(ctx, "shared", 1)
				if err != nil {
					t.Errorf("concurrent allow errored: %v", err)
					return
				}
				if decision.Allowed {
					atomic.AddInt64(&allowed, 1)
				}
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt64(&allowed); got != capacity {
		t.Fatalf("allowed %d requests, want exactly %d", got, capacity)
	}
}

func mustAllow(t *testing.T, limiter *WindowLimiter, ctx context.Context, key string, want bool) {
	t.Helper()
	decision, err := limiter.Allow(ctx, key, 1)
	if err != nil {
		t.Fatalf("Allow(%q) errored: %v", key, err)
	}
	if decision.Allowed != want {
		t.Fatalf("Allow(%q).Allowed = %v, want %v", key, decision.Allowed, want)
	}
}

func TestPolicyResolverDefaultsAndOverrides(t *testing.T) {
	resolver := DefaultPolicyResolver()
	if got := resolver.Resolve("job:create"); got.Limit != 120 || got.Burst != 30 {
		t.Fatalf("job:create policy = %+v", got)
	}
	if got := resolver.Resolve("JOB:CREATE"); got.Limit != 120 {
		t.Fatalf("action resolution should be case-insensitive, got %+v", got)
	}
	if got := resolver.Resolve("unknown:action"); got != resolver.Default() {
		t.Fatalf("unknown action should fall back to default, got %+v", got)
	}

	custom := NewPolicyResolver(Policy{Limit: 10, Window: time.Second}, map[string]Policy{
		"job:read": {Limit: 99, Window: time.Minute},
	})
	if got := custom.Resolve("job:read"); got.Limit != 99 {
		t.Fatalf("custom override = %+v", got)
	}
	policies := custom.Policies()
	if policies["*"].Limit != 10 {
		t.Fatalf("default reported under * = %+v", policies["*"])
	}
	if len(custom.Actions()) != 1 || custom.Actions()[0] != "job:read" {
		t.Fatalf("Actions() = %v", custom.Actions())
	}
}
