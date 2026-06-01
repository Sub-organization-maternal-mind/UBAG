package webhooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/resilience"
)

// makeSender returns an HTTPSender wired to the given test server with a static
// secret resolver and permissive URL policy. breakers may be nil.
func makeSender(server *httptest.Server, breakers *resilience.Registry) HTTPSender {
	return HTTPSender{
		Client:         server.Client(),
		SecretResolver: StaticSecretResolver{"wh_sec_test": "secret_fixture"},
		URLPolicy:      URLPolicy{AllowInsecureHTTP: true, AllowPrivateHosts: true, AllowedHosts: []string{"127.0.0.1"}},
		Now:            func() time.Time { return time.Unix(1700000000, 0) },
		Breakers:       breakers,
	}
}

func makeDelivery(serverURL string) Delivery {
	return Delivery{
		ID:        "dlv_test_001",
		TenantID:  "tenant_a",
		AppID:     "app_a",
		JobID:     "job_1",
		EventName: "job.completed",
		URL:       serverURL,
		SecretID:  "wh_sec_test",
		DedupeKey: "key-1",
		Payload:   []byte(`{"ok":true}`),
	}
}

// TestHTTPSender_NilBreakers_BehavesIdentically verifies that when Breakers is nil
// the sender works exactly as before (success path).
func TestHTTPSender_NilBreakers_BehavesIdentically(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sender := makeSender(server, nil) // nil registry
	result, err := sender.Send(context.Background(), makeDelivery(server.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ErrorClass != "none" {
		t.Fatalf("expected ErrorClass=none, got %q", result.ErrorClass)
	}
	if result.StatusCode != http.StatusNoContent {
		t.Fatalf("expected StatusCode=204, got %d", result.StatusCode)
	}
}

// TestHTTPSender_CircuitOpenAfterThreshold verifies that after FailureThreshold
// consecutive failures the next Send returns circuit_open without dialing.
func TestHTTPSender_CircuitOpenAfterThreshold(t *testing.T) {
	dialCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dialCount++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := resilience.Config{
		FailureThreshold:    3,
		SuccessBudget:       1,
		CooldownBase:        10 * time.Second,
		CooldownMax:         60 * time.Second,
		HalfOpenMaxInflight: 1,
	}
	registry := resilience.NewRegistry(cfg)
	sender := makeSender(server, registry)
	delivery := makeDelivery(server.URL)

	// Drive 3 failures to open the breaker.
	for i := 0; i < 3; i++ {
		result, err := sender.Send(context.Background(), delivery)
		if err != nil {
			t.Fatalf("attempt %d unexpected error: %v", i+1, err)
		}
		if result.StatusCode != http.StatusInternalServerError {
			t.Fatalf("attempt %d expected 500, got %d", i+1, result.StatusCode)
		}
	}

	if dialCount != 3 {
		t.Fatalf("expected 3 dials before open, got %d", dialCount)
	}

	// The breaker should now be open — the next Send must not dial.
	result, err := sender.Send(context.Background(), delivery)
	if err != nil {
		t.Fatalf("circuit_open send unexpected error: %v", err)
	}
	if result.ErrorClass != "circuit_open" {
		t.Fatalf("expected ErrorClass=circuit_open, got %q", result.ErrorClass)
	}
	if result.StatusCode != 0 {
		t.Fatalf("expected StatusCode=0, got %d", result.StatusCode)
	}
	if !result.Retryable {
		t.Fatalf("expected Retryable=true for circuit_open")
	}
	if dialCount != 3 {
		t.Fatalf("server was dialed after breaker opened (dialCount=%d)", dialCount)
	}
}

// TestHTTPSender_BreakerReclosesAfterCooldown verifies that once cooldown elapses
// and a probe succeeds the breaker returns to closed and normal delivery proceeds.
func TestHTTPSender_BreakerReclosesAfterCooldown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sleep-based timing test in short mode")
	}
	// First server: always 500 (to open breaker).
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failServer.Close()

	cfg := resilience.Config{
		FailureThreshold:    2,
		SuccessBudget:       1,
		CooldownBase:        10 * time.Millisecond,
		CooldownMax:         200 * time.Millisecond,
		HalfOpenMaxInflight: 1,
	}
	registry := resilience.NewRegistry(cfg)
	sender := makeSender(failServer, registry)
	delivery := makeDelivery(failServer.URL)

	// Open the breaker.
	for i := 0; i < 2; i++ {
		if _, err := sender.Send(context.Background(), delivery); err != nil {
			t.Fatalf("send %d error: %v", i+1, err)
		}
	}

	// Confirm it is open.
	result, err := sender.Send(context.Background(), delivery)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ErrorClass != "circuit_open" {
		t.Fatalf("expected circuit_open, got %q", result.ErrorClass)
	}

	// Wait for cooldown to elapse.
	time.Sleep(200 * time.Millisecond)

	// Now point delivery at a success server.
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okServer.Close()

	// Re-use same registry but point to the ok host so the same breaker is
	// probed via half-open. We swap URL on the delivery to the ok server.
	// (The breaker is keyed by host; the ok server uses a different ephemeral
	// port on 127.0.0.1 — so the host portion (127.0.0.1) is the same and the
	// same breaker entry is reused.)
	deliveryOK := makeDelivery(okServer.URL)
	// Force the ok server's client to be used as well.
	sender.Client = okServer.Client()

	result, err = sender.Send(context.Background(), deliveryOK)
	if err != nil {
		t.Fatalf("probe send error: %v", err)
	}
	if result.ErrorClass != "none" {
		t.Fatalf("expected successful probe, got ErrorClass=%q StatusCode=%d", result.ErrorClass, result.StatusCode)
	}

	// After one successful probe with SuccessBudget=1 the breaker is closed.
	// Subsequent sends should not be blocked.
	result, err = sender.Send(context.Background(), deliveryOK)
	if err != nil {
		t.Fatalf("post-close send error: %v", err)
	}
	if result.ErrorClass != "none" {
		t.Fatalf("expected none after re-close, got %q", result.ErrorClass)
	}
}
