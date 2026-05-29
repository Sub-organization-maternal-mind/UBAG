package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/templates"
	"github.com/ubag/ubag/apps/gateway/internal/webhooks"
)

type recordingExecutor struct {
	readyErr  error
	stats     executor.Stats
	enqueued  []jobstore.Job
	cancelled []jobstore.Job
	reasons   []string
}

type failingArtifactStore struct {
	err error
}

type failingTemplateStore struct {
	err error
}

func (f failingArtifactStore) Ready(context.Context) error {
	return f.err
}

func (f failingArtifactStore) PutArtifact(context.Context, string, string, string, io.Reader, int64) (artifacts.ArtifactRecord, error) {
	return artifacts.ArtifactRecord{}, f.err
}

func (f failingArtifactStore) GetArtifact(context.Context, string, string) (io.ReadCloser, artifacts.ArtifactRecord, error) {
	return nil, artifacts.ArtifactRecord{}, f.err
}

func (f failingArtifactStore) ListArtifacts(context.Context, string) ([]artifacts.ArtifactRecord, error) {
	return nil, f.err
}

func (f failingArtifactStore) DeleteArtifact(context.Context, string, string) error {
	return f.err
}

func (f failingTemplateStore) Ready(context.Context) error {
	return f.err
}

func (f failingTemplateStore) List(context.Context, templates.ListFilter) ([]templates.Template, error) {
	return nil, f.err
}

func (f failingTemplateStore) GetScoped(context.Context, string, string, string) (templates.Template, bool, error) {
	return templates.Template{}, false, f.err
}

func (r *recordingExecutor) Ready(context.Context) error {
	return r.readyErr
}

func (r *recordingExecutor) EnqueueJob(_ context.Context, job jobstore.Job) (executor.Receipt, error) {
	r.enqueued = append(r.enqueued, job)
	return executor.Receipt{
		Backend:    "recording",
		QueueName:  "jobs",
		MessageID:  job.ID,
		EnqueuedAt: time.Now().UTC(),
	}, nil
}

func (r *recordingExecutor) CancelJob(_ context.Context, job jobstore.Job, reason string) error {
	r.cancelled = append(r.cancelled, job)
	r.reasons = append(r.reasons, reason)
	return nil
}

func (r *recordingExecutor) Stats(context.Context) (executor.Stats, error) {
	if r.stats.QueueName != "" {
		return r.stats, nil
	}
	return executor.Stats{
		QueueName:        "jobs",
		DepthByState:     map[string]int{"queued": len(r.enqueued)},
		OldestAgeByState: map[string]time.Duration{"queued": 0},
	}, nil
}

func TestOperationalRoutes(t *testing.T) {
	server := NewServer(Config{Version: "test"}).Handler()

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{name: "health", path: "/v1/health", wantStatus: http.StatusOK, wantBody: "ok"},
		{name: "ready", path: "/v1/ready", wantStatus: http.StatusOK, wantBody: "ready"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)

			server.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}

			var payload healthResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload.Status != tt.wantBody {
				t.Fatalf("payload status = %q, want %q", payload.Status, tt.wantBody)
			}
			if payload.TraceID == "" {
				t.Fatal("trace id is empty")
			}
		})
	}
}

func TestReadinessIncludesExecutor(t *testing.T) {
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{Version: "test", Executor: dispatcher}).Handler()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/ready", nil)

	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	var payload healthResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Checks["queue"] != true || payload.Checks["executor"] != true {
		t.Fatalf("readiness checks missing queue/executor: %#v", payload.Checks)
	}
}

func TestReadinessReportsExecutorFailure(t *testing.T) {
	dispatcher := &recordingExecutor{readyErr: errors.New("spool unavailable")}
	server := NewServer(Config{Version: "test", Executor: dispatcher}).Handler()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/ready", nil)

	server.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	var payload errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "UBAG-QUEUE-EXECUTOR-READY-001" {
		t.Fatalf("error code = %q", payload.Error.Code)
	}
}

func TestReadinessReportsArtifactFailure(t *testing.T) {
	store := failingArtifactStore{err: errors.New("artifact store unavailable")}
	server := NewServer(Config{Version: "test", Artifacts: store}).Handler()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/ready", nil)

	server.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	var payload errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "UBAG-QUEUE-ARTIFACT-READY-001" {
		t.Fatalf("error code = %q", payload.Error.Code)
	}
}

func TestReadinessReportsTemplateFailure(t *testing.T) {
	store := failingTemplateStore{err: errors.New("template catalog unavailable")}
	server := NewServer(Config{Version: "test", Templates: store}).Handler()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/ready", nil)

	server.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	var payload errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "UBAG-TEMPLATE-READY-001" {
		t.Fatalf("error code = %q", payload.Error.Code)
	}
}

func TestVersionRoute(t *testing.T) {
	server := NewServer(Config{Version: "1.2.3", BuildCommit: "abc123"}).Handler()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/version", nil)

	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}

	var payload versionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.DefaultAPIVersion != DefaultAPIVersion {
		t.Fatalf("default_api_version = %q, want %q", payload.DefaultAPIVersion, DefaultAPIVersion)
	}
	if payload.Version != "1.2.3" || payload.Commit != "abc123" {
		t.Fatalf("version payload = %#v", payload)
	}
}

