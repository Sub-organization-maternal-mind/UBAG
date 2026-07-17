package httpapi

import (
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/appjwt"
)

func newAppJWTTestServer(t *testing.T, pub *rsa.PublicKey) http.Handler {
	t.Helper()
	return NewServer(Config{Version: "test", AppSecret: "dev-secret", AppJWTPublicKey: pub}).Handler()
}

func mintAppJWT(t *testing.T, priv *rsa.PrivateKey, tenantID, appID, role string) string {
	t.Helper()
	token, err := appjwt.IssueToken(tenantID, appID, role, time.Minute, priv)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return token
}

func appJWTHeaders(token, idempotencyKey string) map[string]string {
	headers := map[string]string{
		"Authorization":    "Bearer " + token,
		"Ubag-Api-Version": DefaultAPIVersion,
	}
	if idempotencyKey != "" {
		headers["Idempotency-Key"] = idempotencyKey
	}
	return headers
}

func appJWTJobBody(idempotencyKey, appID string) string {
	return `{"api_version":"2026-05-22","idempotency_key":"` + idempotencyKey + `","client":{"app_id":"` + appID + `","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{}}}`
}

type appJWTJobListResponse struct {
	Jobs []struct {
		JobID string `json:"job_id"`
	} `json:"jobs"`
}

func appJWTListedJobIDs(t *testing.T, handler http.Handler, token string) map[string]bool {
	t.Helper()
	resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", appJWTHeaders(token, ""))
	if resp.Code != http.StatusOK {
		t.Fatalf("list jobs = %d; body=%s", resp.Code, resp.Body.String())
	}
	var listing appJWTJobListResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	ids := map[string]bool{}
	for _, job := range listing.Jobs {
		ids[job.JobID] = true
	}
	return ids
}

// TestAppJWTPerClientScoping proves the core multi-client promise: two client
// apps presenting distinct (tid, sub) JWTs get fully isolated job scopes even
// though they hit the same gateway and the same provider target.
func TestAppJWTPerClientScoping(t *testing.T) {
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	handler := newAppJWTTestServer(t, &priv.PublicKey)

	oetToken := mintAppJWT(t, priv, "tenant_oet", "app_oet", "service")
	lawToken := mintAppJWT(t, priv, "tenant_law", "app_law", "service")

	create := func(token, idem, appID string) string {
		resp := doJSON(handler, http.MethodPost, "/v1/jobs", appJWTJobBody(idem, appID), appJWTHeaders(token, idem))
		if resp.Code != http.StatusAccepted {
			t.Fatalf("create job as %s = %d; body=%s", appID, resp.Code, resp.Body.String())
		}
		var created jobResponse
		if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode create response: %v", err)
		}
		return created.JobID
	}

	oetJob := create(oetToken, "appjwt-oet-0000000001", "oet-prep")
	lawJob := create(lawToken, "appjwt-law-0000000001", "law-order")

	oetIDs := appJWTListedJobIDs(t, handler, oetToken)
	if !oetIDs[oetJob] || oetIDs[lawJob] {
		t.Fatalf("oet listing not scoped: %v (want own %s, not %s)", oetIDs, oetJob, lawJob)
	}
	lawIDs := appJWTListedJobIDs(t, handler, lawToken)
	if !lawIDs[lawJob] || lawIDs[oetJob] {
		t.Fatalf("law listing not scoped: %v (want own %s, not %s)", lawIDs, lawJob, oetJob)
	}

	// Cross-tenant GET by ID must read as not-found, not as a leak.
	cross := doJSON(handler, http.MethodGet, "/v1/jobs/"+lawJob, "", appJWTHeaders(oetToken, ""))
	if cross.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant job GET = %d; want 404; body=%s", cross.Code, cross.Body.String())
	}

	// The static app-secret principal lives in its own default tenant and must
	// not see either client's jobs.
	secretResp := doJSON(handler, http.MethodGet, "/v1/jobs", "", authHeaders(""))
	if secretResp.Code != http.StatusOK {
		t.Fatalf("app-secret list = %d; body=%s", secretResp.Code, secretResp.Body.String())
	}
	var secretListing appJWTJobListResponse
	if err := json.Unmarshal(secretResp.Body.Bytes(), &secretListing); err != nil {
		t.Fatalf("decode app-secret listing: %v", err)
	}
	for _, job := range secretListing.Jobs {
		if job.JobID == oetJob || job.JobID == lawJob {
			t.Fatalf("app-secret tenant sees JWT tenant job %s", job.JobID)
		}
	}
}

