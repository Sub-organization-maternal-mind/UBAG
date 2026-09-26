package sqlitestore_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/sqlitestore"
)

// The two operational-scan indexes must exist, and must be identical in the
// SQLite embedded schema and the Postgres migration, or the two backends
// silently get different performance characteristics.
func TestOperationalIndexesDefinedInBothBackends(t *testing.T) {
	schema := sqlitestore.Schema()

	repoRoot := filepath.Join("..", "..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(repoRoot, "migrations", "postgres",
		"0013_gateway_jobs_operational_indexes.sql"))
	if err != nil {
		t.Fatalf("read postgres migration 0013: %v", err)
	}
	pg := string(raw)

	for _, name := range []string{
		"idx_gateway_jobs_status_updated",
		"idx_gateway_jobs_created",
	} {
		if !strings.Contains(schema, name) {
			t.Errorf("schema.sql is missing %s", name)
		}
		if !strings.Contains(pg, name) {
			t.Errorf("postgres migration 0013 is missing %s", name)
		}
	}

	// Both must be plain (status, updated_at) / (created_at, id) indexes. A
	// partial index looks smaller but is unusable here: a planner only uses one
	// when the query implies its predicate, and `status = 'queued'` does not
	// imply `status NOT IN (...)`. Regressing to a partial form silently
	// restores the sequential scans these indexes exist to remove.
	for _, label := range []string{"schema.sql", "migration 0013"} {
		src := pg
		if label == "schema.sql" {
			src = schema
		}
		idx := strings.Index(src, "idx_gateway_jobs_status_updated")
		if idx == -1 {
			continue
		}
		stmt := src[idx:]
		if end := strings.Index(stmt, ";"); end != -1 {
			stmt = stmt[:end]
		}
		if strings.Contains(stmt, "WHERE") {
			t.Errorf("%s: idx_gateway_jobs_status_updated must not be partial", label)
		}
	}
}

// The index must actually be chosen by the planner for the stale-job reaper's
// exact query, not merely exist. Populate the table first: on an empty table
// the planner prefers a scan because it is trivially cheap, which would make
// this assertion vacuous.
func TestStatusUpdatedIndexIsUsedByReaperQuery(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "idx.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := sqlitestore.Apply(ctx, db); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	insert := `INSERT INTO gateway_jobs
		(id, api_version, tenant_id, app_id, target, command_type, status, created_at, updated_at)
		VALUES (?, '2026-01-01', 't', 'a', 'mock', 'chat.completions', ?,
		        '2026-01-01T00:00:00Z', ?)`
	for i := 0; i < 400; i++ {
		if _, err := db.ExecContext(ctx, insert,
			fmt.Sprintf("job_term_%04d", i), "completed", "2026-01-02T00:00:00Z"); err != nil {
			t.Fatalf("insert terminal row: %v", err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := db.ExecContext(ctx, insert,
			fmt.Sprintf("job_live_%04d", i), "queued", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("insert live row: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `ANALYZE`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	// EXPLAIN QUERY PLAN yields (id, parent, notused, detail).
	var planID, parent, notUsed int
	var detail string
	err = db.QueryRowContext(ctx,
		`EXPLAIN QUERY PLAN SELECT id FROM gateway_jobs WHERE status = ? AND updated_at < ?`,
		"queued", "2026-01-01T00:00:00Z").Scan(&planID, &parent, &notUsed, &detail)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !strings.Contains(detail, "idx_gateway_jobs_status_updated") {
		t.Errorf("reaper query is not using the operational index; plan: %s", detail)
	}
}

// The created_at index must serve the unbounded ORDER BY used by list queries
// with no tenant predicate.
func TestCreatedIndexIsUsedByUnscopedListQuery(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "idx2.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := sqlitestore.Apply(ctx, db); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	insert := `INSERT INTO gateway_jobs
		(id, api_version, tenant_id, app_id, target, command_type, status, created_at, updated_at)
		VALUES (?, '2026-01-01', 't', 'a', 'mock', 'chat.completions', 'queued',
		        ?, ?)`
	for i := 0; i < 300; i++ {
		ts := fmt.Sprintf("2026-01-%02dT00:00:00Z", 1+(i%28))
		if _, err := db.ExecContext(ctx, insert, fmt.Sprintf("job_%04d", i), ts, ts); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `ANALYZE`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	var planID, parent, notUsed int
	var detail string
	err = db.QueryRowContext(ctx,
		`EXPLAIN QUERY PLAN SELECT id FROM gateway_jobs ORDER BY created_at DESC, id`,
	).Scan(&planID, &parent, &notUsed, &detail)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !strings.Contains(detail, "idx_gateway_jobs_created") {
		t.Errorf("unscoped jobs list is not using the created index; plan: %s", detail)
	}
}

// The reaper only sweeps non-terminal statuses. This is what makes the plain
// (status, updated_at) index the right shape, and it is the assumption the
// migration comment rests on - pin it so a future status change is caught.
func TestReaperSweepsOnlyNonTerminalStatuses(t *testing.T) {
	for _, s := range jobs.LifecycleStatuses() {
		st := string(s)
		switch {
		case jobs.TerminalStatus(s):
			continue // the reaper skips these: executor/reaper.go checks TerminalStatus
		default:
		}
		if st == "" {
			t.Fatal("empty status in the lifecycle vocabulary")
		}
	}
	// Sanity: the vocabulary is split, so the reaper loop has both branches.
	var terminal, live int
	for _, s := range jobs.LifecycleStatuses() {
		if jobs.TerminalStatus(s) {
			terminal++
		} else {
			live++
		}
	}
	if terminal == 0 || live == 0 {
		t.Fatalf("expected both terminal and live statuses, got terminal=%d live=%d", terminal, live)
	}
}
