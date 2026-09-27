package idempotency

import (
	"context"
	"sync"
	"time"
)

const defaultTTL = 24 * time.Hour

type MemoryStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	records map[string]Record
}

func NewMemoryStore(ttl time.Duration) *MemoryStore {
	if ttl <= 0 {
		ttl = defaultTTL
	}

	return &MemoryStore{
		ttl:     ttl,
		now:     time.Now,
		records: make(map[string]Record),
	}
}

func (m *MemoryStore) Reserve(_ context.Context, scope Scope, requestHash string) (Decision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now().UTC()
	key := scope.CacheKey()

	if record, ok := m.records[key]; ok && record.ExpiresAt.After(now) {
		// A stale in-flight lock (the reserving process died before
		// Complete/Release and its lock deadline passed) is take-overable
		// regardless of payload hash. Legacy records without a lock deadline
		// keep the legacy behavior: replay or conflict until the TTL expires.
		lockLive := record.LockedUntil.IsZero() || record.LockedUntil.After(now)
		if record.Status != RecordInFlight || lockLive {
			if record.RequestHash != requestHash {
				return Decision{Kind: DecisionConflict, Record: record}, nil
			}

			return Decision{Kind: DecisionReplay, Record: record}, nil
		}
	}

	record := Record{
		Scope:       scope,
		RequestHash: requestHash,
		Status:      RecordInFlight,
		LockedUntil: now.Add(defaultInFlightLock),
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpiresAt:   now.Add(m.ttl),
	}
	m.records[key] = record

	return Decision{Kind: DecisionReserved, Record: record}, nil
}

func (m *MemoryStore) Complete(_ context.Context, scope Scope, requestHash string, resourceID string, httpStatus int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := scope.CacheKey()
	record, ok := m.records[key]
	// Compare-and-set on the payload hash: a stale completion after a
	// different payload took the key over is discarded, not applied.
	if !ok || record.RequestHash != requestHash {
		return nil
	}
	record.ResourceID = resourceID
	record.HTTPStatus = httpStatus
	record.Status = RecordCompleted
	record.LockedUntil = time.Time{}
	record.UpdatedAt = m.now().UTC()
	m.records[key] = record

	return nil
}

func (m *MemoryStore) Release(_ context.Context, scope Scope, requestHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := scope.CacheKey()
	if record, ok := m.records[key]; ok && record.RequestHash != requestHash {
		// A different payload owns the key now; do not delete its record.
		return nil
	}
	delete(m.records, key)
	return nil
}

// Sweep deletes every expired record and returns how many were removed.
func (m *MemoryStore) Sweep(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now().UTC()
	var removed int64
	for key, record := range m.records {
		if !record.ExpiresAt.After(now) {
			delete(m.records, key)
			removed++
		}
	}
	return removed, nil
}

func (m *MemoryStore) Ready(context.Context) error {
	return nil
}
