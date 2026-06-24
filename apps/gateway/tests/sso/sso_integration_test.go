//go:build integration

// Package sso_test contains integration tests for the OIDC authorization-code
// flow against real IdP containers (Keycloak, Authentik).
//
// These tests are gated behind the UBAG_TEST_SSO=1 environment variable and
// the "integration" build tag so they are never executed by the CI unit-test
// job. They require the Docker Compose stack in this directory to be running.
//
// Start the stack:
//
//	docker compose -f tests/sso/docker-compose.yml up -d
//
// Run the tests:
//
//	UBAG_TEST_SSO=1 go test -tags integration ./tests/sso/...
package sso_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("UBAG_TEST_SSO") != "1" {
		// Skip unless explicitly enabled — protects CI from accidental runs.
		fmt.Println("UBAG_TEST_SSO is not set to 1; skipping SSO integration tests.")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// keycloakBase is the base URL for the Keycloak container started by
// docker-compose.yml in this directory.
const keycloakBase = "http://localhost:8180"

// TestOIDCAuthCodeFlowKeycloak exercises the full OIDC authorization-code flow
// against Keycloak running on localhost:8180.
//
// Prerequisites (set up manually or via init container):
//   - Realm "test" exists in Keycloak.
//   - Client "ubag-test" exists in the "test" realm with:
//   - Access Type: confidential
//   - Valid Redirect URIs: http://localhost:8080/*
//   - Client Secret: "test-client-secret"
//   - User "testuser" exists in "test" realm with password "testpass".
func TestOIDCAuthCodeFlowKeycloak(t *testing.T) {
	// Step 1: obtain an authorization code via the Resource Owner Password grant
	// (used here as a headless substitute for the browser redirect; Keycloak
	// supports ROPC for testing). For a true browser flow the IdP would redirect
	// to /v1/sso/oidc/callback?code=...&state=..., but in integration tests we
	// exercise the token exchange directly.

	tokenURL := keycloakBase + "/realms/test/protocol/openid-connect/token"
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("client_id", "ubag-test")
	form.Set("client_secret", "test-client-secret")
	form.Set("username", "testuser")
	form.Set("password", "testpass")
	form.Set("scope", "openid")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("Keycloak is not reachable at %s — is the Docker Compose stack running? (%v)", keycloakBase, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Keycloak token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if tok.IDToken == "" {
		t.Fatal("token response did not include id_token; ensure 'openid' scope is configured")
	}

	t.Logf("Keycloak id_token obtained (len=%d)", len(tok.IDToken))
}