// TestAppJWTRejectsEmptyIdentityClaims: a correctly signed token whose tid,
// sub, or role is empty/whitespace must not authenticate — an empty tid/sub
// would collapse the caller into a shared ""/"" scope and defeat isolation.
func TestAppJWTRejectsEmptyIdentityClaims(t *testing.T) {
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	handler := newAppJWTTestServer(t, &priv.PublicKey)

	now := time.Now().UTC()
	cases := map[string]appjwt.AppClaims{
		"empty tid":       {TenantID: "", AppID: "app_a", Role: "service", IssuedAt: now.Unix(), Expires: now.Add(time.Minute).Unix()},
		"whitespace tid":  {TenantID: "   ", AppID: "app_a", Role: "service", IssuedAt: now.Unix(), Expires: now.Add(time.Minute).Unix()},
		"empty sub":       {TenantID: "tenant_a", AppID: "", Role: "service", IssuedAt: now.Unix(), Expires: now.Add(time.Minute).Unix()},
		"empty role":      {TenantID: "tenant_a", AppID: "app_a", Role: "", IssuedAt: now.Unix(), Expires: now.Add(time.Minute).Unix()},
		"whitespace role": {TenantID: "tenant_a", AppID: "app_a", Role: " ", IssuedAt: now.Unix(), Expires: now.Add(time.Minute).Unix()},
	}
	for name, claims := range cases {
		token, err := appjwt.Sign(claims, priv)
		if err != nil {
			t.Fatalf("%s: sign: %v", name, err)
		}
		resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", appJWTHeaders(token, ""))
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d; want 401; body=%s", name, resp.Code, resp.Body.String())
		}
	}
}

// TestAppJWTRejectsNonExpiringToken: §11 App JWTs are short-lived by contract;
// exp==0 (never expires) must not authenticate.
func TestAppJWTRejectsNonExpiringToken(t *testing.T) {
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	handler := newAppJWTTestServer(t, &priv.PublicKey)

	claims := appjwt.AppClaims{TenantID: "tenant_a", AppID: "app_a", Role: "service", IssuedAt: time.Now().Unix()}
	token, err := appjwt.Sign(claims, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", appJWTHeaders(token, ""))
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("non-expiring token status = %d; want 401; body=%s", resp.Code, resp.Body.String())
	}
}

// TestAppJWTRejectsPaddedIdentityClaims: identity claims must be exactly their
// trimmed form — the gateway rejects padded values rather than normalizing them
// inside the trust boundary (a token with role " service " is issuer error, not
// something auth should quietly repair).
func TestAppJWTRejectsPaddedIdentityClaims(t *testing.T) {
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	handler := newAppJWTTestServer(t, &priv.PublicKey)

	now := time.Now().UTC()
	cases := map[string]appjwt.AppClaims{
		"padded role": {TenantID: "tenant_a", AppID: "app_a", Role: " service ", IssuedAt: now.Unix(), Expires: now.Add(time.Minute).Unix()},
		"padded tid":  {TenantID: " tenant_a ", AppID: "app_a", Role: "service", IssuedAt: now.Unix(), Expires: now.Add(time.Minute).Unix()},
		"padded sub":  {TenantID: "tenant_a", AppID: "app_a\n", Role: "service", IssuedAt: now.Unix(), Expires: now.Add(time.Minute).Unix()},
	}
	for name, claims := range cases {
		token, err := appjwt.Sign(claims, priv)
		if err != nil {
			t.Fatalf("%s: sign: %v", name, err)
		}
		resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", appJWTHeaders(token, ""))
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d; want 401; body=%s", name, resp.Code, resp.Body.String())
		}
	}
}

