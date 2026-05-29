package webhooks

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStoreEnqueueLeaseAndMarkLifecycle(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	delivery, inserted, err := store.Enqueue(context.Background(), EnqueueRequest{
		TenantID:      "tenant_a",
		AppID:         "app_a",
		JobID:         "job_1",
		EventName:     "job.completed",
		URL:           "https://example.com/callback",
		SecretID:      "wh_sec_test",
		DedupeKey:     "job-terminal:job_1:completed",
		Payload:       []byte(`{"ok":true}`),
		MaxAttempts:   3,
		NextAttemptAt: now,
	})
	if err != nil || !inserted {
		t.Fatalf("Enqueue inserted=%v err=%v", inserted, err)
	}
	duplicate, inserted, err := store.Enqueue(context.Background(), EnqueueRequest{
		TenantID:      "tenant_a",
		AppID:         "app_a",
		JobID:         "job_1",
		EventName:     "job.completed",
		URL:           "https://example.com/callback",
		SecretID:      "wh_sec_test",
		DedupeKey:     "job-terminal:job_1:completed",
		Payload:       []byte(`{"ok":true}`),
		NextAttemptAt: now,
	})
	if err != nil || inserted || duplicate.ID != delivery.ID {
		t.Fatalf("duplicate inserted=%v delivery=%#v err=%v", inserted, duplicate, err)
	}
	leased, err := store.LeaseDue(context.Background(), "worker-a", 1, time.Minute)
	if err != nil || len(leased) != 1 {
		t.Fatalf("LeaseDue len=%d err=%v", len(leased), err)
	}
	if leased[0].Status != StatusLeased || leased[0].LeaseID == "" {
		t.Fatalf("unexpected lease: %#v", leased[0])
	}
	if err := store.MarkDelivered(context.Background(), leased[0].ID, leased[0].LeaseID, AttemptResult{StatusCode: 204, ErrorClass: "none"}); err != nil {
		t.Fatalf("MarkDelivered returned error: %v", err)
	}
	loaded, found, err := store.Get(context.Background(), "tenant_a", "app_a", delivery.ID)
	if err != nil || !found || loaded.Status != StatusDelivered || loaded.AttemptCount != 1 {
		t.Fatalf("loaded=%#v found=%v err=%v", loaded, found, err)
	}
}

func TestMemoryStoreReplayRequiresScopedDelivery(t *testing.T) {
	store := NewMemoryStore()
	_, _, err := store.Enqueue(context.Background(), EnqueueRequest{
		TenantID:  "tenant_a",
		AppID:     "app_a",
		EventName: "job.completed",
		URL:       "https://example.com/callback",
		SecretID:  "wh_sec_test",
		DedupeKey: "job-terminal:job_1:completed",
		Payload:   []byte(`{"ok":true}`),
	})
	if err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if _, found, err := store.Replay(context.Background(), "tenant_b", "app_a", "missing", "idem", time.Now()); err != nil || found {
		t.Fatalf("cross-scope replay found=%v err=%v", found, err)
	}
}
