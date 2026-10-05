package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

func overloadJobBody(key string) string {
	return `{"api_version":"2026-05-22","idempotency_key":"` + key + `","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"prompt":"hello"}}}`
}

// A spent concurrency ceiling is an explicit overload answer: 429 with
// Retry-After in the header and retry_after_ms in the body.
func TestConcurrencyCeilingAnswersWithRetryGuidance(t *testing.T) {
	store := jobs.NewMemoryStore()
	registry := topology.NewConcurrencyRegistry()
	srv := NewServer(Config{AppSecret: "dev-secret", Jobs: store, Executor: &recordingExecutor{}, Concurrency: registry}).Handler()

	first := doJSON(srv, http.MethodPost, "/v1/jobs", overloadJobBody("overload_key_00001"), authHeaders("overload_key_00001"))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first create = %d body=%s", first.Code, first.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	job, found, err := store.Get(t.Context(), created.JobID)
	if err != nil || !found {
		t.Fatalf("created job not found: %v", err)
	}
	// Cap the job's own lane at one live token (already held by the first job).
	registry.Report(job.TenantID, topology.ConcurrencyView{Target: job.Target, IdentityRef: job.AppID, CurrentCap: 1})

	rec := doJSON(srv, http.MethodPost, "/v1/jobs", overloadJobBody("overload_key_00002"), authHeaders("overload_key_00002"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second create = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if retry, convErr := strconv.Atoi(rec.Header().Get("Retry-After")); convErr != nil || retry < 1 {
		t.Fatalf("429 without a usable Retry-After: %q", rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), "retry_after_ms") {
		t.Fatalf("429 body lacks retry_after_ms: %s", rec.Body.String())
	}
}

// Two gateway "replicas" on one database share the in-flight ceiling at the
// HTTP layer: the cap of 1 admits exactly one job across both.
func TestSharedAdmissionAcrossReplicas(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "admission.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	backend := topology.NewSQLiteTokenBackend(db)
	if err := backend.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	newReplica := func() http.Handler {
		registry := topology.NewConcurrencyRegistry()
		registry.UseBackend(backend, topology.LaneLimits{Global: 1})
		return NewServer(Config{AppSecret: "dev-secret", Jobs: jobs.NewMemoryStore(), Executor: &recordingExecutor{}, Concurrency: registry}).Handler()
	}
	a, b := newReplica(), newReplica()
	if rec := doJSON(a, http.MethodPost, "/v1/jobs", overloadJobBody("replica_key_000001"), authHeaders("replica_key_000001")); rec.Code != http.StatusAccepted {
		t.Fatalf("replica A = %d body=%s", rec.Code, rec.Body.String())
	}
	rec := doJSON(b, http.MethodPost, "/v1/jobs", overloadJobBody("replica_key_000002"), authHeaders("replica_key_000002"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("replica B = %d, want 429 (shared global ceiling of 1); body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("overload without Retry-After")
	}
}
