package pat

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestSQLiteStore(t *testing.T) (*SQLiteStore, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:pat_test_"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := NewSQLiteStore(db)
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("ready: %v", err)
	}
	return store, db
}

func TestSQLiteSaveResolveRoundTrip(t *testing.T) {
	store, _ := newTestSQLiteStore(t)
	ctx := context.Background()

	token, err := Issue("tenant_oet", "oet-prep", "service", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := store.Save(ctx, token); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok, err := store.Resolve(ctx, token.ID, time.Now())
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if got.TenantID != "tenant_oet" || got.AppID != "oet-prep" || got.Role != "service" {
		t.Fatalf("resolved fields wrong: %+v", got)
	}
	if got.ID != token.ID {
		t.Fatalf("resolved ID = %q, want the presented token", got.ID)
	}
}

func TestSQLiteResolveUnknownToken(t *testing.T) {
	store, _ := newTestSQLiteStore(t)
	_, ok, err := store.Resolve(context.Background(), "ubag_pat_unknown", time.Now())
	if err != nil {
		t.Fatalf("resolve unknown: %v", err)
	}
	if ok {
		t.Fatal("resolve unknown returned ok=true")
	}
}

func TestSQLiteTokenExpiry(t *testing.T) {
	store, _ := newTestSQLiteStore(t)
	ctx := context.Background()

	token, err := Issue("tenant_a", "app_a", "service", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := store.Save(ctx, token); err != nil {
		t.Fatalf("save: %v", err)
	}
	future := token.ExpiresAt.Add(time.Minute)
	if _, ok, _ := store.Resolve(ctx, token.ID, future); ok {
		t.Fatal("expired token resolved as live")
	}
	// A no-expiry token stays live arbitrarily far in the future.
	forever, _ := Issue("tenant_a", "app_a", "service", 0)
	if err := store.Save(ctx, forever); err != nil {
		t.Fatalf("save forever: %v", err)
	}
	if _, ok, _ := store.Resolve(ctx, forever.ID, time.Now().Add(1000*time.Hour)); !ok {
		t.Fatal("no-expiry token did not resolve far in the future")
	}
}

func TestSQLiteRevoke(t *testing.T) {
	store, _ := newTestSQLiteStore(t)
	ctx := context.Background()

	token, _ := Issue("tenant_a", "app_a", "service", time.Hour)
	if err := store.Save(ctx, token); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.Revoke(ctx, token.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok, _ := store.Resolve(ctx, token.ID, time.Now()); ok {
		t.Fatal("revoked token still resolves")
	}
}

// TestSQLiteStoresOnlyHash proves the raw token is never persisted — the row is
// keyed by SHA-256(token), so a store leak reveals no usable credential.
func TestSQLiteStoresOnlyHash(t *testing.T) {
	store, db := newTestSQLiteStore(t)
	ctx := context.Background()

	token, _ := Issue("tenant_a", "app_a", "service", time.Hour)
	if err := store.Save(ctx, token); err != nil {
		t.Fatalf("save: %v", err)
	}
	var key string
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM gateway_pats LIMIT 1`).Scan(&key); err != nil {
		t.Fatalf("select token_hash: %v", err)
	}
	if key == token.ID {
		t.Fatal("raw token stored as primary key; expected a hash")
	}
	if key != hashToken(token.ID) {
		t.Fatalf("primary key is not the token hash: %q", key)
	}
}

// TestSQLitePersistsAcrossStoreInstances proves a second store over the same DB
// resolves a token the first saved — the property that makes PATs survive a
// gateway restart (unlike the in-memory store).
func TestSQLitePersistsAcrossStoreInstances(t *testing.T) {
	store, db := newTestSQLiteStore(t)
	ctx := context.Background()

	token, _ := Issue("tenant_a", "app_a", "service", time.Hour)
	if err := store.Save(ctx, token); err != nil {
		t.Fatalf("save: %v", err)
	}
	second := NewSQLiteStore(db)
	if err := second.Ready(ctx); err != nil {
		t.Fatalf("second ready: %v", err)
	}
	if _, ok, err := second.Resolve(ctx, token.ID, time.Now()); err != nil || !ok {
		t.Fatalf("second store resolve: ok=%v err=%v", ok, err)
	}
}

// TestSQLiteStoreNilSafety: a store with no db reports ErrNotConfigured from
// every method rather than panicking, so a misconfigured gateway fails closed.
func TestSQLiteStoreNilSafety(t *testing.T) {
	store := NewSQLiteStore(nil)
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
