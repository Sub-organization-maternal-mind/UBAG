package cli_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/cli"
	_ "modernc.org/sqlite"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test 1: dispatchBackup — no args returns usage
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatchBackup_NoArgs_ReturnsUsage(t *testing.T) {
	out, err := cli.DispatchBackup(context.Background(), nil)
	if err != nil {
		t.Fatalf("DispatchBackup(nil) error: %v", err)
	}
	if !strings.Contains(out, "ubag backup") {
		t.Errorf("expected usage containing 'ubag backup', got: %q", out)
	}
}

func TestDispatchBackup_EmptyArgs_ReturnsUsage(t *testing.T) {
	out, err := cli.DispatchBackup(context.Background(), []string{})
	if err != nil {
		t.Fatalf("DispatchBackup([]) error: %v", err)
	}
	if !strings.Contains(out, "ubag backup") {
		t.Errorf("expected usage containing 'ubag backup', got: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 2: cmdBackup with --out flag writes backup and returns "components"
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdBackup_WithOutFlag_ReturnsComponents(t *testing.T) {
	// Create a minimal SQLite DB for the backup source.
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "gateway.db")
	setupMinimalSQLiteDB(t, dbPath)

	outDir := filepath.Join(t.TempDir(), "mybackup")

	// Point UBAG_GATEWAY_STORE at our test DB.
	t.Setenv("UBAG_GATEWAY_STORE", dbPath)
	t.Setenv("UBAG_POSTGRES_DSN", "") // no Postgres

	ctx := context.Background()
	out, err := cli.DispatchBackup(ctx, []string{"backup", "--out", outDir})
	if err != nil {
		t.Fatalf("backup command error: %v", err)
	}
	if !strings.Contains(out, "components") {
		t.Errorf("expected 'components' in output, got: %q", out)
	}
	if !strings.Contains(out, outDir) {
		t.Errorf("expected outDir %q in output, got: %q", outDir, out)
	}

	// Verify manifest.json was written.
	manifestPath := filepath.Join(outDir, "manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Errorf("manifest.json not found at %s", manifestPath)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 3: cmdRestore requires --from flag
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdRestore_MissingFrom_ReturnsError(t *testing.T) {
	ctx := context.Background()
	_, err := cli.DispatchBackup(ctx, []string{"restore"})
	if err == nil {
		t.Fatal("expected error when --from is missing, got nil")
	}
	if !strings.Contains(err.Error(), "--from") {
		t.Errorf("expected error to mention '--from', got: %v", err)
	}
}

func TestCmdRestore_EmptyFrom_ReturnsError(t *testing.T) {
	ctx := context.Background()
	_, err := cli.DispatchBackup(ctx, []string{"restore", "--from", ""})
	if err == nil {
		t.Fatal("expected error when --from is empty, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 4: cmdMigrate is idempotent (second run skips already-applied migrations)
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdMigrate_Idempotent(t *testing.T) {
	// Create a temp migrations directory with two simple SQL files.
	migrationsDir := t.TempDir()
	sqliteDir := filepath.Join(migrationsDir, "sqlite")
	if err := os.MkdirAll(sqliteDir, 0o700); err != nil {
		t.Fatalf("create migrations dir: %v", err)
	}

	// Write two minimal migration files.
	migration1 := `CREATE TABLE IF NOT EXISTS test_table_a (id INTEGER PRIMARY KEY, val TEXT);`
	migration2 := `CREATE TABLE IF NOT EXISTS test_table_b (id INTEGER PRIMARY KEY, val TEXT);`
	if err := os.WriteFile(filepath.Join(sqliteDir, "0001_create_a.sql"), []byte(migration1), 0o600); err != nil {
		t.Fatalf("write migration 1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sqliteDir, "0002_create_b.sql"), []byte(migration2), 0o600); err != nil {
		t.Fatalf("write migration 2: %v", err)
	}

	// Create an empty SQLite DB.
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "test.db")
	// Touch the file so SQLite can open it.
	if f, err := os.Create(dbPath); err != nil {
		t.Fatalf("create db file: %v", err)
	} else {
		f.Close()
	}

	t.Setenv("UBAG_MIGRATIONS_DIR", migrationsDir)

	ctx := context.Background()

	// First run: should apply both migrations. The ledger key is the SHORT
	// numeric prefix, matching what the shipped migrations/postgres/*.sql and
	// migrations/sqlite/*.sql files record for themselves, so a database
	// migrated by the container entrypoint is not re-migrated here.
	out1, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath})
	if err != nil {
		t.Fatalf("first migrate run error: %v", err)
	}
	if !strings.Contains(out1, "applied 0001") {
		t.Errorf("first run: expected 'applied 0001', got: %q", out1)
	}
	if !strings.Contains(out1, "applied 0002") {
		t.Errorf("first run: expected 'applied 0002', got: %q", out1)
	}

	// Second run: should skip both (idempotent).
	out2, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath})
	if err != nil {
		t.Fatalf("second migrate run error: %v", err)
	}
	if !strings.Contains(out2, "skipped 0001 (already applied)") {
		t.Errorf("second run: expected 'skipped 0001 (already applied)', got: %q", out2)
	}
	if !strings.Contains(out2, "skipped 0002 (already applied)") {
		t.Errorf("second run: expected 'skipped 0002 (already applied)', got: %q", out2)
	}
	if strings.Contains(out2, "applied ") {
		t.Errorf("second run: no migrations should be applied, got: %q", out2)
	}

	// Verify the tables were actually created.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	for _, tbl := range []string{"test_table_a", "test_table_b"} {
		var name string
		err := db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found after migration: %v", tbl, err)
		}
	}
}

// A migration applied by the container entrypoint (psql, the production path)
// writes its own row to gateway_schema_migrations under the short version key.
// `ubag migrate` used to consult a separate schema_migrations table keyed by the
// full filename, so it saw an untouched database and re-applied every
// migration on top. This pins the shared-ledger contract: a second run must
// see the row the migration file wrote for itself.
func TestCmdMigrate_SeesLedgerWrittenByMigrationFiles(t *testing.T) {
	migrationsDir := t.TempDir()
	sqliteDir := filepath.Join(migrationsDir, "sqlite")
	if err := os.MkdirAll(sqliteDir, 0o700); err != nil {
		t.Fatalf("create migrations dir: %v", err)
	}
	// Records itself into the ledger exactly the way the shipped migrations do
	// (short version key, legacy placeholder checksum, applied_at defaulted).
	body := "CREATE TABLE IF NOT EXISTS ledger_a (id INTEGER PRIMARY KEY);\n" +
		"INSERT INTO gateway_schema_migrations (version, name, checksum) VALUES ('0001', 'ledger_a', 'manual-v0-sqlite') " +
		"ON CONFLICT (version) DO NOTHING;\n"
	if err := os.WriteFile(filepath.Join(sqliteDir, "0001_ledger_a.sql"), []byte(body), 0o600); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	f, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("create db file: %v", err)
	}
	f.Close()

	t.Setenv("UBAG_MIGRATIONS_DIR", migrationsDir)
	ctx := context.Background()

	// First run applies it; the file writes its own ledger row, which the
	// runner must then respect on the next run.
	if out, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath}); err != nil {
		t.Fatalf("first migrate error: %v", err)
	} else if !strings.Contains(out, "applied 0001") {
		t.Fatalf("first run: expected 'applied 0001', got: %q", out)
	}

	out, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath})
	if err != nil {
		t.Fatalf("second migrate error: %v", err)
	}
	if strings.Contains(out, "applied 0001") {
		t.Errorf("migration recorded in the shared ledger was re-applied: %q", out)
	}
	if !strings.Contains(out, "skipped 0001") {
		t.Errorf("expected 'skipped 0001', got: %q", out)
	}

	// The file's own row must not have been corrupted by the runner's insert.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM gateway_schema_migrations WHERE version = '0001'").Scan(&count); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly one ledger row for 0001, got %d", count)
	}
}