func TestDeclaredPublicSurfaceRoutes(t *testing.T) {
	server := NewServer(Config{Version: "test", BuildCommit: "surface", AppSecret: "dev-secret"}).Handler()
	operatorServer := NewServer(Config{Version: "test", BuildCommit: "surface", AppSecret: "dev-secret", ActorRole: "operator"}).Handler()

	collectionPaths := []string{
		"/v1/events",
		"/v1/workflows",
		"/v1/templates",
		"/v1/targets",
		"/v1/adapters",
		"/v1/apps",
		"/v1/devices",
		"/v1/webhooks",
	}
	for _, path := range collectionPaths {
		t.Run(path, func(t *testing.T) {
			response := doJSON(server, http.MethodGet, path, "", authHeaders(""))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
			}

			var payload collectionResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode collection response: %v", err)
			}
			if payload.APIVersion != DefaultAPIVersion || payload.TraceID == "" {
				t.Fatalf("unexpected collection response: %#v", payload)
			}
		})
	}
	audit := doJSON(operatorServer, http.MethodGet, "/v1/audit", "", authHeaders(""))
	if audit.Code != http.StatusOK {
		t.Fatalf("audit status = %d, want %d; body=%s", audit.Code, http.StatusOK, audit.Body.String())
	}
	viewerAudit := doJSON(NewServer(Config{Version: "test", BuildCommit: "surface", AppSecret: "dev-secret", ActorRole: "viewer"}).Handler(), http.MethodGet, "/v1/audit", "", authHeaders(""))
	if viewerAudit.Code != http.StatusForbidden {
		t.Fatalf("viewer audit status = %d, want %d; body=%s", viewerAudit.Code, http.StatusForbidden, viewerAudit.Body.String())
	}

	cache := doJSON(server, http.MethodGet, "/v1/cache", "", authHeaders(""))
	if cache.Code != http.StatusOK {
		t.Fatalf("cache status = %d, want %d; body=%s", cache.Code, http.StatusOK, cache.Body.String())
	}
	var cachePayload cacheStatusResponse
	if err := json.Unmarshal(cache.Body.Bytes(), &cachePayload); err != nil {
		t.Fatalf("decode cache response: %v", err)
	}
	if cachePayload.Profile != "edge" || cachePayload.TraceID == "" {
		t.Fatalf("unexpected cache response: %#v", cachePayload)
	}
	if cachePayload.Enabled || len(cachePayload.Entries) != 0 {
		t.Fatalf("cache status should remain disabled and empty by default: %#v", cachePayload)
	}

	metrics := doJSON(server, http.MethodGet, "/v1/metrics", "", nil)
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d; body=%s", metrics.Code, http.StatusOK, metrics.Body.String())
	}
	if !strings.Contains(metrics.Body.String(), "ubag_gateway_info") {
		t.Fatalf("metrics body missing gateway info: %s", metrics.Body.String())
	}
	if !strings.Contains(metrics.Body.String(), `ubag_queue_depth{queue="jobs",state="queued"}`) {
		t.Fatalf("metrics body missing queue depth: %s", metrics.Body.String())
	}
	if !strings.Contains(metrics.Body.String(), "ubag_worker_jobs_processed_total") {
		t.Fatalf("metrics body missing worker metrics: %s", metrics.Body.String())
	}
	if !strings.Contains(metrics.Body.String(), "ubag_worker_result_ingestions_total") {
		t.Fatalf("metrics body missing worker ingestion metrics: %s", metrics.Body.String())
	}

	stream := doJSON(server, http.MethodGet, "/v1/stream", "", authHeaders(""))
	if stream.Code != http.StatusUpgradeRequired {
		t.Fatalf("stream status = %d, want %d; body=%s", stream.Code, http.StatusUpgradeRequired, stream.Body.String())
	}

	missingAuth := doJSON(server, http.MethodGet, "/v1/templates", "", nil)
	if missingAuth.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d, want %d; body=%s", missingAuth.Code, http.StatusUnauthorized, missingAuth.Body.String())
	}
}

func TestTemplatesCatalogReturnsBuiltIns(t *testing.T) {
	server := NewServer(Config{Version: "test", AppSecret: "dev-secret"}).Handler()

	response := doJSON(server, http.MethodGet, "/v1/templates", "", authHeaders(""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}

	var payload collectionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Kind != "templates" || len(payload.Data) == 0 {
		t.Fatalf("unexpected template catalog: %#v", payload)
	}
	if payload.Data[0]["id"] != "mock.echo.v1" {
		t.Fatalf("first template id = %#v", payload.Data[0]["id"])
	}
}

func TestCollectionsRespectLimitAndCursor(t *testing.T) {
	server := NewServer(Config{Version: "test", AppSecret: "dev-secret"}).Handler()

	first := doJSON(server, http.MethodGet, "/v1/targets?limit=1", "", authHeaders(""))
	if first.Code != http.StatusOK {
		t.Fatalf("first page status = %d; body=%s", first.Code, first.Body.String())
	}
	var firstPayload collectionResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if len(firstPayload.Data) != 1 || firstPayload.NextCursor == nil {
		t.Fatalf("unexpected first page: %#v", firstPayload)
	}

	second := doJSON(server, http.MethodGet, "/v1/targets?limit=1&cursor="+url.QueryEscape(*firstPayload.NextCursor), "", authHeaders(""))
	if second.Code != http.StatusOK {
		t.Fatalf("second page status = %d; body=%s", second.Code, second.Body.String())
	}
	var secondPayload collectionResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondPayload); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(secondPayload.Data) != 1 || secondPayload.Data[0]["key"] == firstPayload.Data[0]["key"] {
		t.Fatalf("cursor did not advance collection: first=%#v second=%#v", firstPayload.Data, secondPayload.Data)
	}
}

func TestAdapterCatalogMatchesWorkerRegistryMistralKey(t *testing.T) {
	server := NewServer(Config{Version: "test", AppSecret: "dev-secret"}).Handler()
	response := doJSON(server, http.MethodGet, "/v1/adapters", "", authHeaders(""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "mistral_web") {
		t.Fatalf("adapter catalog contains stale mistral_web key: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "mistral_lechat") {
		t.Fatalf("adapter catalog missing mistral_lechat key: %s", response.Body.String())
	}
}

func TestWebSocketStreamUpgrade(t *testing.T) {
	testServer := httptest.NewServer(NewServer(Config{AppSecret: "dev-secret"}).Handler())
	defer testServer.Close()

	parsed, err := url.Parse(testServer.URL)
	if err != nil {
		t.Fatalf("parse test URL: %v", err)
	}
	conn, err := net.Dial("tcp", parsed.Host)
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	defer conn.Close()

	_, _ = conn.Write([]byte("GET /v1/stream HTTP/1.1\r\n" +
		"Host: " + parsed.Host + "\r\n" +
		"Authorization: Bearer dev-secret\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"))

	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusSwitchingProtocols)
	}
	if response.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("unexpected websocket accept header: %q", response.Header.Get("Sec-WebSocket-Accept"))
	}
}

