package conversations

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func TestMemoryStoreBindIsUpsertByKey(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	key := Key{TenantID: "t1", AppID: "a1", Target: "mock", ConversationKey: "c1"}
	now := time.Unix(1, 0).UTC()

	first, err := store.Bind(ctx, Conversation{
		TenantID: key.TenantID, AppID: key.AppID, Target: key.Target,
		ConversationKey: key.ConversationKey, ProviderThreadRef: "https://example/chat/1",
		State: StateActive, CreatedAt: now, LastUsedAt: now,
	})
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if first.ProviderThreadRef != "https://example/chat/1" {
		t.Fatalf("thread ref = %q", first.ProviderThreadRef)
	}

	// Re-binding the same key must overwrite, not append.
	if _, err := store.Bind(ctx, Conversation{
		TenantID: key.TenantID, AppID: key.AppID, Target: key.Target,
		ConversationKey: key.ConversationKey, ProviderThreadRef: "https://example/chat/2",
		State: StateActive, CreatedAt: now, LastUsedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("second bind: %v", err)
	}

	got, found, err := store.Resolve(ctx, key)
	if err != nil || !found {
		t.Fatalf("resolve: found=%v err=%v", found, err)
	}
	if got.ProviderThreadRef != "https://example/chat/2" {
		t.Fatalf("thread ref after rebind = %q, want chat/2", got.ProviderThreadRef)
	}

	all, err := store.List(ctx, Filter{TenantID: "t1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(list) = %d, want 1 (upsert must not append)", len(all))
	}
}

func TestMemoryStoreResolveIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Unix(1, 0).UTC()
	if _, err := store.Bind(ctx, Conversation{
		TenantID: "t1", AppID: "a1", Target: "mock", ConversationKey: "c1",
		ProviderThreadRef: "https://example/chat/1", State: StateActive,
		CreatedAt: now, LastUsedAt: now,
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, found, err := store.Resolve(ctx, Key{
		TenantID: "t2", AppID: "a1", Target: "mock", ConversationKey: "c1",
	}); err != nil || found {
		t.Fatalf("cross-tenant resolve: found=%v err=%v, want found=false err=nil", found, err)
	}
}

func TestMemoryStoreMarkBroken(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	key := Key{TenantID: "t1", AppID: "a1", Target: "mock", ConversationKey: "c1"}
	now := time.Unix(1, 0).UTC()
	if _, err := store.Bind(ctx, Conversation{
		TenantID: key.TenantID, AppID: key.AppID, Target: key.Target,
		ConversationKey: key.ConversationKey, ProviderThreadRef: "https://example/chat/1",
		State: StateActive, CreatedAt: now, LastUsedAt: now,
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	got, found, err := store.MarkBroken(ctx, key, now.Add(time.Minute))
	if err != nil || !found {
		t.Fatalf("mark broken: found=%v err=%v", found, err)
	}
	if got.State != StateBroken {
		t.Fatalf("state = %q, want %q", got.State, StateBroken)
	}
}

// newTestSQLiteDB opens an isolated file-backed SQLite database (single
// connection, matching the gateway's other SQLite store tests).
func newTestSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "conversations.db") + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSQLiteStoreBindResolveUpsert(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t) // reuse the alerts test helper idiom
	store := NewSQLiteStore(db)
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}
	key := Key{TenantID: "t1", AppID: "a1", Target: "mock", ConversationKey: "c1"}
	now := time.Unix(1, 0).UTC()
	for _, ref := range []string{"https://example/chat/1", "https://example/chat/2"} {
		if _, err := store.Bind(ctx, Conversation{
			TenantID: key.TenantID, AppID: key.AppID, Target: key.Target,
			ConversationKey: key.ConversationKey, ProviderThreadRef: ref,
			State: StateActive, CreatedAt: now, LastUsedAt: now,
		}); err != nil {
			t.Fatalf("bind %s: %v", ref, err)
		}
	}
	got, found, err := store.Resolve(ctx, key)
	if err != nil || !found {
		t.Fatalf("resolve: found=%v err=%v", found, err)
	}
	if got.ProviderThreadRef != "https://example/chat/2" {
		t.Fatalf("thread ref = %q, want chat/2", got.ProviderThreadRef)
	}
	all, err := store.List(ctx, Filter{TenantID: "t1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(all))
	}
}

// TestPostgresStoreReadyIsEnvGated asserts the Postgres store's assertive
// readiness against a real database when UBAG_TEST_POSTGRES_DSN is set, and is
// skipped otherwise (matching the gateway's other Postgres store tests).
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
		t.Fatalf("Postgres conversations store is not ready: %v", err)
	}
}
