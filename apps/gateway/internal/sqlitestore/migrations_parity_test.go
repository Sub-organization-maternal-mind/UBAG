package sqlitestore

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestSqliteMigrationChainCoversEmbeddedSchema pins workstream-4 item 3: the
// offline migration chain (migrations/sqlite/*.sql, applied by
// `ubag migrate --store sqlite`) must produce every table the gateway's live
// SQLite stores need, i.e. every table in the embedded schema.sql, when run
// against a fresh database.
//
// Known-intentional differences (extra tables in the migrated database, NOT
// checked here):
//   - edge_schema_migrations, edge_idempotency_keys, edge_blob_objects,
//     edge_outbox_events, edge_queue_* - the edge-store tier (0001/0002), the
//     DDL home of packages/edge-store;
//   - webhook_deliveries / webhook_delivery_attempts - the edge webhook-outbox
//     parity schema (0003); no gateway code reads them, but they are pinned by
//     packages/edge-store/test/run-conformance.mjs and therefore deliberately
//     kept;
//   - the 0007 phase-2 blueprint tables (apps, tenants, automation_jobs, ...)
//     and audit_log / outbox_events / semantic_cache / browser_* - not wired
//     into the gateway;
//   - gateway_audit_log, gateway_sessions (0006) and gateway_tenants (0008) -
//     live stores whose SQLite DDL ships only in the migration chain, never in
//     the embedded schema.
func TestSqliteMigrationChainCoversEmbeddedSchema(t *testing.T) {
	// Test CWD is the package directory (apps/gateway/internal/sqlitestore);
	// the repo root is four levels up.
	migrationsDir := filepath.Join("..", "..", "..", "..", "migrations", "sqlite")
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read shipped migrations dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no migration files found")
	}

	ctx := context.Background()
	db := openTestDB(t, "chain.db")
	for _, name := range files {
		body, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin tx for %s: %v", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %s on a fresh database: %v", name, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", name, err)
		}
	}

	got := map[string]bool{}
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}

	// Every CREATE TABLE in the embedded schema must exist after the chain.
	re := regexp.MustCompile(`(?im)^\s*CREATE TABLE IF NOT EXISTS (\w+)`)
	want := re.FindAllStringSubmatch(schemaSQL, -1)
	if len(want) < 14 {
		t.Fatalf("parsed only %d tables from the embedded schema; parser drift", len(want))
	}
	for _, m := range want {
		if !got[m[1]] {
			t.Errorf("table %q exists in the embedded schema (schema.sql) but is never created by migrations/sqlite", m[1])
		}
	}
}
