package cli_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/cli"
)

// ─────────────────────────────────────────────────────────────────────────────
// CmdDoctor
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdDoctor_HealthyServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok","version":"1.0.0"}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	out, err := cli.CmdDoctor(client)
	if err != nil {
		t.Fatalf("CmdDoctor() error: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("expected 'ok' in output: %q", out)
	}
}

func TestCmdDoctor_UnhealthyServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	_, err := cli.CmdDoctor(client)
	if err == nil {
		t.Fatal("expected error from unhealthy server")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdJobsList
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdJobsList_TableOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jobs":[{"job_id":"j1","status":"done","target":"bot"},{"job_id":"j2","status":"queued","target":"api"}]}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	out, err := cli.CmdJobsList(client, false)
	if err != nil {
		t.Fatalf("CmdJobsList() error: %v", err)
	}
	if !strings.Contains(out, "j1") {
		t.Errorf("expected j1 in output: %q", out)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("expected done in output: %q", out)
	}
	if !strings.Contains(out, "|") {
		t.Errorf("expected table formatting with pipes: %q", out)
	}
}

func TestCmdJobsList_JSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jobs":[{"job_id":"j1","status":"done"}]}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	out, err := cli.CmdJobsList(client, true)
	if err != nil {
		t.Fatalf("CmdJobsList(json) error: %v", err)
	}
	if !strings.Contains(out, "j1") {
		t.Errorf("expected j1 in JSON output: %q", out)
	}
	// JSON output should contain indentation.
	if !strings.Contains(out, "\n") {
		t.Errorf("expected indented JSON: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdAuthLogin — writes to temp config path
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdAuthLogin_WritesConfig(t *testing.T) {
	// Point config at a temp directory so this test is isolated.
	dir := t.TempDir()
	cli.SetConfigPath(filepath.Join(dir, ".ubag", "config.json"))

	// CmdAuthLogin doesn't actually use the client, so we can pass a nil-ish one.
	client := &cli.Client{
		BaseURL:    "http://old.example.com",
		AppSecret:  "",
		APIVersion: cli.DefaultAPIVersion,
		HTTPClient: &http.Client{},
	}

	wantURL := "https://new.example.com"
	wantSecret := "my-secret"
	out, err := cli.CmdAuthLogin(client, wantURL, wantSecret)
	if err != nil {
		t.Fatalf("CmdAuthLogin() error: %v", err)
	}
	if !strings.Contains(out, wantURL) {
		t.Errorf("expected %q in output %q", wantURL, out)
	}

	// Verify config was persisted.
	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() after login error: %v", err)
	}
	if cfg.BaseURL != wantURL {
		t.Errorf("saved BaseURL = %q, want %q", cfg.BaseURL, wantURL)
	}
	if cfg.AppSecret != wantSecret {
		t.Errorf("saved AppSecret = %q, want %q", cfg.AppSecret, wantSecret)
	}
}

func TestCmdAuthLogin_Message(t *testing.T) {
	dir := t.TempDir()
	cli.SetConfigPath(filepath.Join(dir, ".ubag", "config.json"))

	client := &cli.Client{
		BaseURL:    "",
		AppSecret:  "",
		APIVersion: cli.DefaultAPIVersion,
		HTTPClient: &http.Client{},
	}

	out, err := cli.CmdAuthLogin(client, "http://example.com", "tok")
	if err != nil {
		t.Fatalf("CmdAuthLogin() error: %v", err)
	}
	if !strings.HasPrefix(out, "Logged in to") {
		t.Errorf("output should start with 'Logged in to', got: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdTargetsList
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdTargetsList_TableOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"browser","kind":"headless"}]`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	out, err := cli.CmdTargetsList(client, false)
	if err != nil {
		t.Fatalf("CmdTargetsList() error: %v", err)
	}
	if !strings.Contains(out, "browser") {
		t.Errorf("expected browser in output: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdCachePurge
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdCachePurge_ReturnsConfirmation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	out, err := cli.CmdCachePurge(client)
	if err != nil {
		t.Fatalf("CmdCachePurge() error: %v", err)
	}
	if !strings.Contains(out, "purged") {
		t.Errorf("expected 'purged' in output: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdVersion
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdVersion_ReturnsVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"3.1.4","api_versions":["2026-05-22"]}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	out, err := cli.CmdVersion(client)
	if err != nil {
		t.Fatalf("CmdVersion() error: %v", err)
	}
	if !strings.Contains(out, "3.1.4") {
		t.Errorf("expected version 3.1.4 in output: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdJobsGet
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdJobsGet_TableOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"job_id":"jget","status":"done","target":"browser"}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	out, err := cli.CmdJobsGet(client, "jget", false)
	if err != nil {
		t.Fatalf("CmdJobsGet() error: %v", err)
	}
	if !strings.Contains(out, "jget") {
		t.Errorf("expected jget in output: %q", out)
	}
}

func TestCmdJobsSend_ReturnsID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"job_id":"new-job","status":"queued"}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	out, err := cli.CmdJobsSend(client, "bot", "hello", "chat")
	if err != nil {
		t.Fatalf("CmdJobsSend() error: %v", err)
	}
	if !strings.Contains(out, "new-job") {
		t.Errorf("expected new-job in output: %q", out)
	}
}
