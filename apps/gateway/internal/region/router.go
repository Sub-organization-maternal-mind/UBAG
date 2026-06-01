package region

import (
	"context"
	"errors"
)

// ErrRegionMismatch is returned by Router.Route when the tenant's home region
// is specified but is not currently healthy (not in StateActive).
var ErrRegionMismatch = errors.New("region: home region unavailable for routing")

// Router resolves the dispatch region for a job given a tenant ID. It wraps a
// HomeRegionResolver to look up tenant pins and a Registry to check health.
type Router struct {
	resolver HomeRegionResolver
	registry *Registry
	current  func() Region
}

// NewRouter creates a Router that routes jobs to a tenant's pinned home region
// when that region is healthy, falls back to no-override when the tenant is
// unpinned or already local, and returns ErrRegionMismatch when the home region
// is pinned but unhealthy.
func NewRouter(resolver HomeRegionResolver, registry *Registry, current func() Region) *Router {
	return &Router{
		resolver: resolver,
		registry: registry,
		current:  current,
	}
}

// Route returns the region to dispatch a job to for the given tenantID.
//
//   - ("", nil)              — no routing override; use current region.
//   - (homeRegion, nil)      — route job to homeRegion (it differs from current and is healthy).
//   - ("", ErrRegionMismatch) — home region is pinned but unreachable.
func (r *Router) Route(ctx context.Context, tenantID string) (Region, error) {
	home, err := r.resolver.HomeRegion(ctx, tenantID)
	if err != nil || home == Region("") {
		// Unpinned tenant or transient resolver error — use current region.
		return Region(""), nil
	}

	current := r.current()
	if home == current {
		// Already local — no redirect needed.
		return Region(""), nil
	}

	if r.registry.IsHealthy(ctx, home) {
		return home, nil
	}

	return Region(""), ErrRegionMismatch
}
