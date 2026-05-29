package webhooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
