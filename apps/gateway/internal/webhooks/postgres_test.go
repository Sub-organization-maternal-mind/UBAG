package webhooks

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresStoreReadyIsEnvGated(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("Postgres webhook store is not ready: %v", err)
	}
}

func TestPostgresStoreLeaseDueDoesNotLeaseTwice(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// One connection keeps the temporary outbox isolated from real deliveries.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TEMP TABLE gateway_webhook_deliveries (LIKE public.gateway_webhook_deliveries INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db)
	delivery, created, err := store.Enqueue(ctx, EnqueueRequest{
		TenantID: "lease-test", AppID: "lease-test", EventName: "job.completed",
		URL: "https://example.com/callback", SecretID: "test-secret",
		DedupeKey: "lease-test", Payload: []byte(`{}`),
	})
	if err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	leased, err := store.LeaseDue(ctx, "worker-one", 10, time.Minute)
	if err != nil {
		t.Fatalf("lease due: %v", err)
	}
	if len(leased) != 1 || leased[0].ID != delivery.ID || leased[0].Status != StatusLeased || leased[0].LeaseID == "" {
		t.Fatalf("unexpected leased deliveries: %+v", leased)
	}
	second, err := store.LeaseDue(ctx, "worker-two", 10, time.Minute)
	if err != nil || len(second) != 0 {
		t.Fatalf("active lease was leased again: count=%d err=%v", len(second), err)
	}
}
