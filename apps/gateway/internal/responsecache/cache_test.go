package responsecache

import (
	"context"
	"testing"
	"time"
)

func TestCacheLookupAndStore(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore().WithClock(fixedClock(now))
	cache := New(store, Options{TTL: time.Hour, Enabled: true, Now: fixedClock(now)})

	req := LookupRequest{TenantID: "t1", AppID: "a1", Target: "mock", Command: "echo", Input: []byte("in")}
	if _, ok, err := cache.Lookup(ctx, req); err != nil || ok {
		t.Fatalf("expected initial miss, got ok=%v err=%v", ok, err)
	}
	if err := cache.Store(ctx, StoreRequest{TenantID: "t1", AppID: "a1", Target: "mock", Command: "echo", Input: []byte("in"), Value: []byte("result")}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	entry, ok, err := cache.Lookup(ctx, req)
	if err != nil || !ok {
		t.Fatalf("expected hit, got ok=%v err=%v", ok, err)
	}
	if string(entry.Value) != "result" {
		t.Fatalf("expected 'result', got %q", entry.Value)
	}
	if !entry.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expected TTL applied, got %v", entry.ExpiresAt)
	}
	if entry.InputHash != HashInput([]byte("in")) {
		t.Fatalf("input hash mismatch")
	}
}

func TestCacheTTLExpiryDeterministic(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{t: time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)}
	store := NewMemoryStore().WithClock(clock.now)
	cache := New(store, Options{TTL: time.Minute, Enabled: true, Now: clock.now})

	req := LookupRequest{TenantID: "t1", AppID: "a1", Input: []byte("in")}
	if err := cache.Store(ctx, StoreRequest{TenantID: "t1", AppID: "a1", Input: []byte("in"), Value: []byte("v")}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, ok, _ := cache.Lookup(ctx, req); !ok {
		t.Fatal("expected hit before TTL expiry")
	}
	clock.advance(90 * time.Second)
	if _, ok, _ := cache.Lookup(ctx, req); ok {
		t.Fatal("expected miss after TTL expiry")
	}
}

func TestCachePrivacyBypass(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore().WithClock(fixedClock(now))
	cache := New(store, Options{TTL: time.Hour, Enabled: true, Now: fixedClock(now)})

	// Privacy store must be a no-op.
	if err := cache.Store(ctx, StoreRequest{TenantID: "t1", AppID: "a1", Input: []byte("in"), Value: []byte("secret"), PrivacyMode: true}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// Non-privacy lookup must not find anything (nothing was written).
	if _, ok, _ := cache.Lookup(ctx, LookupRequest{TenantID: "t1", AppID: "a1", Input: []byte("in")}); ok {
		t.Fatal("privacy store wrote an entry")
	}

	// Seed a normal entry, then privacy lookup must still miss.
	if err := cache.Store(ctx, StoreRequest{TenantID: "t1", AppID: "a1", Input: []byte("in"), Value: []byte("cached")}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, ok, _ := cache.Lookup(ctx, LookupRequest{TenantID: "t1", AppID: "a1", Input: []byte("in"), PrivacyMode: true}); ok {
		t.Fatal("privacy lookup read from cache")
	}
	// Non-privacy lookup still hits.
	if _, ok, _ := cache.Lookup(ctx, LookupRequest{TenantID: "t1", AppID: "a1", Input: []byte("in")}); !ok {
		t.Fatal("expected non-privacy hit")
	}
}

func TestCacheDisabledBypass(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore().WithClock(fixedClock(now))
	cache := New(store, Options{TTL: time.Hour, Enabled: false, Now: fixedClock(now)})

	if cache.Enabled() {
		t.Fatal("expected cache disabled")
	}
	if err := cache.Store(ctx, StoreRequest{TenantID: "t1", AppID: "a1", Input: []byte("in"), Value: []byte("v")}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, ok, _ := cache.Lookup(ctx, LookupRequest{TenantID: "t1", AppID: "a1", Input: []byte("in")}); ok {
		t.Fatal("disabled cache returned a hit")
	}
	// Underlying store must be empty.
	if entries, _ := store.List(ctx, "t1", "a1", 10); len(entries) != 0 {
		t.Fatalf("disabled cache wrote %d entries", len(entries))
	}
}

func TestCacheStatsAndPurge(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore().WithClock(fixedClock(now))
	cache := New(store, Options{TTL: time.Hour, Enabled: true, Now: fixedClock(now)})

	_ = cache.Store(ctx, StoreRequest{TenantID: "t1", AppID: "a1", Input: []byte("in"), Value: []byte("v")})
	_, _, _ = cache.Lookup(ctx, LookupRequest{TenantID: "t1", AppID: "a1", Input: []byte("in")})

	stats, err := cache.Stats(ctx, "t1", "a1")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Entries != 1 || stats.Hits != 1 {
		t.Fatalf("unexpected stats %+v", stats)
	}
	removed, err := cache.Purge(ctx, "t1", "a1")
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 purged, got %d", removed)
	}
}
