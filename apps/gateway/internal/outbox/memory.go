package outbox

import (
	"context"
	"fmt"
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

func (m *MemoryStore) MarkPublished(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now().UTC()
	for i, e := range m.events {
		if e.ID == id {
			m.events[i].PublishedAt = &now
			return nil
		}
	}
	return fmt.Errorf("outbox: event %q not found", id)
}

func (m *MemoryStore) Pending(_ context.Context, limit int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []Event
	for _, e := range m.events {
		if e.PublishedAt == nil {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MemoryStore) Ready(context.Context) error {
	return nil
}
