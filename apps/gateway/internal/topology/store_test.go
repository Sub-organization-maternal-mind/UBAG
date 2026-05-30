package topology

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func seededMemoryStore() *MemoryStore {
	store := NewMemoryStore()
	store.AddInstance(BrowserInstance{InstanceID: "inst-a", WorkerID: "w1", TenantID: "tenant-1", Engine: "chromium", State: "ready", ContextCount: 1, TabCount: 1, CreatedAt: time.Now()})
	store.AddInstance(BrowserInstance{InstanceID: "inst-b", WorkerID: "w1", TenantID: "tenant-1", Engine: "chromium", State: "draining", CreatedAt: time.Now()})
	store.AddInstance(BrowserInstance{InstanceID: "inst-z", WorkerID: "w9", TenantID: "tenant-2", Engine: "firefox", State: "ready", CreatedAt: time.Now()})

	store.AddContext(ProviderContext{ContextID: "ctx-a", InstanceID: "inst-a", TenantID: "tenant-1", TargetID: "chatgpt_web", IdentityRef: "id-1", LoginState: "authenticated", ConversationModel: "url", HasStorageState: true, MaxTabs: 2, CreatedAt: time.Now()})
	store.AddContext(ProviderContext{ContextID: "ctx-z", InstanceID: "inst-z", TenantID: "tenant-2", TargetID: "claude_web", IdentityRef: "id-9", LoginState: "logged_out", ConversationModel: "spa-singleton", MaxTabs: 1, CreatedAt: time.Now()})

	store.AddTab(BrowserTab{TabID: "tab-a", ContextID: "ctx-a", State: "ready", CreatedAt: time.Now()})
	store.AddTab(BrowserTab{TabID: "tab-z", ContextID: "ctx-z", State: "busy", CreatedAt: time.Now()})
	// Orphan tab whose context is unknown should be dropped.
	store.AddTab(BrowserTab{TabID: "tab-orphan", ContextID: "ctx-missing", State: "ready", CreatedAt: time.Now()})
	return store
}

func TestMemoryStoreTenantIsolation(t *testing.T) {
	store := seededMemoryStore()
	ctx := context.Background()

	instances, err := store.ListInstances(ctx, InstanceFilter{TenantID: "tenant-1"})
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 2 {
		t.Fatalf("expected 2 instances for tenant-1, got %d", len(instances))
	}
	for _, instance := range instances {
		if instance.TenantID != "tenant-1" {
			t.Fatalf("tenant leak: got %s", instance.TenantID)
		}
	}

	contexts, err := store.ListContexts(ctx, ContextFilter{TenantID: "tenant-2"})
	if err != nil {
		t.Fatalf("list contexts: %v", err)
	}
	if len(contexts) != 1 || contexts[0].ContextID != "ctx-z" {
		t.Fatalf("expected only ctx-z for tenant-2, got %+v", contexts)
	}

	// Tabs are isolated via their parent context's tenant.
	tabs1, err := store.ListTabs(ctx, TabFilter{TenantID: "tenant-1"})
	if err != nil {
		t.Fatalf("list tabs: %v", err)
	}
	if len(tabs1) != 1 || tabs1[0].TabID != "tab-a" {
		t.Fatalf("expected only tab-a for tenant-1, got %+v", tabs1)
	}
	tabs2, _ := store.ListTabs(ctx, TabFilter{TenantID: "tenant-2"})
	if len(tabs2) != 1 || tabs2[0].TabID != "tab-z" {
		t.Fatalf("expected only tab-z for tenant-2, got %+v", tabs2)
	}
}

func TestMemoryStoreStateFilter(t *testing.T) {
	store := seededMemoryStore()
	instances, _ := store.ListInstances(context.Background(), InstanceFilter{TenantID: "tenant-1", State: "draining"})
	if len(instances) != 1 || instances[0].InstanceID != "inst-b" {
		t.Fatalf("state filter failed: %+v", instances)
	}
}

func TestMemoryStoreSummary(t *testing.T) {
	store := seededMemoryStore()
	summary, err := store.Summary(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.TotalInstances != 2 || summary.TotalContexts != 1 || summary.TotalTabs != 1 {
		t.Fatalf("unexpected totals: %+v", summary)
	}
	if summary.InstancesByState["ready"] != 1 || summary.InstancesByState["draining"] != 1 {
		t.Fatalf("unexpected instance states: %+v", summary.InstancesByState)
	}
	if summary.ContextsByLoginState["authenticated"] != 1 {
		t.Fatalf("unexpected login states: %+v", summary.ContextsByLoginState)
	}
}

func newSQLiteTopologyStore(t *testing.T) (*SQLiteStore, *sql.DB) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "topology.db") + "?_pragma=busy_timeout(5000)"
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
	return store, db
}

