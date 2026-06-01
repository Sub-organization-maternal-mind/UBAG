package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/resilience"
)

// breakerOpenExecutor always returns a *resilience.BreakerOpenError from EnqueueJob.
type breakerOpenExecutor struct {
	target     string
	retryAfter time.Duration
}

func (b *breakerOpenExecutor) Ready(context.Context) error { return nil }

func (b *breakerOpenExecutor) EnqueueJob(_ context.Context, job jobstore.Job) (executor.Receipt, error) {
	target := b.target
	if target == "" {
		target = job.Target
	}
	return executor.Receipt{}, &resilience.BreakerOpenError{
		Target:     target,
		RetryAfter: b.retryAfter,
	}
}

func (b *breakerOpenExecutor) CancelJob(context.Context, jobstore.Job, string) error { return nil }

func (b *breakerOpenExecutor) Stats(context.Context) (executor.Stats, error) {
	return executor.Stats{QueueName: "breaker"}, nil
}

// TestCreateJob_BreakerOpen verifies that when the executor returns *BreakerOpenError,
// the HTTP response is 503 with Retry-After header and code UBAG-QUEUE-BREAKER-OPEN-001.
func TestCreateJob_BreakerOpen(t *testing.T) {
	t.Parallel()

	retryAfter := 30 * time.Second
	dispatcher := &breakerOpenExecutor{retryAfter: retryAfter}
	server := NewServer(Config{AppSecret: "dev-secret", Executor: dispatcher}).Handler()

	body := `{"api_version":"2026-05-22","idempotency_key":"idem_breaker_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"prompt":"hello"}}}`
	response := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_breaker_0001"))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
	}

	retryAfterHeader := response.Header().Get("Retry-After")
	if retryAfterHeader == "" {
		t.Fatal("expected Retry-After header, got none")
	}
	secs, err := strconv.Atoi(retryAfterHeader)
	if err != nil {
		t.Fatalf("Retry-After header %q is not a valid integer: %v", retryAfterHeader, err)
	}
	if secs < 1 {
		t.Fatalf("Retry-After header %d is less than 1", secs)
	}

	var payload errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "UBAG-QUEUE-BREAKER-OPEN-001" {
		t.Errorf("error code = %q, want UBAG-QUEUE-BREAKER-OPEN-001", payload.Error.Code)
	}
	if !payload.Error.Retryable {
		t.Error("expected retryable=true")
	}
}

// TestRetryJob_BreakerOpen verifies the same behaviour on the retry path.
func TestRetryJob_BreakerOpen(t *testing.T) {
	t.Parallel()

	// Create a job with a healthy executor, then swap to a breaker-open executor.
	healthy := &recordingExecutor{}
	store := jobstore.NewMemoryStore()
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store, Executor: healthy}).Handler()

	createBody := `{"api_version":"2026-05-22","idempotency_key":"idem_breaker_retry_01","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{}}}`
	createResp := doJSON(server, http.MethodPost, "/v1/jobs", createBody, authHeaders("idem_breaker_retry_01"))
	if createResp.Code != http.StatusAccepted {
		t.Fatalf("create status = %d; body=%s", createResp.Code, createResp.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	// Now use a server whose executor returns BreakerOpenError.
	retryAfter := 15 * time.Second
	breakerDispatcher := &breakerOpenExecutor{target: "mock", retryAfter: retryAfter}
	retryServer := NewServer(Config{AppSecret: "dev-secret", Jobs: store, Executor: breakerDispatcher}).Handler()

	retryURL := "/v1/jobs/" + created.JobID + "/retry"
	retryBody := `{"idempotency_key":"idem_breaker_retry_02"}`
	response := doJSON(retryServer, http.MethodPost, retryURL, retryBody, authHeaders("idem_breaker_retry_02"))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("retry status = %d, want 503; body=%s", response.Code, response.Body.String())
	}

	retryAfterHeader := response.Header().Get("Retry-After")
	if retryAfterHeader == "" {
		t.Fatal("expected Retry-After header on retry path, got none")
	}
	secs, err := strconv.Atoi(retryAfterHeader)
	if err != nil {
		t.Fatalf("Retry-After header %q is not a valid integer: %v", retryAfterHeader, err)
	}
	if secs < 1 {
		t.Fatalf("Retry-After header %d is less than 1", secs)
	}

	var payload errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "UBAG-QUEUE-BREAKER-OPEN-001" {
		t.Errorf("error code = %q, want UBAG-QUEUE-BREAKER-OPEN-001", payload.Error.Code)
	}
	if !payload.Error.Retryable {
		t.Error("expected retryable=true")
	}
}
