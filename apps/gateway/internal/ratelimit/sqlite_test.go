package ratelimit

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "rl.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// SQLite serializes writers; constrain to a single connection to match the
	// gateway's runtime configuration and avoid "database is locked" flakes.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSQLiteLimitEnforcementAndReset(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(ctx, newSQLiteDB(t))
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	clock := newClock(time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC))
	limiter := newTestLimiter(store, Policy{Limit: 2, Window: time.Minute}, clock)

	mustAllow(t, limiter, ctx, "tenant:app:job:create", true)
	mustAllow(t, limiter, ctx, "tenant:app:job:create", true)
	decision, err := limiter.Allow(ctx, "tenant:app:job:create", 1)
	if err != nil {
		t.Fatalf("3rd request errored: %v", err)
	}
	if decision.Allowed {
		t.Fatal("3rd request should be denied")
	}
	if decision.RetryAfter <= 0 {
		t.Fatalf("denied RetryAfter = %v, want > 0", decision.RetryAfter)
	}

	// New window -> counter resets.
	clock.Advance(time.Minute)
	mustAllow(t, limiter, ctx, "tenant:app:job:create", true)
}

func TestSQLitePeekAndIsolation(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(ctx, newSQLiteDB(t))
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	clock := newClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	store.SetClock(clock.Now)
	window := time.Minute
	ws := WindowStart(clock.Now(), window)

	if n, err := store.Peek(ctx, "k", ws); err != nil || n != 0 {
		t.Fatalf("Peek empty = (%d, %v), want (0, nil)", n, err)
	}
	if _, err := store.Increment(ctx, "k", ws, window, 3); err != nil {
		t.Fatalf("increment: %v", err)
	}
	if n, err := store.Peek(ctx, "k", ws); err != nil || n != 3 {
		t.Fatalf("Peek after increment = (%d, %v), want (3, nil)", n, err)
	}
	// Other key is isolated.
	if n, err := store.Peek(ctx, "other", ws); err != nil || n != 0 {
		t.Fatalf("Peek other key = (%d, %v), want (0, nil)", n, err)
	}
}

func TestSQLiteConcurrentIncrementRace(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(ctx, newSQLiteDB(t))
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	clock := newClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	const capacity = 80
	limiter := newTestLimiter(store, Policy{Limit: capacity, Window: time.Hour}, clock)

	const goroutines = 20
	const perGoroutine = 10
	allowedCh := make(chan bool, goroutines*perGoroutine)
	done := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		go func() {
			for i := 0; i < perGoroutine; i++ {
				decision, err := limiter.Allow(ctx, "shared", 1)
				if err != nil {
					t.Errorf("concurrent allow errored: %v", err)
					allowedCh <- false
					continue
				}
				allowedCh <- decision.Allowed
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < goroutines; g++ {
		<-done
	}
	close(allowedCh)
	allowed := 0
	for ok := range allowedCh {
		if ok {
			allowed++
		}
	}
	if allowed != capacity {
		t.Fatalf("allowed %d requests, want exactly %d", allowed, capacity)
	}
}