func TestSQLiteStoreRedactionAndJoin(t *testing.T) {
	store, db := newSQLiteTopologyStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := db.ExecContext(ctx,
		`INSERT INTO gateway_browser_instances (instance_id, worker_id, tenant_id, engine, state, context_count, tab_count, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		"inst-1", "w1", "tenant-1", "chromium", "ready", 1, 1, now); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	// One context with a secret storage_state_uri (tenant-1), one for tenant-2.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO gateway_provider_contexts (context_id, instance_id, tenant_id, target_id, identity_ref, login_state, conversation_model, storage_state_uri, max_tabs, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"ctx-1", "inst-1", "tenant-1", "chatgpt_web", "id-1", "authenticated", "url", "s3://secret/storage-state.json", 2, now); err != nil {
		t.Fatalf("seed context: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO gateway_provider_contexts (context_id, instance_id, tenant_id, target_id, identity_ref, login_state, conversation_model, max_tabs, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"ctx-2", "inst-1", "tenant-2", "claude_web", "id-2", "logged_out", "url", 2, now); err != nil {
		t.Fatalf("seed context 2: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO gateway_browser_tabs (tab_id, context_id, state, jobs_completed, created_at) VALUES (?,?,?,?,?)`,
		"tab-1", "ctx-1", "busy", 3, now); err != nil {
		t.Fatalf("seed tab: %v", err)
	}

	contexts, err := store.ListContexts(ctx, ContextFilter{TenantID: "tenant-1"})
	if err != nil {
		t.Fatalf("list contexts: %v", err)
	}
	if len(contexts) != 1 {
		t.Fatalf("expected 1 context for tenant-1, got %d", len(contexts))
	}
	if !contexts[0].HasStorageState {
		t.Fatalf("expected has_storage_state=true when storage_state_uri is set")
	}

	// Tabs must be tenant-scoped via the context join.
	tabs, err := store.ListTabs(ctx, TabFilter{TenantID: "tenant-1"})
	if err != nil {
		t.Fatalf("list tabs: %v", err)
	}
	if len(tabs) != 1 || tabs[0].TabID != "tab-1" {
		t.Fatalf("expected tab-1 for tenant-1, got %+v", tabs)
	}
	if tabs2, _ := store.ListTabs(ctx, TabFilter{TenantID: "tenant-2"}); len(tabs2) != 0 {
		t.Fatalf("expected no tabs for tenant-2, got %+v", tabs2)
	}

	summary, err := store.Summary(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.TotalInstances != 1 || summary.TotalContexts != 1 || summary.TotalTabs != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.TabsByState["busy"] != 1 {
		t.Fatalf("unexpected tabs by state: %+v", summary.TabsByState)
	}
}

func TestSQLiteStoreNullableFields(t *testing.T) {
	store, db := newSQLiteTopologyStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO gateway_browser_instances (instance_id, worker_id, tenant_id, engine, state, context_count, tab_count, rss_bytes, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"inst-1", "w1", "tenant-1", "chromium", "ready", 0, 0, 4096, now); err != nil {
		t.Fatalf("seed: %v", err)
	}
	instances, err := store.ListInstances(ctx, InstanceFilter{TenantID: "tenant-1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(instances))
	}
	if instances[0].RSSBytes == nil || *instances[0].RSSBytes != 4096 {
		t.Fatalf("expected rss_bytes 4096, got %v", instances[0].RSSBytes)
	}
	if instances[0].RecycleAt != nil {
		t.Fatalf("expected nil recycle_at, got %v", instances[0].RecycleAt)
	}
}

func TestConcurrencyRegistry(t *testing.T) {
	registry := NewConcurrencyRegistry()
	registry.Report("tenant-1", ConcurrencyView{Target: "chatgpt_web", IdentityRef: "id-1", CurrentCap: 3, Min: 1, Max: 5, InFlight: 2, LastChangeReason: "decrease"})
	registry.Report("tenant-1", ConcurrencyView{Target: "claude_web", IdentityRef: "id-2", CurrentCap: 1})
	registry.Report("tenant-2", ConcurrencyView{Target: "chatgpt_web", IdentityRef: "id-9", CurrentCap: 4})

	views := registry.List("tenant-1")
	if len(views) != 2 {
		t.Fatalf("expected 2 views for tenant-1, got %d", len(views))
	}
	// Sorted by target then identity.
	if views[0].Target != "chatgpt_web" || views[1].Target != "claude_web" {
		t.Fatalf("unexpected ordering: %+v", views)
	}
	if views[0].LastChangeAt.IsZero() {
		t.Fatalf("expected LastChangeAt defaulted to now")
	}
	if other := registry.List("tenant-2"); len(other) != 1 {
		t.Fatalf("tenant isolation broken: %+v", other)
	}
	if empty := registry.List("unknown"); empty == nil || len(empty) != 0 {
		t.Fatalf("expected empty non-nil slice for unknown tenant")
	}

	// Latest report replaces the prior ceiling for the same key.
	registry.Report("tenant-1", ConcurrencyView{Target: "chatgpt_web", IdentityRef: "id-1", CurrentCap: 5})
	updated := registry.List("tenant-1")
	if updated[0].CurrentCap != 5 {
		t.Fatalf("expected updated cap 5, got %d", updated[0].CurrentCap)
	}
}

func TestConcurrencyRegistryNilSafe(t *testing.T) {
	var registry *ConcurrencyRegistry
	registry.Report("tenant-1", ConcurrencyView{Target: "x"}) // must not panic
	if views := registry.List("tenant-1"); len(views) != 0 {
		t.Fatalf("expected empty list from nil registry")
	}
}
