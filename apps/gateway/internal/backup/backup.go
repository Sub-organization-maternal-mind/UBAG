package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Options configures a backup operation.
type Options struct {
	// Profile is recorded in the manifest (e.g. "small", "enterprise").
	Profile string
	// SQLitePath is the source SQLite DB file path (for SQLite stores).
	// Mutually exclusive with PostgresDSN.
	SQLitePath string
	// PostgresDSN is the connection string for pg_dump (for Postgres stores).
	// Only used when set; skipped otherwise.
	PostgresDSN string
	// OutDir is the local directory to write backup files into.
	// The caller is responsible for creating or choosing this directory.
	OutDir string
}

// Run executes the backup and writes all files + manifest.json to opts.OutDir.
// Returns the written Manifest on success.
func Run(ctx context.Context, opts Options) (*Manifest, error) {
	m := &Manifest{
		CreatedAt: time.Now().UTC(),
		Profile:   opts.Profile,
	}

	if opts.SQLitePath != "" {
		entry, err := backupSQLite(ctx, opts.SQLitePath, opts.OutDir)
		if err != nil {
			return nil, fmt.Errorf("backup: sqlite: %w", err)
		}
		m.StoreKind = StoreKindSQLite
		m.Components = append(m.Components, *entry)
	}

	if opts.PostgresDSN != "" {
		entry, err := backupPostgres(ctx, opts.PostgresDSN, opts.OutDir)
		if err != nil {
			return nil, fmt.Errorf("backup: postgres: %w", err)
		}
		if m.StoreKind == "" {
			m.StoreKind = StoreKindPostgres
		}
		m.Components = append(m.Components, *entry)
	}

	if err := m.Write(opts.OutDir); err != nil {
		return nil, err
	}
	return m, nil
}

// backupSQLite checkpoints the WAL, copies the DB file, and returns a
// ComponentEntry for the copy.
func backupSQLite(ctx context.Context, srcPath, outDir string) (*ComponentEntry, error) {
	// Open the SQLite DB to run WAL checkpoint.
	db, err := sql.Open("sqlite", srcPath)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	// Flush WAL to main DB file before copying.
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("wal_checkpoint: %w", err)
	}
	if err := db.Close(); err != nil {
		return nil, fmt.Errorf("close: %w", err)
	}

	destName := "gateway.db"
	destPath := filepath.Join(outDir, destName)
	if err := copyFile(srcPath, destPath); err != nil {
		return nil, fmt.Errorf("copy: %w", err)
	}

	checksum, size, err := checksumFile(destPath)
	if err != nil {
		return nil, fmt.Errorf("checksum: %w", err)
	}

	return &ComponentEntry{
		Name:      "sqlite",
		Path:      destName,
		Checksum:  checksum,
		SizeBytes: size,
	}, nil
}

// backupPostgres runs pg_dump and returns a ComponentEntry for the dump file.
func backupPostgres(ctx context.Context, dsn, outDir string) (*ComponentEntry, error) {
	destName := "gateway.pgdump"
	destPath := filepath.Join(outDir, destName)

	cmd := exec.CommandContext(ctx, "pg_dump",
		"--format=custom",
		"--no-password",
		"--dbname="+dsn,
		"--file="+destPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("pg_dump: %w: %s", err, strings.TrimSpace(string(out)))
	}

	checksum, size, err := checksumFile(destPath)
	if err != nil {
		return nil, fmt.Errorf("checksum: %w", err)
	}

	return &ComponentEntry{
		Name:      "postgres",
		Path:      destName,
		Checksum:  checksum,
		SizeBytes: size,
	}, nil
}

// copyFile copies src to dst, creating dst if it does not exist.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// checksumFile returns the hex SHA-256 and size of the file at path.
func checksumFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// sha256hex returns the hex SHA-256 of data (used by manifest.Verify).
func sha256hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// joinLines joins strings with newline + indent.
func joinLines(ss []string) string {
	return strings.Join(ss, "\n  ")
}
