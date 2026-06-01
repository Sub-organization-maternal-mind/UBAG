package region

import (
	"context"
	"testing"
)

// TestCurrentRegionFromEnv verifies that CurrentRegion reads UBAG_REGION.
func TestCurrentRegionFromEnv(t *testing.T) {
	t.Setenv("UBAG_REGION", "us-west-1")
	got := CurrentRegion()
	if got != Region("us-west-1") {
		t.Fatalf("CurrentRegion() = %q, want %q", got, "us-west-1")
	}
}

// TestCurrentRegionEmpty verifies that an unset UBAG_REGION returns "".
func TestCurrentRegionEmpty(t *testing.T) {
	t.Setenv("UBAG_REGION", "")
	got := CurrentRegion()
	if got != Region("") {
		t.Fatalf("CurrentRegion() = %q, want empty string", got)
	}
}

// TestUnknownRegionDefaultsToActive verifies safe default for unknown regions
// so cross-region routing doesn't break when a region hasn't been registered.
func TestUnknownRegionDefaultsToActive(t *testing.T) {
	reg := NewRegistry(NewMemoryStateStore())
	ctx := context.Background()
	state, err := reg.RegionState(ctx, Region("us-east-99"))
	if err != nil {
		t.Fatalf("RegionState on unknown region: %v", err)
	}
	if state != StateActive {
		t.Fatalf("unknown region state = %v, want StateActive", state)
	}
}

// TestValidTransitions verifies every allowed state machine edge.
func TestValidTransitions(t *testing.T) {
	cases := []struct {
		from, to State
	}{
		{StateActive, StateDraining},
		{StateActive, StateDisabled},
		{StateDraining, StateActive},
		{StateDraining, StateDisabled},
		{StateDisabled, StateActive},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"->"+string(tc.to), func(t *testing.T) {
			store := NewMemoryStateStore()
			reg := NewRegistry(store)
			ctx := context.Background()
			r := Region("test-region")

			// Seed the starting state directly in the store so we bypass the
			// transition guard for setup.
			if err := store.Set(r, tc.from); err != nil {
				t.Fatalf("seed state: %v", err)
			}
			if err := reg.SetState(ctx, r, tc.to); err != nil {
				t.Fatalf("SetState(%v→%v): %v", tc.from, tc.to, err)
			}
			got, err := reg.RegionState(ctx, r)
			if err != nil {
				t.Fatalf("RegionState after transition: %v", err)
			}
			if got != tc.to {
				t.Fatalf("state after transition = %v, want %v", got, tc.to)
			}
		})
	}
}

// TestInvalidTransitionDisabledToDraining verifies that disabled→draining is rejected.
func TestInvalidTransitionDisabledToDraining(t *testing.T) {
	store := NewMemoryStateStore()
	reg := NewRegistry(store)
	ctx := context.Background()
	r := Region("test-region")

	if err := store.Set(r, StateDisabled); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	err := reg.SetState(ctx, r, StateDraining)
	if err == nil {
		t.Fatal("SetState(disabled→draining) should return error, got nil")
	}
}

// TestIsHealthy verifies that IsHealthy returns true only for StateActive.
func TestIsHealthy(t *testing.T) {
	store := NewMemoryStateStore()
	reg := NewRegistry(store)
	ctx := context.Background()

	cases := []struct {
		state   State
		healthy bool
	}{
		{StateActive, true},
		{StateDraining, false},
		{StateDisabled, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			r := Region("region-" + string(tc.state))
			if err := store.Set(r, tc.state); err != nil {
				t.Fatalf("seed state: %v", err)
			}
			got := reg.IsHealthy(ctx, r)
			if got != tc.healthy {
				t.Fatalf("IsHealthy for %v = %v, want %v", tc.state, got, tc.healthy)
			}
		})
	}
}

// TestKnownRegions verifies that KnownRegions reflects all set regions.
func TestKnownRegions(t *testing.T) {
	store := NewMemoryStateStore()
	reg := NewRegistry(store)
	ctx := context.Background()

	regions := []Region{"eu-west-1", "ap-southeast-1", "us-east-1"}
	for _, r := range regions {
		if err := store.Set(r, StateActive); err != nil {
			t.Fatalf("seed %v: %v", r, err)
		}
	}

	known, err := reg.KnownRegions(ctx)
	if err != nil {
		t.Fatalf("KnownRegions: %v", err)
	}
	if len(known) != len(regions) {
		t.Fatalf("KnownRegions len = %d, want %d", len(known), len(regions))
	}
	set := make(map[Region]bool, len(known))
	for _, r := range known {
		set[r] = true
	}
	for _, r := range regions {
		if !set[r] {
			t.Fatalf("KnownRegions missing %v", r)
		}
	}
}

// TestSetStateOnUnknownRegion verifies that setting state on a new region works
// when starting from the "active" default (no entry in store).
func TestSetStateOnUnknownRegion(t *testing.T) {
	reg := NewRegistry(NewMemoryStateStore())
	ctx := context.Background()
	r := Region("brand-new")

	// active→draining should work since unknown defaults to active.
	if err := reg.SetState(ctx, r, StateDraining); err != nil {
		t.Fatalf("SetState on unknown region→draining: %v", err)
	}
	got, err := reg.RegionState(ctx, r)
	if err != nil {
		t.Fatalf("RegionState: %v", err)
	}
	if got != StateDraining {
		t.Fatalf("state = %v, want StateDraining", got)
	}
}
