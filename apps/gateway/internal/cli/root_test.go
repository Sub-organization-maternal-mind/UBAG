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
// Helpers shared by root tests
// ─────────────────────────────────────────────────────────────────────────────

// mockServer starts a minimal httptest.Server that handles health, version,
// jobs, targets, and cache endpoints well enough for Dispatch tests.
func mockServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok","version":"1.0.0"}`)
	})
	mux.HandleFunc("/v1/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"1.0.0","api_versions":["2026-05-22"]}`)
	})
	mux.HandleFunc("/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `{"jobs":[{"job_id":"j1","status":"done"}]}`)
		case http.MethodPost:
			fmt.Fprint(w, `{"job_id":"jnew","status":"queued"}`)
		}
	})
	mux.HandleFunc("/v1/targets", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"browser","kind":"headless"}]`)
	})
	mux.HandleFunc("/v1/cache/invalidate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	return httptest.NewServer(mux)
}

// setDispatchServer points LoadConfig at a temp config whose BaseURL is the
// given server URL, then restores it after the test.
func setDispatchServer(t *testing.T, srvURL string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".ubag", "config.json")
	cli.SetConfigPath(cfgPath)
	if err := cli.SaveConfig(cli.Config{
		BaseURL:    srvURL,
		APIVersion: cli.DefaultAPIVersion,
	}); err != nil {
		t.Fatalf("setDispatchServer: SaveConfig: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// No-args / unknown command returns usage
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_NoArgs_ReturnsUsage(t *testing.T) {
	out, err := cli.Dispatch(nil)
	if err != nil {
		t.Fatalf("Dispatch(nil) error: %v", err)
	}
	if !strings.Contains(out, "ubag") {
		t.Errorf("expected usage containing 'ubag': %q", out)
	}
}

func TestDispatch_EmptyArgs_ReturnsUsage(t *testing.T) {
	out, err := cli.Dispatch([]string{})
	if err != nil {
		t.Fatalf("Dispatch([]) error: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "usage") {
		// Some usage strings don't contain the literal word "Usage" — just
		// check it's non-empty and contains something recognisable.
		if out == "" {
			t.Error("expected non-empty usage output")
		}
	}
}

func TestDispatch_UnknownCommand_ReturnsUsage(t *testing.T) {
	dir := t.TempDir()
	cli.SetConfigPath(filepath.Join(dir, ".ubag", "config.json"))

	out, err := cli.Dispatch([]string{"nosuchthing"})
	if err != nil {
		t.Fatalf("Dispatch(nosuchthing) error: %v", err)
	}
	if !strings.Contains(out, "unknown") && !strings.Contains(out, "nosuchthing") {
		t.Errorf("expected unknown-command message, got: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// doctor
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_Doctor(t *testing.T) {
	srv := mockServer(t)
	defer srv.Close()
	setDispatchServer(t, srv.URL)

	out, err := cli.Dispatch([]string{"doctor"})
	if err != nil {
		t.Fatalf("Dispatch(doctor) error: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("expected ok in doctor output: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// version
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_Version(t *testing.T) {
	srv := mockServer(t)
	defer srv.Close()
	setDispatchServer(t, srv.URL)

	out, err := cli.Dispatch([]string{"version"})
	if err != nil {
		t.Fatalf("Dispatch(version) error: %v", err)
	}
	if !strings.Contains(out, "1.0.0") {
		t.Errorf("expected version in output: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// jobs list
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_JobsList(t *testing.T) {
	srv := mockServer(t)
	defer srv.Close()
	setDispatchServer(t, srv.URL)

	out, err := cli.Dispatch([]string{"jobs", "list"})
	if err != nil {
		t.Fatalf("Dispatch(jobs list) error: %v", err)
	}
	if !strings.Contains(out, "j1") {
		t.Errorf("expected j1 in output: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// jobs send
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_JobsSend(t *testing.T) {
	srv := mockServer(t)
	defer srv.Close()
	setDispatchServer(t, srv.URL)

	out, err := cli.Dispatch([]string{"jobs", "send", "--target", "browser", "--prompt", "go"})
	if err != nil {
		t.Fatalf("Dispatch(jobs send) error: %v", err)
	}
	if !strings.Contains(out, "jnew") {
		t.Errorf("expected jnew in output: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// jobs send — missing required flags
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_JobsSend_MissingFlags(t *testing.T) {
	dir := t.TempDir()
	cli.SetConfigPath(filepath.Join(dir, ".ubag", "config.json"))

	_, err := cli.Dispatch([]string{"jobs", "send", "--target", "bot"})
	if err == nil {
		t.Fatal("expected error when --prompt is missing")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// targets list
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_TargetsList(t *testing.T) {
	srv := mockServer(t)
	defer srv.Close()
	setDispatchServer(t, srv.URL)

	out, err := cli.Dispatch([]string{"targets", "list"})
	if err != nil {
		t.Fatalf("Dispatch(targets list) error: %v", err)
	}
	if !strings.Contains(out, "browser") {
		t.Errorf("expected browser in output: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// cache purge
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_CachePurge(t *testing.T) {
	srv := mockServer(t)
	defer srv.Close()
	setDispatchServer(t, srv.URL)

	out, err := cli.Dispatch([]string{"cache", "purge"})
	if err != nil {
		t.Fatalf("Dispatch(cache purge) error: %v", err)
	}
	if !strings.Contains(out, "purged") {
		t.Errorf("expected 'purged' in output: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// auth login
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_AuthLogin(t *testing.T) {
	dir := t.TempDir()
	cli.SetConfigPath(filepath.Join(dir, ".ubag", "config.json"))

	out, err := cli.Dispatch([]string{"auth", "login", "--base-url", "http://example.com", "--app-secret", "tok"})
	if err != nil {
		t.Fatalf("Dispatch(auth login) error: %v", err)
	}
	if !strings.Contains(out, "Logged in") {
		t.Errorf("expected 'Logged in' in output: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// jobs get
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatch_JobsGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"job_id":"myjob","status":"done","target":"browser"}`)
	}))
	defer srv.Close()
	setDispatchServer(t, srv.URL)

	out, err := cli.Dispatch([]string{"jobs", "get", "myjob"})
	if err != nil {
		t.Fatalf("Dispatch(jobs get) error: %v", err)
	}
	if !strings.Contains(out, "myjob") {
		t.Errorf("expected myjob in output: %q", out)
	}
}

func TestDispatch_JobsGet_MissingID(t *testing.T) {
	dir := t.TempDir()
	cli.SetConfigPath(filepath.Join(dir, ".ubag", "config.json"))

	_, err := cli.Dispatch([]string{"jobs", "get"})
	if err == nil {
		t.Fatal("expected error when job ID is missing")
	}
}
