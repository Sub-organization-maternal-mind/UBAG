package outbox

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStore_AppendPendingMarkPublished(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	if err := s.Append(ctx, "e1", "jobs.dispatch", []byte(`{"job_id":"e1"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Append(ctx, "e2", "jobs.dispatch", []byte(`{"job_id":"e2"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	pending, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("want 2 pending, got %d", len(pending))
	}

	if err := s.MarkPublished(ctx, "e1"); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}

	pending, err = s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending after mark: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 pending after mark, got %d", len(pending))
	}
	if pending[0].ID != "e2" {
		t.Fatalf("want e2 pending, got %q", pending[0].ID)
	}
}

func TestMemoryStore_DuplicateAppendIdempotent(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	if err := s.Append(ctx, "dup", "t", []byte("x")); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	// Second append with same ID must be silently ignored (idempotent, matches DB ON CONFLICT DO NOTHING).
	if err := s.Append(ctx, "dup", "t", []byte("x")); err != nil {
		t.Fatalf("duplicate Append should be idempotent, got error: %v", err)
	}

	// Still only one event pending.
	pending, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 pending after idempotent append, got %d", len(pending))
	}
}

func TestMemoryStore_PendingLimit(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		if err := s.Append(ctx, id, "t", []byte(id)); err != nil {
			t.Fatalf("Append %q: %v", id, err)
		}
	}

	pending, err := s.Pending(ctx, 3)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("want 3, got %d", len(pending))
	}
}

func TestMemoryStore_MarkPublishedSetsTimestamp(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	before := time.Now().UTC().Add(-time.Second)
	if err := s.Append(ctx, "ts", "t", []byte("x")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.MarkPublished(ctx, "ts"); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}

	// Verify via public API: the event should no longer appear in Pending.
	pending, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	for _, e := range pending {
		if e.ID == "ts" {
			t.Fatal("event still in Pending after MarkPublished")
		}
	}

	// Verify PublishedAt was set by checking it is after before.
	// Access internals only for the timestamp assertion (same package white-box test).
	s.mu.Lock()
	var found *Event
	for i := range s.events {
		if s.events[i].ID == "ts" {
			found = &s.events[i]
			break
		}
	}
	s.mu.Unlock()

	if found == nil {
		t.Fatal("event not found in store")
	}
	if found.PublishedAt == nil {
		t.Fatal("PublishedAt is nil after MarkPublished")
	}
	if found.PublishedAt.Before(before) {
		t.Fatalf("PublishedAt %v is before expected lower bound %v", found.PublishedAt, before)
	}
}

func TestMemoryStore_Ready(t *testing.T) {
	s := NewMemoryStore()
	if err := s.Ready(context.Background()); err != nil {
		t.Fatalf("Ready: %v", err)
	}
}

func TestMemoryStore_MarkPublishedNotFound(t *testing.T) {
	s := NewMemoryStore()
	if err := s.MarkPublished(context.Background(), "nonexistent"); err == nil {
		t.Fatal("expected error for nonexistent id, got nil")
	}
}
