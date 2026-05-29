package ratelimit

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is an in-process fixed-window counter store backed by a mutex. It
// is intended for single-node deployments and tests. Expired windows are pruned
// lazily on each Increment so memory stays bounded by the number of active keys.
type MemoryStore struct {
	mu       sync.Mutex
	counters map[counterKey]int
	expiry   map[counterKey]time.Time
	now      func() time.Time
}

type counterKey struct {
	key         string
	windowStart int64 // UnixNano of the window start
}

// NewMemoryStore builds an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		counters: map[counterKey]int{},
		expiry:   map[counterKey]time.Time{},
		now:      time.Now,
	}
}

// SetClock overrides the time source used for pruning. Intended for tests.
func (m *MemoryStore) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	m.mu.Lock()
	m.now = now
	m.mu.Unlock()
}

// Increment adds cost to the (key, windowStart) counter and returns the new total.
func (m *MemoryStore) Increment(_ context.Context, key string, windowStart time.Time, window time.Duration, cost int) (int, error) {
	if cost <= 0 {
		cost = 1
	}
	ck := counterKey{key: key, windowStart: windowStart.UTC().UnixNano()}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(window)
	m.counters[ck] += cost
	if window > 0 {
		m.expiry[ck] = windowStart.UTC().Add(window)
	}
	return m.counters[ck], nil
}

// Peek returns the current count for (key, windowStart) without mutating it.
func (m *MemoryStore) Peek(_ context.Context, key string, windowStart time.Time) (int, error) {
	ck := counterKey{key: key, windowStart: windowStart.UTC().UnixNano()}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[ck], nil
}

// pruneLocked removes counters whose window has fully elapsed. Caller holds mu.
func (m *MemoryStore) pruneLocked(window time.Duration) {
	if len(m.expiry) == 0 {
		return
	}
	now := m.now().UTC()
	for ck, expiresAt := range m.expiry {
		if !expiresAt.After(now) {
			delete(m.counters, ck)
			delete(m.expiry, ck)
		}
	}
}
