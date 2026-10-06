package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// profileTestServer wires the real memory stores the routes sit on: the
// tenant-owned ProfileStore, the node registry and the audit chain.
type profileTestServer struct {
	handler  http.Handler
	profiles helperauth.ProfileStore
	nodes    *nodes.MemoryStore
	auditLog *audit.MemoryStore
}

func newProfileTestServer(t *testing.T, role string) *profileTestServer {
	t.Helper()
	p := helperauth.NewMemoryProfileStore()
	reg := nodes.NewMemoryStore()
	auditLog := audit.NewMemoryStore()
	server := NewServer(Config{
		AppSecret: "dev-secret", ActorRole: role,
		FleetProfiles: p, FleetNodes: reg, Audit: auditLog,
	})
	return &profileTestServer{handler: server.Handler(), profiles: p, nodes: reg, auditLog: auditLog}
}

func (s *profileTestServer) grantNode(t *testing.T, id string, state string) {
	t.Helper()
	a := nodes.Allocation{
		NodeID: id, Region: "eu-west", Endpoint: "10.8.0.2:7443", URISAN: nodes.NodeURISAN(id),
		CPUMillis: 3000, MemoryBytes: 5 << 30, ReservationState: nodes.ReservationKnown,
		State: state, MaxBrowserWorkloads: 2, ValidUntil: time.Now().Add(24 * time.Hour).UTC(), Generation: 1,
	}
	if err := s.nodes.ApplyAllocation(context.Background(), a, time.Now()); err != nil {
		t.Fatalf("grantNode: %v", err)
	}
}

func bindBody(tenant, provider, identity, node string) string {
	return `{"tenant_id":"` + tenant + `","provider":"` + provider + `","identity_ref":"` + identity + `","node_id":"` + node + `"}`
}

