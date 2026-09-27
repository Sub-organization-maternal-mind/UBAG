package sqlitestore

import (
	"context"
	"testing"
)

// TestApplyMigratesPreExistingJobsTableKeepsForeignKeys pins workstream-4
// item 6: the RENAME-based gateway_jobs rebuild must not repoint the FK
// children (gateway_job_events REFERENCES gateway_jobs) at the
// gateway_jobs_migrate_backup name, which the rebuild's final DROP TABLE would
// orphan. Modern SQLite's ALTER TABLE RENAME rewrites REFERENCES clauses in
// other tables unless PRAGMA legacy_alter_table = ON is in effect.
//
// After Apply on a pre-existing database:
//   - PRAGMA foreign_key_check must return no rows, and
//   - new child rows must be insertable with FK enforcement enabled (i.e. the
//     children really point at the recreated gateway_jobs, not a dropped
//     table).
func TestApplyMigratesPreExistingJobsTableKeepsForeignKeys(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, "old-fk.db")
	if _, err := db.ExecContext(ctx, oldJobsTable); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	// The child table exactly as the embedded schema defines it.
	if _, err := db.ExecContext(ctx, `CREATE TABLE gateway_job_events (
		id TEXT PRIMARY KEY,
		job_id TEXT NOT NULL REFERENCES gateway_jobs(id) ON DELETE CASCADE,
		api_version TEXT NOT NULL,
		type TEXT NOT NULL,
		sequence INTEGER NOT NULL CHECK (sequence >= 1),
		data_json TEXT NOT NULL DEFAULT '{}',
		trace_id TEXT,
		created_at TEXT NOT NULL,
		UNIQUE (job_id, sequence)
	)`); err != nil {
		t.Fatalf("create gateway_job_events: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO gateway_jobs
		(id, api_version, tenant_id, app_id, target, command_type, status, event_sequence, created_at, updated_at)
		VALUES ('job_fk0001', '2026-05-22', 't', 'a', 'mock', 'chat', 'running', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed old job row: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO gateway_job_events
		(id, job_id, api_version, type, sequence, created_at)
		VALUES ('evt_0001', 'job_fk0001', '2026-05-22', 'state', 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed old event row: %v", err)
	}

	if err := Apply(ctx, db); err != nil {
		t.Fatalf("Apply on old database: %v", err)
	}

	// 1. No orphaned foreign keys anywhere.
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowid, parent int64
		_ = rows.Scan(&table, &rowid, &parent)
		t.Fatalf("foreign_key_check reported a violation in %q (rowid %d, parent %d) after the jobs rebuild", table, rowid, parent)
	}
	rows.Close()

	// 2. The old rows survive.
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM gateway_jobs WHERE id = 'job_fk0001'`).Scan(&status); err != nil {
		t.Fatalf("old job row lost in migration: %v", err)
	}
	if status != "running" {
		t.Fatalf("old job row corrupted: status=%q", status)
	}

	// 3. Children resolve against the RECREATED gateway_jobs: inserting a new
	// event with FK enforcement on must succeed, and an event for an unknown
	// job must fail - proving the FK points at a live parent table.
	if _, err := db.ExecContext(ctx, `INSERT INTO gateway_job_events
		(id, job_id, api_version, type, sequence, created_at)
		VALUES ('evt_0002', 'job_fk0001', '2026-05-22', 'state', 2, '2026-01-01T00:00:01Z')`); err != nil {
		t.Fatalf("insert into gateway_job_events after rebuild: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO gateway_job_events
		(id, job_id, api_version, type, sequence, created_at)
		VALUES ('evt_0003', 'job_missing', '2026-05-22', 'state', 1, '2026-01-01T00:00:02Z')`); err == nil {
		t.Fatal("gateway_job_events foreign key is not enforced against the recreated gateway_jobs table")
	}
}
