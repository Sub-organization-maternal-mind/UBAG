package responsecache

import (
	"context"
	"testing"
	"time"
)

// TestMemoryStoreBoundedEviction verifies the memory store evicts its
// oldest-CreatedAt entries once the configured bound is exceeded, so
// request-content-keyed entries cannot grow the map without limit.
func TestMemoryStoreBoundedEviction(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore().WithMaxEntries(2)

	base := time.Now().Add(-time.Hour)
	entries := []string{"key-1", "key-2", "key-3"}
	for index, key := range entries {
		if err := store.Set(ctx, Entry{
			Key:       key,
			TenantID:  "t1",
			AppID:     "a1",
			Value:     []byte(key),
			CreatedAt: base.Add(time.Duration(index) * time.Minute),
			ExpiresAt: base.Add(time.Duration(index) * time.Minute).Add(time.Hour),
		}); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	// Oldest (key-1) must be gone; the two newest remain.
	if _, ok, err := store.Get(ctx, "t1", "a1", "key-1"); ok || err != nil {
		t.Fatalf("key-1 should have been evicted, ok=%v err=%v", ok, err)
	}
	for _, key := range []string{"key-2", "key-3"} {
		if _, ok, err := store.Get(ctx, "t1", "a1", key); !ok || err != nil {
			t.Fatalf("%s should remain, ok=%v err=%v", key, ok, err)
		}
	}
}
