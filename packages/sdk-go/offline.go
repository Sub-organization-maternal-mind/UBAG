package ubag

import (
	"fmt"
	"sync"
	"time"
)

type OfflineEntry struct {
	ID         string         `json:"id"`
	Request    map[string]any `json:"request"`
	EnqueuedAt string         `json:"enqueued_at"`
	Attempts   int            `json:"attempts"`
}

type OfflineStore interface {
	Read() []OfflineEntry
	Write(entries []OfflineEntry)
}

type MemoryOfflineStore struct {
	mu      sync.Mutex
	entries []OfflineEntry
}

func NewMemoryOfflineStore() *MemoryOfflineStore { return &MemoryOfflineStore{} }

func (m *MemoryOfflineStore) Read() []OfflineEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]OfflineEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

func (m *MemoryOfflineStore) Write(entries []OfflineEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = entries
}

type OfflineQueue struct {
	store   OfflineStore
	counter int
}

func NewOfflineQueue(store OfflineStore) *OfflineQueue { return &OfflineQueue{store: store} }

func (q *OfflineQueue) Enqueue(request map[string]any) OfflineEntry {
	entries := q.store.Read()
	q.counter++
	entry := OfflineEntry{
		ID:         fmt.Sprintf("q_%d_%d", time.Now().UnixNano(), q.counter),
		Request:    request,
		EnqueuedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	entries = append(entries, entry)
	q.store.Write(entries)
	return entry
}

// Flush sends entries FIFO; on the first sender error it persists the
// remaining entries and returns the error.
func (q *OfflineQueue) Flush(sender func(map[string]any) error) error {
	entries := q.store.Read()
	for len(entries) > 0 {
		if err := sender(entries[0].Request); err != nil {
			entries[0].Attempts++
			q.store.Write(entries)
			return err
		}
		entries = entries[1:]
		q.store.Write(entries)
	}
	return nil
}

func (q *OfflineQueue) Size() int { return len(q.store.Read()) }
