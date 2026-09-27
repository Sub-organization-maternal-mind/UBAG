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
	verifyFlag := fs.Bool("verify", false, "verify the checksums of already-applied migrations without applying anything; placeholder ledger rows are backfilled with real file checksums")
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
		if *verifyFlag {
			return runSQLiteMigrations(ctx, dsn, migrationsDir, true)
		}
		return runSQLiteMigrations(ctx, dsn, migrationsDir, false)
	case "postgres":
		if *verifyFlag {
			return runPostgresMigrations(ctx, dsn, migrationsDir, true)
		}
		return runPostgresMigrations(ctx, dsn, migrationsDir, false)
	default:
		return "", fmt.Errorf("migrate: unknown store %q (want sqlite or postgres)", *storeFlag)
	}
}

// runSQLiteMigrations opens a SQLite database and applies (or verifies) the
// SQL migration files in migrationsDir.
func runSQLiteMigrations(ctx context.Context, dbPath, migrationsDir string, verify bool) (string, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", fmt.Errorf("migrate: open sqlite %q: %w", dbPath, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if verify {
		return verifyMigrations(ctx, db, migrationsDir, "?")
	}
	return applyMigrations(ctx, db, migrationsDir, "?")
}

// runPostgresMigrations opens a Postgres database and applies (or verifies)
// the SQL migration files in migrationsDir.
func runPostgresMigrations(ctx context.Context, dsn, migrationsDir string, verify bool) (string, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return "", fmt.Errorf("migrate: open postgres: %w", err)
	}
	defer db.Close()

	if verify {
		return verifyMigrations(ctx, db, migrationsDir, "$1")
	}
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

// ensureLedgerTable creates the canonical gateway_schema_migrations ledger if
// it does not exist. It is normally created by migration 0001 itself, but the
// runner must work on an empty database.
//
// The column set and defaults MUST match the table the migration files create
// (migrations/postgres/0001_gateway_stores.sql and
// internal/sqlitestore/schema.sql). Every shipped migration records itself
// with a three-column INSERT that omits applied_at, relying on that default.
// If this table were created without the default, 0001's own CREATE TABLE IF
// NOT EXISTS would be a no-op and its ledger INSERT would fail on the NOT NULL
// constraint - i.e. migrating a fresh database would break on the very first
// file.
func ensureLedgerTable(ctx context.Context, db *sql.DB, placeholder string) error {
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
		return fmt.Errorf("migrate: create gateway_schema_migrations: %w", err)
	}
	return nil
}

// fileChecksum reads a migration file and returns its sha256 hex digest. The
// runner is the sole source of ledger checksums: a file cannot contain its own
// sha256 (circular), so the INSERTs inside the shipped migration files only
// ever carry placeholders and this computed value is what lands in the ledger.
func fileChecksum(path string) (string, error) {
	sqlBytes, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(sqlBytes)
	return hex.EncodeToString(sum[:]), nil
}

