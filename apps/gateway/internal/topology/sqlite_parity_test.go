package topology

import (
	"database/sql"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/sqlitetest"
)

// TestSQLiteMigrationParity pins the single-source invariant for the browser
// topology schema: migrations/sqlite/0004_browser_topology.sql (consumed by
// `ubag db-migrate`) and the runtime self-bootstrap (SQLiteStore.Ready, the
// path every gateway boot takes) must produce identical tables and indexes.
func TestSQLiteMigrationParity(t *testing.T) {
	sqlitetest.AssertMigrationParity(t, "0004_browser_topology.sql", []string{
		"gateway_browser_instances",
		"gateway_provider_contexts",
		"gateway_browser_tabs",
		"gateway_browser_sessions",
	}, func(db *sql.DB) error { return NewSQLiteStore(db).Ready(t.Context()) })
}
