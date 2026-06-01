package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/sso"
)

// ---------------------------------------------------------------------------
// Stubs / helpers
// ---------------------------------------------------------------------------

// mockExchanger implements sso.TokenExchanger for tests. It returns the
// configured idToken and accessToken without making any HTTP calls.
type mockExchanger struct {
	idToken     string
	accessToken string
	err         error
}

func (m *mockExchanger) Exchange(_ context.Context, _, _, _, _, _ string) (string, string, error) {
	if m.err != nil {
		return "", "", m.err
	}
	return m.idToken, m.accessToken, nil
}

// ---------------------------------------------------------------------------
// GET /v1/sso/oidc/authorize tests
// ---------------------------------------------------------------------------

func TestOIDCAuthorizeEndpoint501WhenNoAuthFlow(t *testing.T) {
	// SSOAuthFlow is nil: expect 501.
	server := NewServer(Config{AppSecret: "dev-secret", SSO: sso.NewMemoryStore()}).Handler()
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sso/oidc/authorize", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("want 501 when SSOAuthFlow is nil, got %d; body=%s", resp.Code, resp.Body.String())
	}
}

func TestOIDCAuthorizeEndpointMethodNotAllowed(t *testing.T) {
	flow := sso.NewAuthCodeFlow()
	server := NewServer(Config{
		AppSecret:   "dev-secret",
		SSO:         sso.NewMemoryStore(),
		SSOAuthFlow: flow,
	}).Handler()

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sso/oidc/authorize", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405 for POST /authorize, got %d", resp.Code)
	}
}

func TestOIDCAuthorizeEndpoint400WhenNoOIDCConfig(t *testing.T) {
	flow := sso.NewAuthCodeFlow()
	server := NewServer(Config{
		AppSecret:   "dev-secret",
		SSO:         sso.NewMemoryStore(), // empty store — no OIDC config stored
		SSOAuthFlow: flow,
	}).Handler()

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sso/oidc/authorize", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("want 400 when no OIDC config, got %d; body=%s", resp.Code, resp.Body.String())
	}
}

func TestOIDCAuthorizeEndpointRedirects(t *testing.T) {
	// Set up an OIDC config in the store with an AuthorizationURL.
	store := sso.NewMemoryStore()
	if err := store.SetOIDC(context.Background(), "tenant_edge", sso.OIDCConfig{
		Issuer:           "https://idp.example/",
		ClientID:         "test-client",
		AuthorizationURL: "https://idp.example/auth",
	}); err != nil {
		t.Fatalf("SetOIDC: %v", err)
	}

	flow := sso.NewAuthCodeFlow()
	server := NewServer(Config{
		AppSecret:   "dev-secret",
		SSO:         store,
		SSOAuthFlow: flow,
	}).Handler()

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sso/oidc/authorize?redirect_uri=https%3A%2F%2Fapp.example%2Fcb", nil)
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusFound {
		t.Fatalf("want 302, got %d; body=%s", resp.Code, resp.Body.String())
	}
	location := resp.Header().Get("Location")
	if !strings.Contains(location, "https://idp.example/auth") {
		t.Errorf("Location does not start with authorization URL: %q", location)
	}
	if !strings.Contains(location, "response_type=code") {
		t.Errorf("Location missing response_type=code: %q", location)
	}
	if !strings.Contains(location, "client_id=test-client") {
		t.Errorf("Location missing client_id: %q", location)
	}
	if !strings.Contains(location, "state=") {
		t.Errorf("Location missing state: %q", location)
	}
	if !strings.Contains(location, "nonce=") {
		t.Errorf("Location missing nonce: %q", location)
	}
}

// ---------------------------------------------------------------------------
// GET /v1/sso/oidc/callback?code=&state= tests
// ---------------------------------------------------------------------------

func TestOIDCCallbackGetFlow501WhenNoAuthFlow(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", SSO: sso.NewMemoryStore()}).Handler()
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sso/oidc/callback?code=abc&state=xyz", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("want 501 when SSOAuthFlow nil on GET callback, got %d", resp.Code)
	}
}

func TestOIDCCallbackGetFlowMissingParams(t *testing.T) {
	flow := sso.NewAuthCodeFlow()
	server := NewServer(Config{
		AppSecret:   "dev-secret",
		SSO:         sso.NewMemoryStore(),
		SSOAuthFlow: flow,
	}).Handler()

	// Missing state.
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sso/oidc/callback?code=abc", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("missing state: want 400, got %d; body=%s", resp.Code, resp.Body.String())
	}

	// Missing code.
	resp2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/sso/oidc/callback?state=xyz", nil)
	server.ServeHTTP(resp2, req2)
	if resp2.Code != http.StatusBadRequest {
		t.Fatalf("missing code: want 400, got %d; body=%s", resp2.Code, resp2.Body.String())
	}
}