func TestBearerAuthSchemeIsCaseInsensitive(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret"}).Handler()
	headers := map[string]string{
		"Authorization":    "bearer dev-secret",
		"Ubag-Api-Version": DefaultAPIVersion,
	}
	response := doJSON(server, http.MethodGet, "/v1/templates", "", headers)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
}

func TestCreateJobIdempotency(t *testing.T) {
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", Executor: dispatcher}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_00000000001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"prompt":"hello"}}}`

	first := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_00000000001"))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want %d; body=%s", first.Code, http.StatusAccepted, first.Body.String())
	}

	var firstPayload jobResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if firstPayload.JobID == "" || firstPayload.IdempotentReplay {
		t.Fatalf("unexpected first response: %#v", firstPayload)
	}

	replay := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_00000000001"))
	if replay.Code != http.StatusAccepted {
		t.Fatalf("replay status = %d, want %d; body=%s", replay.Code, http.StatusAccepted, replay.Body.String())
	}

	var replayPayload jobResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &replayPayload); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if replayPayload.JobID != firstPayload.JobID {
		t.Fatalf("replay job id = %q, want %q", replayPayload.JobID, firstPayload.JobID)
	}
	if !replayPayload.IdempotentReplay {
		t.Fatal("replay response did not set idempotent_replay")
	}
	if len(dispatcher.enqueued) != 1 {
		t.Fatalf("executor enqueue count = %d, want 1", len(dispatcher.enqueued))
	}

	conflictBody := `{"api_version":"2026-05-22","idempotency_key":"idem_00000000001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"prompt":"different"}}}`
	conflict := doJSON(server, http.MethodPost, "/v1/jobs", conflictBody, authHeaders("idem_00000000001"))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want %d; body=%s", conflict.Code, http.StatusConflict, conflict.Body.String())
	}

	var errorPayload errorEnvelope
	if err := json.Unmarshal(conflict.Body.Bytes(), &errorPayload); err != nil {
		t.Fatalf("decode conflict response: %v", err)
	}
	if errorPayload.Error.Code != "UBAG-VALIDATION-IDEMPOTENCY-CONFLICT-001" {
		t.Fatalf("error code = %q", errorPayload.Error.Code)
	}
}

func TestCreateJobEnqueuesExecutorPayload(t *testing.T) {
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", TenantID: "tenant_a", AppID: "app_a", Executor: dispatcher}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_enqueue_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"prompt":"hello"}}}`

	response := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_enqueue_0001"))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if len(dispatcher.enqueued) != 1 {
		t.Fatalf("executor enqueue count = %d, want 1", len(dispatcher.enqueued))
	}
	job := dispatcher.enqueued[0]
	if job.APIVersion != DefaultAPIVersion || job.TenantID != "tenant_a" || job.AppID != "app_a" {
		t.Fatalf("executor saw unstamped job: %#v", job)
	}
	if job.Target != "mock" || job.CommandType != "submit" || job.Input["prompt"] != "hello" {
		t.Fatalf("executor saw wrong payload: %#v", job)
	}
	if job.TraceID == "" {
		t.Fatal("executor job trace id is empty")
	}
}

func TestCreateJobAppliesTemplateBeforeStorageAndEnqueue(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store, Executor: dispatcher}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_template_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock_target","command_type":"echo","template_id":"mock.echo.v1","input":{"prompt":"override","extra":"value"},"options":{"temperature":0}}}`

	response := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_template_0001"))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if len(dispatcher.enqueued) != 1 {
		t.Fatalf("executor enqueue count = %d, want 1", len(dispatcher.enqueued))
	}
	job := dispatcher.enqueued[0]
	if job.TemplateID != "mock.echo.v1" || job.Input["prompt"] != "override" || job.Input["extra"] != "value" {
		t.Fatalf("template was not applied before enqueue: %#v", job)
	}
	if job.Options["return_mode"] != "final" || job.Options["cache_policy"] != "none" || fmt.Sprint(job.Options["temperature"]) != "0" {
		t.Fatalf("template options were not merged: %#v", job.Options)
	}
}

func TestCreateJobAppliesTemplateDefaultsBeforeValidation(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store, Executor: dispatcher}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_template_defaults","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"template_id":"mock.echo.v1"}}`

	response := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_template_defaults"))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if len(dispatcher.enqueued) != 1 {
		t.Fatalf("executor enqueue count = %d, want 1", len(dispatcher.enqueued))
	}
	job := dispatcher.enqueued[0]
	if job.Target != "mock_target" || job.CommandType != "echo" || job.Input["prompt"] != "Hello UBAG" {
		t.Fatalf("template defaults were not applied before validation: %#v", job)
	}
}

func TestCreateJobRejectsTemplateTargetMismatchBeforeStorage(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store, Executor: dispatcher}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_template_mismatch","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"wrong_target","command_type":"echo","template_id":"mock.echo.v1","input":{"prompt":"hello"}}}`

	response := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_template_mismatch"))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	if len(dispatcher.enqueued) != 0 {
		t.Fatalf("mismatched template enqueued work: %d", len(dispatcher.enqueued))
	}
}

