package audit

import (
	"database/sql"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/sqlitetest"
)

// TestSQLiteMigrationParity pins the single-source invariant for the audit
// schema: migrations/sqlite/0006_audit_sessions.sql (consumed by
// `ubag db-migrate`) and the runtime self-bootstrap (SQLiteStore.Ready, the
// path every gateway boot takes) must produce an identical gateway_audit_log
// table and indexes.
func TestSQLiteMigrationParity(t *testing.T) {
	sqlitetest.AssertMigrationParity(t, "0006_audit_sessions.sql", []string{"gateway_audit_log"},
		func(db *sql.DB) error { return NewSQLiteStore(db).Ready(t.Context()) })
}
