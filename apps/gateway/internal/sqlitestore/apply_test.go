package sqlitestore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// oldJobsTable is the pre-scheduled-shape gateway_jobs definition (no
// not_before column, 13-value status CHECK): the exact shape every sqlite
// database created before the scheduled-job migration carries.
const oldJobsTable = `
CREATE TABLE IF NOT EXISTS gateway_jobs (
  id TEXT PRIMARY KEY,
  api_version TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  app_id TEXT NOT NULL,
  idempotency_key TEXT,
  target TEXT NOT NULL,
  command_type TEXT NOT NULL,
  client_json TEXT,
  conversation_id TEXT,
  template_id TEXT,
  input_json TEXT,
  options_json TEXT,
  callbacks_json TEXT,
  context_json TEXT,
  status TEXT NOT NULL CHECK (
    status IN (
      'created',
      'queued',
      'assigned',
      'running',
      'token_streaming',
      'completing',
      'completed',
      'completed_with_warnings',
      'failed_retryable',
      'failed_terminal',
      'dead_letter',
      'cancelled',
      'timed_out'
    )
  ),
  result_json TEXT,
  trace_id TEXT,
  retry_of TEXT,
  event_sequence INTEGER NOT NULL DEFAULT 0 CHECK (event_sequence >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);`

func openTestDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestApplyMigratesPreExistingJobsTable pins the upgrade path: a database
// created by the old schema keeps every row and gains scheduled-job support
// (not_before column + widened CHECK) after Apply. Without this, scheduled
// job creation fails on every pre-existing sqlite database.
func TestApplyMigratesPreExistingJobsTable(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, "old.db")
	if _, err := db.ExecContext(ctx, oldJobsTable); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO gateway_jobs
		(id, api_version, tenant_id, app_id, target, command_type, status, event_sequence, created_at, updated_at)
		VALUES ('job_old001', '2026-05-22', 't', 'a', 'mock', 'chat', 'running', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed old row: %v", err)
	}

	if err := Apply(ctx, db); err != nil {
		t.Fatalf("Apply on old database: %v", err)
	}

	var status, target string
	if err := db.QueryRowContext(ctx, `SELECT status, target FROM gateway_jobs WHERE id = 'job_old001'`).Scan(&status, &target); err != nil {
		t.Fatalf("old row lost in migration: %v", err)
	}
	if status != "running" || target != "mock" {
		t.Fatalf("old row corrupted: status=%q target=%q", status, target)
	}

	// The widened CHECK now accepts scheduled; the new column stores the time.
	if _, err := db.ExecContext(ctx, `INSERT INTO gateway_jobs
		(id, api_version, tenant_id, app_id, target, command_type, status, event_sequence, created_at, updated_at, not_before)
		VALUES ('job_new001', '2026-05-22', 't', 'a', 'mock', 'chat', 'scheduled', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-07-01T09:30:00Z')`); err != nil {
		t.Fatalf("scheduled insert after migration: %v", err)
	}

	// Second Apply is a no-op (idempotent boot path).
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
}

// TestApplyFreshDatabaseHasScheduledShape pins the fresh-boot path: brand-new
// databases come out of Apply with the column and the widened CHECK directly.
func TestApplyFreshDatabaseHasScheduledShape(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, "fresh.db")
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("Apply on fresh database: %v", err)
	}
	var tableSQL string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'gateway_jobs'`).Scan(&tableSQL); err != nil {
		t.Fatalf("read table definition: %v", err)
	}
	upper := strings.ToUpper(tableSQL)
	if !strings.Contains(upper, "NOT_BEFORE") {
		t.Fatal("fresh gateway_jobs lacks not_before column")
	}
	if !strings.Contains(upper, "'SCHEDULED'") {
		t.Fatal("fresh gateway_jobs CHECK lacks scheduled status")
	}
}

// TestExtractJobsDDLGuardsFormatDrift pins the migration's parser contract:
// the extractor must find the table and all four job indexes in the embedded
// schema, or fail closed (never migrate with a half-parsed definition).
func TestExtractJobsDDLGuardsFormatDrift(t *testing.T) {
	create, indexes, err := extractJobsDDL()
	if err != nil {
		t.Fatalf("extractJobsDDL: %v", err)
	}
	if !strings.Contains(strings.ToUpper(create), "NOT_BEFORE") {
		t.Fatal("extracted table definition lacks not_before")
	}
	if len(indexes) != 4 {
		t.Fatalf("expected 4 gateway_jobs indexes, got %d", len(indexes))
	}
}
