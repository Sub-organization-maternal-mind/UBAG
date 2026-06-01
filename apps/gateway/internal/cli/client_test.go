package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/cli"
)

// newTestClient builds a Client pointed at the given httptest.Server.
func newTestClient(srv *httptest.Server) *cli.Client {
	return &cli.Client{
		BaseURL:    srv.URL,
		AppSecret:  "test-secret",
		APIVersion: cli.DefaultAPIVersion,
		HTTPClient: srv.Client(),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Health
// ─────────────────────────────────────────────────────────────────────────────

func TestHealth_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Ubag-Api-Version"); got != cli.DefaultAPIVersion {
			t.Errorf("Ubag-Api-Version = %q, want %q", got, cli.DefaultAPIVersion)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("Authorization = %q, want Bearer test-secret", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","version":"1.2.3"}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	h, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("Status = %q, want ok", h.Status)
	}
	if h.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", h.Version)
	}
}

func TestHealth_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	_, err := client.Health(context.Background())
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Version
// ─────────────────────────────────────────────────────────────────────────────

func TestVersion_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/version" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"2.0.0","api_versions":["2026-05-22"]}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	v, err := client.Version(context.Background())
	if err != nil {
		t.Fatalf("Version() error: %v", err)
	}
	if v.Version != "2.0.0" {
		t.Errorf("Version = %q, want 2.0.0", v.Version)
	}
	if len(v.APIVersions) != 1 || v.APIVersions[0] != "2026-05-22" {
		t.Errorf("APIVersions = %v, want [2026-05-22]", v.APIVersions)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetJob
// ─────────────────────────────────────────────────────────────────────────────

func TestGetJob_ReturnsCorrectID(t *testing.T) {
	wantID := "job-abc-123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs/"+wantID {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"job_id":%q,"status":"running","target":"browser"}`, wantID)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	job, err := client.GetJob(context.Background(), wantID)
	if err != nil {
		t.Fatalf("GetJob() error: %v", err)
	}
	if job.ID != wantID {
		t.Errorf("ID = %q, want %q", job.ID, wantID)
	}
	if job.Status != "running" {
		t.Errorf("Status = %q, want running", job.Status)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CreateJob — verify correct JSON body is sent
// ─────────────────────────────────────────────────────────────────────────────

func TestCreateJob_SendsCorrectBody(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"job_id":"job-new","status":"queued","target":"mybot"}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	req := cli.CreateJobRequest{
		Target:      "mybot",
		Prompt:      "hello world",
		CommandType: "chat",
	}
	job, err := client.CreateJob(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateJob() error: %v", err)
	}
	if job.ID != "job-new" {
		t.Errorf("ID = %q, want job-new", job.ID)
	}

	var decoded cli.CreateJobRequest
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if decoded.Target != "mybot" {
		t.Errorf("body.target = %q, want mybot", decoded.Target)
	}
	if decoded.Prompt != "hello world" {
		t.Errorf("body.prompt = %q, want hello world", decoded.Prompt)
	}
	if decoded.CommandType != "chat" {
		t.Errorf("body.command_type = %q, want chat", decoded.CommandType)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ListJobs
// ─────────────────────────────────────────────────────────────────────────────

func TestListJobs_ReturnsJobs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs" || r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jobs":[{"job_id":"j1","status":"done"},{"job_id":"j2","status":"queued"}],"api_version":"2026-05-22"}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	jobs, err := client.ListJobs(context.Background())
	if err != nil {
		t.Fatalf("ListJobs() error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}
	if jobs[0].ID != "j1" {
		t.Errorf("jobs[0].ID = %q, want j1", jobs[0].ID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ListTargets
// ─────────────────────────────────────────────────────────────────────────────

func TestListTargets_ReturnsArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/targets" || r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"name":"browser","kind":"headless"},{"name":"api","kind":"rest"}]`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	targets, err := client.ListTargets(context.Background())
	if err != nil {
		t.Fatalf("ListTargets() error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}
	if targets[0].Name != "browser" {
		t.Errorf("targets[0].Name = %q, want browser", targets[0].Name)
	}
	if targets[0].Kind != "headless" {
		t.Errorf("targets[0].Kind = %q, want headless", targets[0].Kind)
	}
}

func TestListTargets_WrappedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"name":"wrapped","kind":"k1"}],"api_version":"2026-05-22"}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	targets, err := client.ListTargets(context.Background())
	if err != nil {
		t.Fatalf("ListTargets() wrapped error: %v", err)
	}
	if len(targets) != 1 || targets[0].Name != "wrapped" {
		t.Errorf("unexpected targets: %v", targets)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PurgeCache
// ─────────────────────────────────────────────────────────────────────────────

func TestPurgeCache_SendsPostToCorrectURL(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	if err := client.PurgeCache(context.Background()); err != nil {
		t.Fatalf("PurgeCache() error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/cache/invalidate" {
		t.Errorf("path = %q, want /v1/cache/invalidate", gotPath)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WatchJob — SSE streaming
// ─────────────────────────────────────────────────────────────────────────────

func TestWatchJob_StreamsEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/sse/jobs/") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		// Write a few SSE lines then close.
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter does not implement Flusher")
			return
		}
		events := []string{
			"data: {\"status\":\"running\"}",
			"",
			"data: {\"status\":\"done\"}",
			"",
		}
		for _, line := range events {
			fmt.Fprintln(w, line)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	client := newTestClient(srv)
	var received []string
	err := client.WatchJob(context.Background(), "job-sse-1", func(event string) {
		received = append(received, event)
	})
	if err != nil {
		t.Fatalf("WatchJob() error: %v", err)
	}
	// Expect the two non-empty data lines.
	want := []string{
		`data: {"status":"running"}`,
		`data: {"status":"done"}`,
	}
	if len(received) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(received), len(want), received)
	}
	for i, w := range want {
		if received[i] != w {
			t.Errorf("event[%d] = %q, want %q", i, received[i], w)
		}
	}
}

func TestWatchJob_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// Send one event, then block until the client disconnects.
		fmt.Fprintln(w, "data: {\"status\":\"running\"}")
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	client := newTestClient(srv)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = client.WatchJob(ctx, "job-cancel", func(event string) {
			cancel() // cancel after receiving first event
		})
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("WatchJob did not return after context cancellation within 3s")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// No auth header when AppSecret is empty
// ─────────────────────────────────────────────────────────────────────────────

func TestNoAuthHeader_WhenSecretEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no Authorization header, got %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer srv.Close()

	client := &cli.Client{
		BaseURL:    srv.URL,
		AppSecret:  "",
		APIVersion: cli.DefaultAPIVersion,
		HTTPClient: srv.Client(),
	}
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health() error: %v", err)
	}
}
