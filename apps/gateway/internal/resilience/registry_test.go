package resilience

import (
	"sync"
	"testing"
)

// cfg returns a Config with a low failure threshold so tests can open a breaker
// cheaply without sleeping.
func testRegistryCfg() Config {
	cfg := DefaultConfig()
	cfg.FailureThreshold = 1
	return cfg
}

// TestRegistry_DistinctKeys verifies that two different (kind, target) pairs
// produce independent *Breaker instances.
func TestRegistry_DistinctKeys(t *testing.T) {
	r := NewRegistry(testRegistryCfg())

	b1 := r.Get(KindAdapter, "svc-a")
	b2 := r.Get(KindWebhook, "svc-a")

	if b1 == b2 {
		t.Fatal("expected distinct Breaker instances for different kinds; got the same pointer")
	}

	// Open b1 and ensure b2 remains closed.
	b1.RecordFailure()
	if b1.State() != StateOpen {
		t.Fatalf("b1 expected open, got %s", b1.State())
	}
	if b2.State() != StateClosed {
		t.Fatalf("b2 expected closed, got %s", b2.State())
	}
}

// TestRegistry_SameKey verifies that repeated calls with the same (kind, target)
// return the identical *Breaker instance (lazy singleton).
func TestRegistry_SameKey(t *testing.T) {
	r := NewRegistry(testRegistryCfg())

	b1 := r.Get(KindUpstream, "api.example.com")
	b2 := r.Get(KindUpstream, "api.example.com")

	if b1 != b2 {
		t.Fatal("expected same Breaker instance for repeated Get; got different pointers")
	}
}

// TestRegistry_SnapshotState verifies that Snapshot reflects the current state
// of each breaker — including one that has been opened.
func TestRegistry_SnapshotState(t *testing.T) {
	r := NewRegistry(testRegistryCfg())

	r.Get(KindAdapter, "closed-svc")              // stays closed
	r.Get(KindWebhook, "open-svc").RecordFailure() // trips open (threshold = 1)

	snaps := r.Snapshot()
	if len(snaps) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snaps))
	}

	states := make(map[string]State, len(snaps))
	for _, s := range snaps {
		states[string(s.Kind)+":"+s.Target] = s.State
	}

	if got := states["adapter:closed-svc"]; got != StateClosed {
		t.Errorf("adapter:closed-svc: expected closed, got %s", got)
	}
	if got := states["webhook:open-svc"]; got != StateOpen {
		t.Errorf("webhook:open-svc: expected open, got %s", got)
	}
}

// TestRegistry_EmptySnapshot verifies that Snapshot on a registry with no
// breakers returns a non-nil, empty slice (not nil).
func TestRegistry_EmptySnapshot(t *testing.T) {
	r := NewRegistry(DefaultConfig())
	snaps := r.Snapshot()

	if snaps == nil {
		t.Fatal("expected non-nil empty slice from Snapshot; got nil")
	}
	if len(snaps) != 0 {
		t.Fatalf("expected empty snapshot, got %d entries", len(snaps))
	}
}

// TestRegistry_ConcurrentGet verifies that concurrent Get calls for the same
// key never create duplicate breakers.
func TestRegistry_ConcurrentGet(t *testing.T) {
	const goroutines = 50
	r := NewRegistry(DefaultConfig())

	results := make([]*Breaker, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			results[idx] = r.Get(KindAdapter, "shared-target")
		}(i)
	}
	wg.Wait()

	first := results[0]
	for i, b := range results {
		if b != first {
			t.Fatalf("goroutine %d returned a different *Breaker instance; expected singleton", i)
		}
	}

	// Registry should contain exactly one breaker.
	snaps := r.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 breaker in registry after concurrent gets, got %d", len(snaps))
	}
}
