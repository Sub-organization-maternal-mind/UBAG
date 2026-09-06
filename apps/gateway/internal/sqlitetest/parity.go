// Package sqlitetest holds the shared migration-parity check used by every
// store package that carries both a sqlite migration file (consumed by
// `ubag db-migrate`) and a runtime self-bootstrap (the path every gateway
// boot takes): the two must produce identical tables and indexes, or schema
// changes drift silently between the offline and online provisioning paths.
package sqlitetest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// BootstrapFunc builds a store over db and runs its schema bootstrap
// (Ready/Migrate/EnsureSchema) — the exact runtime path.
type BootstrapFunc func(db *sql.DB) error

// AssertMigrationParity applies migrationFile to one database, runs bootstrap
// on another, and requires identical columns and indexes for every table.
// The migration path is resolved relative to the calling package's test
// working directory (Go runs package tests with CWD set to the package dir).
func AssertMigrationParity(t *testing.T, migrationFile string, tables []string, bootstrap BootstrapFunc) {
	t.Helper()
	ctx := context.Background()

	migrated, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "migrated.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	migrated.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = migrated.Close() })

	bootstrapped, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "bootstrapped.db"))
	if err != nil {
		t.Fatalf("open bootstrapped db: %v", err)
	}
	bootstrapped.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = bootstrapped.Close() })

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", "sqlite", migrationFile))
	if err != nil {
		t.Fatalf("read migration file: %v", err)
	}
	// The file is self-contained (tracking-table CREATE included, like the
	// edge series) — exec it exactly as `ubag db-migrate` would.
	if _, err := migrated.ExecContext(ctx, string(raw)); err != nil {
		t.Fatalf("apply migration file: %v", err)
	}
	if err := bootstrap(bootstrapped); err != nil {
		t.Fatalf("runtime bootstrap: %v", err)
	}

	for _, table := range tables {
		if want, got := pragmaTableInfo(t, ctx, migrated, table), pragmaTableInfo(t, ctx, bootstrapped, table); want != got {
			t.Errorf("table %s columns differ:\n migration: %s\n bootstrap: %s", table, want, got)
		}
		if want, got := indexDefs(t, ctx, migrated, table), indexDefs(t, ctx, bootstrapped, table); want != got {
			t.Errorf("table %s indexes differ:\n migration: %s\n bootstrap: %s", table, want, got)
		}
	}
}

func pragmaTableInfo(t *testing.T, ctx context.Context, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT cid, name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt *string
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan pragma: %v", err)
		}
		d := "NULL"
		if dflt != nil {
			d = *dflt
		}
		out = append(out, strings.Join([]string{
			norm(name), norm(typ), strconv.Itoa(notnull), d, strconv.Itoa(pk),
		}, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pragma rows: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("table %s missing on one side", table)
	}
	return strings.Join(out, "\n")
}

func indexDefs(t *testing.T, ctx context.Context, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT name, sql FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql IS NOT NULL ORDER BY name", table)
	if err != nil {
		t.Fatalf("index list(%s): %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		out = append(out, name+" :: "+norm(def))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("index rows: %v", err)
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func norm(s string) string {
	return strings.ToUpper(strings.Join(strings.Fields(s), " "))
}
