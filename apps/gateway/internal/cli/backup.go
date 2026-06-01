package cli

import (
	"context"
	"database/sql"
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

// applyMigrations creates the schema_migrations table if needed, reads SQL
// files from migrationsDir, and applies each one that hasn't been applied yet.
// placeholder is "?" for SQLite and "$1" for Postgres.
func applyMigrations(ctx context.Context, db *sql.DB, migrationsDir string, placeholder string) (string, error) {
	if placeholder != "?" && placeholder != "$1" {
		return "", fmt.Errorf("migrate: unsupported placeholder %q (must be ? or $1)", placeholder)
	}

	// Ensure the tracking table exists.
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`)
	if err != nil {
		return "", fmt.Errorf("migrate: create schema_migrations: %w", err)
	}

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
		// version = filename without .sql extension
		version := strings.TrimSuffix(filename, ".sql")

		// Check if already applied.
		var existing string
		checkSQL := "SELECT version FROM schema_migrations WHERE version = " + placeholder
		row := db.QueryRowContext(ctx, checkSQL, version)
		scanErr := row.Scan(&existing)
		if scanErr != nil && scanErr != sql.ErrNoRows {
			return "", fmt.Errorf("migrate: check version %q: %w", version, scanErr)
		}

		if scanErr == nil {
			// Already applied.
			lines = append(lines, "skipped "+version+" (already applied)")
			continue
		}

		// Read and execute the migration file.
		sqlPath := filepath.Join(migrationsDir, filename)
		sqlBytes, err := os.ReadFile(sqlPath)
		if err != nil {
			return "", fmt.Errorf("migrate: read %q: %w", filename, err)
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

		// Record the migration. Use dialect-appropriate placeholders.
		var insertSQL string
		if placeholder == "$1" {
			insertSQL = "INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)"
		} else {
			insertSQL = "INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)"
		}
		appliedAt := time.Now().UTC().Format(time.RFC3339)
		if _, err := tx.ExecContext(ctx, insertSQL, version, version, appliedAt); err != nil {
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
