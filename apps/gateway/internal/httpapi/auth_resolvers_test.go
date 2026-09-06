package httpapi

import (
	"net/http"
	"testing"
)

// TestCredentialChainAcceptsInjectedResolver proves the seam: a new
// credential type is one small adapter function added to the chain — no
// other edits. The demo resolver authenticates end-to-end through withAuth,
// while unknown credentials still get 401.
func TestCredentialChainAcceptsInjectedResolver(t *testing.T) {
	srv := NewServer(Config{
		Version:   "test",
		AppSecret: "dev-secret",
		ActorRole: "service",
		TenantID:  "tenant_root",
		AppID:     "app_root",
	})
	demo := func(r *http.Request) (authenticatedPrincipal, bool) {
		if r.Header.Get("Authorization") == "Bearer demo-test-credential" {
			return authenticatedPrincipal{Role: "viewer", TenantID: "tenant_demo", AppID: "app_demo"}, true
		}
		return authenticatedPrincipal{}, false
	}
	srv.credentialResolvers = append(
		[]func(*http.Request) (authenticatedPrincipal, bool){demo},
		srv.credentialResolvers...,
	)
	handler := srv.Handler()

	demoHeaders := map[string]string{
		"Authorization":    "Bearer demo-test-credential",
		"Ubag-Api-Version": DefaultAPIVersion,
	}
	if resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", demoHeaders); resp.Code != http.StatusOK {
		t.Fatalf("demo credential list jobs = %d; want 200; body=%s", resp.Code, resp.Body.String())
	}

	badHeaders := map[string]string{
		"Authorization":    "Bearer nope",
		"Ubag-Api-Version": DefaultAPIVersion,
	}
	if resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", badHeaders); resp.Code != http.StatusUnauthorized {
		t.Fatalf("unknown credential = %d; want 401", resp.Code)
	}
}
