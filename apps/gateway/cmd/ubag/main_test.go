package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// edgeEnvKeys are the environment variables setEdgeDefaults may write. Each
// test pre-registers them (empty string counts as unset for the defaults
// logic) so t.Setenv restores the caller's environment afterwards.
var edgeEnvKeys = []string{
	"UBAG_PROFILE",
	"UBAG_GATEWAY_STORE",
	"UBAG_EXECUTOR_MODE",
	"UBAG_WORKER_CONSUMER_ENABLED",
	"UBAG_EXECUTOR_SPOOL_DIR",
	"UBAG_SQLITE_DSN",
}

func resetEdgeEnv(t *testing.T) {
	t.Helper()
	for _, key := range edgeEnvKeys {
		t.Setenv(key, "")
	}
}

// With nothing configured, `ubag start` must be able to boot in single-process
// edge mode: setEdgeDefaults fills the whole edge-profile env.
func TestSetEdgeDefaultsFillsUnsetVariables(t *testing.T) {
	resetEdgeEnv(t)

	setEdgeDefaults()

	if got := os.Getenv("UBAG_PROFILE"); got != "edge" {
		t.Errorf("UBAG_PROFILE = %q, want %q", got, "edge")
	}
	if got := os.Getenv("UBAG_GATEWAY_STORE"); got != "sqlite" {
		t.Errorf("UBAG_GATEWAY_STORE = %q, want %q", got, "sqlite")
	}
	if got := os.Getenv("UBAG_EXECUTOR_MODE"); got != "file" {
		t.Errorf("UBAG_EXECUTOR_MODE = %q, want %q", got, "file")
	}
	if got := os.Getenv("UBAG_WORKER_CONSUMER_ENABLED"); got != "true" {
		t.Errorf("UBAG_WORKER_CONSUMER_ENABLED = %q, want %q", got, "true")
	}
	if got := os.Getenv("UBAG_EXECUTOR_SPOOL_DIR"); !strings.HasSuffix(got, filepath.Join(".ubag", "spool")) {
		t.Errorf("UBAG_EXECUTOR_SPOOL_DIR = %q, want a path ending in .ubag/spool", got)
	}
	dsn := os.Getenv("UBAG_SQLITE_DSN")
	if !strings.HasPrefix(dsn, "file:") {
		t.Errorf("UBAG_SQLITE_DSN = %q, want a file: DSN", dsn)
	}
	for _, pragma := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(ON)"} {
		if !strings.Contains(dsn, pragma) {
			t.Errorf("UBAG_SQLITE_DSN = %q, missing pragma %s", dsn, pragma)
		}
	}
}

// Operator-provided configuration wins: setEdgeDefaults must never overwrite a
// variable that is already set.
func TestSetEdgeDefaultsRespectsExistingValues(t *testing.T) {
	resetEdgeEnv(t)
	t.Setenv("UBAG_PROFILE", "small")
	t.Setenv("UBAG_GATEWAY_STORE", "postgres")
	t.Setenv("UBAG_SQLITE_DSN", "file:custom.db")

	setEdgeDefaults()

	if got := os.Getenv("UBAG_PROFILE"); got != "small" {
		t.Errorf("UBAG_PROFILE = %q, want the pre-set value %q", got, "small")
	}
	if got := os.Getenv("UBAG_GATEWAY_STORE"); got != "postgres" {
		t.Errorf("UBAG_GATEWAY_STORE = %q, want the pre-set value %q", got, "postgres")
	}
	if got := os.Getenv("UBAG_SQLITE_DSN"); got != "file:custom.db" {
		t.Errorf("UBAG_SQLITE_DSN = %q, want the pre-set value %q", got, "file:custom.db")
	}
}

func TestEdgeSpoolDir(t *testing.T) {
	dir, err := edgeSpoolDir()
	if err != nil {
		t.Fatalf("edgeSpoolDir: %v", err)
	}
	if !strings.HasSuffix(dir, filepath.Join(".ubag", "spool")) {
		t.Errorf("edgeSpoolDir = %q, want a path under ~/.ubag/spool", dir)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("edgeSpoolDir = %q, want an absolute path", dir)
	}
}

func TestEdgeSQLiteDSN(t *testing.T) {
	dsn, err := edgeSQLiteDSN()
	if err != nil {
		t.Fatalf("edgeSQLiteDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "file:") {
		t.Errorf("edgeSQLiteDSN = %q, want a file: DSN", dsn)
	}
	if !strings.Contains(dsn, filepath.Join(".ubag", "gateway.db")) {
		t.Errorf("edgeSQLiteDSN = %q, want the gateway.db path under ~/.ubag", dsn)
	}
	if !strings.Contains(dsn, "journal_mode(WAL)") {
		t.Errorf("edgeSQLiteDSN = %q, want WAL journal mode", dsn)
	}
}
