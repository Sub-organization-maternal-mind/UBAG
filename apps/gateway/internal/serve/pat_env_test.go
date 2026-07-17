package serve

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/pat"
	_ "modernc.org/sqlite"
)

func TestPATDisabledByDefault(t *testing.T) {
	t.Setenv("UBAG_PAT_ENABLED", "")
	out, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv: %v", err)
	}
	if out.pat != nil {
		t.Fatalf("PAT store should be nil (disabled) by default, got %T", out.pat)
	}
}

func TestPATEnabledMemoryStore(t *testing.T) {
	t.Setenv("UBAG_PAT_ENABLED", "true")
	t.Setenv("UBAG_PAT_DEFAULT_TTL_MS", "")
	out, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv: %v", err)
	}
	if _, ok := out.pat.(*pat.MemoryStore); !ok {
		t.Fatalf("expected *pat.MemoryStore when enabled with memory kind, got %T", out.pat)
	}
	if out.patDefaultTTL != 0 {
		t.Fatalf("default PAT TTL should be 0 (no expiry), got %v", out.patDefaultTTL)
	}
}

func TestPATDefaultTTLFromEnv(t *testing.T) {
	t.Setenv("UBAG_PAT_ENABLED", "true")
	t.Setenv("UBAG_PAT_DEFAULT_TTL_MS", "3600000") // 1h
	out, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv: %v", err)
	}
	if out.patDefaultTTL != time.Hour {
		t.Fatalf("PAT TTL = %v, want 1h", out.patDefaultTTL)
	}
}

// TestPATEnabledSQLiteStore covers the persistent-store wiring branch: with the
// sqlite store kind and a real db, the PAT store is a *pat.SQLiteStore whose
// schema is ready (so issued tokens survive restarts).
func TestPATEnabledSQLiteStore(t *testing.T) {
	db, err := sql.Open("sqlite", "file:pat_serve_wiring_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	t.Setenv("UBAG_PAT_ENABLED", "true")
	t.Setenv("UBAG_PAT_DEFAULT_TTL_MS", "")
	out, err := newEnterpriseStoresFromEnv(context.Background(), "sqlite", db)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv sqlite: %v", err)
	}
	store, ok := out.pat.(*pat.SQLiteStore)
	if !ok {
		t.Fatalf("expected *pat.SQLiteStore for sqlite kind, got %T", out.pat)
	}
	// Ready ran during wiring, so a round-trip works against the created schema.
	token, err := pat.Issue("tenant_wire", "app_wire", "service", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := store.Save(context.Background(), token); err != nil {
		t.Fatalf("save against wired store: %v", err)
	}
	if _, found, err := store.Resolve(context.Background(), token.ID, time.Now()); err != nil || !found {
		t.Fatalf("resolve against wired store: found=%v err=%v", found, err)
	}
}
