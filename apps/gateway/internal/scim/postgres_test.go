package scim

import (
	"context"
	"database/sql"
	"os"
	"testing"

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
	store, err := NewPostgresStore(db)
	if err != nil {
		t.Fatalf("construct postgres scim store: %v", err)
	}
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("Postgres SCIM store is not ready: %v", err)
	}
}
