package cli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/backup"
	_ "modernc.org/sqlite"
)

// DispatchBackup handles the 'ubag backup', 'ubag restore', and 'ubag migrate'
// top-level commands. These are local operations; no gateway Client is used.
// It is exported so that tests can call it directly.
func DispatchBackup(ctx context.Context, args []string) (string, error) {
	// For long-running operations (backup, restore, migrate), wrap with
	// a signal-aware context so pg_dump / S3 transfers can be cancelled.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(args) == 0 {
		return backupUsage(), nil
	}
	switch args[0] {
	case "backup":
		return cmdBackup(ctx, args[1:])
	case "restore":
		return cmdRestore(ctx, args[1:])
	case "migrate":
		return cmdMigrate(ctx, args[1:])
	default:
		return fmt.Sprintf("unknown backup command %q\n\n%s", args[0], backupUsage()), nil
	}
}

// cmdBackup implements: ubag backup [--out <dir>] [--profile <profile>]
func cmdBackup(ctx context.Context, args []string) (string, error) {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // prevent duplicate usage write to stderr on --help
	defaultOut := "ubag-backup-" + time.Now().UTC().Format("20060102-150405")
	outDir := fs.String("out", defaultOut, "Output directory (local path or s3://bucket/prefix)")
	defaultProfile := os.Getenv("UBAG_PROFILE")
	if defaultProfile == "" {
		defaultProfile = "small"
	}
	profile := fs.String("profile", defaultProfile, "Backup profile name")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return backupUsage(), nil
		}
		return "", err
	}

	sqlitePath := os.Getenv("UBAG_GATEWAY_STORE")
	if sqlitePath == "" {
		sqlitePath = "ubag-gateway.db"
	}
	postgresDSN := os.Getenv("UBAG_POSTGRES_DSN")

	// Ensure output directory exists for local paths.
	if !strings.HasPrefix(*outDir, "s3://") {
		if err := os.MkdirAll(*outDir, 0o700); err != nil {
			return "", fmt.Errorf("backup: create output dir: %w", err)
		}
	}

	m, err := backup.Run(ctx, backup.Options{
		Profile:     *profile,
		SQLitePath:  sqlitePath,
		PostgresDSN: postgresDSN,
		OutDir:      *outDir,
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("backup written to %s (%d components)", *outDir, len(m.Components)), nil
}

// cmdRestore implements: ubag restore --from <dir|s3://bucket/prefix>
func cmdRestore(ctx context.Context, args []string) (string, error) {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // prevent duplicate usage write to stderr on --help
	from := fs.String("from", "", "Source backup directory (local path or s3://bucket/prefix)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return backupUsage(), nil
		}
		return "", err
	}

	if *from == "" {
		return "", fmt.Errorf("restore: --from is required")
	}

	to := os.Getenv("UBAG_GATEWAY_STORE")
	if to == "" {
		to = "ubag-gateway.db"
	}

	if err := backup.Restore(ctx, backup.RestoreOptions{
		From: *from,
		To:   to,
	}); err != nil {
		return "", err
	}

	return "restore complete", nil
}

// cmdMigrate implements: ubag migrate [--store sqlite|postgres] [--dsn <dsn>]
func cmdMigrate(ctx context.Context, args []string) (string, error) {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // prevent duplicate usage write to stderr on --help

	// Determine default store kind from env.
	defaultStore := "sqlite"
	gatewayStore := os.Getenv("UBAG_GATEWAY_STORE")
	if strings.HasPrefix(gatewayStore, "postgres://") {
		defaultStore = "postgres"
	}

	storeFlag := fs.String("store", defaultStore, "Store type: sqlite or postgres")
	dsnFlag := fs.String("dsn", "", "Connection string (defaults to $UBAG_POSTGRES_DSN or $UBAG_GATEWAY_STORE)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return backupUsage(), nil
		}
		return "", err
	}

	// Resolve DSN.
	dsn := *dsnFlag
	if dsn == "" {
		switch *storeFlag {
		case "postgres":
			dsn = os.Getenv("UBAG_POSTGRES_DSN")
			if dsn == "" {
				return "", fmt.Errorf("migrate: --dsn is required for postgres store (or set $UBAG_POSTGRES_DSN)")
			}
		default: // sqlite
			dsn = gatewayStore
			if dsn == "" {
				dsn = "ubag-gateway.db"
			}
		}
	}

	// Resolve migrations directory.
	migrationsDir := os.Getenv("UBAG_MIGRATIONS_DIR")
	if migrationsDir == "" {
		// Fall back to ./migrations/<dialect>/ relative to CWD.
		migrationsDir = filepath.Join("migrations", *storeFlag)
	} else {
		migrationsDir = filepath.Join(migrationsDir, *storeFlag)
	}

	switch *storeFlag {
	case "sqlite":
		return runSQLiteMigrations(ctx, dsn, migrationsDir)
	case "postgres":
		return runPostgresMigrations(ctx, dsn, migrationsDir)
	default:
		return "", fmt.Errorf("migrate: unknown store %q (want sqlite or postgres)", *storeFlag)
	}
}

