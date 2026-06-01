// Package region provides region identity, state management, and a registry
// for multi-region routing in the UBAG gateway.
//
// A blank Region value ("") represents the single-region default, which is
// appropriate for deployments that don't need multi-region awareness.
package region

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// Region identifies a deployment region. An empty string means single-region
// (default) mode.
type Region string

// State represents the operational state of a region.
type State string

const (
	// StateActive means the region is healthy and accepting traffic.
	StateActive State = "active"
	// StateDraining means the region is winding down — no new sessions, but
	// existing ones are allowed to finish.
	StateDraining State = "draining"
	// StateDisabled means the region is offline and should not receive traffic.
	StateDisabled State = "disabled"
)

// CurrentRegion returns the region this process is running in by reading the
// UBAG_REGION environment variable. Returns Region("") when the variable is
// unset or empty, which indicates single-region mode.
func CurrentRegion() Region {
	return Region(os.Getenv("UBAG_REGION"))
}

// StateStore is the storage abstraction for region state. Implementations must
// be safe for concurrent use.
type StateStore interface {
	// Get returns the current State for region r. Implementations MUST return
	// StateActive (not an error) when r is unknown — callers rely on this for
	// safe-default routing.
	Get(r Region) (State, error)
	// Set stores the state for region r.
	Set(r Region, s State) error
	// List returns a snapshot of all region→state mappings.
	List() (map[Region]State, error)
}

// MemoryStateStore is a thread-safe, in-memory implementation of StateStore.
// It is the only backing store for now; SQL/Redis backends will be added later.
type MemoryStateStore struct {
	mu     sync.RWMutex
	states map[Region]State
}

// NewMemoryStateStore allocates an empty MemoryStateStore.
func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{states: make(map[Region]State)}
}

// Get returns the state for r, or StateActive if r is unknown.
func (m *MemoryStateStore) Get(r Region) (State, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.states[r]
	if !ok {
		return StateActive, nil
	}
	return s, nil
}

// Set stores state s for region r.
func (m *MemoryStateStore) Set(r Region, s State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[r] = s
	return nil
}

// List returns a copy of the entire state map.
func (m *MemoryStateStore) List() (map[Region]State, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make(map[Region]State, len(m.states))
	for k, v := range m.states {
		cp[k] = v
	}
	return cp, nil
}

// validTransitions encodes the allowed state machine edges.
// disabled→draining is explicitly absent.
var validTransitions = map[State]map[State]bool{
	StateActive: {
		StateDraining: true,
		StateDisabled: true,
	},
	StateDraining: {
		StateActive:   true,
		StateDisabled: true,
	},
	StateDisabled: {
		StateActive: true,
		// StateDisabled→StateDraining is intentionally omitted (invalid).
	},
}

// Registry manages region state using a pluggable StateStore.
type Registry struct {
	store StateStore
}

// NewRegistry creates a Registry backed by store.
func NewRegistry(store StateStore) *Registry {
	return &Registry{store: store}
}

// RegionState returns the current state of region r. If r is unknown it returns
// StateActive, which is the safe default for routing (unknown ≠ unhealthy).
func (reg *Registry) RegionState(_ context.Context, r Region) (State, error) {
	return reg.store.Get(r)
}

// SetState transitions region r to state next. It enforces the allowed
// transitions:
//
//	active    → draining, disabled
//	draining  → active, disabled
//	disabled  → active
//
// disabled→draining is explicitly forbidden because a disabled region should
// not be returned to service incrementally without first re-enabling it.
func (reg *Registry) SetState(_ context.Context, r Region, next State) error {
	current, err := reg.store.Get(r)
	if err != nil {
		return fmt.Errorf("region %q: get current state: %w", r, err)
	}
	allowed, ok := validTransitions[current]
	if !ok || !allowed[next] {
		return fmt.Errorf("region %q: invalid state transition %s→%s", r, current, next)
	}
	return reg.store.Set(r, next)
}

// IsHealthy reports whether region r is in StateActive.
func (reg *Registry) IsHealthy(_ context.Context, r Region) bool {
	s, err := reg.store.Get(r)
	if err != nil {
		return false
	}
	return s == StateActive
}

// KnownRegions returns all regions that have been explicitly registered in the
// store. Regions that have only been queried (and therefore defaulted to
// StateActive) are not included.
func (reg *Registry) KnownRegions(_ context.Context) ([]Region, error) {
	all, err := reg.store.List()
	if err != nil {
		return nil, err
	}
	regions := make([]Region, 0, len(all))
	for r := range all {
		regions = append(regions, r)
	}
	return regions, nil
}
