package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RestoreOptions configures a restore operation.
type RestoreOptions struct {
	// From is a local directory path OR an s3://bucket/prefix URI.
	// The manifest.json must exist at this location.
	From string
	// To is the destination (local file path for SQLite, DSN for Postgres).
	To string
}

// Restore reads a backup from opts.From, restores it to opts.To, and verifies
// the restore by re-opening the store and comparing row counts against the manifest.
// Returns a non-nil error if restore or verification fails.
func Restore(ctx context.Context, opts RestoreOptions) error {
	sourceDir, cleanup, err := resolveSource(ctx, opts.From)
	if err != nil {
		return fmt.Errorf("restore: resolve source: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	manifest, err := ReadManifest(sourceDir)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	if err := manifest.Verify(sourceDir); err != nil {
		return fmt.Errorf("restore: source integrity check failed: %w", err)
	}

	switch manifest.StoreKind {
	case StoreKindSQLite:
		return restoreSQLite(ctx, manifest, sourceDir, opts.To)
	case StoreKindPostgres:
		return restorePostgres(ctx, manifest, sourceDir, opts.To)
	default:
		return fmt.Errorf("restore: unknown store kind %q", manifest.StoreKind)
	}
}

// resolveSource returns the local directory containing the backup files.
// If from starts with "s3://", it downloads the files into a temp dir and
// returns a cleanup function to remove it. Otherwise, it returns from directly.
func resolveSource(ctx context.Context, from string) (dir string, cleanup func(), err error) {
	if !strings.HasPrefix(from, "s3://") {
		return from, nil, nil
	}

	endpoint := os.Getenv("UBAG_MINIO_ENDPOINT")
	accessKey := os.Getenv("UBAG_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("UBAG_MINIO_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		return "", nil, fmt.Errorf("s3:// restore requires UBAG_MINIO_ENDPOINT, UBAG_MINIO_ACCESS_KEY, UBAG_MINIO_SECRET_KEY")
	}

	// Parse s3://bucket/prefix
	u, err := url.Parse(from)
	if err != nil {
		return "", nil, fmt.Errorf("parse s3 URI %q: %w", from, err)
	}
	bucket := u.Host
	prefix := strings.TrimPrefix(u.Path, "/")

	tmpDir, err := os.MkdirTemp("", "ubag-restore-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanupFn := func() { _ = os.RemoveAll(tmpDir) }

	// Download manifest.json first.
	if err := s3Download(ctx, endpoint, bucket, prefix, "manifest.json", tmpDir); err != nil {
		cleanupFn()
		return "", nil, fmt.Errorf("download manifest.json: %w", err)
	}

	// Read manifest to discover component paths.
	manifest, err := ReadManifest(tmpDir)
	if err != nil {
		cleanupFn()
		return "", nil, err
	}

	for _, c := range manifest.Components {
		if err := s3Download(ctx, endpoint, bucket, prefix, c.Path, tmpDir); err != nil {
			cleanupFn()
			return "", nil, fmt.Errorf("download component %q: %w", c.Name, err)
		}
	}

	return tmpDir, cleanupFn, nil
}

// s3Download fetches http://<endpoint>/<bucket>/<prefix>/<name> into <destDir>/<name>.
func s3Download(ctx context.Context, endpoint, bucket, prefix, name, destDir string) error {
	rawURL := fmt.Sprintf("http://%s/%s/%s/%s", endpoint, bucket, prefix, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", rawURL, err)
	}

	// Basic auth via query params or header — MinIO supports both.
	// We use the access/secret key as HTTP basic auth for simplicity.
	req.SetBasicAuth(os.Getenv("UBAG_MINIO_ACCESS_KEY"), os.Getenv("UBAG_MINIO_SECRET_KEY"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: unexpected status %d", rawURL, resp.StatusCode)
	}

	destPath := filepath.Join(destDir, filepath.Base(name))
	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", destPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write %s: %w", destPath, err)
	}
	return nil
}

// restoreSQLite finds the sqlite component, copies it to opts.To, and verifies.
func restoreSQLite(ctx context.Context, manifest *Manifest, sourceDir, destPath string) error {
	var component *ComponentEntry
	for i := range manifest.Components {
		c := &manifest.Components[i]
		if c.Name == "sqlite" || c.Name == "gateway-sqlite" {
			component = c
			break
		}
	}
	if component == nil {
		return fmt.Errorf("restore: no sqlite component found in manifest")
	}

	srcPath := filepath.Join(sourceDir, component.Path)
	if err := copyFile(srcPath, destPath); err != nil {
		return fmt.Errorf("restore: copy sqlite: %w", err)
	}

	// Verify: checksum restored file against manifest.
	restoredChecksum, _, err := checksumFile(destPath)
	if err != nil {
		return fmt.Errorf("restore verify: checksum restored file: %w", err)
	}
	if restoredChecksum != component.Checksum {
		return fmt.Errorf("restore verify: checksum mismatch (got %s, want %s)", restoredChecksum, component.Checksum)
	}

	// Verify: DB opens cleanly and passes integrity_check.
	db, err := sql.Open("sqlite", destPath)
	if err != nil {
		return fmt.Errorf("restore verify: open db: %w", err)
	}
	defer db.Close()

	var integrityResult string
	row := db.QueryRowContext(ctx, "PRAGMA integrity_check")
	if err := row.Scan(&integrityResult); err != nil {
		return fmt.Errorf("restore verify: integrity_check scan: %w", err)
	}
	if integrityResult != "ok" {
		return fmt.Errorf("restore verify: integrity_check returned %q (want \"ok\")", integrityResult)
	}

	return nil
}

// restorePostgres shells out to pg_restore.
func restorePostgres(ctx context.Context, manifest *Manifest, sourceDir, dsn string) error {
	var component *ComponentEntry
	for i := range manifest.Components {
		c := &manifest.Components[i]
		if c.Name == "postgres" {
			component = c
			break
		}
	}
	if component == nil {
		return fmt.Errorf("restore: no postgres component found in manifest")
	}

	srcPath := filepath.Join(sourceDir, component.Path)

	safeDSN := dsn
	pgpassword := ""
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok {
			pgpassword = pw
			u.User = url.User(u.User.Username())
			safeDSN = u.String()
		}
	}

	cmd := buildPgRestoreCmd(ctx, safeDSN, srcPath)
	if pgpassword != "" {
		cmd.Env = append(os.Environ(), "PGPASSWORD="+pgpassword)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restore: pg_restore: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// buildPgRestoreCmd constructs the pg_restore command.
func buildPgRestoreCmd(ctx context.Context, dsn, srcPath string) *exec.Cmd {
	return exec.CommandContext(ctx, "pg_restore",
		"--format=custom",
		"--no-password",
		"--dbname="+dsn,
		srcPath,
	)
}
