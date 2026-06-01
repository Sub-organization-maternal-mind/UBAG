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

	// First run: should apply both migrations.
	out1, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath})
	if err != nil {
		t.Fatalf("first migrate run error: %v", err)
	}
	if !strings.Contains(out1, "applied 0001_create_a") {
		t.Errorf("first run: expected 'applied 0001_create_a', got: %q", out1)
	}
	if !strings.Contains(out1, "applied 0002_create_b") {
		t.Errorf("first run: expected 'applied 0002_create_b', got: %q", out1)
	}

	// Second run: should skip both (idempotent).
	out2, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath})
	if err != nil {
		t.Fatalf("second migrate run error: %v", err)
	}
	if !strings.Contains(out2, "skipped 0001_create_a") {
		t.Errorf("second run: expected 'skipped 0001_create_a', got: %q", out2)
	}
	if !strings.Contains(out2, "skipped 0002_create_b") {
		t.Errorf("second run: expected 'skipped 0002_create_b', got: %q", out2)
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