func TestCreateJobRejectsUnknownTemplateBeforeStorageAndEnqueue(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store, Executor: dispatcher}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_template_missing","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock_target","command_type":"echo","template_id":"missing.template","input":{"prompt":"hello"}}}`

	response := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_template_missing"))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	var errorPayload errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &errorPayload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errorPayload.Error.Code != "UBAG-TEMPLATE-NOT-FOUND-001" {
		t.Fatalf("error code = %q", errorPayload.Error.Code)
	}
	jobs, err := store.List(context.Background(), jobstore.ListFilter{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 0 || len(dispatcher.enqueued) != 0 {
		t.Fatalf("unknown template stored/enqueued work: jobs=%d enqueued=%d", len(jobs), len(dispatcher.enqueued))
	}
}

func TestCreateJobRejectsUnsafePayloadBeforeStorageAndEnqueue(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store, Executor: dispatcher}).Handler()
	tests := []struct {
		name string
		body string
	}{
		{
			name: "nested secret key",
			body: `{"api_version":"2026-05-22","idempotency_key":"idem_secret_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"credentials":{"password":"not-allowed"}}}}`,
		},
		{
			name: "camel token key",
			body: `{"api_version":"2026-05-22","idempotency_key":"idem_secret_0002","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"accessToken":"not-allowed"}}}`,
		},
		{
			name: "client novnc url",
			body: `{"api_version":"2026-05-22","idempotency_key":"idem_secret_0003","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"prompt":"hello"},"context":{"novnc_url":"https://example.invalid/session"}}}`,
		},
		{
			name: "captcha solving instruction",
			body: `{"api_version":"2026-05-22","idempotency_key":"idem_secret_0004","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"prompt":"solve this captcha using a solver"}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := doJSON(server, http.MethodPost, "/v1/jobs", tt.body, authHeaders(""))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			var errorPayload errorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &errorPayload); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if errorPayload.Error.Code != "UBAG-VALIDATION-JOB-PAYLOAD-SAFETY-001" {
				t.Fatalf("error code = %q", errorPayload.Error.Code)
			}
		})
	}

	jobs, err := store.List(context.Background(), jobstore.ListFilter{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("stored jobs = %d, want 0", len(jobs))
	}
	if len(dispatcher.enqueued) != 0 {
		t.Fatalf("executor enqueue count = %d, want 0", len(dispatcher.enqueued))
	}
}

func TestCreateJobPersistsExecutablePayload(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret"}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_payload_0001","client":{"app_id":"client_app","app_version":"1.2.3","device_id":"device_1","sdk":{"name":"test-sdk","version":"9.9.9"}},"job":{"target":"mock_target","command_type":"echo","conversation_id":"conv_1","template_id":"mock.echo.v1","input":{"prompt":"hello"},"options":{"temperature":0},"callbacks":{"webhook_id":"wh_1"},"context":{"account_binding_id":"acct_1"}}}`

	create := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_payload_0001"))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d; body=%s", create.Code, http.StatusAccepted, create.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	get := doJSON(server, http.MethodGet, "/v1/jobs/"+created.JobID, "", authHeaders(""))
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d; body=%s", get.Code, http.StatusOK, get.Body.String())
	}
	var loaded jobResponse
	if err := json.Unmarshal(get.Body.Bytes(), &loaded); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if loaded.Metadata["conversation_id"] != "conv_1" || loaded.Metadata["template_id"] != "mock.echo.v1" {
		t.Fatalf("metadata missing conversation/template: %#v", loaded.Metadata)
	}
	if _, ok := loaded.Metadata["input"].(map[string]any); !ok {
		t.Fatalf("metadata missing input payload: %#v", loaded.Metadata)
	}
	if _, ok := loaded.Metadata["callbacks"].(map[string]any); !ok {
		t.Fatalf("metadata missing callbacks payload: %#v", loaded.Metadata)
	}
}

func TestJobRoutes(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret"}).Handler()
	createBody := `{"api_version":"2026-05-22","idempotency_key":"idem_routes_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{}}}`
	create := doJSON(server, http.MethodPost, "/v1/jobs", createBody, authHeaders("idem_routes_0001"))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d; body=%s", create.Code, http.StatusAccepted, create.Body.String())
	}

	var created jobResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	tests := []struct {
		name       string
		method     string
		path       string
		headers    map[string]string
		wantStatus int
	}{
		{name: "list", method: http.MethodGet, path: "/v1/jobs", headers: authHeaders(""), wantStatus: http.StatusOK},
		{name: "get", method: http.MethodGet, path: "/v1/jobs/" + created.JobID, headers: authHeaders(""), wantStatus: http.StatusOK},
		{name: "events", method: http.MethodGet, path: "/v1/jobs/" + created.JobID + "/events", headers: authHeaders(""), wantStatus: http.StatusOK},
		{name: "cancel requires idempotency", method: http.MethodPost, path: "/v1/jobs/" + created.JobID + "/cancel", headers: authHeaders(""), wantStatus: http.StatusBadRequest},
		{name: "cancel", method: http.MethodPost, path: "/v1/jobs/" + created.JobID + "/cancel", headers: authHeaders("idem_cancel_0001"), wantStatus: http.StatusAccepted},
		{name: "retry", method: http.MethodPost, path: "/v1/jobs/" + created.JobID + "/retry", headers: authHeaders("idem_retry_00001"), wantStatus: http.StatusAccepted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := doJSON(server, tt.method, tt.path, "", tt.headers)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantStatus, response.Body.String())
			}
		})
	}
}

func TestGlobalEventsRouteListsScopedJobEvents(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret"}).Handler()
	createBody := `{"api_version":"2026-05-22","idempotency_key":"idem_events_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{}}}`
	create := doJSON(server, http.MethodPost, "/v1/jobs", createBody, authHeaders("idem_events_0001"))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d; body=%s", create.Code, create.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	response := doJSON(server, http.MethodGet, "/v1/events?limit=1", "", authHeaders(""))
	if response.Code != http.StatusOK {
		t.Fatalf("events status = %d; body=%s", response.Code, response.Body.String())
	}
	var payload collectionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if payload.Kind != "events" || len(payload.Data) != 1 || payload.Data[0]["job_id"] != created.JobID {
		t.Fatalf("unexpected events payload: %#v", payload)
	}
}

func TestRetryJobEnqueuesExecutorPayload(t *testing.T) {
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", Executor: dispatcher}).Handler()
	createBody := `{"api_version":"2026-05-22","idempotency_key":"idem_retry_enqueue_create","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"prompt":"hello"}}}`
	create := doJSON(server, http.MethodPost, "/v1/jobs", createBody, authHeaders("idem_retry_enqueue_create"))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d; body=%s", create.Code, create.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	retry := doJSON(server, http.MethodPost, "/v1/jobs/"+created.JobID+"/retry", "", authHeaders("idem_retry_enqueue_001"))
	if retry.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d; body=%s", retry.Code, retry.Body.String())
	}
	if len(dispatcher.enqueued) != 2 {
		t.Fatalf("executor enqueue count = %d, want 2", len(dispatcher.enqueued))
	}
	if dispatcher.enqueued[1].RetryOf != created.JobID {
		t.Fatalf("retry_of = %q, want %q", dispatcher.enqueued[1].RetryOf, created.JobID)
	}
}

