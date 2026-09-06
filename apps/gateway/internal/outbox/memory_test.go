package outbox

import (
	"bytes"
	"context"
	"testing"
)

func TestMemoryStore_AppendIsIdempotent(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	payload := []byte(`{"k":"v"}`)
	if err := s.Append(ctx, "e1", "jobs.dispatch", payload); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := s.Append(ctx, "e1", "jobs.dispatch", payload); err != nil {
		t.Fatalf("second append (must be idempotent): %v", err)
	}

	// Internal dedupe check: appending the same id twice must not grow the list.
	s.mu.Lock()
	n := len(s.events)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("want 1 stored event, got %d", n)
	}
}

func TestMemoryStore_AppendPreservesPayload(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	payload := []byte(`{"job":"job_1","trace_id":"trace_1"}`)
	if err := s.Append(ctx, "job_1", "jobs.dispatch", payload); err != nil {
		t.Fatalf("append: %v", err)
	}

	s.mu.Lock()
	got := append([]byte(nil), s.events[0].Payload...)
	topic := s.events[0].Topic
	s.mu.Unlock()

	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %s", got)
	}
	if topic != "jobs.dispatch" {
		t.Fatalf("topic mismatch: got %q", topic)
	}
}
