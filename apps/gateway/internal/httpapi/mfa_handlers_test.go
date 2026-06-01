package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/mfa"
	"github.com/ubag/ubag/apps/gateway/internal/scim"
	"github.com/ubag/ubag/apps/gateway/internal/session"
)

// mfaSessionHeaders returns headers that authenticate as a session-based user.
// The token is placed in the Authorization header (as a bearer), causing the
// withAuth middleware to resolve it as a session principal (SessionBased=true).
func mfaSessionHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization":    "Bearer " + token,
		"Ubag-Api-Version": DefaultAPIVersion,
	}
}

// createTestSession mints a live session in the given store and returns the
// plaintext bearer token.
func createTestSession(t *testing.T, store session.Store, tenantID, role, subject string) string {
	t.Helper()
	now := time.Now().UTC()
	_, token, err := store.Create(context.Background(), session.Session{
		TenantID:  tenantID,
		Role:      role,
		Subject:   subject,
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
	return token
}

// enrollMFA enrolls the given user in MFA and returns the result.
func enrollMFA(t *testing.T, store mfa.Store, tenantID, userID string) mfa.EnrollResult {
	t.Helper()
	result, err := mfa.Enroll(context.Background(), store, mfa.EnrollRequest{
		TenantID: tenantID,
		UserID:   userID,
		Issuer:   "UBAG",
	})
	if err != nil {
		t.Fatalf("enroll mfa: %v", err)
	}
	return result
}

// TestMFAEnrollHandlerOK verifies that POST /v1/mfa/enroll with a session-based
// authenticated request returns 200 with secret, otpauth_uri, and recovery_codes.
func TestMFAEnrollHandlerOK(t *testing.T) {
	sessions := session.NewMemoryStore()
	mfaStore := mfa.NewMemoryStore()
	mfaSvc := &mfa.Service{Store: mfaStore}

	token := createTestSession(t, sessions, defaultTenantID, "service", "user_enroll_test")
	server := NewServer(Config{
		AppSecret: "dev-secret",
		Sessions:  sessions,
		MFA:       mfaSvc,
	}).Handler()

	resp := doJSON(server, http.MethodPost, "/v1/mfa/enroll", `{"issuer":"UBAG"}`, mfaSessionHeaders(token))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	var payload struct {
		Secret        string   `json:"secret"`
		OTPAuthURI    string   `json:"otpauth_uri"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Secret == "" {
		t.Error("expected non-empty secret")
	}
	if payload.OTPAuthURI == "" {
		t.Error("expected non-empty otpauth_uri")
	}
	if len(payload.RecoveryCodes) == 0 {
		t.Error("expected at least one recovery code")
	}
}

// TestMFAVerifyHandlerOK verifies that POST /v1/mfa/verify with a correct TOTP
// code returns 200 with {"verified": true}.
func TestMFAVerifyHandlerOK(t *testing.T) {
	sessions := session.NewMemoryStore()
	mfaStore := mfa.NewMemoryStore()
	fixedTime := time.Unix(1_700_001_000, 0)
	mfaSvc := &mfa.Service{
		Store: mfaStore,
		Clock: func() time.Time { return fixedTime },
	}

	token := createTestSession(t, sessions, defaultTenantID, "service", "user_verify_ok")
	server := NewServer(Config{
		AppSecret: "dev-secret",
		Sessions:  sessions,
		MFA:       mfaSvc,
	}).Handler()

	// Enroll directly via the store so we know the secret.
	enrollment := enrollMFA(t, mfaStore, defaultTenantID, "user_verify_ok")
	code, err := mfa.TOTP(enrollment.Secret, fixedTime)
	if err != nil {
		t.Fatalf("TOTP: %v", err)
	}

	body := `{"code":"` + code + `"}`
	resp := doJSON(server, http.MethodPost, "/v1/mfa/verify", body, mfaSessionHeaders(token))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["verified"] != true {
		t.Errorf("expected verified=true; got %v", payload["verified"])
	}
}

// TestMFAVerifyHandlerWrongCode verifies that POST /v1/mfa/verify with an
// incorrect code returns 401.
func TestMFAVerifyHandlerWrongCode(t *testing.T) {
	sessions := session.NewMemoryStore()
	mfaStore := mfa.NewMemoryStore()
	mfaSvc := &mfa.Service{Store: mfaStore}

	token := createTestSession(t, sessions, defaultTenantID, "service", "user_verify_bad")
	server := NewServer(Config{
		AppSecret: "dev-secret",
		Sessions:  sessions,
		MFA:       mfaSvc,
	}).Handler()

	// Enroll so the user exists, then send the wrong code.
	_ = enrollMFA(t, mfaStore, defaultTenantID, "user_verify_bad")

	resp := doJSON(server, http.MethodPost, "/v1/mfa/verify", `{"code":"000000"}`, mfaSessionHeaders(token))
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusUnauthorized, resp.Body.String())
	}

	var payload errorEnvelope
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "UBAG-MFA-INVALID-CODE-001" {
		t.Errorf("error code = %q, want UBAG-MFA-INVALID-CODE-001", payload.Error.Code)
	}
}

// TestMFAGateBlocksUnverifiedSession verifies that a session-based request to a
// role:manage action with MFA enabled but no prior MFA verification returns 403
// with the UBAG-AUTHZ-MFA-REQUIRED-001 error code.
func TestMFAGateBlocksUnverifiedSession(t *testing.T) {
	sessions := session.NewMemoryStore()
	mfaStore := mfa.NewMemoryStore()
	mfaSvc := &mfa.Service{Store: mfaStore}

	// Create a session with admin role (which has role:manage permission).
	token := createTestSession(t, sessions, defaultTenantID, "admin", "user_mfa_blocked")
	// Provide a real SCIM store so the nil-guard in scimGuard doesn't short-circuit
	// before the MFA gate fires inside authorizeGatewayAction.
	server := NewServer(Config{
		AppSecret: "dev-secret",
		Sessions:  sessions,
		MFA:       mfaSvc,
		SCIM:      scim.NewMemoryStore(),
	}).Handler()

	// SCIM uses role:manage — hit it without MFA verification.
	resp := doJSON(server, http.MethodGet, "/v1/scim/v2/Users", "", mfaSessionHeaders(token))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusForbidden, resp.Body.String())
	}

	var payload errorEnvelope
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "UBAG-AUTHZ-MFA-REQUIRED-001" {
		t.Errorf("error code = %q, want UBAG-AUTHZ-MFA-REQUIRED-001", payload.Error.Code)
	}
}

// TestMFAGateExemptsStaticAPIKey verifies that a static API-key request to a
// role:manage action is NOT blocked by the MFA gate even when MFA is enabled,
// because SessionBased=false for static key principals.
func TestMFAGateExemptsStaticAPIKey(t *testing.T) {
	mfaStore := mfa.NewMemoryStore()
	mfaSvc := &mfa.Service{Store: mfaStore}

	// Server configured with admin role and static API key (AppSecret).
	// The "Bearer dev-secret" path sets SessionBased=false.
	// A real SCIM store is provided so the nil-guard doesn't short-circuit.
	server := NewServer(Config{
		AppSecret: "dev-secret",
		ActorRole: "admin",
		MFA:       mfaSvc,
		SCIM:      scim.NewMemoryStore(),
	}).Handler()

	// admin role has role:manage; static key is exempt from MFA gate.
	// Use a SCIM list route (role:manage) — expect 200 not 403.
	resp := doRaw(server, http.MethodGet, "/v1/scim/v2/Users", "", "application/scim+json",
		map[string]string{
			"Authorization":    "Bearer dev-secret",
			"Ubag-Api-Version": DefaultAPIVersion,
			"Content-Type":     "application/scim+json",
		})

	// The static API key must NOT be blocked by the MFA gate.
	if resp.Code == http.StatusForbidden {
		var payload errorEnvelope
		_ = json.Unmarshal(resp.Body.Bytes(), &payload)
		if strings.Contains(payload.Error.Code, "MFA-REQUIRED") {
			t.Fatalf("static API key must not get MFA-REQUIRED 403; got code=%q body=%s",
				payload.Error.Code, resp.Body.String())
		}
		t.Fatalf("static API key got 403 from MFA gate (role:manage should be exempt); body=%s", resp.Body.String())
	}
	// Expect 200 (SCIM list returns empty result set).
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (scim list ok); body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
}