func TestFleetProfileBindListRevokeLifecycle(t *testing.T) {
	ts := newProfileTestServer(t, "superadmin")
	ts.grantNode(t, "helper-1", nodes.StateActive)
	ctx := context.Background()

	// Bind.
	resp := doJSON(ts.handler, http.MethodPost, "/v1/fleet/profiles", bindBody("tenant_a", "chatgpt_web", "slot-1", "helper-1"), authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("bind = %d; body=%s", resp.Code, resp.Body.String())
	}
	var bound struct {
		Kind string `json:"kind"`
		Data struct {
			ProfileRef string `json:"profile_ref"`
			NodeID     string `json:"node_id"`
			State      string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &bound); err != nil {
		t.Fatal(err)
	}
	if bound.Kind != "fleet_profile" || !strings.HasPrefix(bound.Data.ProfileRef, "pr_") || bound.Data.NodeID != "helper-1" || bound.Data.State != helperauth.ProfileActive {
		t.Fatalf("bound = %+v", bound)
	}

	// Bind is idempotent for the same tuple.
	resp = doJSON(ts.handler, http.MethodPost, "/v1/fleet/profiles", bindBody("tenant_a", "chatgpt_web", "slot-1", "helper-1"), authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("rebind = %d; body=%s", resp.Code, resp.Body.String())
	}
	var again struct {
		Data struct {
			ProfileRef string `json:"profile_ref"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.Data.ProfileRef != bound.Data.ProfileRef {
		t.Fatalf("rebind minted %q, want the same %q", again.Data.ProfileRef, bound.Data.ProfileRef)
	}

	// List scoped to the tenant and provider.
	resp = doJSON(ts.handler, http.MethodGet, "/v1/fleet/profiles?tenant_id=tenant_a&provider=chatgpt_web", "", authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("list = %d; body=%s", resp.Code, resp.Body.String())
	}
	var list struct {
		Kind  string           `json:"kind"`
		Total int              `json:"total"`
		Data  []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Kind != "fleet_profiles" || list.Total != 1 || len(list.Data) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// Revoke, then revoke again: idempotent false.
	revoke := "/v1/fleet/profiles/" + bound.Data.ProfileRef + "/revoke"
	resp = doJSON(ts.handler, http.MethodPost, revoke, `{"tenant_id":"tenant_a"}`, authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("revoke = %d; body=%s", resp.Code, resp.Body.String())
	}
	var rev struct {
		Kind    string `json:"kind"`
		Revoked bool   `json:"revoked"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &rev); err != nil {
		t.Fatal(err)
	}
	if rev.Kind != "fleet_profile_revoked" || !rev.Revoked {
		t.Fatalf("revoke body = %+v", rev)
	}
	resp = doJSON(ts.handler, http.MethodPost, revoke, `{"tenant_id":"tenant_a"}`, authHeaders(""))
	if err := json.Unmarshal(resp.Body.Bytes(), &rev); err != nil || rev.Revoked {
		t.Fatalf("second revoke = %d %+v %v", resp.Code, rev, err)
	}

	// The revoked ref no longer lists.
	resp = doJSON(ts.handler, http.MethodGet, "/v1/fleet/profiles?tenant_id=tenant_a", "", authHeaders(""))
	if err := json.Unmarshal(resp.Body.Bytes(), &list); err != nil || list.Total != 0 {
		t.Fatalf("list after revoke = %d %+v %v", resp.Code, list, err)
	}

	// Both audit events are in the tenant's chain.
	for _, want := range []string{"profile.bound", "profile.revoked"} {
		recs, err := ts.auditLog.List(ctx, audit.Filter{TenantID: "tenant_a"})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, rec := range recs {
			if rec.Action == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("audit chain has no %s: %+v", want, recs)
		}
	}
}

func TestFleetProfileBindChecksTheNode(t *testing.T) {
	ts := newProfileTestServer(t, "superadmin")
	ts.grantNode(t, "helper-1", nodes.StateActive)
	ts.grantNode(t, "helper-2", nodes.StateRevoked)

	for _, tc := range []struct {
		name string
		body string
		node string
	}{
		{"unknown node", bindBody("tenant_a", "chatgpt_web", "slot-1", "helper-9"), ""},
		{"revoked node", bindBody("tenant_a", "chatgpt_web", "slot-1", "helper-2"), ""},
		{"missing fields", `{"tenant_id":"tenant_a","provider":"","identity_ref":"s","node_id":"helper-1"}`, ""},
		{"bad node id", bindBody("tenant_a", "chatgpt_web", "slot-1", "../etc"), ""},
	} {
		resp := doJSON(ts.handler, http.MethodPost, "/v1/fleet/profiles", tc.body, authHeaders(""))
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400; body=%s", tc.name, resp.Code, resp.Body.String())
		}
		if !strings.Contains(resp.Body.String(), "UBAG-VALIDATION-") {
			t.Fatalf("%s body has no stable code: %s", tc.name, resp.Body.String())
		}
	}
}

func TestFleetProfileRoutesDenyEveryRoleButSuperadmin(t *testing.T) {
	for _, role := range []string{"viewer", "developer", "operator", "admin", "service"} {
		ts := newProfileTestServer(t, role)
		ts.grantNode(t, "helper-1", nodes.StateActive)
		for _, tc := range []struct {
			method, path, body string
		}{
			{http.MethodGet, "/v1/fleet/profiles?tenant_id=tenant_a", ""},
			{http.MethodPost, "/v1/fleet/profiles", bindBody("tenant_a", "chatgpt_web", "slot-1", "helper-1")},
			{http.MethodPost, "/v1/fleet/profiles/pr_x/revoke", `{"tenant_id":"tenant_a"}`},
		} {
			resp := doJSON(ts.handler, tc.method, tc.path, tc.body, authHeaders(""))
			if resp.Code != http.StatusForbidden {
				t.Fatalf("%s %s %s = %d, want 403; body=%s", role, tc.method, tc.path, resp.Code, resp.Body.String())
			}
		}
	}
}

func TestFleetProfileRoutesWithoutASourceAre501(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "superadmin"}).Handler()
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/v1/fleet/profiles?tenant_id=tenant_a", ""},
		{http.MethodPost, "/v1/fleet/profiles", bindBody("tenant_a", "chatgpt_web", "slot-1", "helper-1")},
		{http.MethodPost, "/v1/fleet/profiles/pr_x/revoke", `{"tenant_id":"tenant_a"}`},
	} {
		resp := doJSON(server, tc.method, tc.path, tc.body, authHeaders(""))
		if resp.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s = %d, want 501; body=%s", tc.method, tc.path, resp.Code, resp.Body.String())
		}
	}
}

func TestFleetProfileListRequiresTenantScope(t *testing.T) {
	ts := newProfileTestServer(t, "superadmin")
	for _, q := range []string{"", "?tenant_id=", "?provider=chatgpt_web"} {
		resp := doJSON(ts.handler, http.MethodGet, "/v1/fleet/profiles"+q, "", authHeaders(""))
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("list%s = %d, want 400; body=%s", q, resp.Code, resp.Body.String())
		}
	}
}
