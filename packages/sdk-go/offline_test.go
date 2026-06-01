package ubag

import "testing"

func TestOfflineQueueRoundTrip(t *testing.T) {
	store := NewMemoryOfflineStore()
	q := NewOfflineQueue(store)
	q.Enqueue(map[string]any{"target": "mock"})
	q.Enqueue(map[string]any{"target": "mock"})
	if q.Size() != 2 {
		t.Fatalf("expected 2, got %d", q.Size())
	}
	var sent int
	err := q.Flush(func(req map[string]any) error { sent++; return nil })
	if err != nil {
		t.Fatalf("flush error: %v", err)
	}
	if sent != 2 || q.Size() != 0 {
		t.Fatalf("expected 2 sent and empty queue, got sent=%d size=%d", sent, q.Size())
	}
}
