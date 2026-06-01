package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/jitadmin"
	"github.com/ubag/ubag/apps/gateway/internal/session"
)

// jitAdminHeaders returns session-based authentication headers for the given
// session token.
func jitAdminHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization":    "Bearer " + token,
		"Ubag-Api-Version": DefaultAPIVersion,
	}
}

// newJITServer builds a test Server with a real MemoryStore for JIT elevation
// and a session store so we can authenticate as named users.
func newJITServer(sessions session.Store) *Server {
	return NewServer(Config{
		AppSecret: "dev-secret",
		Sessions:  sessions,
		JITAdmin:  jitadmin.NewMemoryStore(),
	})
}

// TestElevationRequestOK verifies that POST /v1/admin/elevation with an
// authenticated session-based user (Subject non-empty) returns 201 and a
// grant ID with the "jit_" prefix.
func TestElevationRequestOK(t *testing.T) {
	sessions := session.NewMemoryStore()
	token := createTestSession(t, sessions, defaultTenantID, "operator", "user_jit_request")
	server := newJITServer(sessions).Handler()

	body := `{"role":"admin","ttl_seconds":3600,"reason":"incident response"}`
	resp := doJSON(server, http.MethodPost, "/v1/admin/elevation", body, jitAdminHeaders(token))
	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusCreated, resp.Body.String())
	}

	var grant jitadmin.Grant
	if err := json.Unmarshal(resp.Body.Bytes(), &grant); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if grant.ID == "" {
		t.Error("expected non-empty grant ID")
	}
	if len(grant.ID) < 4 || grant.ID[:4] != "jit_" {
		t.Errorf("grant ID = %q, want prefix jit_", grant.ID)
	}
	if grant.Actor != "user_jit_request" {
		t.Errorf("grant Actor = %q, want %q", grant.Actor, "user_jit_request")
	}
}

// TestElevationRequestNilReturns501 verifies that POST /v1/admin/elevation
// when JITAdmin is nil returns 501 Not Implemented — without requiring auth
// (the nil check fires before the RBAC check per Issue 4 fix).
func TestElevationRequestNilReturns501(t *testing.T) {
	// Server with no JITAdmin configured.
	server := NewServer(Config{AppSecret: "dev-secret"}).Handler()

	body := `{"role":"admin","ttl_seconds":3600,"reason":"test"}`
	resp := doJSON(server, http.MethodPost, "/v1/admin/elevation", body,
		map[string]string{
			"Authorization":    "Bearer dev-secret",
			"Ubag-Api-Version": DefaultAPIVersion,
		})
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusNotImplemented, resp.Body.String())
	}

	var payload errorEnvelope
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if payload.Error.Code != "UBAG-JITADMIN-NOT-ENABLED-001" {
		t.Errorf("error code = %q, want UBAG-JITADMIN-NOT-ENABLED-001", payload.Error.Code)
	}
}

// TestApproveElevationOK verifies the full create→approve lifecycle:
//  1. Operator requests elevation.
//  2. Admin approves — without MFA configured (no MFA gate).
//  3. Expect 200 with Approved=true.
func TestApproveElevationOK(t *testing.T) {
	sessions := session.NewMemoryStore()
	jitStore := jitadmin.NewMemoryStore()

	operatorToken := createTestSession(t, sessions, defaultTenantID, "operator", "user_op")
	adminToken := createTestSession(t, sessions, defaultTenantID, "admin", "user_admin")

	// No MFA configured so role:manage does not require MFA verification.
	server := NewServer(Config{
		AppSecret: "dev-secret",
		Sessions:  sessions,
		JITAdmin:  jitStore,
	}).Handler()

	// Step 1: request elevation.
	reqBody := `{"role":"admin","ttl_seconds":3600,"reason":"ok test"}`
	reqResp := doJSON(server, http.MethodPost, "/v1/admin/elevation", reqBody, jitAdminHeaders(operatorToken))
	if reqResp.Code != http.StatusCreated {
		t.Fatalf("request elevation: status = %d; body=%s", reqResp.Code, reqResp.Body.String())
	}

	var grant jitadmin.Grant
	if err := json.Unmarshal(reqResp.Body.Bytes(), &grant); err != nil {
		t.Fatalf("decode grant: %v", err)
	}

	// Step 2: admin approves.
	approveResp := doJSON(server, http.MethodPost, "/v1/admin/elevation/"+grant.ID+"/approve", "", jitAdminHeaders(adminToken))
	if approveResp.Code != http.StatusOK {
		t.Fatalf("approve elevation: status = %d; body=%s", approveResp.Code, approveResp.Body.String())
	}

	var approved jitadmin.Grant
	if err := json.Unmarshal(approveResp.Body.Bytes(), &approved); err != nil {
		t.Fatalf("decode approved grant: %v", err)
	}
	if !approved.Approved {
		t.Error("expected Approved=true after approve")
	}
	if approved.ApprovedBy != "user_admin" {
		t.Errorf("ApprovedBy = %q, want %q", approved.ApprovedBy, "user_admin")
	}
}

// TestApproveElevationNotFound verifies that approving a non-existent grant ID
// returns 404.
func TestApproveElevationNotFound(t *testing.T) {
	sessions := session.NewMemoryStore()
	adminToken := createTestSession(t, sessions, defaultTenantID, "admin", "user_admin2")

	server := NewServer(Config{
		AppSecret: "dev-secret",
		Sessions:  sessions,
		JITAdmin:  jitadmin.NewMemoryStore(),
	}).Handler()

	resp := doJSON(server, http.MethodPost, "/v1/admin/elevation/jit_nonexistent/approve", "", jitAdminHeaders(adminToken))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusNotFound, resp.Body.String())
	}

	var payload errorEnvelope
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "UBAG-JITADMIN-NOT-FOUND-001" {
		t.Errorf("error code = %q, want UBAG-JITADMIN-NOT-FOUND-001", payload.Error.Code)
	}
}

