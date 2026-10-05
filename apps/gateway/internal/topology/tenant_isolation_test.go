package topology

import (
	"context"
	"testing"
	"time"
)

// TestTopologyCrossTenantReportLeavesOwnerRowIntact: IDs are worker-chosen, so
// tenant B re-reporting tenant A's instance/context/tab IDs must create its own
// rows, never overwrite A's, and a tab pointing at a context B does not own is
// dropped.
func TestTopologyCrossTenantReportLeavesOwnerRowIntact(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	store.AddInstance(BrowserInstance{InstanceID: "inst-1", WorkerID: "wA", TenantID: "tenant-a", Engine: "chromium", State: "ready", RemoteEndpoint: "http://a:9222", CreatedAt: now})
	store.AddContext(ProviderContext{ContextID: "ctx-1", InstanceID: "inst-1", TenantID: "tenant-a", TargetID: "chatgpt_web", IdentityRef: "id-a", LoginState: "authenticated", MaxTabs: 2, CreatedAt: now})
	store.AddTab(BrowserTab{TenantID: "tenant-a", TabID: "tab-1", ContextID: "ctx-1", State: "ready", CreatedAt: now})

	// Tenant B reports the SAME ids (hijack attempt) plus a tab on a context it does not own.
	store.AddInstance(BrowserInstance{InstanceID: "inst-1", WorkerID: "wB", TenantID: "tenant-b", Engine: "firefox", State: "draining", CreatedAt: now})
	store.AddContext(ProviderContext{ContextID: "ctx-1", InstanceID: "inst-1", TenantID: "tenant-b", TargetID: "gemini_web", IdentityRef: "id-b", LoginState: "logged_out", MaxTabs: 1, CreatedAt: now})
	store.AddTab(BrowserTab{TenantID: "tenant-b", TabID: "tab-1", ContextID: "ctx-1", State: "busy", CreatedAt: now})
	store.AddTab(BrowserTab{TenantID: "tenant-b", TabID: "tab-b-only", ContextID: "ctx-not-b", State: "busy", CreatedAt: now})

	a, _ := store.ListInstances(ctx, InstanceFilter{TenantID: "tenant-a"})
	if len(a) != 1 || a[0].WorkerID != "wA" || a[0].State != "ready" || a[0].RemoteEndpoint != "http://a:9222" {
		t.Fatalf("tenant-a instance overwritten: %+v", a)
	}
	ac, _ := store.ListContexts(ctx, ContextFilter{TenantID: "tenant-a"})
	if len(ac) != 1 || ac[0].IdentityRef != "id-a" || ac[0].LoginState != "authenticated" {
		t.Fatalf("tenant-a context overwritten: %+v", ac)
	}
	at, _ := store.ListTabs(ctx, TabFilter{TenantID: "tenant-a"})
	if len(at) != 1 || at[0].State != "ready" {
		t.Fatalf("tenant-a tab overwritten: %+v", at)
	}
	bt, _ := store.ListTabs(ctx, TabFilter{TenantID: "tenant-b"})
	if len(bt) != 1 || bt[0].TabID != "tab-1" || bt[0].State != "busy" {
		t.Fatalf("tenant-b should own exactly its own tab-1 (orphan dropped): %+v", bt)
	}

	// Tenant B has NO context with this id: a tab for it must be dropped.
	store2 := NewMemoryStore()
	store2.AddContext(ProviderContext{ContextID: "ctx-a", InstanceID: "i", TenantID: "tenant-a", TargetID: "t", IdentityRef: "x", CreatedAt: now})
	store2.AddTab(BrowserTab{TenantID: "tenant-b", TabID: "tab-evil", ContextID: "ctx-a", State: "busy", CreatedAt: now})
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		tabs, _ := store2.ListTabs(ctx, TabFilter{TenantID: tenant})
		if len(tabs) != 0 {
			t.Fatalf("cross-tenant tab must be dropped, %s sees %+v", tenant, tabs)
		}
	}
}
