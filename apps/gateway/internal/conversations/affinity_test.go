package conversations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// affinityStores returns every backend that can run without external services.
func affinityStores(t *testing.T) map[string]Store {
	t.Helper()
	sqlite := NewSQLiteStore(newTestSQLiteDB(t))
	if err := sqlite.Ready(context.Background()); err != nil {
		t.Fatalf("sqlite ready: %v", err)
	}
	return map[string]Store{"memory": NewMemoryStore(), "sqlite": sqlite}
}

func helperConv(tenant, key, node, profile string) Conversation {
	now := time.Unix(10, 0).UTC()
	return Conversation{
		TenantID: tenant, AppID: "app", Target: "chatgpt_web", ConversationKey: key,
		ProviderThreadRef: "https://chat.example/c/" + key, State: StateActive,
		CreatedAt: now, LastUsedAt: now, NodeID: node, ProfileRef: profile,
	}
}

// testNodeAffinityContract is shared by every backend, Postgres included.
func testNodeAffinityContract(t *testing.T, store Store, tenantA, tenantB, node string) {
	t.Helper()
	ctx := context.Background()
	kOf := func(c Conversation) Key {
		return Key{TenantID: c.TenantID, AppID: c.AppID, Target: c.Target, ConversationKey: c.ConversationKey}
	}

	// Node and profile round-trip through Bind and Resolve.
	a1 := helperConv(tenantA, "c1", node, "pr_a1")
	a2 := helperConv(tenantA, "c2", node, "pr_a2")
	b1 := helperConv(tenantB, "c1", node, "pr_b1")
	other := helperConv(tenantA, "c3", node+"-other", "pr_a3")
	local := helperConv(tenantA, "c4", "", "") // run by the local worker
	for _, c := range []Conversation{a1, a2, b1, other, local} {
		got, err := store.Bind(ctx, c)
		if err != nil {
			t.Fatalf("bind %s/%s: %v", c.TenantID, c.ConversationKey, err)
		}
		if got.NodeID != c.NodeID || got.ProfileRef != c.ProfileRef {
			t.Fatalf("bind returned node=%q profile=%q, want %q/%q", got.NodeID, got.ProfileRef, c.NodeID, c.ProfileRef)
		}
	}
	got, found, err := store.Resolve(ctx, kOf(a1))
	if err != nil || !found || got.NodeID != node || got.ProfileRef != "pr_a1" {
		t.Fatalf("resolve = %+v found=%v err=%v", got, found, err)
	}
	listed, err := store.List(ctx, Filter{TenantID: tenantA})
	if err != nil || len(listed) != 4 {
		t.Fatalf("list = %d, %v; want 4", len(listed), err)
	}
	for _, c := range listed {
		if c.ConversationKey == "c1" && (c.NodeID != node || c.ProfileRef != "pr_a1") {
			t.Fatalf("list lost the affinity: %+v", c)
		}
	}

	// A lost node breaks every active conversation on it, across tenants, and
	// nothing else.
	n, err := store.MarkNodeBroken(ctx, node)
	if err != nil || n != 3 {
		t.Fatalf("MarkNodeBroken = %d, %v; want 3 (a1, a2, b1)", n, err)
	}
	for _, c := range []Conversation{a1, a2, b1} {
		got, _, _ := store.Resolve(ctx, kOf(c))
		if got.State != StateBroken {
			t.Fatalf("%s/%s state = %q, want broken", c.TenantID, c.ConversationKey, got.State)
		}
		// No migration: the binding keeps its node, profile and thread.
		if got.NodeID != node || got.ProfileRef != c.ProfileRef || got.ProviderThreadRef != c.ProviderThreadRef {
			t.Fatalf("broken conversation was rewritten: %+v", got)
		}
	}
	for _, c := range []Conversation{other, local} {
		if got, _, _ := store.Resolve(ctx, kOf(c)); got.State != StateActive {
			t.Fatalf("%s state = %q; an unrelated conversation was broken", c.ConversationKey, got.State)
		}
	}
	// Idempotent, and an empty node id matches nothing (not "every local row").
	if n, err := store.MarkNodeBroken(ctx, node); err != nil || n != 0 {
		t.Fatalf("second MarkNodeBroken = %d, %v; want 0", n, err)
	}
	if n, err := store.MarkNodeBroken(ctx, ""); err != nil || n != 0 {
		t.Fatalf("empty node MarkNodeBroken = %d, %v; want 0", n, err)
	}
	if got, _, _ := store.Resolve(ctx, kOf(local)); got.State != StateActive {
		t.Fatal("local conversation was broken by an empty node id")
	}

	// A fresh bind (on_missing=restart) re-activates on a new node and profile.
	fresh, err := store.Bind(ctx, helperConv(tenantA, "c1", node+"-other", "pr_a9"))
	if err != nil || fresh.State != StateActive || fresh.NodeID != node+"-other" || fresh.ProfileRef != "pr_a9" {
		t.Fatalf("rebind = %+v, %v", fresh, err)
	}
	// Binding without affinity (local worker) clears it.
	cleared, err := store.Bind(ctx, helperConv(tenantA, "c1", "", ""))
	if err != nil || cleared.NodeID != "" || cleared.ProfileRef != "" {
		t.Fatalf("local rebind = %+v, %v", cleared, err)
	}

	// A half-set or oversized affinity fails closed and stores nothing.
	for name, c := range map[string]Conversation{
		"node without profile": helperConv(tenantA, "bad1", node, ""),
		"profile without node": helperConv(tenantA, "bad2", "", "pr_x"),
		"oversized profile":    helperConv(tenantA, "bad3", node, strings.Repeat("p", maxAffinityLen+1)),
		"oversized node":       helperConv(tenantA, "bad4", strings.Repeat("n", maxAffinityLen+1), "pr_x"),
	} {
		if _, err := store.Bind(ctx, c); !errors.Is(err, ErrInvalidAffinity) {
			t.Errorf("%s: err = %v, want ErrInvalidAffinity", name, err)
		}
		if _, found, _ := store.Resolve(ctx, kOf(c)); found {
			t.Errorf("%s: a rejected bind was stored", name)
		}
	}
}