func TestCancelJobDelegatesToExecutorOnceWithReason(t *testing.T) {
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", Executor: dispatcher}).Handler()
	createBody := `{"api_version":"2026-05-22","idempotency_key":"idem_cancel_delegate_create","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"prompt":"hello"}}}`
	create := doJSON(server, http.MethodPost, "/v1/jobs", createBody, authHeaders("idem_cancel_delegate_create"))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d; body=%s", create.Code, create.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	cancelBody := `{"api_version":"2026-05-22","idempotency_key":"idem_cancel_delegate","reason":"operator_requested"}`
	cancel := doJSON(server, http.MethodPost, "/v1/jobs/"+created.JobID+"/cancel", cancelBody, authHeaders("idem_cancel_delegate"))
	if cancel.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d; body=%s", cancel.Code, cancel.Body.String())
	}
	replay := doJSON(server, http.MethodPost, "/v1/jobs/"+created.JobID+"/cancel", cancelBody, authHeaders("idem_cancel_delegate"))
	if replay.Code != http.StatusAccepted {
		t.Fatalf("cancel replay status = %d; body=%s", replay.Code, replay.Body.String())
	}
	if len(dispatcher.cancelled) != 1 {
		t.Fatalf("executor cancel count = %d, want 1", len(dispatcher.cancelled))
	}
	if dispatcher.cancelled[0].ID != created.JobID || dispatcher.reasons[0] != "operator_requested" {
		t.Fatalf("unexpected cancellation: jobs=%#v reasons=%#v", dispatcher.cancelled, dispatcher.reasons)
	}
}

func TestJobEventsListAndSSEUseDurableEventHistory(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret"}).Handler()
	createBody := `{"api_version":"2026-05-22","idempotency_key":"idem_events_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{}}}`
	create := doJSON(server, http.MethodPost, "/v1/jobs", createBody, authHeaders("idem_events_0001"))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d; body=%s", create.Code, create.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	events := doJSON(server, http.MethodGet, "/v1/jobs/"+created.JobID+"/events", "", authHeaders(""))
	if events.Code != http.StatusOK {
		t.Fatalf("events status = %d; body=%s", events.Code, events.Body.String())
	}
	var eventPayload jobEventsResponse
	if err := json.Unmarshal(events.Body.Bytes(), &eventPayload); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(eventPayload.Events) == 0 || eventPayload.Events[0].EventID == "" || eventPayload.Events[0].Type != "queued" {
		t.Fatalf("unexpected events payload: %#v", eventPayload)
	}

	firstPage := doJSON(server, http.MethodGet, "/v1/jobs/"+created.JobID+"/events?cursor=0&limit=1", "", authHeaders(""))
	if firstPage.Code != http.StatusOK {
		t.Fatalf("first page status = %d; body=%s", firstPage.Code, firstPage.Body.String())
	}
	var firstPagePayload jobEventsResponse
	if err := json.Unmarshal(firstPage.Body.Bytes(), &firstPagePayload); err != nil {
		t.Fatalf("decode first page response: %v", err)
	}
	if len(firstPagePayload.Events) != 1 || firstPagePayload.Events[0].Sequence != eventPayload.Events[0].Sequence {
		t.Fatalf("cursor=0 did not return the first event: %#v", firstPagePayload)
	}
	emptyPage := doJSON(server, http.MethodGet, "/v1/jobs/"+created.JobID+"/events?after_sequence=1&cursor=1", "", authHeaders(""))
	if emptyPage.Code != http.StatusOK {
		t.Fatalf("matching cursor status = %d; body=%s", emptyPage.Code, emptyPage.Body.String())
	}
	var emptyPagePayload jobEventsResponse
	if err := json.Unmarshal(emptyPage.Body.Bytes(), &emptyPagePayload); err != nil {
		t.Fatalf("decode empty page response: %v", err)
	}
	if len(emptyPagePayload.Events) != 0 {
		t.Fatalf("matching cursor did not advance past first event: %#v", emptyPagePayload)
	}
	conflictingCursor := doJSON(server, http.MethodGet, "/v1/jobs/"+created.JobID+"/events?after_sequence=0&cursor=1", "", authHeaders(""))
	if conflictingCursor.Code != http.StatusBadRequest {
		t.Fatalf("conflicting cursor status = %d, want %d; body=%s", conflictingCursor.Code, http.StatusBadRequest, conflictingCursor.Body.String())
	}

	sse := doJSON(server, http.MethodGet, "/v1/sse/jobs/"+created.JobID+"?snapshot=true", "", authHeaders(""))
	if sse.Code != http.StatusOK {
		t.Fatalf("sse status = %d; body=%s", sse.Code, sse.Body.String())
	}
	if !strings.Contains(sse.Body.String(), "event: job.queued") {
		t.Fatalf("sse did not include queued event: %s", sse.Body.String())
	}
}

func TestTenantBoundaryHidesCrossTenantJobAccess(t *testing.T) {
	store := jobstore.NewMemoryStore()
	tenantAServer := NewServer(Config{AppSecret: "dev-secret", TenantID: "tenant_a", AppID: "app_a", Jobs: store}).Handler()
	tenantBServer := NewServer(Config{AppSecret: "dev-secret", TenantID: "tenant_b", AppID: "app_a", Jobs: store}).Handler()
	createBody := `{"api_version":"2026-05-22","idempotency_key":"idem_tenant_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{}}}`
	create := doJSON(tenantAServer, http.MethodPost, "/v1/jobs", createBody, authHeaders("idem_tenant_0001"))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d; body=%s", create.Code, create.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	response := doJSON(tenantBServer, http.MethodGet, "/v1/jobs/"+created.JobID, "", authHeaders(""))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusNotFound, response.Body.String())
	}
}

