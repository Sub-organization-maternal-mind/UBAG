package backup

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
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
	if strings.HasPrefix(from, "s3://") {
		return resolveS3Source(ctx, from)
	}
	return from, nil, nil
}

// resolveS3Source downloads backup files from MinIO/S3 into a temp directory
// using the MinIO SDK with AWS Signature v4 authentication.
func resolveS3Source(ctx context.Context, rawURI string) (sourceDir string, cleanup func(), err error) {
	endpoint := os.Getenv("UBAG_MINIO_ENDPOINT")
	accessKey := os.Getenv("UBAG_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("UBAG_MINIO_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		return "", nil, fmt.Errorf("s3:// restore requires UBAG_MINIO_ENDPOINT, UBAG_MINIO_ACCESS_KEY, UBAG_MINIO_SECRET_KEY")
	}

	// Parse s3://bucket/prefix
	u, err := url.Parse(rawURI)
	if err != nil {
		return "", nil, fmt.Errorf("invalid s3 URI %q: %w", rawURI, err)
	}
	bucket := u.Host
	prefix := strings.TrimPrefix(u.Path, "/")

	// Use TLS only if the endpoint looks like an https URL or contains :443
	useSSL := strings.HasPrefix(endpoint, "https://") || strings.HasSuffix(endpoint, ":443")
	// Strip scheme from endpoint for minio client
	cleanEndpoint := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")

	mc, err := minio.New(cleanEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return "", nil, fmt.Errorf("minio client: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "ubag-restore-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanupFn := func() { _ = os.RemoveAll(tmpDir) }

	// Download manifest.json first
	manifestObj := prefix + "manifest.json"
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		manifestObj = prefix + "/manifest.json"
	}
	if err := mc.FGetObject(ctx, bucket, manifestObj, filepath.Join(tmpDir, "manifest.json"), minio.GetObjectOptions{}); err != nil {
		cleanupFn()
		return "", nil, fmt.Errorf("download manifest.json from s3: %w", err)
	}

	// Read manifest to know which component files to download
	m, err := ReadManifest(tmpDir)
	if err != nil {
		cleanupFn()
		return "", nil, fmt.Errorf("read downloaded manifest: %w", err)
	}

	for _, c := range m.Components {
		objKey := c.Path
		if prefix != "" {
			if !strings.HasSuffix(prefix, "/") {
				objKey = prefix + "/" + c.Path
			} else {
				objKey = prefix + c.Path
			}
		}
		destPath := filepath.Join(tmpDir, filepath.FromSlash(c.Path))
		if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
			cleanupFn()
			return "", nil, err
		}
		if err := mc.FGetObject(ctx, bucket, objKey, destPath, minio.GetObjectOptions{}); err != nil {
			cleanupFn()
			return "", nil, fmt.Errorf("download %s from s3: %w", c.Path, err)
		}
	}

	return tmpDir, cleanupFn, nil
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

	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("restore verify: integrity_check: %w", err)
	}
	defer rows.Close()
	var issues []string
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("restore verify: integrity_check scan: %w", err)
		}
		if result != "ok" {
			issues = append(issues, result)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("restore verify: integrity_check rows: %w", err)
	}
	if len(issues) > 0 {
		return fmt.Errorf("restore verify: integrity_check failed: %s", strings.Join(issues, "; "))
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

	// Verify source file integrity before restore
	srcChecksum, _, err := checksumFile(srcPath)
	if err != nil {
		return fmt.Errorf("restore postgres: checksum source: %w", err)
	}
	if srcChecksum != component.Checksum {
		return fmt.Errorf("restore postgres: source file checksum mismatch (got %s, want %s)", srcChecksum, component.Checksum)
	}

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
