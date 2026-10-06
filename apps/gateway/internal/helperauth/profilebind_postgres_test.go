package helperauth

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// openProfilesPostgres returns a migrated database, or skips the test. Like
// every *_postgres_test.go here it needs UBAG_TEST_POSTGRES_DSN (run via
// `node tools/run-postgres-roundtrip-tests.mjs --apply-migrations`, which CI
// dispatches); without it the test skips.
func openProfilesPostgres(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("PingContext: %v", err)
	}
	// 0001 creates gateway_schema_migrations, 0010 the conversations table that
	// 0024 alters; every file is idempotent.
	for _, name := range []string{"0001_gateway_stores.sql", "0010_conversations.sql", "0024_helper_profiles.sql"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", "postgres", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.ExecContext(context.Background(), string(body)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
	return db
}

func TestPostgresProfileStoreContract(t *testing.T) {
	db := openProfilesPostgres(t)
	prefix := fmt.Sprintf("pgt%d", time.Now().UnixNano())
	tenantA, tenantB := prefix+"-a", prefix+"-b"
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM gateway_helper_profiles WHERE tenant_id LIKE $1`, prefix+"%")
	})
	testProfileStoreContract(t, NewPostgresProfileStore(db), tenantA, tenantB)
}

func TestPostgresProfileStoreLimit(t *testing.T) {
	db := openProfilesPostgres(t)
	ctx := context.Background()
	tenant := fmt.Sprintf("pgt%d-limit", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM gateway_helper_profiles WHERE tenant_id = $1`, tenant)
	})
	store := NewPostgresProfileStore(db)
	for i := 0; i < MaxProfilesPerProvider; i++ {
		if _, err := store.Bind(ctx, tenant, "chatgpt_web", fmt.Sprintf("id-%d", i), "node-1", t0); err != nil {
			t.Fatalf("bind %d: %v", i, err)
		}
	}
	if _, err := store.Bind(ctx, tenant, "chatgpt_web", "one-too-many", "node-1", t0); err != ErrTooManyProfiles {
		t.Fatalf("err = %v, want ErrTooManyProfiles", err)
	}
}

func TestPostgresProfileStoreNilSafe(t *testing.T) {
	var s *PostgresProfileStore
	if err := s.Ready(context.Background()); err != ErrNotConfigured {
		t.Fatalf("nil store Ready = %v", err)
	}
	if _, err := NewPostgresProfileStore(nil).Bind(context.Background(), "t", "p", "i", "n", t0); err != ErrNotConfigured {
		t.Fatalf("nil db Bind = %v", err)
	}
}
