package ratelimit

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresStoreIsEnvGated exercises the Postgres store against a real
// database when UBAG_TEST_POSTGRES_DSN is set, and is skipped otherwise. It
// mirrors the webhooks Postgres test gating pattern.
func TestPostgresStoreIsEnvGated(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()

	store, err := NewPostgresStore(ctx, db)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	clock := newClock(time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC))
	limiter := newTestLimiter(store, Policy{Limit: 2, Window: time.Minute}, clock)

	key := "test:ratelimit:" + time.Now().UTC().Format(time.RFC3339Nano)
	mustAllow(t, limiter, ctx, key, true)
	mustAllow(t, limiter, ctx, key, true)
	mustAllow(t, limiter, ctx, key, false)

	clock.Advance(time.Minute)
	mustAllow(t, limiter, ctx, key, true)
}
