package webhooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/resilience"
)

func TestDeliveryWorkerSignsAndDeliversWebhook(t *testing.T) {
	received := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store := NewMemoryStore()
	store.now = func() time.Time { return time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC) }
	delivery, _, err := store.Enqueue(context.Background(), EnqueueRequest{
		TenantID:      "tenant_a",
		AppID:         "app_a",
		JobID:         "job_1",
		EventName:     "job.completed",
		URL:           server.URL,
		SecretID:      "wh_sec_test",
		DedupeKey:     "job-terminal:job_1:completed",
		Payload:       []byte(`{"api_version":"2026-05-22"}`),
		MaxAttempts:   3,
		NextAttemptAt: store.now(),
	})
	if err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	worker := DeliveryWorker{
		Store: store,
		Sender: HTTPSender{
			Client:         server.Client(),
			SecretResolver: StaticSecretResolver{"wh_sec_test": "secret_fixture"},
			URLPolicy:      URLPolicy{AllowInsecureHTTP: true, AllowPrivateHosts: true, AllowedHosts: []string{"127.0.0.1"}},
			Now:            func() time.Time { return time.Unix(1700000000, 0) },
			APIVersion:     "2026-05-22",
		},
		WorkerID:    "worker-a",
		BatchSize:   1,
		LeaseFor:    time.Minute,
		RetryPolicy: RetryPolicy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: time.Minute},
	}
	processed, err := worker.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	request := <-received
	if request.Header.Get(SignatureHeader) == "" || request.Header.Get(NonceHeader) == "" || request.Header.Get(DeliveryIDHeader) != delivery.ID {
		t.Fatalf("missing webhook headers: %#v", request.Header)
	}
	loaded, found, err := store.Get(context.Background(), "tenant_a", "app_a", delivery.ID)
	if err != nil || !found || loaded.Status != StatusDelivered {
		t.Fatalf("delivery not delivered: loaded=%#v found=%v err=%v", loaded, found, err)
	}
}

// TestDeliveryWorker_CircuitOpenUsesMarkRetry verifies that when a delivery
// comes back with ErrorClass="circuit_open" the worker calls MarkRetry (not
// MarkDeadLetter) and schedules a future retry time driven by the breaker's
// cooldown.
func TestDeliveryWorker_CircuitOpenUsesMarkRetry(t *testing.T) {
	// Server always returns 500 so we can exhaust the threshold.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	store.now = func() time.Time { return now }

	delivery, _, err := store.Enqueue(context.Background(), EnqueueRequest{
		TenantID:      "tenant_a",
		AppID:         "app_a",
		EventName:     "job.completed",
		URL:           server.URL,
		SecretID:      "wh_sec_test",
		DedupeKey:     "job-terminal:job_1:completed",
		Payload:       []byte(`{"ok":true}`),
		MaxAttempts:   10, // large so we don't hit dead-letter via retry count
		NextAttemptAt: now,
	})
	if err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}

	cfg := resilience.Config{
		FailureThreshold:    2,
		SuccessBudget:       1,
		CooldownBase:        30 * time.Second,
		CooldownMax:         60 * time.Second,
		HalfOpenMaxInflight: 1,
	}
	registry := resilience.NewRegistry(cfg)
	sender := HTTPSender{
		Client:         server.Client(),
		SecretResolver: StaticSecretResolver{"wh_sec_test": "secret_fixture"},
		URLPolicy:      URLPolicy{AllowInsecureHTTP: true, AllowPrivateHosts: true, AllowedHosts: []string{"127.0.0.1"}},
		Now:            func() time.Time { return now },
		Breakers:       registry,
	}
	worker := DeliveryWorker{
		Store:       store,
		Sender:      sender,
		WorkerID:    "worker-a",
		BatchSize:   1,
		LeaseFor:    time.Minute,
		RetryPolicy: RetryPolicy{MaxAttempts: 10, BaseDelay: time.Second, MaxDelay: time.Minute},
		Now:         func() time.Time { return now },
		Breakers:    registry,
	}

	// First two runs: actual 500 responses (open the breaker).
	for i := 0; i < 2; i++ {
		// Re-lease the delivery by advancing time past NextAttemptAt.
		loaded, _, _ := store.Get(context.Background(), "tenant_a", "app_a", delivery.ID)
		now = loaded.NextAttemptAt
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce %d error: %v", i+1, err)
		}
	}

	// Third run: breaker should be open — worker must call MarkRetry, not MarkDeadLetter.
	loaded, _, _ := store.Get(context.Background(), "tenant_a", "app_a", delivery.ID)
	now = loaded.NextAttemptAt
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("circuit_open RunOnce error: %v", err)
	}

	loaded, _, _ = store.Get(context.Background(), "tenant_a", "app_a", delivery.ID)
	if loaded.Status != StatusRetryScheduled {
		t.Fatalf("expected StatusRetryScheduled after circuit_open, got %s", loaded.Status)
	}
	if !loaded.NextAttemptAt.After(now) {
		t.Fatalf("expected NextAttemptAt > now for circuit_open retry, got %v (now %v)", loaded.NextAttemptAt, now)
	}
	if loaded.LastErrorClass != "circuit_open" {
		t.Fatalf("expected LastErrorClass=circuit_open, got %q", loaded.LastErrorClass)
	}
}

func TestDeliveryWorkerRetriesThenDeadLetters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	store.now = func() time.Time { return now }
	delivery, _, err := store.Enqueue(context.Background(), EnqueueRequest{
		TenantID:      "tenant_a",
		AppID:         "app_a",
		EventName:     "job.completed",
		URL:           server.URL,
		SecretID:      "wh_sec_test",
		DedupeKey:     "job-terminal:job_1:completed",
		Payload:       []byte(`{"ok":true}`),
		MaxAttempts:   2,
		NextAttemptAt: now,
	})
	if err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	worker := DeliveryWorker{
		Store: store,
		Sender: HTTPSender{
			Client:         server.Client(),
			SecretResolver: StaticSecretResolver{"wh_sec_test": "secret_fixture"},
			URLPolicy:      URLPolicy{AllowInsecureHTTP: true, AllowPrivateHosts: true, AllowedHosts: []string{"127.0.0.1"}},
		},
		WorkerID:    "worker-a",
		BatchSize:   1,
		LeaseFor:    time.Minute,
		RetryPolicy: RetryPolicy{MaxAttempts: 2, BaseDelay: time.Second, MaxDelay: time.Minute},
		Now:         func() time.Time { return now },
	}
	if processed, err := worker.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("first RunOnce processed=%v err=%v", processed, err)
	}
	loaded, _, _ := store.Get(context.Background(), "tenant_a", "app_a", delivery.ID)
	if loaded.Status != StatusRetryScheduled || loaded.AttemptCount != 1 {
		t.Fatalf("after first attempt = %#v", loaded)
	}
	now = loaded.NextAttemptAt
	if processed, err := worker.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("second RunOnce processed=%v err=%v", processed, err)
	}
	loaded, _, _ = store.Get(context.Background(), "tenant_a", "app_a", delivery.ID)
	if loaded.Status != StatusDeadLettered || loaded.AttemptCount != 2 {
		t.Fatalf("after second attempt = %#v", loaded)
	}
}
