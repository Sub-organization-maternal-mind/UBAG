package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/cli"
)

// withTempConfig points the package-level config path at a temp file inside
// dir and restores the original path when done.
func withTempConfig(t *testing.T) (cfgPath string, restore func()) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, ".ubag", "config.json")
	cli.SetConfigPath(cfgPath)
	return cfgPath, func() {
		// Reset to default by pointing at a non-existent path.
		// The next test that calls withTempConfig will overwrite it anyway.
		cli.SetConfigPath(cfgPath) // keep as-is; each test sets its own
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LoadConfig — defaults when no file
// ─────────────────────────────────────────────────────────────────────────────

func TestLoadConfig_DefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	cli.SetConfigPath(filepath.Join(dir, "nonexistent", "config.json"))

	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.BaseURL != cli.DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, cli.DefaultBaseURL)
	}
	if cfg.APIVersion != cli.DefaultAPIVersion {
		t.Errorf("APIVersion = %q, want %q", cfg.APIVersion, cli.DefaultAPIVersion)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SaveConfig / LoadConfig round-trip
// ─────────────────────────────────────────────────────────────────────────────

func TestSaveAndLoadConfig_RoundTrip(t *testing.T) {
	cfgPath, _ := withTempConfig(t)
	_ = cfgPath // path already set by withTempConfig

	want := cli.Config{
		BaseURL:    "https://api.example.com",
		AppSecret:  "super-secret",
		APIVersion: "2026-05-22",
	}

	if err := cli.SaveConfig(want); err != nil {
		t.Fatalf("SaveConfig() error: %v", err)
	}

	got, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if got.BaseURL != want.BaseURL {
		t.Errorf("BaseURL = %q, want %q", got.BaseURL, want.BaseURL)
	}
	if got.AppSecret != want.AppSecret {
		t.Errorf("AppSecret = %q, want %q", got.AppSecret, want.AppSecret)
	}
	if got.APIVersion != want.APIVersion {
		t.Errorf("APIVersion = %q, want %q", got.APIVersion, want.APIVersion)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SaveConfig — correct JSON keys
// ─────────────────────────────────────────────────────────────────────────────

func TestSaveConfig_CorrectJSONKeys(t *testing.T) {
	cfgPath, _ := withTempConfig(t)

	want := cli.Config{
		BaseURL:    "http://localhost:9090",
		AppSecret:  "key123",
		APIVersion: "2026-05-22",
	}
	if err := cli.SaveConfig(want); err != nil {
		t.Fatalf("SaveConfig() error: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}
	if m["base_url"] != want.BaseURL {
		t.Errorf("base_url = %q, want %q", m["base_url"], want.BaseURL)
	}
	if m["app_secret"] != want.AppSecret {
		t.Errorf("app_secret = %q, want %q", m["app_secret"], want.AppSecret)
	}
	if m["api_version"] != want.APIVersion {
		t.Errorf("api_version = %q, want %q", m["api_version"], want.APIVersion)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SaveConfig — file permissions (non-Windows only)
// ─────────────────────────────────────────────────────────────────────────────

func TestSaveConfig_FilePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod is a no-op on Windows")
	}

	cfgPath, _ := withTempConfig(t)

	if err := cli.SaveConfig(cli.Config{
		BaseURL:    "http://localhost:8080",
		APIVersion: cli.DefaultAPIVersion,
	}); err != nil {
		t.Fatalf("SaveConfig() error: %v", err)
	}

	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}
}