// The old runner recorded versions under the full filename key in a separate
// table. Those databases must not have every migration replayed onto them.
func TestCmdMigrate_AdoptsLegacySchemaMigrationsTable(t *testing.T) {
	migrationsDir := t.TempDir()
	sqliteDir := filepath.Join(migrationsDir, "sqlite")
	if err := os.MkdirAll(sqliteDir, 0o700); err != nil {
		t.Fatalf("create migrations dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(sqliteDir, "0001_legacy_a.sql"),
		[]byte("CREATE TABLE IF NOT EXISTS legacy_a (id INTEGER PRIMARY KEY);"), 0o600,
	); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	f, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("create db file: %v", err)
	}
	f.Close()

	// Seed the pre-unification ledger exactly as the old runner left it.
	seed, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("seed legacy table: %v", err)
	}
	if _, err := seed.Exec(
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES ('0001_legacy_a', '0001_legacy_a', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	seed.Close()

	t.Setenv("UBAG_MIGRATIONS_DIR", migrationsDir)
	out, err := cli.DispatchBackup(context.Background(), []string{"migrate", "--store", "sqlite", "--dsn", dbPath})
	if err != nil {
		t.Fatalf("migrate error: %v", err)
	}
	if strings.Contains(out, "applied 0001") {
		t.Errorf("legacy-ledger migration was replayed: %q", out)
	}
}

// Editing an already-applied migration used to be skipped silently forever,
// because the old ledger had no checksum column at all. A real sha256 in the
// ledger must now be enforced.
func TestCmdMigrate_DetectsEditedMigration(t *testing.T) {
	migrationsDir := t.TempDir()
	sqliteDir := filepath.Join(migrationsDir, "sqlite")
	if err := os.MkdirAll(sqliteDir, 0o700); err != nil {
		t.Fatalf("create migrations dir: %v", err)
	}
	path := filepath.Join(sqliteDir, "0001_drift.sql")
	if err := os.WriteFile(path, []byte("CREATE TABLE IF NOT EXISTS drift_a (id INTEGER PRIMARY KEY);"), 0o600); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	f, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("create db file: %v", err)
	}
	f.Close()

	t.Setenv("UBAG_MIGRATIONS_DIR", migrationsDir)
	ctx := context.Background()
	if _, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath}); err != nil {
		t.Fatalf("first migrate error: %v", err)
	}

	// Rewrite the file with different content; the recorded sha256 no longer
	// matches and the runner must refuse rather than silently skip.
	if err := os.WriteFile(path, []byte("CREATE TABLE IF NOT EXISTS drift_a (id INTEGER PRIMARY KEY, extra TEXT);"), 0o600); err != nil {
		t.Fatalf("rewrite migration: %v", err)
	}
	_, err = cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath})
	if err == nil {
		t.Fatal("expected an error for an edited already-applied migration")
	}
	if !strings.Contains(err.Error(), "was already applied with checksum") {
		t.Errorf("expected a checksum-drift error, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 5: unknown backup subcommand returns message (not error)
// ─────────────────────────────────────────────────────────────────────────────

func TestDispatchBackup_UnknownSubcmd(t *testing.T) {
	out, err := cli.DispatchBackup(context.Background(), []string{"frobnicate"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("expected 'unknown' in output, got: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// setupMinimalSQLiteDB creates a minimal SQLite DB at path with one table and row,
// suitable for backup tests.
func setupMinimalSQLiteDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("setupMinimalSQLiteDB: open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatalf("setupMinimalSQLiteDB: create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO items (name) VALUES ('test-row')`); err != nil {
		t.Fatalf("setupMinimalSQLiteDB: insert: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("setupMinimalSQLiteDB: close: %v", err)
	}
}
