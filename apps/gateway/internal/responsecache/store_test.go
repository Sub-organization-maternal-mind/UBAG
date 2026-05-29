package responsecache

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func mustSet(t *testing.T, store Store, entry Entry) {
	t.Helper()
	if err := store.Set(context.Background(), entry); err != nil {
		t.Fatalf("Set: %v", err)
	}
}

// storeFactory builds a fresh Store bound to the provided clock so the same
// suite exercises MemoryStore and SQLiteStore identically.
type storeFactory func(t *testing.T, now func() time.Time) Store

func memoryFactory(t *testing.T, now func() time.Time) Store {
	return NewMemoryStore().WithClock(now)
}

func sqliteFactory(t *testing.T, now func() time.Time) Store {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "cache.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store := NewSQLiteStore(db).WithClock(now)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return store
}

func eachStore(t *testing.T, run func(t *testing.T, factory storeFactory)) {
	t.Helper()
	factories := map[string]storeFactory{
		"memory": memoryFactory,
		"sqlite": sqliteFactory,
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			run(t, factory)
		})
	}
}

func TestBuildKeyDeterministicAndScoped(t *testing.T) {
	a := BuildKey("t1", "a1", "mock", "echo", []byte(`{"x":1}`))
	b := BuildKey("t1", "a1", "mock", "echo", []byte(`{"x":1}`))
	if a != b {
		t.Fatalf("expected deterministic key, got %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(a))
	}
	// Field boundaries must not collide.
	if BuildKey("ab", "c", "t", "cmd", nil) == BuildKey("a", "bc", "t", "cmd", nil) {
		t.Fatal("field boundary collision in BuildKey")
	}
	// Different tenant => different key.
	if BuildKey("t1", "a1", "mock", "echo", nil) == BuildKey("t2", "a1", "mock", "echo", nil) {
		t.Fatal("expected tenant to affect key")
	}
}

func TestStoreHitMiss(t *testing.T) {
	eachStore(t, func(t *testing.T, factory storeFactory) {
		ctx := context.Background()
		now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
		store := factory(t, fixedClock(now))
		key := BuildKey("t1", "a1", "mock", "echo", []byte("in"))

		if _, ok, err := store.Get(ctx, "t1", "a1", key); err != nil || ok {
			t.Fatalf("expected miss, got ok=%v err=%v", ok, err)
		}
		mustSet(t, store, Entry{Key: key, TenantID: "t1", AppID: "a1", Target: "mock", Command: "echo", InputHash: HashInput([]byte("in")), Value: []byte("result"), CreatedAt: now, ExpiresAt: now.Add(time.Hour)})

		got, ok, err := store.Get(ctx, "t1", "a1", key)
		if err != nil || !ok {
			t.Fatalf("expected hit, got ok=%v err=%v", ok, err)
		}
		if string(got.Value) != "result" {
			t.Fatalf("expected value 'result', got %q", got.Value)
		}
		if !got.ExpiresAt.Equal(now.Add(time.Hour)) {
			t.Fatalf("expiry mismatch: %v", got.ExpiresAt)
		}
	})
}

func TestStoreTTLExpiry(t *testing.T) {
	eachStore(t, func(t *testing.T, factory storeFactory) {
		ctx := context.Background()
		clock := &mutableClock{t: time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)}
		store := factory(t, clock.now)
		key := BuildKey("t1", "a1", "mock", "echo", []byte("in"))
		mustSet(t, store, Entry{Key: key, TenantID: "t1", AppID: "a1", Value: []byte("v"), CreatedAt: clock.t, ExpiresAt: clock.t.Add(time.Minute)})

		if _, ok, _ := store.Get(ctx, "t1", "a1", key); !ok {
			t.Fatal("expected hit before expiry")
		}
		clock.advance(2 * time.Minute)
		if _, ok, _ := store.Get(ctx, "t1", "a1", key); ok {
			t.Fatal("expected miss after expiry")
		}
		// Expired entry should also be excluded from List and Stats.
		entries, err := store.List(ctx, "t1", "a1", 10)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("expected 0 live entries, got %d", len(entries))
		}
	})
}

func TestStoreScopeIsolation(t *testing.T) {
	eachStore(t, func(t *testing.T, factory storeFactory) {
		ctx := context.Background()
		now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
		store := factory(t, fixedClock(now))
		key := BuildKey("tenantA", "a1", "mock", "echo", []byte("in"))
		mustSet(t, store, Entry{Key: key, TenantID: "tenantA", AppID: "a1", Value: []byte("secretA"), CreatedAt: now})

		// Same key string, different tenant must not read tenant A's value.
		if _, ok, _ := store.Get(ctx, "tenantB", "a1", key); ok {
			t.Fatal("tenant B read tenant A entry")
		}
		// Different app within same tenant is isolated too.
		if _, ok, _ := store.Get(ctx, "tenantA", "a2", key); ok {
			t.Fatal("app a2 read app a1 entry")
		}
		if _, ok, _ := store.Get(ctx, "tenantA", "a1", key); !ok {
			t.Fatal("tenant A could not read its own entry")
		}
	})
}

