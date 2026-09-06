// Package storekit is the shared persistence wiring kit (ADR-0004 discipline
// at the store seam). Store INTERFACES stay bespoke per package — they
// genuinely differ — but the survival kit every store trio re-implements
// (schema assertions, the memory/sqlite/postgres pick, and the wiring-test
// recipe) lives here once.
package storekit

import (
	"context"
	"database/sql"
	"fmt"
)

// RequirePostgresObject asserts a Postgres table exists (to_regclass).
// Replaces the 15 byte-identical per-package copies.
func RequirePostgresObject(ctx context.Context, db *sql.DB, objectName string) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, objectName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s is missing", objectName)
	}
	return nil
}

// RequireSQLiteObject asserts a SQLite table exists (sqlite_master).
// Replaces the per-package assert-only copies.
func RequireSQLiteObject(ctx context.Context, db *sql.DB, objectName string) error {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, objectName).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("%s is missing", objectName)
	}
	return nil
}

// Kind is the configured gateway store kind (UBAG_GATEWAY_STORE).
type Kind string

const (
	KindMemory   Kind = "memory"
	KindSQLite   Kind = "sqlite"
	KindPostgres Kind = "postgres"
)

// Pick selects the right store adapter for the configured kind when a DB
// handle is present, falling back to the memory implementation otherwise.
// The 13 copy-paste three-way switch blocks in serve.go each become one call:
//
//	store, err := storekit.Pick(kind, db, "rate limit",
//	     func(db *sql.DB) (Store, error) { return ratelimit.NewSQLiteStore(ctx, db) },
//	     func(db *sql.DB) (Store, error) { return ratelimit.NewPostgresStore(ctx, db) },
//	     ratelimit.NewMemoryStore,
//	 )
//
// A nil db always yields the memory adapter, whatever the kind says — the
// historical behavior of every switch block.
func Pick[T any](kind Kind, db *sql.DB, what string, sqlite func(*sql.DB) (T, error), postgres func(*sql.DB) (T, error), memory func() T) (T, error) {
	switch {
	case kind == KindSQLite && db != nil:
		return sqlite(db)
	case kind == KindPostgres && db != nil:
		return postgres(db)
	default:
		return memory(), nil
	}
}
