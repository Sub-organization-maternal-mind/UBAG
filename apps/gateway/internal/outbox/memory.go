package outbox

import (
	"context"
	"sync"
	"time"
)

type MemoryStore struct {
	mu     sync.Mutex
	events []Event
	now    func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{now: time.Now}
}

func (m *MemoryStore) Append(_ context.Context, id, topic string, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, e := range m.events {
		if e.ID == id {
			return nil // idempotent: same contract as ON CONFLICT DO NOTHING in DB backends
		}
	}
	m.events = append(m.events, Event{
		ID:        id,
		Topic:     topic,
		Payload:   payload,
		CreatedAt: m.now().UTC(),
	})
	return nil
}
