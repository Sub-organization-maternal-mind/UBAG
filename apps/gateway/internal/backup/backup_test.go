package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// newTestSQLiteDB creates a temp SQLite DB with a simple table + row.
func newTestSQLiteDB(t *testing.T) (dbPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO items (name) VALUES ('test-row')`); err != nil {
		t.Fatalf("insert row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	return dbPath
}

// TestBackup_SQLiteRoundTrip verifies the happy-path SQLite backup:
// manifest exists, is parseable, checksum matches, and Verify passes.
func TestBackup_SQLiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	srcDB := newTestSQLiteDB(t)
	outDir := t.TempDir()

	m, err := Run(ctx, Options{
		Profile:    "test",
		SQLitePath: srcDB,
		OutDir:     outDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// manifest.json must exist and be re-parseable
	readBack, err := ReadManifest(outDir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if readBack.Profile != "test" {
		t.Errorf("profile: want %q, got %q", "test", readBack.Profile)
	}
	if readBack.StoreKind != StoreKindSQLite {
		t.Errorf("store_kind: want %q, got %q", StoreKindSQLite, readBack.StoreKind)
	}

	// Must have exactly one component
	if len(m.Components) != 1 {
		t.Fatalf("components: want 1, got %d", len(m.Components))
	}
	comp := m.Components[0]

	// Checksum must match the actual backup file
	backupFile := filepath.Join(outDir, comp.Path)
	data, err := os.ReadFile(backupFile)
	if err != nil {
		t.Fatalf("read backup file: %v", err)
	}
	sum := sha256.Sum256(data)
	wantChecksum := hex.EncodeToString(sum[:])
	if comp.Checksum != wantChecksum {
		t.Errorf("checksum mismatch: want %s, got %s", wantChecksum, comp.Checksum)
	}

	// Verify must pass
	if err := m.Verify(outDir); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

// TestBackup_TamperDetection verifies that overwriting the backup file causes
// Verify to return a non-nil error.
func TestBackup_TamperDetection(t *testing.T) {
	ctx := context.Background()
	srcDB := newTestSQLiteDB(t)
	outDir := t.TempDir()

	m, err := Run(ctx, Options{
		SQLitePath: srcDB,
		OutDir:     outDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Tamper: overwrite backup file with garbage
	backupFile := filepath.Join(outDir, m.Components[0].Path)
	if err := os.WriteFile(backupFile, []byte("corrupted content"), 0o600); err != nil {
		t.Fatalf("tamper write: %v", err)
	}

	if err := m.Verify(outDir); err == nil {
		t.Error("Verify: want non-nil error after tampering, got nil")
	}
}

// TestBackup_PostgresSkippedWhenNoDSN verifies that when PostgresDSN is empty,
// no .pgdump file is created and the manifest has only 1 component.
func TestBackup_PostgresSkippedWhenNoDSN(t *testing.T) {
	ctx := context.Background()
	srcDB := newTestSQLiteDB(t)
	outDir := t.TempDir()

	m, err := Run(ctx, Options{
		SQLitePath:  srcDB,
		PostgresDSN: "", // explicitly empty
		OutDir:      outDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(m.Components) != 1 {
		t.Errorf("components: want 1, got %d", len(m.Components))
	}

	pgDump := filepath.Join(outDir, "gateway.pgdump")
	if _, err := os.Stat(pgDump); !os.IsNotExist(err) {
		t.Errorf("expected gateway.pgdump to not exist, but it does (or stat error: %v)", err)
	}
}

// TestBackup_PostgresRoundTrip is an integration test that only runs when
// UBAG_TEST_POSTGRES_DSN is set.
func TestBackup_PostgresRoundTrip(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN not set")
	}

	ctx := context.Background()
	outDir := t.TempDir()

	m, err := Run(ctx, Options{
		Profile:     "integration",
		PostgresDSN: dsn,
		OutDir:      outDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have at least one component (the pgdump)
	if len(m.Components) == 0 {
		t.Fatal("components: want at least 1")
	}

	pgDump := filepath.Join(outDir, "gateway.pgdump")
	if _, err := os.Stat(pgDump); err != nil {
		t.Errorf("gateway.pgdump: %v", err)
	}

	if err := m.Verify(outDir); err != nil {
		t.Errorf("Verify: %v", err)
	}
}
