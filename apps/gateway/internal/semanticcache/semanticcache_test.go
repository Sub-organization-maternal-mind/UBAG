package semanticcache

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStorePutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	key := CacheKey{Target: "chatgpt", CommandType: "submit", AppID: "app1", TenantID: "t1", Locale: "en"}
	input := []byte(`{"prompt":"hello world"}`)
	entry := Entry{
		Output: map[string]any{"text": "hi there"},
		Tags:   []string{"target:chatgpt"},
	}

	if err := s.Put(ctx, key, input, entry); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.Get(ctx, key, input)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: expected hit, got miss")
	}
	if got.CacheSource != "exact" {
		t.Errorf("CacheSource = %q, want exact", got.CacheSource)
	}
}

func TestMemoryStoreMissDifferentInput(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	key := CacheKey{Target: "t", CommandType: "c", AppID: "a", TenantID: "t1"}
	_ = s.Put(ctx, key, []byte("input1"), Entry{})
	_, ok, _ := s.Get(ctx, key, []byte("input2"))
	if ok {
		t.Error("different input must be a cache miss")
	}
}

func TestMemoryStoreExpiry(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	key := CacheKey{Target: "t", CommandType: "c", TenantID: "t1"}
	entry := Entry{ExpiresAt: time.Now().Add(-time.Second)} // already expired
	_ = s.Put(ctx, key, []byte("input"), entry)
	_, ok, _ := s.Get(ctx, key, []byte("input"))
	if ok {
		t.Error("expired entry must be a cache miss")
	}
}

func TestMemoryStoreInvalidateByTag(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	key1 := CacheKey{Target: "t1", CommandType: "c", TenantID: "tenant1"}
	key2 := CacheKey{Target: "t2", CommandType: "c", TenantID: "tenant1"}
	_ = s.Put(ctx, key1, []byte("i1"), Entry{Tags: []string{"target:t1", "shared"}})
	_ = s.Put(ctx, key2, []byte("i2"), Entry{Tags: []string{"target:t2"}})

	n, err := s.InvalidateByTag(ctx, "tenant1", "target:t1")
	if err != nil {
		t.Fatalf("InvalidateByTag: %v", err)
	}
	if n != 1 {
		t.Errorf("InvalidateByTag: removed %d, want 1", n)
	}
	_, ok, _ := s.Get(ctx, key1, []byte("i1"))
	if ok {
		t.Error("invalidated entry must be a cache miss")
	}
	_, ok, _ = s.Get(ctx, key2, []byte("i2"))
	if !ok {
		t.Error("unaffected entry must still be a cache hit")
	}
}

func TestMemoryStorePurge(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	for i := 0; i < 3; i++ {
		k := CacheKey{Target: "t", CommandType: "c", TenantID: "tenant_purge"}
		_ = s.Put(ctx, k, []byte{byte(i)}, Entry{})
	}
	_ = s.Put(ctx, CacheKey{Target: "t", CommandType: "c", TenantID: "other"}, []byte("x"), Entry{})

	n, err := s.Purge(ctx, "tenant_purge")
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n != 3 {
		t.Errorf("Purge: removed %d, want 3", n)
	}
	if snap := s.Snapshot(); len(snap) != 1 {
		t.Errorf("after purge, want 1 entry remaining, got %d", len(snap))
	}
}

func TestBuildExactKeyStability(t *testing.T) {
	key := CacheKey{Target: "t", CommandType: "c", AppID: "a", TenantID: "ten", Locale: "en"}
	k1 := buildExactKey(key, []byte("input"))
	k2 := buildExactKey(key, []byte("input"))
	if k1 != k2 {
		t.Error("buildExactKey must be stable for the same inputs")
	}
	k3 := buildExactKey(key, []byte("other"))
	if k1 == k3 {
		t.Error("buildExactKey must differ for different inputs")
	}
}