func TestOIDCCallbackStateMismatch(t *testing.T) {
	flow := sso.NewAuthCodeFlow()
	server := NewServer(Config{
		AppSecret:   "dev-secret",
		SSO:         sso.NewMemoryStore(),
		SSOAuthFlow: flow,
	}).Handler()

	// State was never stored — should return 401.
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sso/oidc/callback?code=real-code&state=bad-state", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("bad state: want 401, got %d; body=%s", resp.Code, resp.Body.String())
	}
	var env errorEnvelope
	_ = json.Unmarshal(resp.Body.Bytes(), &env)
	if env.Error.Code != "UBAG-AUTH-SSO-STATE-001" {
		t.Errorf("error code = %q, want UBAG-AUTH-SSO-STATE-001", env.Error.Code)
	}
}

func TestOIDCCallbackGetFlowSuccess(t *testing.T) {
	// Build an OIDC config store with the test key.
	key, pubPEM, _ := testRSAKeypair(t)
	store := sso.NewMemoryStore()
	if err := store.SetOIDC(context.Background(), "tenant_edge", sso.OIDCConfig{
		Issuer:            "https://idp.example/",
		ClientID:          "test-client",
		ClientSecretRef:   "vault://secret/oidc",
		AuthorizationURL:  "https://idp.example/auth",
		TokenURL:          "https://idp.example/token",
		JWKSPublicKeysPEM: []string{pubPEM},
		AllowedAudiences:  []string{"test-client"},
		AttributeMapping: sso.AttributeMapping{
			StaticTenantID: "tenant_edge",
			StaticAppID:    "app_default",
			DefaultRole:    "viewer",
		},
	}); err != nil {
		t.Fatalf("SetOIDC: %v", err)
	}

	// Build a signed ID token.
	now := timeNow()
	rawIDToken := signTestJWT(t, key, map[string]any{"alg": "RS256", "typ": "JWT"}, map[string]any{
		"iss":   "https://idp.example/",
		"aud":   "test-client",
		"sub":   "user-42",
		"email": "user@example.com",
		"iat":   now.Add(-60).Unix(),
		"nbf":   now.Add(-60).Unix(),
		"exp":   now.Add(3600).Unix(),
	})

	// Mock exchanger that returns the pre-built ID token.
	exchanger := &mockExchanger{idToken: rawIDToken}
	memStore := sso.NewMemoryStateStore(0) // default TTL
	flow := sso.NewAuthCodeFlowWithStore(memStore, exchanger)

	// Pre-generate a valid state so the callback can consume it.
	state, err := flow.GenerateState("test-nonce")
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}

	server := NewServer(Config{
		AppSecret:   "dev-secret",
		SSO:         store,
		SSOAuthFlow: flow,
	}).Handler()

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sso/oidc/callback?code=test-code&state="+state, nil)
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", resp.Code, resp.Body.String())
	}
	var response ssoPrincipalResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Subject != "user-42" {
		t.Errorf("subject = %q, want user-42", response.Subject)
	}
	if response.Email != "user@example.com" {
		t.Errorf("email = %q, want user@example.com", response.Email)
	}
}

// ---------------------------------------------------------------------------
// POST /v1/sso/oidc/callback backward compat (existing direct id_token flow)
// ---------------------------------------------------------------------------

func TestOIDCCallbackPostFlowStillWorks(t *testing.T) {
	key, pubPEM, _ := testRSAKeypair(t)
	store := sso.NewMemoryStore()
	if err := store.SetOIDC(context.Background(), "tenant_edge", sso.OIDCConfig{
		Issuer:            "https://idp.example/",
		ClientID:          "test-client",
		JWKSPublicKeysPEM: []string{pubPEM},
		AllowedAudiences:  []string{"test-client"},
		AttributeMapping: sso.AttributeMapping{
			StaticTenantID: "tenant_edge",
			StaticAppID:    "app_default",
			DefaultRole:    "viewer",
		},
	}); err != nil {
		t.Fatalf("SetOIDC: %v", err)
	}

	now := timeNow()
	rawIDToken := signTestJWT(t, key, map[string]any{"alg": "RS256"}, map[string]any{
		"iss":   "https://idp.example/",
		"aud":   "test-client",
		"sub":   "user-post",
		"email": "post@example.com",
		"iat":   now.Add(-60).Unix(),
		"nbf":   now.Add(-60).Unix(),
		"exp":   now.Add(3600).Unix(),
	})

	server := NewServer(Config{
		AppSecret: "dev-secret",
		SSO:       store,
	}).Handler()

	body := `{"api_version":"2026-05-22","id_token":"` + rawIDToken + `"}`
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sso/oidc/callback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer dev-secret")
	req.Header.Set("Ubag-Api-Version", DefaultAPIVersion)
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("POST callback: want 200, got %d; body=%s", resp.Code, resp.Body.String())
	}
	var response ssoPrincipalResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Subject != "user-post" {
		t.Errorf("subject = %q, want user-post", response.Subject)
	}
}

// ---------------------------------------------------------------------------
// Local test helpers (avoid import cycle — duplicate minimal helpers here)
// ---------------------------------------------------------------------------

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"time"
)

func testRSAKeypair(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return key, pubPEM, ""
}

func signTestJWT(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	headerSeg := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsSeg := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerSeg + "." + claimsSeg
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func timeNow() time.Time { return time.Now().UTC() }