// applyMigrations applies every not-yet-applied .sql file in migrationsDir
// against the canonical gateway_schema_migrations ledger - the same table, the
// same short version key and the same checksum column that the migration files
// themselves write. The runner previously used its own "schema_migrations"
// table keyed by the full filename, so a database migrated by the container
// entrypoint (psql, the production path) looked untouched to `ubag db-migrate`
// and every migration was re-applied on top of it.
//
// The runner's ledger row is AUTHORITATIVE for the checksum. The migration
// files write their own placeholder rows first (a file cannot embed its own
// sha256 - circular - so those INSERTs carry "manual-v0*"/"" placeholders),
// and the runner's insert runs after them in the same transaction, overwriting
// the placeholder with the real sha256 of the file bytes. With a plain
// ON CONFLICT DO NOTHING the file's placeholder would win and drift detection
// could never fire.
//
// placeholder is "?" for SQLite and "$1" for Postgres.
func applyMigrations(ctx context.Context, db *sql.DB, migrationsDir string, placeholder string) (string, error) {
	if placeholder != "?" && placeholder != "$1" {
		return "", fmt.Errorf("migrate: unsupported placeholder %q (must be ? or $1)", placeholder)
	}

	if err := ensureLedgerTable(ctx, db, placeholder); err != nil {
		return "", err
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
		// Serialize against the container entrypoint (deploy/small/
		// gateway-entrypoint.sh takes the same transaction-scoped lock around
		// its whole psql run), so a concurrent gateway boot or db-migrate
		// cannot interleave DDL. Released when this transaction ends; SQLite
		// is single-writer and needs no lock.
		if placeholder == "$1" {
			if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('ubag-migrations'))"); err != nil {
				_ = tx.Rollback()
				return "", fmt.Errorf("migrate: advisory lock for %q: %w", filename, err)
			}
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return "", fmt.Errorf("migrate: apply %q: %w", filename, err)
		}

		// Record the migration. The migration files write their own ledger row
		// (with a placeholder checksum - see fileChecksum), so this upsert
		// overwrites it: the runner's real sha256 of the file bytes is
		// authoritative, otherwise drift detection can never fire. applied_at
		// is left as recorded by whichever row came first.
		var insertSQL string
		if placeholder == "$1" {
			insertSQL = "INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at) " +
				"VALUES ($1, $2, $3, $4) ON CONFLICT (version) DO UPDATE " +
				"SET checksum = EXCLUDED.checksum, name = EXCLUDED.name"
		} else {
			insertSQL = "INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at) " +
				"VALUES (?, ?, ?, ?) ON CONFLICT (version) DO UPDATE " +
				"SET checksum = EXCLUDED.checksum, name = EXCLUDED.name"
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

// verifyMigrations implements the "migrate --verify" mode: it applies nothing
// and instead reconciles the ledger's checksums against the files on disk.
//
//   - a real, matching checksum is reported as ok;
//   - a legacy placeholder ("", "manual-v0*", "sha256:placeholder*" - written
//     by the migration files' own INSERTs or by pre-unification ledgers) is
//     BACKFILLED with the real sha256 of the file, so drift detection becomes
//     active for that migration on every later run;
//   - a version recorded only by the pre-unification "schema_migrations" table
//     is adopted into the canonical ledger with the real checksum;
//   - a real checksum that does not match the file is drift: the command fails
//     closed listing every drifted file;
//   - a file with no ledger row is reported as missing but is NOT an error -
//     the container entrypoint legitimately skips optional migrations
//     (UBAG_ALLOW_OPTIONAL_MIGRATIONS).
func verifyMigrations(ctx context.Context, db *sql.DB, migrationsDir string, placeholder string) (string, error) {
	if placeholder != "?" && placeholder != "$1" {
		return "", fmt.Errorf("migrate: unsupported placeholder %q (must be ? or $1)", placeholder)
	}

	if err := ensureLedgerTable(ctx, db, placeholder); err != nil {
		return "", err
	}
	legacy := legacyAppliedVersions(ctx, db)

	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("migrate: verify: no migrations directory at %s", migrationsDir), nil
		}
		return "", fmt.Errorf("migrate: verify: read migrations dir %q: %w", migrationsDir, err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return "migrate: verify: no migration files found", nil
	}

	var lines []string
	var drift []string
	ok, backfilled, missing := 0, 0, 0
	// ph returns the parameter placeholder for the i-th (0-based) argument,
	// matching the dialect's convention ("?" for SQLite, "$n" for Postgres).
	ph := func(i int) string {
		if placeholder == "$1" {
			return fmt.Sprintf("$%d", i+1)
		}
		return "?"
	}
	for _, filename := range files {
		version := migrationVersion(filename)
		longKey := strings.TrimSuffix(filename, ".sql")

		checksum, err := fileChecksum(filepath.Join(migrationsDir, filename))
		if err != nil {
			return "", fmt.Errorf("migrate: verify: read %q: %w", filename, err)
		}

		var recordedSum string
		scanErr := db.QueryRowContext(ctx,
			"SELECT checksum FROM gateway_schema_migrations WHERE version = "+placeholder, version,
		).Scan(&recordedSum)
		if scanErr != nil && scanErr != sql.ErrNoRows {
			return "", fmt.Errorf("migrate: verify: check version %q: %w", version, scanErr)
		}

		name := strings.TrimPrefix(longKey, version+"_")
		if name == "" {
			name = longKey
		}

		switch {
		case scanErr == sql.ErrNoRows && (legacy[version] || legacy[longKey]):
			// Applied by the pre-unification runner only. Adopt it into the
			// canonical ledger with the real checksum.
			insertSQL := "INSERT INTO gateway_schema_migrations (version, name, checksum) " +
				"VALUES (" + ph(0) + ", " + ph(1) + ", " + ph(2) + ") " +
				"ON CONFLICT (version) DO NOTHING"
			if _, err := db.ExecContext(ctx, insertSQL, version, name, checksum); err != nil {
				return "", fmt.Errorf("migrate: verify: adopt %q: %w", filename, err)
			}
			lines = append(lines, "adopted "+version+" (legacy runner row; recorded with real checksum)")
			backfilled++
		case scanErr == sql.ErrNoRows:
			lines = append(lines, "missing "+version+" (not applied)")
			missing++
		case isLegacyPlaceholderChecksum(recordedSum):
			updateSQL := "UPDATE gateway_schema_migrations SET checksum = " + ph(0) +
				" WHERE version = " + ph(1)
			if _, err := db.ExecContext(ctx, updateSQL, checksum, version); err != nil {
				return "", fmt.Errorf("migrate: verify: backfill %q: %w", filename, err)
			}
			lines = append(lines, "backfilled "+version+" (placeholder replaced with real checksum)")
			backfilled++
		case recordedSum == checksum:
			lines = append(lines, "ok "+version)
			ok++
		default:
			drift = append(drift, fmt.Sprintf(
				"  %s was applied with checksum %s but the file on disk hashes to %s - "+
					"an already-applied migration was edited; add a new migration file instead",
				filename, recordedSum, checksum))
		}
	}

	if len(drift) > 0 {
		return "", fmt.Errorf("migrate: verify: checksum drift detected:\n%s", strings.Join(drift, "\n"))
	}
	return fmt.Sprintf("migrate: verify: %d files checked (ok=%d backfilled=%d missing=%d)\n%s",
		len(files), ok, backfilled, missing, strings.Join(lines, "\n")), nil
}

// backupUsage returns the usage string for backup/restore/migrate commands.
func backupUsage() string {
	return strings.TrimSpace(`
Usage: ubag backup  --out <dir|s3://...>
       ubag restore --from <dir|s3://...>
       ubag migrate [--store sqlite|postgres] [--verify]
`) + "\n"
}
