package region

import (
	"context"
	"testing"
)

// makeRouter is a helper that wires up a Router with an in-memory resolver and
// registry, using the provided pins (tenantID → Region) and state overrides.
func makeRouter(
	pins map[string]Region,
	states map[Region]State,
	currentRegion Region,
) *Router {
	store := NewMemoryStateStore()
	for r, s := range states {
		_ = store.Set(r, s)
	}
	registry := NewRegistry(store)
	resolver := NewMemoryResolver(pins)
	return NewRouter(resolver, registry, func() Region { return currentRegion })
}

// TestRouterPinnedTenantRoutesToHomeRegion verifies that a tenant pinned to a
// healthy region that differs from the current region gets routed there.
func TestRouterPinnedTenantRoutesToHomeRegion(t *testing.T) {
	t.Parallel()
	router := makeRouter(
		map[string]Region{"tenant-a": Region("us-west-1")},
		map[Region]State{Region("us-west-1"): StateActive},
		Region("eu-west-1"), // current region
	)
	got, err := router.Route(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("Route returned unexpected error: %v", err)
	}
	if got != Region("us-west-1") {
		t.Fatalf("Route() = %q, want %q", got, "us-west-1")
	}
}

// TestRouterUnpinnedTenantNoOverride verifies that an unpinned tenant gets no
// routing override (returns "").
func TestRouterUnpinnedTenantNoOverride(t *testing.T) {
	t.Parallel()
	router := makeRouter(
		map[string]Region{}, // no pins
		map[Region]State{},
		Region("eu-west-1"),
	)
	got, err := router.Route(context.Background(), "tenant-unpinned")
	if err != nil {
		t.Fatalf("Route returned unexpected error: %v", err)
	}
	if got != Region("") {
		t.Fatalf("Route() = %q, want empty (no override)", got)
	}
}

// TestRouterSameRegionNoOverride verifies that when the home region equals the
// current region, Route returns ("", nil) — no redirect needed.
func TestRouterSameRegionNoOverride(t *testing.T) {
	t.Parallel()
	router := makeRouter(
		map[string]Region{"tenant-b": Region("us-east-1")},
		map[Region]State{Region("us-east-1"): StateActive},
		Region("us-east-1"), // same as home
	)
	got, err := router.Route(context.Background(), "tenant-b")
	if err != nil {
		t.Fatalf("Route returned unexpected error: %v", err)
	}
	if got != Region("") {
		t.Fatalf("Route() = %q, want empty (already local)", got)
	}
}

// TestRouterHomeRegionUnhealthyReturnsMismatch verifies that when the home
// region is pinned but not in StateActive, ErrRegionMismatch is returned.
func TestRouterHomeRegionUnhealthyReturnsMismatch(t *testing.T) {
	t.Parallel()
	for _, state := range []State{StateDraining, StateDisabled} {
		state := state
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			router := makeRouter(
				map[string]Region{"tenant-c": Region("ap-southeast-1")},
				map[Region]State{Region("ap-southeast-1"): state},
				Region("eu-west-1"),
			)
			got, err := router.Route(context.Background(), "tenant-c")
			if err != ErrRegionMismatch {
				t.Fatalf("Route() error = %v, want ErrRegionMismatch", err)
			}
			if got != Region("") {
				t.Fatalf("Route() region = %q, want empty on error", got)
			}
		})
	}
}

// TestRouterEmptyTenantIDNoOverride verifies that an empty tenantID is treated
// as unpinned and returns no override.
func TestRouterEmptyTenantIDNoOverride(t *testing.T) {
	t.Parallel()
	router := makeRouter(
		map[string]Region{},
		map[Region]State{},
		Region("us-east-1"),
	)
	got, err := router.Route(context.Background(), "")
	if err != nil {
		t.Fatalf("Route('') returned unexpected error: %v", err)
	}
	if got != Region("") {
		t.Fatalf("Route('') = %q, want empty", got)
	}
}
