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
		if record.RequestHash != requestHash {
			return Decision{Kind: DecisionConflict, Record: record}, nil
		}

		return Decision{Kind: DecisionReplay, Record: record}, nil
	}

	record := Record{
		Scope:       scope,
		RequestHash: requestHash,
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpiresAt:   now.Add(m.ttl),
	}
	m.records[key] = record

	return Decision{Kind: DecisionReserved, Record: record}, nil
}

func (m *MemoryStore) Complete(_ context.Context, scope Scope, resourceID string, httpStatus int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := scope.CacheKey()
	record := m.records[key]
	record.ResourceID = resourceID
	record.HTTPStatus = httpStatus
	record.UpdatedAt = m.now().UTC()
	m.records[key] = record

	return nil
}

func (m *MemoryStore) Release(_ context.Context, scope Scope) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.records, scope.CacheKey())
	return nil
}

func (m *MemoryStore) Ready(context.Context) error {
	return nil
}