// TestAppJWTRejectsFarFutureExpiry: §11 App JWTs are short-lived; the gateway
// caps accepted token lifetime so a leaked long-exp token cannot grant access
// indefinitely (the only other remedy would be rotating the key for everyone).
func TestAppJWTRejectsFarFutureExpiry(t *testing.T) {
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	handler := newAppJWTTestServer(t, &priv.PublicKey)

	now := time.Now().UTC()
	longLived, err := appjwt.Sign(appjwt.AppClaims{TenantID: "tenant_a", AppID: "app_a", Role: "service", IssuedAt: now.Unix(), Expires: now.Add(48 * time.Hour).Unix()}, priv)
	if err != nil {
		t.Fatalf("sign long-lived: %v", err)
	}
	if resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", appJWTHeaders(longLived, "")); resp.Code != http.StatusUnauthorized {
		t.Fatalf("48h token status = %d; want 401; body=%s", resp.Code, resp.Body.String())
	}

	nearExpiry, err := appjwt.Sign(appjwt.AppClaims{TenantID: "tenant_a", AppID: "app_a", Role: "service", IssuedAt: now.Unix(), Expires: now.Add(time.Hour).Unix()}, priv)
	if err != nil {
		t.Fatalf("sign 1h token: %v", err)
	}
	if resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", appJWTHeaders(nearExpiry, "")); resp.Code != http.StatusOK {
		t.Fatalf("1h token status = %d; want 200; body=%s", resp.Code, resp.Body.String())
	}
}

// TestAppJWTIgnoredWhenNotConfigured: a valid App JWT presented to a gateway
// with no public key configured must fall through to the generic 401, never
// authenticate or panic.
func TestAppJWTIgnoredWhenNotConfigured(t *testing.T) {
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	handler := NewServer(Config{Version: "test", AppSecret: "dev-secret"}).Handler()

	token := mintAppJWT(t, priv, "tenant_a", "app_a", "service")
	resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", appJWTHeaders(token, ""))
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("JWT against JWT-disabled gateway = %d; want 401; body=%s", resp.Code, resp.Body.String())
	}
}

// TestAppJWTRejectsExpiredOrForeignSignature guards the existing verification
// behavior end-to-end through the middleware.
func TestAppJWTRejectsExpiredOrForeignSignature(t *testing.T) {
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	foreign, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate foreign key pair: %v", err)
	}
	handler := newAppJWTTestServer(t, &priv.PublicKey)

	now := time.Now().UTC()
	expired, err := appjwt.Sign(appjwt.AppClaims{TenantID: "tenant_a", AppID: "app_a", Role: "service", IssuedAt: now.Add(-2 * time.Minute).Unix(), Expires: now.Add(-time.Minute).Unix()}, priv)
	if err != nil {
		t.Fatalf("sign expired: %v", err)
	}
	if resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", appJWTHeaders(expired, "")); resp.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d; want 401", resp.Code)
	}

	foreignToken := mintAppJWT(t, foreign, "tenant_a", "app_a", "service")
	if resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", appJWTHeaders(foreignToken, "")); resp.Code != http.StatusUnauthorized {
		t.Fatalf("foreign-signature token status = %d; want 401", resp.Code)
	}
}

// TestAppSecretStillAuthenticatesWhenJWTConfigured: enabling App JWT must not
// regress the static app-secret path (both run side by side).
func TestAppSecretStillAuthenticatesWhenJWTConfigured(t *testing.T) {
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	handler := newAppJWTTestServer(t, &priv.PublicKey)

	resp := doJSON(handler, http.MethodGet, "/v1/jobs", "", authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("app-secret list with JWT configured = %d; want 200; body=%s", resp.Code, resp.Body.String())
	}
}
