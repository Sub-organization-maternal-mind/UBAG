package region

import (
	"context"
	"testing"
)

// TestMemoryHomeRegionResolverPinned verifies that a pinned tenant resolves to
// its configured home region, not to the current region.
func TestMemoryHomeRegionResolverPinned(t *testing.T) {
	t.Parallel()
	r := NewMemoryResolver(map[string]Region{
		"tenant-a": Region("eu-west-1"),
	})
	got, err := r.HomeRegion(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("HomeRegion: %v", err)
	}
	if got != Region("eu-west-1") {
		t.Fatalf("HomeRegion(pinned) = %q, want %q", got, "eu-west-1")
	}
}

// TestMemoryHomeRegionResolverUnpinned verifies that an unpinned tenant (not in
// the map) returns an empty Region.
func TestMemoryHomeRegionResolverUnpinned(t *testing.T) {
	t.Parallel()
	r := NewMemoryResolver(map[string]Region{
		"tenant-a": Region("eu-west-1"),
	})
	got, err := r.HomeRegion(context.Background(), "tenant-b")
	if err != nil {
		t.Fatalf("HomeRegion: %v", err)
	}
	if got != Region("") {
		t.Fatalf("HomeRegion(unpinned) = %q, want empty", got)
	}
}

// TestMemoryHomeRegionResolverEmptyTenantID verifies that an empty tenantID is
// handled safely and returns an empty Region (no panic, no error).
func TestMemoryHomeRegionResolverEmptyTenantID(t *testing.T) {
	t.Parallel()
	r := NewMemoryResolver(map[string]Region{
		"tenant-a": Region("eu-west-1"),
	})
	got, err := r.HomeRegion(context.Background(), "")
	if err != nil {
		t.Fatalf("HomeRegion(empty tenantID): %v", err)
	}
	if got != Region("") {
		t.Fatalf("HomeRegion(empty tenantID) = %q, want empty", got)
	}
}

// TestMemoryHomeRegionResolverNilMap verifies that a nil-initialised resolver
// (NewMemoryResolver(nil)) is safe to call.
func TestMemoryHomeRegionResolverNilMap(t *testing.T) {
	t.Parallel()
	r := NewMemoryResolver(nil)
	got, err := r.HomeRegion(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("HomeRegion on nil-map resolver: %v", err)
	}
	if got != Region("") {
		t.Fatalf("HomeRegion(nil map) = %q, want empty", got)
	}
}

// TestPinUnpinnedUsesCurrent verifies that Pin() falls back to current() when
// the resolver returns an empty region for that tenant.
func TestPinUnpinnedUsesCurrent(t *testing.T) {
	t.Parallel()
	resolver := NewMemoryResolver(nil) // no pins
	current := func() Region { return Region("us-east-1") }
	got := Pin(context.Background(), "tenant-x", resolver, current)
	if got != Region("us-east-1") {
		t.Fatalf("Pin(unpinned) = %q, want current %q", got, "us-east-1")
	}
}

// TestPinPinnedOverridesCurrent verifies that Pin() returns the pinned region
// and ignores current() when the tenant has a home region.
func TestPinPinnedOverridesCurrent(t *testing.T) {
	t.Parallel()
	resolver := NewMemoryResolver(map[string]Region{
		"tenant-y": Region("ap-southeast-1"),
	})
	current := func() Region { return Region("us-east-1") }
	got := Pin(context.Background(), "tenant-y", resolver, current)
	if got != Region("ap-southeast-1") {
		t.Fatalf("Pin(pinned) = %q, want pinned %q", got, "ap-southeast-1")
	}
}

// TestPinEmptyTenantIDReturnsCurrent verifies that a nil/empty tenantID falls
// through to current() rather than panicking or returning an error.
func TestPinEmptyTenantIDReturnsCurrent(t *testing.T) {
	t.Parallel()
	resolver := NewMemoryResolver(map[string]Region{
		"tenant-z": Region("eu-central-1"),
	})
	current := func() Region { return Region("us-west-2") }
	got := Pin(context.Background(), "", resolver, current)
	if got != Region("us-west-2") {
		t.Fatalf("Pin(empty tenantID) = %q, want current %q", got, "us-west-2")
	}
}

// TestMemoryHomeRegionResolverConcurrency verifies that concurrent reads on
// MemoryHomeRegionResolver do not race.
func TestMemoryHomeRegionResolverConcurrency(t *testing.T) {
	t.Parallel()
	r := NewMemoryResolver(map[string]Region{"t": Region("us-east-1")})
	ctx := context.Background()
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			r.HomeRegion(ctx, "t") //nolint:errcheck
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
