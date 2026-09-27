package cli_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/cli"
)

// writeMigrationFile writes a migration file and returns its sha256 hex digest.
func writeMigrationFile(t *testing.T, dir, filename, body string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write migration %s: %v", filename, err)
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func ledgerChecksum(t *testing.T, dbPath, version string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var checksum string
	if err := db.QueryRow("SELECT checksum FROM gateway_schema_migrations WHERE version = ?", version).Scan(&checksum); err != nil {
		t.Fatalf("read ledger checksum for %s: %v", version, err)
	}
	return checksum
}

// TestCmdMigrate_RunnerChecksumIsAuthoritative pins workstream-4 item 1: the
// shipped migration files self-record PLACEHOLDER ledger checksums (a file
// cannot contain its own sha256 - that is circular), so the runner's insert
// must overwrite them with the real sha256 of the file bytes. With the old
// ON CONFLICT DO NOTHING the file's placeholder won and drift detection could
// never fire.
func TestCmdMigrate_RunnerChecksumIsAuthoritative(t *testing.T) {
	migrationsDir := t.TempDir()
	sqliteDir := filepath.Join(migrationsDir, "sqlite")
	if err := os.MkdirAll(sqliteDir, 0o700); err != nil {
		t.Fatalf("create migrations dir: %v", err)
	}

	// Self-records exactly the way the shipped migrations do: placeholder
	// checksum, short version key.
	body := "CREATE TABLE IF NOT EXISTS authoritative_a (id INTEGER PRIMARY KEY);\n" +
		"INSERT INTO gateway_schema_migrations (version, name, checksum) VALUES ('0001', 'authoritative_a', 'manual-v0-sqlite') " +
		"ON CONFLICT (version) DO NOTHING;\n"
	want := writeMigrationFile(t, sqliteDir, "0001_authoritative_a.sql", body)

	dbPath := filepath.Join(t.TempDir(), "test.db")
	f, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("create db file: %v", err)
	}
	f.Close()

	t.Setenv("UBAG_MIGRATIONS_DIR", migrationsDir)
	out, err := cli.DispatchBackup(context.Background(), []string{"migrate", "--store", "sqlite", "--dsn", dbPath})
	if err != nil {
		t.Fatalf("migrate error: %v", err)
	}
	if !strings.Contains(out, "applied 0001") {
		t.Fatalf("expected 'applied 0001', got: %q", out)
	}

	got := ledgerChecksum(t, dbPath, "0001")
	if got == "manual-v0-sqlite" {
		t.Fatal("the migration file's placeholder checksum won; drift detection can never fire")
	}
	if got != want {
		t.Fatalf("ledger checksum %q does not match the real file sha256 %q", got, want)
	}
}

// TestCmdMigrate_VerifyBackfillsPlaceholderChecksums covers the "migrate
// --verify" backfill: already-applied migrations whose ledger rows still carry
// a legacy placeholder get the real sha256 written, so drift detection becomes
// active for them without re-running anything.
func TestCmdMigrate_VerifyBackfillsPlaceholderChecksums(t *testing.T) {
	migrationsDir := t.TempDir()
	sqliteDir := filepath.Join(migrationsDir, "sqlite")
	if err := os.MkdirAll(sqliteDir, 0o700); err != nil {
		t.Fatalf("create migrations dir: %v", err)
	}
	want1 := writeMigrationFile(t, sqliteDir, "0001_backfill_a.sql", "CREATE TABLE IF NOT EXISTS backfill_a (id INTEGER PRIMARY KEY);")
	want2 := writeMigrationFile(t, sqliteDir, "0002_backfill_b.sql", "CREATE TABLE IF NOT EXISTS backfill_b (id INTEGER PRIMARY KEY);")

	dbPath := filepath.Join(t.TempDir(), "test.db")
	f, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("create db file: %v", err)
	}
	f.Close()

	t.Setenv("UBAG_MIGRATIONS_DIR", migrationsDir)
	ctx := context.Background()
	if _, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath}); err != nil {
		t.Fatalf("migrate error: %v", err)
	}

	// Simulate the shipped placeholder conventions: "manual-v0*" (files that
	// self-record) and "" (files that record an empty checksum).
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`UPDATE gateway_schema_migrations SET checksum = 'manual-v0-sqlite' WHERE version = '0001'`); err != nil {
		t.Fatalf("seed placeholder checksum: %v", err)
	}
	if _, err := db.Exec(`UPDATE gateway_schema_migrations SET checksum = '' WHERE version = '0002'`); err != nil {
		t.Fatalf("seed empty checksum: %v", err)
	}
	db.Close()

	out, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath, "--verify"})
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !strings.Contains(out, "backfilled 0001") || !strings.Contains(out, "backfilled 0002") {
		t.Fatalf("expected both migrations backfilled, got: %q", out)
	}
	for _, tc := range []struct{ version, want string }{{"0001", want1}, {"0002", want2}} {
		if got := ledgerChecksum(t, dbPath, tc.version); got != tc.want {
			t.Errorf("version %s: ledger checksum %q, want real file sha256 %q", tc.version, got, tc.want)
		}
	}
}