func TestCallerCannotSpoofActorRoleOrTenantHeaders(t *testing.T) {
	store := jobstore.NewMemoryStore()
	viewerServer := NewServer(Config{AppSecret: "dev-secret", ActorRole: "viewer", TenantID: "tenant_a", AppID: "app_a", Jobs: store}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_spoof_00001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{}}}`
	headers := authHeaders("idem_spoof_00001")
	headers["Ubag-Actor-Role"] = "superadmin"
	headers["Ubag-Tenant-Id"] = "tenant_b"
	headers["Ubag-App-Id"] = "app_b"

	response := doJSON(viewerServer, http.MethodPost, "/v1/jobs", body, headers)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusForbidden, response.Body.String())
	}
}

func TestListJobsFiltersAndPagination(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret"}).Handler()
	firstBody := `{"api_version":"2026-05-22","idempotency_key":"idem_filter_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{}}}`
	secondBody := `{"api_version":"2026-05-22","idempotency_key":"idem_filter_0002","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"generic_chat","command_type":"chat.prompt","input":{}}}`
	if response := doJSON(server, http.MethodPost, "/v1/jobs", firstBody, authHeaders("idem_filter_0001")); response.Code != http.StatusAccepted {
		t.Fatalf("first create status = %d; body=%s", response.Code, response.Body.String())
	}
	if response := doJSON(server, http.MethodPost, "/v1/jobs", secondBody, authHeaders("idem_filter_0002")); response.Code != http.StatusAccepted {
		t.Fatalf("second create status = %d; body=%s", response.Code, response.Body.String())
	}

	list := doJSON(server, http.MethodGet, "/v1/jobs?limit=1&filter%5Btarget%5D=generic_chat&sort=created_at", "", authHeaders(""))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d; body=%s", list.Code, list.Body.String())
	}
	var payload listJobsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(payload.Jobs) != 1 || payload.Jobs[0].Target != "generic_chat" {
		t.Fatalf("unexpected list payload: %#v", payload)
	}
}

func TestWebhookReplayRouteRequiresAuthorizedRole(t *testing.T) {
	viewerStore, viewerDelivery := seededWebhookStore(t)
	operatorStore, operatorDelivery := seededWebhookStore(t)
	viewerServer := NewServer(Config{AppSecret: "dev-secret", ActorRole: "viewer", Webhooks: viewerStore}).Handler()
	operatorServer := NewServer(Config{AppSecret: "dev-secret", ActorRole: "operator", Webhooks: operatorStore}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_webhook_0001","delivery_id":"` + operatorDelivery.ID + `","reason":"operator_retry"}`
	headers := authHeaders("idem_webhook_0001")
	deniedBody := `{"api_version":"2026-05-22","idempotency_key":"idem_webhook_0001","delivery_id":"` + viewerDelivery.ID + `","reason":"operator_retry"}`
	denied := doJSON(viewerServer, http.MethodPost, "/v1/webhooks/replay", deniedBody, headers)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d, want %d; body=%s", denied.Code, http.StatusForbidden, denied.Body.String())
	}

	accepted := doJSON(operatorServer, http.MethodPost, "/v1/webhooks/replay", body, headers)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("accepted status = %d, want %d; body=%s", accepted.Code, http.StatusAccepted, accepted.Body.String())
	}
}

func TestWebhookReplayIdempotency(t *testing.T) {
	store, delivery := seededWebhookStore(t)
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "operator", Webhooks: store}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_webhook_0002","delivery_id":"` + delivery.ID + `","reason":"operator_retry"}`
	headers := authHeaders("idem_webhook_0002")

	first := doJSON(server, http.MethodPost, "/v1/webhooks/replay", body, headers)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want %d; body=%s", first.Code, http.StatusAccepted, first.Body.String())
	}
	replay := doJSON(server, http.MethodPost, "/v1/webhooks/replay", body, headers)
	if replay.Code != http.StatusAccepted {
		t.Fatalf("replay status = %d, want %d; body=%s", replay.Code, http.StatusAccepted, replay.Body.String())
	}
	var replayPayload webhookReplayResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &replayPayload); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if !replayPayload.IdempotentReplay {
		t.Fatal("webhook replay did not mark idempotent replay")
	}

	conflict := doJSON(server, http.MethodPost, "/v1/webhooks/replay", strings.Replace(body, "operator_retry", "different", 1), headers)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want %d; body=%s", conflict.Code, http.StatusConflict, conflict.Body.String())
	}
}

func TestWebhookReplayRejectsMissingDelivery(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "operator"}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_webhook_missing","delivery_id":"whd_missing","reason":"operator_retry"}`
	response := doJSON(server, http.MethodPost, "/v1/webhooks/replay", body, authHeaders("idem_webhook_missing"))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusNotFound, response.Body.String())
	}
}

func TestCreateJobRejectsUnsafeWebhookCallback(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret"}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_webhook_bad_url","client":{"app_id":"client_app","app_version":"1.2.3","sdk":{"name":"test-sdk","version":"9.9.9"}},"job":{"target":"mock","command_type":"submit","input":{"prompt":"hello"},"callbacks":{"webhook_url":"http://127.0.0.1/callback","webhook_secret_id":"wh_sec_test"}}}`
	response := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_webhook_bad_url"))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	var payload errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if payload.Error.Code != "UBAG-VALIDATION-WEBHOOK-CALLBACK-001" {
		t.Fatalf("error code = %q", payload.Error.Code)
	}
}

func TestCreateJobAllowsWebhookSecretReferenceID(t *testing.T) {
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{
		AppSecret: "dev-secret",
		Executor:  dispatcher,
		WebhookURLPolicy: webhooks.URLPolicy{
			AllowAnyPublicHost: true,
		},
	}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_webhook_secret_ref","client":{"app_id":"client_app","app_version":"1.2.3","sdk":{"name":"test-sdk","version":"9.9.9"}},"job":{"target":"mock","command_type":"submit","input":{"prompt":"hello"},"callbacks":{"webhook_url":"https://hooks.example.invalid/callback","webhook_secret_id":"wh_sec_test"}}}`
	response := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_webhook_secret_ref"))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if len(dispatcher.enqueued) != 1 {
		t.Fatalf("executor enqueue count = %d, want 1", len(dispatcher.enqueued))
	}
}

func seededWebhookStore(t *testing.T) (*webhooks.MemoryStore, webhooks.Delivery) {
	t.Helper()
	store := webhooks.NewMemoryStore()
	delivery, inserted, err := store.Enqueue(context.Background(), webhooks.EnqueueRequest{
		TenantID:      defaultTenantID,
		AppID:         defaultAppID,
		JobID:         "job_webhook_seed",
		EventName:     "job.completed",
		URL:           "https://example.com/callback",
		SecretID:      "wh_sec_seed",
		DedupeKey:     "seed:job_webhook_seed:completed",
		Payload:       []byte(`{"api_version":"2026-05-22"}`),
		MaxAttempts:   3,
		NextAttemptAt: time.Now().UTC(),
	})
	if err != nil || !inserted {
		t.Fatalf("seed webhook delivery inserted=%v err=%v", inserted, err)
	}
	return store, delivery
}

