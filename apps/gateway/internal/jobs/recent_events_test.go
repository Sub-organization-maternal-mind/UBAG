package jobs

import (
	"context"
	"fmt"
	"testing"
)

// TestMemoryStoreRecentEvents verifies the bounded tail scan used by the hot
// status-poll path: it returns at most `limit` of the newest events, in
// ascending sequence order, and reports found=false for an unknown job.
func TestMemoryStoreRecentEvents(t *testing.T) {
	m := NewMemoryStore()
	m.jobs["job_1"] = Job{ID: "job_1", Status: StatusTokenStreaming}
	for i := 1; i <= 500; i++ {
		m.events["job_1"] = append(m.events["job_1"], Event{
			ID:       fmt.Sprintf("evt_%03d", i),
			JobID:    "job_1",
			Sequence: i,
			Type:     "token",
		})
	}

	got, found, err := m.RecentEvents(context.Background(), "job_1", 50)
	if err != nil {
		t.Fatalf("RecentEvents error: %v", err)
	}
	if !found {
		t.Fatalf("expected found=true for existing job")
	}
	if len(got) != 50 {
		t.Fatalf("expected 50 events, got %d", len(got))
	}
	// The newest 50 events (451..500) in ascending order.
	if got[0].Sequence != 451 || got[len(got)-1].Sequence != 500 {
		t.Fatalf("expected ascending tail 451..500, got %d..%d", got[0].Sequence, got[len(got)-1].Sequence)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Sequence <= got[i-1].Sequence {
			t.Fatalf("events not ascending at %d: %d <= %d", i, got[i].Sequence, got[i-1].Sequence)
		}
	}

	// Fewer events than the limit: return them all.
	small, found, err := m.RecentEvents(context.Background(), "job_1", 1000)
	if err != nil || !found {
		t.Fatalf("RecentEvents(1000) found=%v err=%v", found, err)
	}
	if len(small) != 500 {
		t.Fatalf("expected all 500 events, got %d", len(small))
	}

	// Unknown job: found=false.
	if _, found, _ := m.RecentEvents(context.Background(), "missing", 50); found {
		t.Fatalf("expected found=false for unknown job")
	}
}
