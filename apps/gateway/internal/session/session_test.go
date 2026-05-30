package session

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newSQLiteSessionStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "session.db") + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	store := NewSQLiteStore(db)
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("ready: %v", err)
	}
	return store
}

func samplePrincipal(now time.Time) Session {
	return Session{
		TenantID:  "tenant-1",
		AppID:     "app-1",
		Role:      "operator",
		Subject:   "user@example.com",
		Email:     "user@example.com",
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Hour),
	}
}

func runSessionAssertions(t *testing.T, store Store) {
	ctx := context.Background()
	now := time.Now().UTC()

	created, token, err := store.Create(ctx, samplePrincipal(now))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	if created.ID == "" {
		t.Fatal("expected a session id")
	}

	resolved, ok, err := store.Resolve(ctx, token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !ok {
		t.Fatal("expected live session to resolve")
	}
	if resolved.Role != "operator" || resolved.TenantID != "tenant-1" || resolved.Subject != "user@example.com" {
		t.Errorf("resolved principal mismatch: %+v", resolved)
	}

	// Expired token does not resolve.
	if _, ok, err := store.Resolve(ctx, token, now.Add(2*time.Hour)); err != nil || ok {
		t.Errorf("expected expired session to be rejected, ok=%v err=%v", ok, err)
	}

	// Unknown token does not resolve.
	if _, ok, err := store.Resolve(ctx, "not-a-real-token", now); err != nil || ok {
		t.Errorf("expected unknown token to be rejected, ok=%v err=%v", ok, err)
	}

	// Revoke is effective and idempotent.
	revoked, err := store.Revoke(ctx, token, now)
	if err != nil || !revoked {
		t.Fatalf("revoke: revoked=%v err=%v", revoked, err)
	}
	if again, err := store.Revoke(ctx, token, now); err != nil || again {
		t.Errorf("expected second revoke to be a no-op, again=%v err=%v", again, err)
	}
	if _, ok, err := store.Resolve(ctx, token, now.Add(time.Minute)); err != nil || ok {
		t.Errorf("expected revoked session to be rejected, ok=%v err=%v", ok, err)
	}
}

func TestMemorySessionStore(t *testing.T) {
	runSessionAssertions(t, NewMemoryStore())
}

func TestSQLiteSessionStore(t *testing.T) {
	runSessionAssertions(t, newSQLiteSessionStore(t))
}

func TestTokensAreUniqueAndHashed(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()
	_, token1, err := store.Create(ctx, samplePrincipal(now))
	if err != nil {
		t.Fatalf("create 1: %v", err)
	}
	_, token2, err := store.Create(ctx, samplePrincipal(now))
	if err != nil {
		t.Fatalf("create 2: %v", err)
	}
	if token1 == token2 {
		t.Fatal("expected distinct tokens")
	}
	// The plaintext token must never be a stored key.
	if _, ok := store.byHash[token1]; ok {
		t.Fatal("plaintext token must not be used as a store key")
	}
	if _, ok := store.byHash[hashToken(token1)]; !ok {
		t.Fatal("expected token hash to be the store key")
	}
}
