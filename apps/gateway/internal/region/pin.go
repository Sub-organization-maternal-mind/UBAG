package region

import (
	"context"
	"sync"
)

// HomeRegionResolver resolves the home region for a given tenant. Implementations
// must be safe for concurrent use.
//
// HomeRegion returns the tenant's pinned home Region, or Region("") when the
// tenant has no pin. A non-nil error indicates a transient lookup failure; the
// caller should treat that as unpinned (fall back to current region).
type HomeRegionResolver interface {
	HomeRegion(ctx context.Context, tenantID string) (Region, error)
}

// MemoryHomeRegionResolver is a thread-safe, in-memory implementation of
// HomeRegionResolver backed by a plain map. It is intended for tests and
// single-binary deployments; SQL-backed resolvers will be added later.
type MemoryHomeRegionResolver struct {
	mu   sync.RWMutex
	pins map[string]Region
}

// NewMemoryResolver creates a MemoryHomeRegionResolver pre-populated with the
// provided pins. A nil pins map is safe — it behaves as an empty resolver.
func NewMemoryResolver(pins map[string]Region) *MemoryHomeRegionResolver {
	cp := make(map[string]Region, len(pins))
	for k, v := range pins {
		cp[k] = v
	}
	return &MemoryHomeRegionResolver{pins: cp}
}

// HomeRegion returns the pinned region for tenantID, or Region("") if the
// tenant has no pin (including when tenantID is empty).
func (m *MemoryHomeRegionResolver) HomeRegion(_ context.Context, tenantID string) (Region, error) {
	if tenantID == "" {
		return Region(""), nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pins[tenantID], nil
}

// Pin returns the tenant's home region when one is set, or falls back to
// current() when the tenant is unpinned (empty region) or when the resolver
// returns an error.
//
// Parameters:
//   - ctx:      request context forwarded to the resolver.
//   - tenantID: the tenant being routed; empty string is treated as unpinned.
//   - resolver: the HomeRegionResolver to query.
//   - current:  a func that returns the local region; called only when the
//     tenant is unpinned.
func Pin(ctx context.Context, tenantID string, resolver HomeRegionResolver, current func() Region) Region {
	if tenantID == "" {
		return current()
	}
	home, err := resolver.HomeRegion(ctx, tenantID)
	if err != nil || home == Region("") {
		return current()
	}
	return home
}
