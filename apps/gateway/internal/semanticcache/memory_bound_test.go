package semanticcache

import (
	"context"
	"testing"
	"time"
)

// TestMemoryStoreBoundedEviction verifies the memory store evicts its
// oldest-CreatedAt entries once the configured bound is exceeded, so
// high-cardinality inputs cannot grow the map without limit.
func TestMemoryStoreBoundedEviction(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore().WithMaxEntries(2)

	base := time.Now().Add(-time.Hour)
	keys := []string{"one", "two", "three"}
	for index, word := range keys {
		entry := Entry{
			Output:    word,
			CreatedAt: base.Add(time.Duration(index) * time.Minute),
			ExpiresAt: base.Add(time.Duration(index) * time.Minute).Add(time.Hour),
		}
		if err := store.Put(ctx, CacheKey{Target: "mock", TenantID: "t1", AppID: "a1"}, []byte(word), entry); err != nil {
			t.Fatalf("put %s: %v", word, err)
		}
	}

	if _, ok, err := store.Get(ctx, CacheKey{Target: "mock", TenantID: "t1", AppID: "a1"}, []byte("one")); ok || err != nil {
		t.Fatalf("oldest entry should have been evicted, ok=%v err=%v", ok, err)
	}
	for _, word := range []string{"two", "three"} {
		if _, ok, err := store.Get(ctx, CacheKey{Target: "mock", TenantID: "t1", AppID: "a1"}, []byte(word)); !ok || err != nil {
			t.Fatalf("%s should remain, ok=%v err=%v", word, ok, err)
		}
	}
}