func TestStorePurge(t *testing.T) {
	eachStore(t, func(t *testing.T, factory storeFactory) {
		ctx := context.Background()
		now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
		store := factory(t, fixedClock(now))
		for i := 0; i < 3; i++ {
			key := BuildKey("t1", "a1", "mock", "echo", []byte(fmt.Sprintf("in%d", i)))
			mustSet(t, store, Entry{Key: key, TenantID: "t1", AppID: "a1", Value: []byte("v"), CreatedAt: now})
		}
		otherKey := BuildKey("t1", "a2", "mock", "echo", []byte("in"))
		mustSet(t, store, Entry{Key: otherKey, TenantID: "t1", AppID: "a2", Value: []byte("v"), CreatedAt: now})

		removed, err := store.Purge(ctx, "t1", "a1")
		if err != nil {
			t.Fatalf("Purge: %v", err)
		}
		if removed != 3 {
			t.Fatalf("expected 3 removed, got %d", removed)
		}
		entries, _ := store.List(ctx, "t1", "a1", 10)
		if len(entries) != 0 {
			t.Fatalf("expected scope empty after purge, got %d", len(entries))
		}
		// Other scope untouched.
		if _, ok, _ := store.Get(ctx, "t1", "a2", otherKey); !ok {
			t.Fatal("purge removed entries from other scope")
		}
	})
}

func TestStoreStatsCounting(t *testing.T) {
	eachStore(t, func(t *testing.T, factory storeFactory) {
		ctx := context.Background()
		now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
		store := factory(t, fixedClock(now))
		key := BuildKey("t1", "a1", "mock", "echo", []byte("in"))

		// 2 misses.
		_, _, _ = store.Get(ctx, "t1", "a1", key)
		_, _, _ = store.Get(ctx, "t1", "a1", key)
		mustSet(t, store, Entry{Key: key, TenantID: "t1", AppID: "a1", Value: []byte("v"), CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
		// 3 hits.
		for i := 0; i < 3; i++ {
			_, _, _ = store.Get(ctx, "t1", "a1", key)
		}

		stats, err := store.Stats(ctx, "t1", "a1")
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if stats.Entries != 1 {
			t.Fatalf("expected 1 entry, got %d", stats.Entries)
		}
		if stats.Hits != 3 {
			t.Fatalf("expected 3 hits, got %d", stats.Hits)
		}
		if stats.Misses != 2 {
			t.Fatalf("expected 2 misses, got %d", stats.Misses)
		}
	})
}

func TestStoreListOrderingAndLimit(t *testing.T) {
	eachStore(t, func(t *testing.T, factory storeFactory) {
		ctx := context.Background()
		base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
		store := factory(t, fixedClock(base.Add(time.Hour)))
		for i := 0; i < 5; i++ {
			key := BuildKey("t1", "a1", "mock", "echo", []byte(fmt.Sprintf("in%d", i)))
			mustSet(t, store, Entry{Key: key, TenantID: "t1", AppID: "a1", Value: []byte("v"), CreatedAt: base.Add(time.Duration(i) * time.Minute), ExpiresAt: base.Add(2 * time.Hour)})
		}
		entries, err := store.List(ctx, "t1", "a1", 3)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(entries) != 3 {
			t.Fatalf("expected 3 entries (limit), got %d", len(entries))
		}
		// Newest first.
		if !entries[0].CreatedAt.After(entries[1].CreatedAt) {
			t.Fatalf("expected newest-first ordering")
		}
	})
}

func TestStoreDelete(t *testing.T) {
	eachStore(t, func(t *testing.T, factory storeFactory) {
		ctx := context.Background()
		now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
		store := factory(t, fixedClock(now))
		key := BuildKey("t1", "a1", "mock", "echo", []byte("in"))
		mustSet(t, store, Entry{Key: key, TenantID: "t1", AppID: "a1", Value: []byte("v"), CreatedAt: now})
		if err := store.Delete(ctx, "t1", "a1", key); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, ok, _ := store.Get(ctx, "t1", "a1", key); ok {
			t.Fatal("expected miss after delete")
		}
	})
}

type mutableClock struct {
	t time.Time
}

func (c *mutableClock) now() time.Time { return c.t }

func (c *mutableClock) advance(d time.Duration) { c.t = c.t.Add(d) }