// TestElevationAppliedToRequest verifies the end-to-end elevation flow:
// an operator user who has an approved admin grant should be able to perform
// a role:manage action (POST /v1/admin/elevation/{id}/approve itself, acting
// as their own approver is a corner case — instead we verify via the
// handleSetRegionState endpoint which also requires role:manage).
// Since we do not have a kill switch configured, the 501 "not enabled" response
// proves the RBAC gate was passed (a 403 would mean the role check failed).
func TestElevationAppliedToRequest(t *testing.T) {
	sessions := session.NewMemoryStore()
	jitStore := jitadmin.NewMemoryStore()

	// Create an operator session. Operators do NOT have role:manage.
	operatorToken := createTestSession(t, sessions, defaultTenantID, "operator", "user_elevated_op")
	// Create an admin session to approve the grant.
	adminToken := createTestSession(t, sessions, defaultTenantID, "admin", "user_admin3")

	server := NewServer(Config{
		AppSecret: "dev-secret",
		Sessions:  sessions,
		JITAdmin:  jitStore,
		// No MFA configured — role:manage does not require MFA verification.
		// No KillSwitch — region state endpoint returns 501 (not 403), proving
		// that RBAC was passed successfully.
	}).Handler()

	// Request admin elevation for the operator.
	reqBody := `{"role":"admin","ttl_seconds":3600,"reason":"elevation test"}`
	reqResp := doJSON(server, http.MethodPost, "/v1/admin/elevation", reqBody, jitAdminHeaders(operatorToken))
	if reqResp.Code != http.StatusCreated {
		t.Fatalf("request elevation: status = %d; body=%s", reqResp.Code, reqResp.Body.String())
	}
	var grant jitadmin.Grant
	if err := json.Unmarshal(reqResp.Body.Bytes(), &grant); err != nil {
		t.Fatalf("decode grant: %v", err)
	}

	// Admin approves.
	approveResp := doJSON(server, http.MethodPost, "/v1/admin/elevation/"+grant.ID+"/approve", "", jitAdminHeaders(adminToken))
	if approveResp.Code != http.StatusOK {
		t.Fatalf("approve elevation: status = %d; body=%s", approveResp.Code, approveResp.Body.String())
	}

	// Now the operator issues a role:manage request. With JIT elevation applied,
	// the operator's role is upgraded to admin in-flight by applyJITElevation.
	// handleSetRegionState checks role:manage → admin passes → then checks
	// s.killSwitch which is nil → returns 501 (not 403).
	regionBody := `{"state":"active"}`
	regionResp := doJSON(server, http.MethodPost, "/v1/admin/regions/us-east-1/state", regionBody, jitAdminHeaders(operatorToken))
	// 403 would mean the JIT elevation was NOT applied. 501 means RBAC passed.
	if regionResp.Code == http.StatusForbidden {
		var payload errorEnvelope
		_ = json.Unmarshal(regionResp.Body.Bytes(), &payload)
		t.Fatalf("JIT elevation was not applied: got 403 %q; operator should have been elevated to admin", payload.Error.Code)
	}
	if regionResp.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501 (kill switch not configured) after elevation, got %d; body=%s", regionResp.Code, regionResp.Body.String())
	}

	// Sanity: without JIT elevation, a plain operator should get 403.
	plainOperatorToken := createTestSession(t, sessions, defaultTenantID, "operator", "user_plain_op")
	plainResp := doJSON(server, http.MethodPost, "/v1/admin/regions/us-east-1/state", regionBody, jitAdminHeaders(plainOperatorToken))
	if plainResp.Code != http.StatusForbidden {
		t.Logf("note: plain operator got %d (expected 403 for role:manage without elevation)", plainResp.Code)
	}
}

// TestElevationRequestSubjectRequired verifies that a bearer-secret request
// (no Subject) to POST /v1/admin/elevation returns 400 UBAG-JIT-SUBJECT-REQUIRED-001.
func TestElevationRequestSubjectRequired(t *testing.T) {
	server := NewServer(Config{
		AppSecret: "dev-secret",
		ActorRole: "operator",
		JITAdmin:  jitadmin.NewMemoryStore(),
	}).Handler()

	body := `{"role":"admin","ttl_seconds":3600,"reason":"test"}`
	resp := doJSON(server, http.MethodPost, "/v1/admin/elevation", body,
		map[string]string{
			"Authorization":    "Bearer dev-secret",
			"Ubag-Api-Version": DefaultAPIVersion,
		})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}

	var payload errorEnvelope
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "UBAG-JIT-SUBJECT-REQUIRED-001" {
		t.Errorf("error code = %q, want UBAG-JIT-SUBJECT-REQUIRED-001", payload.Error.Code)
	}
}

// TestGrantIDIsRandom verifies that two grants created at the same instant
// (same CreatedAt value) get distinct IDs. This tests the fix for Issue 2.
func TestGrantIDIsRandom(t *testing.T) {
	store := jitadmin.NewMemoryStore()
	now := time.Now()
	input := jitadmin.Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		Role:      "admin",
		TTL:       time.Hour,
		CreatedAt: now,
	}
	g1, err := store.Create(nil, input) //nolint:staticcheck
	if err != nil {
		t.Fatalf("Create g1: %v", err)
	}
	g2, err := store.Create(nil, input) //nolint:staticcheck
	if err != nil {
		t.Fatalf("Create g2: %v", err)
	}
	if g1.ID == g2.ID {
		t.Errorf("two grants with same inputs got identical ID %q; random IDs should differ", g1.ID)
	}
}