// runSQLiteMigrations applies pending SQL migration files to a SQLite database.
func runSQLiteMigrations(ctx context.Context, dbPath, migrationsDir string) (string, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", fmt.Errorf("migrate: open sqlite %q: %w", dbPath, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	return applyMigrations(ctx, db, migrationsDir, "?")
}

// runPostgresMigrations applies pending SQL migration files to a Postgres database.
func runPostgresMigrations(ctx context.Context, dsn, migrationsDir string) (string, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return "", fmt.Errorf("migrate: open postgres: %w", err)
	}
	defer db.Close()

	return applyMigrations(ctx, db, migrationsDir, "$1")
}

// isLegacyPlaceholderChecksum reports whether a recorded checksum predates real
// verification, in which case it carries no information about the file on disk
// and a mismatch must be tolerated rather than treated as drift.
//
// Three conventions are in the wild, all of which are uninformative:
//   - ""            - migrations/postgres/0009..0012 write an empty checksum
//   - "manual-v0*"  - 0001..0007 per-dialect hand-written placeholders
//   - "sha256:placeholder*" - 0008
//
// Anything else is a real sha256 and IS enforced. Matching on the prefix rather
// than enumerating keeps this correct as further migrations are added using the
// same conventions, instead of hard-failing an existing deployment on its next
// migrate run.
func isLegacyPlaceholderChecksum(sum string) bool {
	if sum == "" {
		return true
	}
	return strings.HasPrefix(sum, "manual-v0") ||
		strings.HasPrefix(sum, "sha256:placeholder")
}

// migrationVersion derives the ledger key from a migration filename. The SQL
// files themselves record the SHORT form ("0001"), so that is the canonical
// key; the long filename form is also probed because earlier versions of this
// runner recorded that instead.
func migrationVersion(filename string) string {
	base := strings.TrimSuffix(filename, ".sql")
	if i := strings.Index(base, "_"); i > 0 {
		if prefix := base[:i]; strings.Trim(prefix, "0123456789") == "" {
			return prefix
		}
	}
	return base
}

// legacyAppliedVersions returns the versions recorded by the pre-unification
// runner, which used a separate "schema_migrations" table keyed by the full
// filename. Any error is swallowed: the table usually does not exist, and it
// is only ever an extra source of "already applied" truth.
func legacyAppliedVersions(ctx context.Context, db *sql.DB) map[string]bool {
	applied := map[string]bool{}
	rows, err := db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return applied
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			continue
		}
		applied[v] = true
	}
	return applied
}

