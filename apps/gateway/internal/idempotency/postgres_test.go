package idempotency

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresStoreContract(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}

	db := openPostgresTestDB(t, dsn)
	defer db.Close()
	applyPostgresGatewayMigration(t, db)

	store := NewPostgresStore(db, time.Hour)
	scope := Scope{
		TenantID:  "tenant_pg_idem_" + time.Now().UTC().Format("20060102150405"),
		AppID:     "app_pg_idem",
		Operation: "jobs.create",
		Key:       "idem_pg_idempotency_contract",
	}
	defer cleanupPostgresIdempotency(t, db, scope.TenantID)

	first, err := store.Reserve(context.Background(), scope, "hash-one")
	if err != nil {
		t.Fatalf("Reserve first returned error: %v", err)
	}
	if first.Kind != DecisionReserved {
		t.Fatalf("first decision = %s, want %s", first.Kind, DecisionReserved)
	}

	if err := store.Complete(context.Background(), scope, "job_pg_contract", 202); err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	replay, err := store.Reserve(context.Background(), scope, "hash-one")
	if err != nil {
		t.Fatalf("Reserve replay returned error: %v", err)
	}
	if replay.Kind != DecisionReplay || replay.Record.ResourceID != "job_pg_contract" || replay.Record.HTTPStatus != 202 {
		t.Fatalf("replay decision mismatch: %#v", replay)
	}

	conflict, err := store.Reserve(context.Background(), scope, "hash-two")
	if err != nil {
		t.Fatalf("Reserve conflict returned error: %v", err)
	}
	if conflict.Kind != DecisionConflict {
		t.Fatalf("conflict decision = %s, want %s", conflict.Kind, DecisionConflict)
	}

	if err := store.Release(context.Background(), scope); err != nil {
		t.Fatalf("Release returned error: %v", err)
	}
	second, err := store.Reserve(context.Background(), scope, "hash-two")
	if err != nil {
		t.Fatalf("Reserve after release returned error: %v", err)
	}
	if second.Kind != DecisionReserved {
		t.Fatalf("second decision = %s, want %s", second.Kind, DecisionReserved)
	}
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("Ready returned error: %v", err)
	}
}

func TestPostgresStoreScopeIsolationAndExpiry(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}

	db := openPostgresTestDB(t, dsn)
	defer db.Close()
	applyPostgresGatewayMigration(t, db)

	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	store := NewPostgresStore(db, time.Hour)
	store.now = func() time.Time { return now }
	scope := Scope{
		TenantID:  "tenant_pg_idem_scope_" + now.Format("20060102150405"),
		AppID:     "app_pg_idem",
		Operation: "jobs.create",
		Key:       "idem_pg_idempotency_scope",
	}
	otherScope := scope
	otherScope.TenantID = scope.TenantID + "_other"
	defer cleanupPostgresIdempotency(t, db, scope.TenantID)
	defer cleanupPostgresIdempotency(t, db, otherScope.TenantID)

	first, err := store.Reserve(context.Background(), scope, "hash-one")
	if err != nil || first.Kind != DecisionReserved {
		t.Fatalf("Reserve first = %#v err=%v, want reserved", first, err)
	}
	other, err := store.Reserve(context.Background(), otherScope, "hash-two")
	if err != nil || other.Kind != DecisionReserved {
		t.Fatalf("Reserve other scope = %#v err=%v, want reserved", other, err)
	}
	replay, err := store.Reserve(context.Background(), scope, "hash-one")
	if err != nil || replay.Kind != DecisionReplay {
		t.Fatalf("Reserve replay = %#v err=%v, want replay", replay, err)
	}

	now = now.Add(2 * time.Hour)
	expired, err := store.Reserve(context.Background(), scope, "hash-three")
	if err != nil {
		t.Fatalf("Reserve expired returned error: %v", err)
	}
	if expired.Kind != DecisionReserved || expired.Record.RequestHash != "hash-three" || expired.Record.ResourceID != "" {
		t.Fatalf("expired reserve mismatch: %#v", expired)
	}
}

func openPostgresTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open returned error: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("PingContext returned error: %v", err)
	}
	return db
}

func applyPostgresGatewayMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "migrations", "postgres", "0001_gateway_stores.sql")
	sqlBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), string(sqlBytes)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
}

func cleanupPostgresIdempotency(t *testing.T, db *sql.DB, tenantID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `DELETE FROM gateway_idempotency_records WHERE tenant_id = $1`, tenantID); err != nil {
		t.Fatalf("cleanup idempotency records: %v", err)
	}
}
