package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Postgres runs of the attempt ledger. Like every other Postgres test in this
// package they run only when UBAG_TEST_POSTGRES_DSN is set (pnpm
// test:gateway:postgres); the memory run of the same contract is
// TestMemoryAttemptStoreContract.

func applyPostgresAttemptsMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "migrations", "postgres", "0022_gateway_job_attempts.sql")
	sqlBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), string(sqlBytes)); err != nil {
		t.Fatalf("apply migration 0022: %v", err)
	}
}

func postgresAttemptDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}
	db := openPostgresTestDB(t, dsn)
	t.Cleanup(func() { _ = db.Close() })
	applyPostgresGatewayMigration(t, db)
	applyPostgresAttemptsMigration(t, db)
	return db
}

func TestPostgresAttemptStoreContract(t *testing.T) {
	db := postgresAttemptDB(t)
	runAttemptStoreContract(t, func(t *testing.T) attemptEnv {
		clock := newAttemptClock()
		store := NewPostgresStore(db)
		store.now = clock.Now
		tenant := fmt.Sprintf("tenant_pg_attempts_%d", time.Now().UnixNano())
		t.Cleanup(func() { cleanupPostgresJobs(t, db, tenant) }) // attempts cascade with the job
		var mu sync.Mutex
		n := 0
		return attemptEnv{store: store, clock: clock, newJob: func(t *testing.T) Job {
			mu.Lock()
			n++
			id := n
			mu.Unlock()
			return createAttemptTestJob(t, store, tenant, fmt.Sprintf("trace_pg_attempt_%d", id))
		}}
	})
}

// TestPostgresAttemptSchemaBackstops pins the database-level guarantees that
// back the BeginAttempt transaction: one live lease per job, one generation per
// job, cascade delete with the job, and Ready requiring the table once the
// ledger is enabled.
func TestPostgresAttemptSchemaBackstops(t *testing.T) {
	db := postgresAttemptDB(t)
	ctx := context.Background()
	store := NewPostgresStore(db)
	tenant := fmt.Sprintf("tenant_pg_attempt_schema_%d", time.Now().UnixNano())
	job := createAttemptTestJob(t, store, tenant, "trace_pg_attempt_schema")
	t.Cleanup(func() { cleanupPostgresJobs(t, db, tenant) })

	store.EnableAttempts()
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("Ready with the attempts table applied: %v", err)
	}

	insert := func(attemptID string, generation int, state string) error {
		_, err := db.ExecContext(ctx, `
INSERT INTO gateway_job_attempts (job_id, attempt_id, generation, state, lease_expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, now(), now(), now())`, job.ID, attemptID, generation, state)
		return err
	}
	if err := insert("att_a", 1, "active"); err != nil {
		t.Fatal(err)
	}
	if err := insert("att_b", 2, "active"); err == nil {
		t.Fatal("a second active attempt for one job was accepted")
	}
	if err := insert("att_c", 1, "expired"); err == nil {
		t.Fatal("a duplicate (job, generation) was accepted")
	}
	if err := insert("att_d", 0, "expired"); err == nil {
		t.Fatal("generation 0 was accepted")
	}
	if err := insert("att_e", 2, "bogus"); err == nil {
		t.Fatal("an unknown state was accepted")
	}
	if err := insert("att_f", 2, "expired"); err != nil {
		t.Fatalf("a finished predecessor must not block history: %v", err)
	}
}

// TestPostgresAttemptsMigrationShape is not DSN-gated: it keeps the migration
// honest (additive, idempotent, cascading, fenced) without a database.
func TestPostgresAttemptsMigrationShape(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", "postgres", "0022_gateway_job_attempts.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sqlText := string(body)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS gateway_job_attempts",
		"REFERENCES gateway_jobs(id) ON DELETE CASCADE",
		"PRIMARY KEY (job_id, attempt_id)",
		"UNIQUE (job_id, generation)",
		"CHECK (generation >= 1)",
		"CHECK (state IN ('active', 'finished', 'expired'))",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_job_attempts_active",
		"WHERE state = 'active'",
		"VALUES ('0022', 'gateway_job_attempts'",
		"ON CONFLICT (version) DO NOTHING",
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("migration 0022 is missing %q", want)
		}
	}
	for _, forbidden := range []string{"DROP ", "ALTER TABLE gateway_jobs", "TRUNCATE", "DELETE FROM"} {
		if strings.Contains(sqlText, forbidden) {
			t.Errorf("migration 0022 must be additive; found %q", forbidden)
		}
	}
}