// TestCmdMigrate_VerifyDetectsDrift pins the fail-closed behavior of --verify:
// a real recorded checksum that no longer matches the file on disk is an
// error, not a silent skip.
func TestCmdMigrate_VerifyDetectsDrift(t *testing.T) {
	migrationsDir := t.TempDir()
	sqliteDir := filepath.Join(migrationsDir, "sqlite")
	if err := os.MkdirAll(sqliteDir, 0o700); err != nil {
		t.Fatalf("create migrations dir: %v", err)
	}
	path := filepath.Join(sqliteDir, "0001_verify_drift.sql")
	if err := os.WriteFile(path, []byte("CREATE TABLE IF NOT EXISTS verify_drift (id INTEGER PRIMARY KEY);"), 0o600); err != nil {
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
		t.Fatalf("migrate error: %v", err)
	}

	if err := os.WriteFile(path, []byte("CREATE TABLE IF NOT EXISTS verify_drift (id INTEGER PRIMARY KEY, extra TEXT);"), 0o600); err != nil {
		t.Fatalf("rewrite migration: %v", err)
	}
	_, err = cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath, "--verify"})
	if err == nil {
		t.Fatal("expected a checksum drift error from --verify")
	}
	if !strings.Contains(err.Error(), "checksum drift detected") {
		t.Errorf("expected a drift error, got: %v", err)
	}
}

// TestCmdMigrate_VerifyReportsMissingWithoutApplying: --verify applies
// nothing; a file with no ledger row is reported as missing but is not an
// error (the container entrypoint legitimately skips optional migrations).
func TestCmdMigrate_VerifyReportsMissingWithoutApplying(t *testing.T) {
	migrationsDir := t.TempDir()
	sqliteDir := filepath.Join(migrationsDir, "sqlite")
	if err := os.MkdirAll(sqliteDir, 0o700); err != nil {
		t.Fatalf("create migrations dir: %v", err)
	}
	writeMigrationFile(t, sqliteDir, "0001_applied.sql", "CREATE TABLE IF NOT EXISTS verify_missing_a (id INTEGER PRIMARY KEY);")
	writeMigrationFile(t, sqliteDir, "0002_never_applied.sql", "CREATE TABLE IF NOT EXISTS verify_missing_b (id INTEGER PRIMARY KEY);")

	dbPath := filepath.Join(t.TempDir(), "test.db")
	f, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("create db file: %v", err)
	}
	f.Close()

	t.Setenv("UBAG_MIGRATIONS_DIR", migrationsDir)
	ctx := context.Background()
	// Apply only 0001: remove 0002 from disk before the first migrate, then
	// put it back for the verify run.
	second := filepath.Join(sqliteDir, "0002_never_applied.sql")
	body, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("read second migration: %v", err)
	}
	if err := os.Remove(second); err != nil {
		t.Fatalf("remove second migration: %v", err)
	}
	if _, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath}); err != nil {
		t.Fatalf("migrate error: %v", err)
	}
	if err := os.WriteFile(second, body, 0o600); err != nil {
		t.Fatalf("restore second migration: %v", err)
	}

	out, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath, "--verify"})
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !strings.Contains(out, "missing 0002") {
		t.Fatalf("expected 'missing 0002' in verify output, got: %q", out)
	}

	// Nothing may have been applied by verify.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'verify_missing_b'").Scan(&count); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if count != 0 {
		t.Error("--verify applied a migration; it must be read-only")
	}
}

// TestCmdMigrate_ShippedSqliteMigrationsRecordRealChecksums runs the real
// migrations/sqlite chain against a fresh database and asserts that every
// shipped file ends up in the ledger with its true sha256 - no placeholders.
// This is the end-to-end form of the backfill guarantee: a fresh
// `ubag db-migrate --store sqlite` produces a ledger where drift detection is
// active for every file.
func TestCmdMigrate_ShippedSqliteMigrationsRecordRealChecksums(t *testing.T) {
	// Test CWD is the package directory (apps/gateway/internal/cli); the repo
	// root is four levels up.
	migrationsDir, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "migrations"))
	if err != nil {
		t.Fatalf("resolve migrations dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(migrationsDir, "sqlite")); err != nil {
		t.Skipf("shipped migrations directory not found at %s: %v", migrationsDir, err)
	}

	dbPath := filepath.Join(t.TempDir(), "shipped.db")
	f, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("create db file: %v", err)
	}
	f.Close()

	t.Setenv("UBAG_MIGRATIONS_DIR", migrationsDir)
	ctx := context.Background()
	if _, err := cli.DispatchBackup(ctx, []string{"migrate", "--store", "sqlite", "--dsn", dbPath}); err != nil {
		t.Fatalf("shipped sqlite migration chain failed on a fresh database: %v", err)
	}

	sqliteDir := filepath.Join(migrationsDir, "sqlite")
	entries, err := os.ReadDir(sqliteDir)
	if err != nil {
		t.Fatalf("read shipped migrations: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version := strings.SplitN(e.Name(), "_", 2)[0]
		body, err := os.ReadFile(filepath.Join(sqliteDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		sum := sha256.Sum256(body)
		want := hex.EncodeToString(sum[:])

		got := ledgerChecksum(t, dbPath, version)
		if got != want {
			t.Errorf("%s: ledger checksum %q, want real file sha256 %q", e.Name(), got, want)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no shipped migration files were checked")
	}
}