func TestNodeAffinityContract(t *testing.T) {
	for name, store := range affinityStores(t) {
		t.Run(name, func(t *testing.T) {
			testNodeAffinityContract(t, store, "tenant-a", "tenant-b", "node-1")
		})
	}
}

func TestConversationJSONHidesNodeAffinity(t *testing.T) {
	raw, err := json.Marshal(helperConv("t", "c", "node-1", "pr_secret"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"node-1", "pr_secret", "node_id", "profile_ref"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("tenant-facing JSON leaks %q: %s", leak, raw)
		}
	}
}

func TestManagerMarkNodeBroken(t *testing.T) {
	var nilManager *Manager
	if n, err := nilManager.MarkNodeBroken(context.Background(), "node-1"); n != 0 || err != nil {
		t.Fatalf("nil manager = %d, %v; want a no-op", n, err)
	}
	mgr := NewManager(NewMemoryStore(), nil, "memory")
	if _, err := mgr.Bind(context.Background(), helperConv("t", "c", "node-1", "pr_1")); err != nil {
		t.Fatal(err)
	}
	if n, err := mgr.MarkNodeBroken(context.Background(), "node-1"); n != 1 || err != nil {
		t.Fatalf("manager MarkNodeBroken = %d, %v", n, err)
	}
}

// A SQLite database created before the helper plane gains the columns in place
// and keeps its rows local.
func TestSQLiteReadyAddsAffinityColumnsToOldTable(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{`
CREATE TABLE gateway_conversations (
	tenant_id TEXT NOT NULL, app_id TEXT NOT NULL, target TEXT NOT NULL, conversation_key TEXT NOT NULL,
	provider_thread_ref TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'active',
	created_at TEXT NOT NULL, last_used_at TEXT NOT NULL DEFAULT '', last_job_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (tenant_id, app_id, target, conversation_key))`,
		`INSERT INTO gateway_conversations (tenant_id, app_id, target, conversation_key, provider_thread_ref, created_at)
		 VALUES ('t', 'app', 'chatgpt_web', 'old', 'https://chat.example/c/old', '2026-01-01T00:00:00.000000Z')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	store := NewSQLiteStore(db)
	for i := 0; i < 2; i++ { // second Ready proves the migration is idempotent
		if err := store.Ready(ctx); err != nil {
			t.Fatalf("ready #%d: %v", i+1, err)
		}
	}
	got, found, err := store.Resolve(ctx, Key{TenantID: "t", AppID: "app", Target: "chatgpt_web", ConversationKey: "old"})
	if err != nil || !found || got.NodeID != "" || got.ProfileRef != "" || got.ProviderThreadRef == "" {
		t.Fatalf("old row = %+v found=%v err=%v", got, found, err)
	}
	if n, err := store.MarkNodeBroken(ctx, "node-1"); err != nil || n != 0 {
		t.Fatalf("MarkNodeBroken on old rows = %d, %v; want 0", n, err)
	}
}

// TestPostgresNodeAffinityContract runs the same contract against Postgres. It
// is env-gated like the other Postgres tests (UBAG_TEST_POSTGRES_DSN, with
// migrations applied by tools/run-postgres-roundtrip-tests.mjs).
func TestPostgresNodeAffinityContract(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewPostgresStore(db)
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("not ready (apply migrations/postgres/0024_helper_profiles.sql): %v", err)
	}
	prefix := fmt.Sprintf("pgt%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM gateway_conversations WHERE tenant_id LIKE $1`, prefix+"%")
	})
	// The node id is unique to this run: MarkNodeBroken is node-wide, so it must
	// not touch rows other tests or tenants own.
	testNodeAffinityContract(t, store, prefix+"-a", prefix+"-b", prefix+"-node")
}
