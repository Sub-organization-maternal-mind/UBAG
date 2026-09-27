package idempotency

import (
	"context"
	"testing"
	"time"
)

func newTestMemoryStore(now func() time.Time) *MemoryStore {
	store := NewMemoryStore(time.Hour)
	store.now = now
	return store
}

// TestMemoryStoreCompleteIsCASOnRequestHash pins the guard that keeps a stale
// completion from being attributed to a record a different payload took over.
func TestMemoryStoreCompleteIsCASOnRequestHash(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	store := newTestMemoryStore(func() time.Time { return now })
	scope := Scope{TenantID: "t", AppID: "a", Operation: "jobs.create", Key: "key-cas-complete"}

	if _, err := store.Reserve(ctx, scope, "hash-one"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// Stale completion carrying the wrong payload hash must be discarded.
	if err := store.Complete(ctx, scope, "hash-two", "job-stale", 202); err != nil {
		t.Fatalf("stale complete: %v", err)
	}
	replay, err := store.Reserve(ctx, scope, "hash-one")
	if err != nil {
		t.Fatalf("replay after stale complete: %v", err)
	}
	if replay.Kind != DecisionReplay || replay.Record.ResourceID != "" || replay.Record.Status != RecordInFlight {
		t.Fatalf("stale complete mutated the record: %#v", replay)
	}

	if err := store.Complete(ctx, scope, "hash-one", "job-real", 202); err != nil {
		t.Fatalf("complete: %v", err)
	}
	replay, err = store.Reserve(ctx, scope, "hash-one")
	if err != nil {
		t.Fatalf("replay after complete: %v", err)
	}
	if replay.Kind != DecisionReplay || replay.Record.ResourceID != "job-real" || replay.Record.HTTPStatus != 202 || replay.Record.Status != RecordCompleted {
		t.Fatalf("completed replay mismatch: %#v", replay)
	}
}

// TestMemoryStoreReleaseIsCASOnRequestHash pins that a stale release after a
// different payload took the key over cannot delete the new owner's record.
func TestMemoryStoreReleaseIsCASOnRequestHash(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	store := newTestMemoryStore(func() time.Time { return now })
	scope := Scope{TenantID: "t", AppID: "a", Operation: "jobs.create", Key: "key-cas-release"}

	if _, err := store.Reserve(ctx, scope, "hash-one"); err != nil {
		t.Fatalf("reserve one: %v", err)
	}
	// The in-flight lock deadline passes: a different payload may take over.
	now = now.Add(defaultInFlightLock + time.Second)
	reserved, err := store.Reserve(ctx, scope, "hash-two")
	if err != nil {
		t.Fatalf("take-over reserve: %v", err)
	}
	if reserved.Kind != DecisionReserved {
		t.Fatalf("take-over decision = %s, want reserved", reserved.Kind)
	}
	// The stale owner's release must not drop the new owner's record.
	if err := store.Release(ctx, scope, "hash-one"); err != nil {
		t.Fatalf("stale release: %v", err)
	}
	replay, err := store.Reserve(ctx, scope, "hash-two")
	if err != nil {
		t.Fatalf("replay after stale release: %v", err)
	}
	if replay.Kind != DecisionReplay {
		t.Fatalf("stale release dropped the new owner's record: %s", replay.Kind)
	}
	if err := store.Release(ctx, scope, "hash-two"); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	reserved, err = store.Reserve(ctx, scope, "hash-three")
	if err != nil || reserved.Kind != DecisionReserved {
		t.Fatalf("reserve after owner release = %s err=%v, want reserved", reserved.Kind, err)
	}
}

// TestMemoryStoreSweepDeletesExpired pins the TTL sweep the expires_at index
// exists to serve: expired records disappear, live records stay.
func TestMemoryStoreSweepDeletesExpired(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	store := newTestMemoryStore(func() time.Time { return now })
	liveScope := Scope{TenantID: "t", AppID: "a", Operation: "jobs.create", Key: "key-live"}
	expiredScope := Scope{TenantID: "t", AppID: "a", Operation: "jobs.create", Key: "key-expired"}

	if _, err := store.Reserve(ctx, expiredScope, "hash-expired"); err != nil {
		t.Fatalf("reserve expired: %v", err)
	}
	now = now.Add(2 * time.Hour)
	// Reserved after the clock moved: this one stays live for the sweep.
	if _, err := store.Reserve(ctx, liveScope, "hash-live"); err != nil {
		t.Fatalf("reserve live: %v", err)
	}

	removed, err := store.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed != 1 {
		t.Fatalf("sweep removed %d records, want 1", removed)
	}
	replay, err := store.Reserve(ctx, liveScope, "hash-live")
	if err != nil || replay.Kind != DecisionReplay {
		t.Fatalf("live record must survive the sweep: %s err=%v", replay.Kind, err)
	}
}