// applyMigrations applies every not-yet-applied .sql file in migrationsDir
// against the canonical gateway_schema_migrations ledger - the same table, the
// same short version key and the same checksum column that the migration files
// themselves write. The runner previously used its own "schema_migrations"
// table keyed by the full filename, so a database migrated by the container
// entrypoint (psql, the production path) looked untouched to `ubag db-migrate`
// and every migration was re-applied on top of it.
//
// placeholder is "?" for SQLite and "$1" for Postgres.
func applyMigrations(ctx context.Context, db *sql.DB, migrationsDir string, placeholder string) (string, error) {
	if placeholder != "?" && placeholder != "$1" {
		return "", fmt.Errorf("migrate: unsupported placeholder %q (must be ? or $1)", placeholder)
	}

	// Ensure the canonical tracking table exists. It is normally created by
	// migration 0001 itself, but the runner must work on an empty database.
	//
	// The column set and defaults MUST match the table the migration files
	// create (migrations/postgres/0001_gateway_stores.sql and
	// internal/sqlitestore/schema.sql). Every shipped migration records itself
	// with a three-column INSERT that omits applied_at, relying on that
	// default. If this table were created without the default, 0001's own
	// CREATE TABLE IF NOT EXISTS would be a no-op and its ledger INSERT would
	// fail on the NOT NULL constraint - i.e. migrating a fresh database would
	// break on the very first file.
	appliedAtDefault := "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"
	if placeholder == "$1" {
		appliedAtDefault = "now()"
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS gateway_schema_migrations (
		version    TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		checksum   TEXT NOT NULL DEFAULT '',
		applied_at TEXT NOT NULL DEFAULT (`+appliedAtDefault+`)
	)`); err != nil {
		return "", fmt.Errorf("migrate: create gateway_schema_migrations: %w", err)
	}

	legacy := legacyAppliedVersions(ctx, db)

	// Read migration files.
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("migrate: no migrations directory at %s", migrationsDir), nil
		}
		return "", fmt.Errorf("migrate: read migrations dir %q: %w", migrationsDir, err)
	}

	// Collect and sort .sql files.
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	if len(files) == 0 {
		return "migrate: no migration files found", nil
	}

	var lines []string
	for _, filename := range files {
		version := migrationVersion(filename)
		longKey := strings.TrimSuffix(filename, ".sql")

		// Read the file up front: its bytes are both the checksum input and
		// the statement to execute.
		sqlPath := filepath.Join(migrationsDir, filename)
		sqlBytes, err := os.ReadFile(sqlPath)
		if err != nil {
			return "", fmt.Errorf("migrate: read %q: %w", filename, err)
		}
		sum := sha256.Sum256(sqlBytes)
		checksum := hex.EncodeToString(sum[:])

		// Check if already applied, and whether the file changed since.
		var recordedName, recordedSum string
		checkSQL := "SELECT name, checksum FROM gateway_schema_migrations WHERE version = " + placeholder
		row := db.QueryRowContext(ctx, checkSQL, version)
		scanErr := row.Scan(&recordedName, &recordedSum)
		if scanErr != nil && scanErr != sql.ErrNoRows {
			return "", fmt.Errorf("migrate: check version %q: %w", version, scanErr)
		}

		applied := scanErr == nil
		if !applied && (legacy[version] || legacy[longKey]) {
			// Recorded by the old runner under the filename key. Adopt it
			// rather than re-running the statements.
			applied = true
		}

		if applied {
			// Drift check: refuse to skip silently when the file on disk no
			// longer matches what was applied. Legacy placeholder checksums
			// carry no information and are always tolerated.
			if recordedSum != "" && recordedSum != checksum && !isLegacyPlaceholderChecksum(recordedSum) {
				return "", fmt.Errorf(
					"migrate: %s was already applied with checksum %s but the file on disk hashes to %s; "+
						"an already-applied migration was edited - add a new migration file instead",
					filename, recordedSum, checksum)
			}
			lines = append(lines, "skipped "+version+" (already applied)")
			continue
		}

		// Apply the migration inside a transaction for atomicity.
		tx, txErr := db.BeginTx(ctx, nil)
		if txErr != nil {
			return "", fmt.Errorf("migrate: begin tx for %q: %w", filename, txErr)
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return "", fmt.Errorf("migrate: apply %q: %w", filename, err)
		}

		// Record the migration. The migration files write their own ledger row,
		// so this must not conflict with it.
		var insertSQL string
		if placeholder == "$1" {
			insertSQL = "INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at) " +
				"VALUES ($1, $2, $3, $4) ON CONFLICT (version) DO NOTHING"
		} else {
			insertSQL = "INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at) " +
				"VALUES (?, ?, ?, ?) ON CONFLICT (version) DO NOTHING"
		}
		appliedAt := time.Now().UTC().Format(time.RFC3339)
		name := strings.TrimPrefix(longKey, version+"_")
		if name == "" {
			name = longKey
		}
		if _, err := tx.ExecContext(ctx, insertSQL, version, name, checksum, appliedAt); err != nil {
			_ = tx.Rollback()
			return "", fmt.Errorf("migrate: record %q: %w", filename, err)
		}
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("migrate: commit %q: %w", filename, err)
		}

		lines = append(lines, "applied "+version)
	}

	return strings.Join(lines, "\n"), nil
}

// backupUsage returns the usage string for backup/restore/migrate commands.
func backupUsage() string {
	return strings.TrimSpace(`
Usage: ubag backup  --out <dir|s3://...>
       ubag restore --from <dir|s3://...>
       ubag migrate [--store sqlite|postgres]
`) + "\n"
}
