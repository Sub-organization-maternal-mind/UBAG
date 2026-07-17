package pat

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresStoreContract round-trips a PAT through the Postgres store. It is
// skipped unless UBAG_TEST_POSTGRES_DSN points at a database (run via
// `pnpm test:gateway:postgres`), matching every other *_postgres_test.go here.
func TestPostgresStoreContract(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}

	db := openPostgresTestDB(t, dsn)
	defer db.Close()
	applyPostgresPATMigration(t, db)

	store := NewPostgresStore(db)
	ctx := context.Background()
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000")
	token, err := Issue("tenant_pg_pat_"+suffix, "app_pg_pat", "service", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	defer cleanupPostgresPAT(t, db, token.TenantID)

	if err := store.Save(ctx, token); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok, err := store.Resolve(ctx, token.ID, time.Now())
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if got.TenantID != token.TenantID || got.AppID != "app_pg_pat" || got.Role != "service" {
		t.Fatalf("resolved fields wrong: %+v", got)
	}
	if got.ID != token.ID {
		t.Fatalf("resolved ID = %q, want the presented token", got.ID)
	}

	// Only the hash is persisted, never the raw token.
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM gateway_pats WHERE tenant_id = $1`, token.TenantID).Scan(&stored); err != nil {
		t.Fatalf("select token_hash: %v", err)
	}
	if stored == token.ID || stored != hashToken(token.ID) {
		t.Fatalf("token_hash is not the SHA-256 of the token: %q", stored)
	}

	// Expired as of a time past ExpiresAt.
	if _, ok, _ := store.Resolve(ctx, token.ID, token.ExpiresAt.Add(time.Minute)); ok {
		t.Fatal("expired token resolved as live")
	}

	// Revocation is durable.
	if err := store.Revoke(ctx, token.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok, _ := store.Resolve(ctx, token.ID, time.Now()); ok {
		t.Fatal("revoked token still resolves")
	}

	// Unknown token.
	if _, ok, _ := store.Resolve(ctx, "ubag_pat_unknown_"+suffix, time.Now()); ok {
		t.Fatal("unknown token resolved")
	}
}

// TestPostgresStoreNilSafety runs without a database: every method must report
// ErrNotConfigured (not panic) when the store has no db, so a misconfigured
// gateway fails closed.
func TestPostgresStoreNilSafety(t *testing.T) {
	store := NewPostgresStore(nil)
	ctx := context.Background()

	if err := store.Ready(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Ready nil db = %v, want ErrNotConfigured", err)
	}
	if err := store.Save(ctx, Token{ID: "ubag_pat_x", TenantID: "t", AppID: "a"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Save nil db = %v, want ErrNotConfigured", err)
	}
	if _, _, err := store.Resolve(ctx, "ubag_pat_x", time.Now()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Resolve nil db = %v, want ErrNotConfigured", err)
	}
	if err := store.Revoke(ctx, "ubag_pat_x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Revoke nil db = %v, want ErrNotConfigured", err)
	}
}

func openPostgresTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("PingContext: %v", err)
	}
	return db
}

// applyPostgresPATMigration applies the base migration (which creates
// gateway_schema_migrations) then 0011 (gateway_pats). Both are idempotent
// (IF NOT EXISTS / ON CONFLICT), so re-running against a live DB is safe.
func applyPostgresPATMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range []string{"0001_gateway_stores.sql", "0011_personal_access_tokens.sql"} {
		path := filepath.Join("..", "..", "..", "..", "migrations", "postgres", name)
		sqlBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.ExecContext(context.Background(), string(sqlBytes)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
}

func cleanupPostgresPAT(t *testing.T, db *sql.DB, tenantID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `DELETE FROM gateway_pats WHERE tenant_id = $1`, tenantID); err != nil {
		t.Fatalf("cleanup gateway_pats: %v", err)
	}
}
