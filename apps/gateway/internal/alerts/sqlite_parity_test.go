package alerts

import (
	"database/sql"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/sqlitetest"
)

// TestSQLiteMigrationParity pins the single-source invariant for the alert
// schema: the offline migration file (migrations/sqlite/0005_alerts.sql,
// consumed by `ubag db-migrate`) and the runtime self-bootstrap
// (SQLiteStore.Ready, the path every gateway boot takes) must produce
// identical tables and indexes. A schema change in one place without the
// other fails here instead of drifting silently.
func TestSQLiteMigrationParity(t *testing.T) {
	sqlitetest.AssertMigrationParity(t, "0005_alerts.sql", []string{"gateway_alerts"},
		func(db *sql.DB) error { return NewSQLiteStore(db).Ready(t.Context()) })
}
