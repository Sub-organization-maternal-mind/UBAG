package nodes

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryStore is the in-process Store (dev, tests, single replica). State is
// lost on restart; production uses PostgresStore.
type MemoryStore struct {
	mu          sync.Mutex
	allocations map[string]Allocation
	states      map[string]HelperState
	registry    map[string]RegistryEntry
}

var _ Store = (*MemoryStore)(nil)

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		allocations: map[string]Allocation{},
		states:      map[string]HelperState{},
		registry:    map[string]RegistryEntry{},
	}
}

func (s *MemoryStore) Ready(context.Context) error { return nil }

func (s *MemoryStore) ApplyAllocation(_ context.Context, a Allocation, now time.Time) error {
	if err := a.Validate(); err != nil {
		return err
	}
	a.ValidUntil, a.AcceptedAt = a.ValidUntil.UTC(), now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.allocations[a.NodeID]
	switch {
	case !exists:
		if len(s.allocations) >= MaxNodes {
			return ErrTooManyNodes
		}
	case a.Generation < old.Generation:
		return ErrStaleGeneration
	case a.Generation == old.Generation && !a.sameExceptValidity(old):
		return ErrGenerationConflict
	}
	s.allocations[a.NodeID] = a
	if e, ok := s.registry[a.NodeID]; ok && a.State == StateRevoked && !e.Revoked() {
		e.RevokedAt, e.UpdatedAt = a.AcceptedAt, a.AcceptedAt
		s.registry[a.NodeID] = e
	}
	return nil
}

func (s *MemoryStore) GetAllocation(_ context.Context, nodeID string) (Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.allocations[nodeID]
	if !ok {
		return Allocation{}, ErrNotFound
	}
	return a, nil
}

func (s *MemoryStore) ListAllocations(context.Context) ([]Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Allocation, 0, len(s.allocations))
	for _, a := range s.allocations {
		out = append(out, a)
	}
	slices.SortFunc(out, func(x, y Allocation) int { return strings.Compare(x.NodeID, y.NodeID) })
	return out, nil
}

func (s *MemoryStore) PutState(_ context.Context, st HelperState) error {
	if err := st.validate(); err != nil {
		return err
	}
	st.LastHeartbeat, st.PressureCalmSince = st.LastHeartbeat.UTC(), utcOrZero(st.PressureCalmSince)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.allocations[st.NodeID]; !ok {
		return ErrNotFound
	}
	if old, ok := s.states[st.NodeID]; ok && old.LastHeartbeat.After(st.LastHeartbeat) {
		return nil
	}
	s.states[st.NodeID] = st
	return nil
}

func (s *MemoryStore) GetState(_ context.Context, nodeID string) (HelperState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[nodeID]
	if !ok {
		return HelperState{}, ErrNotFound
	}
	return st, nil
}

func (s *MemoryStore) PutRegistry(_ context.Context, e RegistryEntry, now time.Time) error {
	if err := e.validate(); err != nil {
		return err
	}
	e.RevokedAt, e.UpdatedAt = time.Time{}, now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.registry[e.NodeID]
	switch {
	case exists && old.Revoked(), s.allocations[e.NodeID].State == StateRevoked:
		return ErrNodeRevoked
	case !exists && len(s.registry) >= MaxNodes:
		return ErrTooManyNodes
	}
	s.registry[e.NodeID] = e
	return nil
}

func (s *MemoryStore) GetRegistry(_ context.Context, nodeID string) (RegistryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.registry[nodeID]
	if !ok {
		return RegistryEntry{}, ErrNotFound
	}
	return e, nil
}

func (s *MemoryStore) PromoteSPKI(_ context.Context, nodeID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.registry[nodeID]
	switch {
	case !ok:
		return ErrNotFound
	case e.Revoked():
		return ErrNodeRevoked
	case e.SPKINext == "":
		return ErrInvalid
	}
	e.SPKICurrent, e.SPKINext, e.UpdatedAt = e.SPKINext, "", now.UTC()
	s.registry[nodeID] = e
	return nil
}

func (s *MemoryStore) RevokeNode(_ context.Context, nodeID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.registry[nodeID]
	if !ok {
		return ErrNotFound
	}
	if !e.Revoked() {
		e.RevokedAt, e.UpdatedAt = now.UTC(), now.UTC()
		s.registry[nodeID] = e
	}
	return nil
}

func utcOrZero(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC()
}
