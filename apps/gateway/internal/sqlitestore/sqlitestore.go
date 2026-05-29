// Package sqlitestore provides the embedded schema and shared helpers for the
// gateway's SQLite-backed runtime stores. The schema mirrors the Postgres
// gateway migrations so the SQLite job, idempotency, webhook outbox and
// artifact metadata stores can auto-create their tables on startup.
package sqlitestore

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed schema.sql
var schemaSQL string

// Schema returns the embedded SQLite DDL used by the gateway stores.
func Schema() string {
	return schemaSQL
}

// Apply runs the embedded schema against db. It is safe to call repeatedly
// because every statement uses IF NOT EXISTS / INSERT OR IGNORE semantics.
func Apply(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("sqlitestore: db is nil")
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("sqlitestore: apply schema: %w", err)
	}
	return nil
}
