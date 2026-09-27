package sso

import (
	"testing"
	"time"
)

// TestMemoryStateStoreBoundedEviction verifies the in-memory authcode state
// store evicts its oldest-created entries once the bound is exceeded, so
// abandoned login attempts cannot grow the map without limit.
func TestMemoryStateStoreBoundedEviction(t *testing.T) {
	store := NewMemoryStateStore(time.Hour).WithMaxEntries(2)

	if err := store.Set("state-1", "nonce-1"); err != nil {
		t.Fatalf("set state-1: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := store.Set("state-2", "nonce-2"); err != nil {
		t.Fatalf("set state-2: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := store.Set("state-3", "nonce-3"); err != nil {
		t.Fatalf("set state-3: %v", err)
	}

	if _, ok := store.Consume("state-1"); ok {
		t.Fatal("state-1 should have been evicted")
	}
	for _, state := range []string{"state-2", "state-3"} {
		if _, ok := store.Consume(state); !ok {
			t.Fatalf("%s should remain", state)
		}
	}
}