func TestJobArtifactRoutesLifecycle(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
	jobID := createArtifactRouteJob(t, server, "idem_artifact_lifecycle_0001")

	missingIdempotency := doRaw(server, http.MethodPut, "/v1/jobs/"+jobID+"/artifacts/report.txt", "hello artifact", "text/plain", authHeaders(""))
	if missingIdempotency.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency status = %d, want %d; body=%s", missingIdempotency.Code, http.StatusBadRequest, missingIdempotency.Body.String())
	}

	put := doRaw(server, http.MethodPut, "/v1/jobs/"+jobID+"/artifacts/report.txt", "hello artifact", "text/plain", authHeaders("idem_artifact_put_0001"))
	if put.Code != http.StatusCreated {
		t.Fatalf("put status = %d, want %d; body=%s", put.Code, http.StatusCreated, put.Body.String())
	}
	if strings.Contains(put.Body.String(), "bucket") {
		t.Fatalf("artifact response leaked bucket: %s", put.Body.String())
	}
	var putPayload struct {
		Artifact artifactRecordResponse `json:"artifact"`
		TraceID  string                 `json:"trace_id"`
	}
	if err := json.Unmarshal(put.Body.Bytes(), &putPayload); err != nil {
		t.Fatalf("decode put response: %v", err)
	}
	if putPayload.Artifact.JobID != jobID || putPayload.Artifact.Key != "report.txt" {
		t.Fatalf("unexpected artifact metadata: %#v", putPayload.Artifact)
	}
	if putPayload.Artifact.ContentType != "text/plain" || putPayload.Artifact.SizeBytes != int64(len("hello artifact")) || putPayload.Artifact.Checksum == "" {
		t.Fatalf("incomplete artifact metadata: %#v", putPayload.Artifact)
	}
	if putPayload.TraceID == "" {
		t.Fatal("trace id is empty")
	}
	replayPut := doRaw(server, http.MethodPut, "/v1/jobs/"+jobID+"/artifacts/report.txt", "hello artifact", "text/plain", authHeaders("idem_artifact_put_0001"))
	if replayPut.Code != http.StatusCreated || !strings.Contains(replayPut.Body.String(), `"idempotent_replay":true`) {
		t.Fatalf("put replay status = %d; body=%s", replayPut.Code, replayPut.Body.String())
	}

	list := doJSON(server, http.MethodGet, "/v1/jobs/"+jobID+"/artifacts", "", authHeaders(""))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d; body=%s", list.Code, http.StatusOK, list.Body.String())
	}
	var listPayload struct {
		Data []artifactRecordResponse `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listPayload.Data) != 1 || listPayload.Data[0].Key != "report.txt" {
		t.Fatalf("unexpected list payload: %#v", listPayload)
	}

	get := doRaw(server, http.MethodGet, "/v1/jobs/"+jobID+"/artifacts/report.txt", "", "", authHeaders(""))
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d; body=%s", get.Code, http.StatusOK, get.Body.String())
	}
	if get.Body.String() != "hello artifact" {
		t.Fatalf("artifact body = %q", get.Body.String())
	}
	if get.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("content type = %q", get.Header().Get("Content-Type"))
	}
	if get.Header().Get("Ubag-Artifact-Checksum") == "" || get.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing artifact safety headers: %#v", get.Header())
	}

	missingDeleteIdempotency := doRaw(server, http.MethodDelete, "/v1/jobs/"+jobID+"/artifacts/report.txt", "", "", authHeaders(""))
	if missingDeleteIdempotency.Code != http.StatusBadRequest {
		t.Fatalf("missing delete idempotency status = %d, want %d; body=%s", missingDeleteIdempotency.Code, http.StatusBadRequest, missingDeleteIdempotency.Body.String())
	}
	del := doRaw(server, http.MethodDelete, "/v1/jobs/"+jobID+"/artifacts/report.txt", "", "", authHeaders("idem_artifact_delete_0001"))
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d; body=%s", del.Code, http.StatusNoContent, del.Body.String())
	}
	replayDelete := doRaw(server, http.MethodDelete, "/v1/jobs/"+jobID+"/artifacts/report.txt", "", "", authHeaders("idem_artifact_delete_0001"))
	if replayDelete.Code != http.StatusNoContent {
		t.Fatalf("delete replay status = %d, want %d; body=%s", replayDelete.Code, http.StatusNoContent, replayDelete.Body.String())
	}
	missing := doRaw(server, http.MethodGet, "/v1/jobs/"+jobID+"/artifacts/report.txt", "", "", authHeaders(""))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing get status = %d, want %d; body=%s", missing.Code, http.StatusNotFound, missing.Body.String())
	}
}

func TestJobArtifactRoutesAuthorizeAndScope(t *testing.T) {
	jobs := jobstore.NewMemoryStore()
	artifactStore := artifacts.NewMemoryArtifactStore()
	developerServer := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", TenantID: "tenant_a", AppID: "app_a", Jobs: jobs, Artifacts: artifactStore}).Handler()
	viewerServer := NewServer(Config{AppSecret: "dev-secret", ActorRole: "viewer", TenantID: "tenant_a", AppID: "app_a", Jobs: jobs, Artifacts: artifactStore}).Handler()
	otherTenantServer := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", TenantID: "tenant_b", AppID: "app_a", Jobs: jobs, Artifacts: artifactStore}).Handler()
	jobID := createArtifactRouteJob(t, developerServer, "idem_artifact_authz_0001")

	put := doRaw(developerServer, http.MethodPut, "/v1/jobs/"+jobID+"/artifacts/report.txt", "hello", "text/plain", authHeaders("idem_artifact_authz_put"))
	if put.Code != http.StatusCreated {
		t.Fatalf("developer put status = %d; body=%s", put.Code, put.Body.String())
	}

	viewerList := doJSON(viewerServer, http.MethodGet, "/v1/jobs/"+jobID+"/artifacts", "", authHeaders(""))
	if viewerList.Code != http.StatusOK {
		t.Fatalf("viewer list status = %d, want %d; body=%s", viewerList.Code, http.StatusOK, viewerList.Body.String())
	}
	viewerGet := doRaw(viewerServer, http.MethodGet, "/v1/jobs/"+jobID+"/artifacts/report.txt", "", "", authHeaders(""))
	if viewerGet.Code != http.StatusOK {
		t.Fatalf("viewer get status = %d, want %d; body=%s", viewerGet.Code, http.StatusOK, viewerGet.Body.String())
	}
	viewerPut := doRaw(viewerServer, http.MethodPut, "/v1/jobs/"+jobID+"/artifacts/viewer.txt", "nope", "text/plain", authHeaders(""))
	if viewerPut.Code != http.StatusForbidden {
		t.Fatalf("viewer put status = %d, want %d; body=%s", viewerPut.Code, http.StatusForbidden, viewerPut.Body.String())
	}
	viewerDelete := doRaw(viewerServer, http.MethodDelete, "/v1/jobs/"+jobID+"/artifacts/report.txt", "", "", authHeaders(""))
	if viewerDelete.Code != http.StatusForbidden {
		t.Fatalf("viewer delete status = %d, want %d; body=%s", viewerDelete.Code, http.StatusForbidden, viewerDelete.Body.String())
	}

	crossTenantGet := doRaw(otherTenantServer, http.MethodGet, "/v1/jobs/"+jobID+"/artifacts/report.txt", "", "", authHeaders(""))
	if crossTenantGet.Code != http.StatusNotFound {
		t.Fatalf("cross tenant status = %d, want %d; body=%s", crossTenantGet.Code, http.StatusNotFound, crossTenantGet.Body.String())
	}
}

func TestJobArtifactRoutesRejectOversizedUploadAndWrongMethods(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
	jobID := createArtifactRouteJob(t, server, "idem_artifact_limits_0001")

	oversized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/v1/jobs/"+jobID+"/artifacts/large.bin", strings.NewReader("x"))
	request.ContentLength = maxArtifactBodyBytes + 1
	request.Header.Set("Authorization", "Bearer dev-secret")
	request.Header.Set("Ubag-Api-Version", DefaultAPIVersion)
	request.Header.Set("Content-Type", "application/octet-stream")
	server.ServeHTTP(oversized, request)
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want %d; body=%s", oversized.Code, http.StatusRequestEntityTooLarge, oversized.Body.String())
	}

	wrongCollectionMethod := doJSON(server, http.MethodPost, "/v1/jobs/"+jobID+"/artifacts", "", authHeaders(""))
	if wrongCollectionMethod.Code != http.StatusMethodNotAllowed || wrongCollectionMethod.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("collection method status=%d allow=%q body=%s", wrongCollectionMethod.Code, wrongCollectionMethod.Header().Get("Allow"), wrongCollectionMethod.Body.String())
	}
	wrongArtifactMethod := doJSON(server, http.MethodPost, "/v1/jobs/"+jobID+"/artifacts/report.txt", "", authHeaders(""))
	if wrongArtifactMethod.Code != http.StatusMethodNotAllowed || wrongArtifactMethod.Header().Get("Allow") != "GET, PUT, DELETE" {
		t.Fatalf("artifact method status=%d allow=%q body=%s", wrongArtifactMethod.Code, wrongArtifactMethod.Header().Get("Allow"), wrongArtifactMethod.Body.String())
	}
}

func TestStableErrorEnvelope(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret"}).Handler()

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "not found", method: http.MethodGet, path: "/missing", wantStatus: http.StatusNotFound, wantCode: "UBAG-VALIDATION-ROUTE-001"},
		{name: "method not allowed", method: http.MethodPost, path: "/v1/health", wantStatus: http.StatusMethodNotAllowed, wantCode: "UBAG-VALIDATION-METHOD-001"},
		{name: "missing auth", method: http.MethodGet, path: "/v1/jobs", wantStatus: http.StatusUnauthorized, wantCode: "UBAG-AUTH-MISSING-001"},
		{name: "missing api version", method: http.MethodPost, path: "/v1/jobs", body: `{"job":{"target":"mock","command_type":"submit"}}`, wantStatus: http.StatusBadRequest, wantCode: "UBAG-VALIDATION-API-VERSION-001"},
		{name: "missing idempotency", method: http.MethodPost, path: "/v1/jobs", body: `{"api_version":"2026-05-22","job":{"target":"mock","command_type":"submit"}}`, wantStatus: http.StatusBadRequest, wantCode: "UBAG-VALIDATION-IDEMPOTENCY-KEY-MISSING-001"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := map[string]string(nil)
			if strings.HasPrefix(tt.path, "/v1/jobs") && tt.wantCode != "UBAG-AUTH-MISSING-001" {
				headers = authHeaders("")
				if tt.wantCode == "UBAG-VALIDATION-API-VERSION-001" {
					delete(headers, "Ubag-Api-Version")
				}
			}
			response := doJSON(server, tt.method, tt.path, tt.body, headers)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantStatus, response.Body.String())
			}

			var payload errorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if payload.Error.Code != tt.wantCode {
				t.Fatalf("error code = %q, want %q", payload.Error.Code, tt.wantCode)
			}
			if payload.Error.TraceID == "" {
				t.Fatal("error trace id is empty")
			}
		})
	}
}

func doJSON(handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	handler.ServeHTTP(response, request)

	return response
}

func doRaw(handler http.Handler, method, path, body, contentType string, headers map[string]string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	handler.ServeHTTP(response, request)

	return response
}

func createArtifactRouteJob(t *testing.T, handler http.Handler, idempotencyKey string) string {
	t.Helper()
	body := `{"api_version":"2026-05-22","idempotency_key":"` + idempotencyKey + `","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{}}}`
	create := doJSON(handler, http.MethodPost, "/v1/jobs", body, authHeaders(idempotencyKey))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d; body=%s", create.Code, create.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return created.JobID
}

func authHeaders(idempotencyKey string) map[string]string {
	headers := map[string]string{
		"Authorization":    "Bearer dev-secret",
		"Ubag-Api-Version": DefaultAPIVersion,
	}
	if idempotencyKey != "" {
		headers["Idempotency-Key"] = idempotencyKey
	}

	return headers
}
