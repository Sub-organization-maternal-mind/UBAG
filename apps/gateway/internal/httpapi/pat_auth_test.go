package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/pat"
)

// newPATServer builds a server with PAT issuance enabled (memory store) and the
// app-secret principal bound to the given role, mirroring how serve.go wires the
// store once UBAG_PAT_ENABLED is set.
func newPATServer(t *testing.T, actorRole string) http.Handler {
	t.Helper()
	return NewServer(Config{
		Version:   "test",
		AppSecret: "dev-secret",
		ActorRole: actorRole,
		TenantID:  "tenant_root",
		AppID:     "app_root",
		PAT:       pat.NewMemoryStore(),
	}).Handler()
}

func issuePAT(t *testing.T, handler http.Handler, body string) (*issuePatResponse, int) {
	t.Helper()
	headers := map[string]string{
		"Authorization":    "Bearer dev-secret",
		"Ubag-Api-Version": DefaultAPIVersion,
	}
	resp := doRaw(handler, http.MethodPost, "/v1/auth/pat", body, "application/json", headers)
	if resp.Code != http.StatusCreated {
		return nil, resp.Code
	}
	var issued issuePatResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	return &issued, resp.Code
}

func patAuthHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization":    "Bearer " + token,
		"Ubag-Api-Version": DefaultAPIVersion,
	}
}

// TestPATIssueThenAuthenticate: superadmin issues a PAT, and that opaque
// ubag_pat_ token then authenticates a request, scoped to the token's tenant.
func TestPATIssueThenAuthenticate(t *testing.T) {
	handler := newPATServer(t, "superadmin")

	issued, code := issuePAT(t, handler, `{"tenant_id":"tenant_oet","app_id":"oet-prep","role":"service"}`)
	if issued == nil {
		t.Fatalf("issue PAT failed with status %d", code)
	}
	if !strings.HasPrefix(issued.Token, "ubag_pat_") {
		t.Fatalf("issued token has wrong prefix: %q", issued.Token)
	}
	if issued.TenantID != "tenant_oet" || issued.AppID != "oet-prep" || issued.Role != "service" {
		t.Fatalf("issued token scope wrong: %+v", issued)
	}

	// The PAT authenticates a normal request.
	resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", patAuthHeaders(issued.Token))
	if resp.Code != http.StatusOK {
		t.Fatalf("list jobs with PAT = %d; want 200; body=%s", resp.Code, resp.Body.String())
	}
}

// TestPATScopesJobsToItsTenant: jobs created under one PAT are invisible to a
// PAT scoped to a different tenant.
func TestPATScopesJobsToItsTenant(t *testing.T) {
	handler := newPATServer(t, "superadmin")

	oet, _ := issuePAT(t, handler, `{"tenant_id":"tenant_oet","app_id":"oet-prep","role":"service"}`)
	law, _ := issuePAT(t, handler, `{"tenant_id":"tenant_law","app_id":"law-order","role":"service"}`)
	if oet == nil || law == nil {
		t.Fatal("issuing PATs failed")
	}

	body := `{"api_version":"2026-05-22","idempotency_key":"pat-oet-000000001","client":{"app_id":"oet-prep","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{}}}`
	headers := patAuthHeaders(oet.Token)
	headers["Idempotency-Key"] = "pat-oet-000000001"
	create := doJSON(handler, http.MethodPost, "/v1/jobs", body, headers)
	if create.Code != http.StatusAccepted {
		t.Fatalf("create job with OET PAT = %d; body=%s", create.Code, create.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	// The law-tenant PAT must not see the OET job.
	list := doJSON(handler, http.MethodGet, "/v1/jobs", "", patAuthHeaders(law.Token))
	if list.Code != http.StatusOK {
		t.Fatalf("law list = %d", list.Code)
	}
	if strings.Contains(list.Body.String(), created.JobID) {
		t.Fatalf("law-tenant PAT sees OET job %s: %s", created.JobID, list.Body.String())
	}

	// Cross-tenant GET by ID reads as not-found.
	cross := doJSON(handler, http.MethodGet, "/v1/jobs/"+created.JobID, "", patAuthHeaders(law.Token))
	if cross.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant PAT job GET = %d; want 404", cross.Code)
	}
}

// TestPATIssuanceRequiresSuperadmin: a non-superadmin app-secret principal is
// forbidden from issuing PATs.
func TestPATIssuanceRequiresSuperadmin(t *testing.T) {
	for _, role := range []string{"service", "developer", "operator", "admin", "viewer"} {
		handler := newPATServer(t, role)
		_, code := issuePAT(t, handler, `{"tenant_id":"tenant_a","app_id":"app_a","role":"service"}`)
		if code != http.StatusForbidden {
			t.Fatalf("role %q issuing PAT = %d; want 403", role, code)
		}
	}
}

// TestPATDisabledReturns501: with no store configured (feature off), issuance
// returns 501 and a ubag_pat_ bearer does not authenticate.
func TestPATDisabledReturns501(t *testing.T) {
	handler := NewServer(Config{Version: "test", AppSecret: "dev-secret", ActorRole: "superadmin"}).Handler()

	headers := map[string]string{"Authorization": "Bearer dev-secret", "Ubag-Api-Version": DefaultAPIVersion}
	resp := doRaw(handler, http.MethodPost, "/v1/auth/pat", `{"tenant_id":"t","app_id":"a","role":"service"}`, "application/json", headers)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("issue with PAT disabled = %d; want 501; body=%s", resp.Code, resp.Body.String())
	}

	// A well-formed but unknown PAT is rejected (no store to resolve it).
	unknown := doJSON(handler, http.MethodGet, "/v1/jobs", "", patAuthHeaders("ubag_pat_deadbeef"))
	if unknown.Code != http.StatusUnauthorized {
		t.Fatalf("unknown PAT against disabled gateway = %d; want 401", unknown.Code)
	}
}

// TestPATRevokedTokenRejected: a revoked token no longer authenticates.
func TestPATRevokedTokenRejected(t *testing.T) {
	store := pat.NewMemoryStore()
	handler := NewServer(Config{
		Version: "test", AppSecret: "dev-secret", ActorRole: "superadmin",
		TenantID: "tenant_root", AppID: "app_root", PAT: store,
	}).Handler()

	issued, code := issuePAT(t, handler, `{"tenant_id":"tenant_a","app_id":"app_a","role":"service"}`)
	if issued == nil {
		t.Fatalf("issue failed: %d", code)
	}
	if resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", patAuthHeaders(issued.Token)); resp.Code != http.StatusOK {
		t.Fatalf("pre-revoke list = %d; want 200", resp.Code)
	}
	if err := store.Revoke(t.Context(), issued.Token); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", patAuthHeaders(issued.Token)); resp.Code != http.StatusUnauthorized {
		t.Fatalf("post-revoke list = %d; want 401", resp.Code)
	}
}
