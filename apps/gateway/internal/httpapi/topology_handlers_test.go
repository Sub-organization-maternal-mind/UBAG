package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

func seededTopologyStore() *topology.MemoryStore {
	store := topology.NewMemoryStore()
	store.AddInstance(topology.BrowserInstance{InstanceID: "inst-1", WorkerID: "w1", TenantID: defaultTenantID, Engine: "chromium", State: "ready", CreatedAt: time.Now()})
	store.AddContext(topology.ProviderContext{ContextID: "ctx-1", InstanceID: "inst-1", TenantID: defaultTenantID, TargetID: "chatgpt_web", IdentityRef: "id-1", LoginState: "authenticated", ConversationModel: "url", HasStorageState: true, MaxTabs: 2, CreatedAt: time.Now()})
	store.AddTab(topology.BrowserTab{TabID: "tab-1", ContextID: "ctx-1", State: "busy", CreatedAt: time.Now()})
	return store
}

func TestBrowserRoutesReturn501WhenUnconfigured(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
	for _, path := range []string{"/v1/browser/instances", "/v1/browser/contexts", "/v1/browser/tabs", "/v1/browser/summary", "/v1/concurrency"} {
		resp := doJSON(server, http.MethodGet, path, "", authHeaders(""))
		if resp.Code != http.StatusNotImplemented {
			t.Fatalf("GET %s = %d, want 501; body=%s", path, resp.Code, resp.Body.String())
		}
	}
}

func TestBrowserRoutesViewerDenied(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "viewer", Topology: seededTopologyStore(), Concurrency: topology.NewConcurrencyRegistry()}).Handler()
	for _, path := range []string{"/v1/browser/instances", "/v1/browser/contexts", "/v1/browser/tabs", "/v1/browser/summary", "/v1/concurrency"} {
		resp := doJSON(server, http.MethodGet, path, "", authHeaders(""))
		if resp.Code != http.StatusForbidden {
			t.Fatalf("viewer GET %s = %d, want 403; body=%s", path, resp.Code, resp.Body.String())
		}
	}
}

func TestBrowserRoutesDeveloperAllowed(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Topology: seededTopologyStore(), Concurrency: topology.NewConcurrencyRegistry()}).Handler()
	for _, path := range []string{"/v1/browser/instances", "/v1/browser/contexts", "/v1/browser/tabs", "/v1/browser/summary", "/v1/concurrency"} {
		resp := doJSON(server, http.MethodGet, path, "", authHeaders(""))
		if resp.Code != http.StatusOK {
			t.Fatalf("developer GET %s = %d, want 200; body=%s", path, resp.Code, resp.Body.String())
		}
	}
}

func TestBrowserContextsRedactStorageState(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Topology: seededTopologyStore()}).Handler()
	resp := doJSON(server, http.MethodGet, "/v1/browser/contexts", "", authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("contexts = %d; body=%s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("expected 1 context, got %d", len(payload.Data))
	}
	if _, leaked := payload.Data[0]["storage_state_uri"]; leaked {
		t.Fatalf("storage_state_uri must never be exposed: %+v", payload.Data[0])
	}
	if has, ok := payload.Data[0]["has_storage_state"].(bool); !ok || !has {
		t.Fatalf("expected has_storage_state=true, got %+v", payload.Data[0]["has_storage_state"])
	}
}

func TestConcurrencyReturnsReportedView(t *testing.T) {
	registry := topology.NewConcurrencyRegistry()
	registry.Report(defaultTenantID, topology.ConcurrencyView{Target: "chatgpt_web", IdentityRef: "id-1", CurrentCap: 3, Min: 1, Max: 5, InFlight: 2})
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Concurrency: registry}).Handler()

	resp := doJSON(server, http.MethodGet, "/v1/concurrency", "", authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("concurrency = %d; body=%s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0]["target"] != "chatgpt_web" {
		t.Fatalf("unexpected concurrency payload: %+v", payload.Data)
	}
}
