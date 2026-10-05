package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// A media-only message with an unknown role must not slip past role
// validation (review repro).
func TestFacadeRejectsUnknownRoleOnMediaOnlyMessage(t *testing.T) {
	_, _, ok := flattenFacadeMessages([]openAIFacadeMessage{
		{Role: "user", Content: "describe this"},
		{Role: "invalid_role", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AQ=="}}}},
	})
	if ok {
		t.Fatal("media-only content bypassed role validation")
	}
}

func TestByteBudgetIsNonBlockingAndBounded(t *testing.T) {
	b := &byteBudget{limit: 100}
	if !b.tryAcquire(60) || b.tryAcquire(60) {
		t.Fatal("second 60-byte reservation must be refused while the first is held")
	}
	b.release(60)
	if !b.tryAcquire(60) {
		t.Fatal("released bytes must be reusable")
	}
	b.release(60)
	// A single request larger than the whole budget may run alone, never twice.
	if !b.tryAcquire(500) || b.tryAcquire(1) {
		t.Fatal("an oversized lone request runs alone")
	}
	b.release(500)
	if b.used.Load() != 0 {
		t.Fatalf("budget leaked %d bytes", b.used.Load())
	}
}

// With the in-flight limit at 1, a second concurrent request is refused with
// an explicit 503 + Retry-After while probes keep answering.
func TestInflightLimitRefusesExcessRequestsButNotProbes(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	srv := NewServer(Config{AppSecret: "dev-secret", Executor: &recordingExecutor{}, MaxInflightRequests: 1})
	blocked := srv.withInflightLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
	}))
	go blocked.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/jobs", nil))
	<-entered
	rec := httptest.NewRecorder()
	blocked.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/jobs", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("excess request = %d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	probe := httptest.NewRecorder()
	srv.withInflightLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })).
		ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if probe.Code != http.StatusOK {
		t.Fatalf("probe was throttled: %d", probe.Code)
	}
	close(release)
}

// Concurrent facade bodies beyond the shared upload-memory budget are refused
// explicitly instead of all being buffered.
func TestUploadMemoryBudgetRefusesBeyondBudget(t *testing.T) {
	srv := NewServer(Config{AppSecret: "dev-secret", Executor: &recordingExecutor{}, UploadMemoryBytes: 1 << 20})
	req := httptest.NewRequest(http.MethodPost, "/v1/openai/chat/completions", nil)
	req.ContentLength = 400 << 10
	w1 := httptest.NewRecorder()
	release, ok := srv.reserveUploadBytes(w1, req, uploadReservation(req, srv.facadeMaxBody, facadeDecodeFactor), true)
	if !ok {
		t.Fatalf("first reservation refused: %s", w1.Body.String())
	}
	defer release()
	w2 := httptest.NewRecorder()
	if _, ok := srv.reserveUploadBytes(w2, req, uploadReservation(req, srv.facadeMaxBody, facadeDecodeFactor), true); ok {
		t.Fatal("second 1.2 MiB reservation fit in a 1 MiB budget")
	}
	if w2.Code != http.StatusServiceUnavailable || w2.Header().Get("Retry-After") == "" {
		t.Fatalf("refusal = %d retry-after=%q", w2.Code, w2.Header().Get("Retry-After"))
	}
	metrics := httptest.NewRecorder()
	srv.writeOverloadMetrics(metrics)
	if !strings.Contains(metrics.Body.String(), `ubag_admission_rejections_total{reason="upload_memory"} 1`) {
		t.Fatalf("rejection not counted:\n%s", metrics.Body.String())
	}
}
