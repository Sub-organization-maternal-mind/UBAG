package jobs

import (
	"os"
	"testing"
	"time"
)

// TestPostgresStoreWaitEventsContract runs the shared WaitEvents
// characterization suite against Postgres. Skipped unless
// UBAG_TEST_POSTGRES_DSN is set (pnpm test:gateway:postgres).
func TestPostgresStoreWaitEventsContract(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}
	db := openPostgresTestDB(t, dsn)
	defer db.Close()
	applyPostgresGatewayMigration(t, db)

	store := NewPostgresStore(db)
	tenantID := "tenant_pg_wait_events_" + time.Now().UTC().Format("20060102150405")
	defer cleanupPostgresJobs(t, db, tenantID)
	waitEventsContract(t, store, tenantID)
}
