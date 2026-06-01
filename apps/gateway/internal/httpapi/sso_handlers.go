package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/session"
	"github.com/ubag/ubag/apps/gateway/internal/sso"
)

type ssoConfigRequest struct {
	APIVersion string          `json:"api_version,omitempty"`
	Type       string          `json:"type"`
	OIDC       *sso.OIDCConfig `json:"oidc,omitempty"`
	SAML       *sso.SAMLConfig `json:"saml,omitempty"`
}

type ssoConfigResponse struct {
	APIVersion string           `json:"api_version"`
	TenantID   string           `json:"tenant_id"`
	OIDC       []sso.OIDCConfig `json:"oidc"`
	SAML       []sso.SAMLConfig `json:"saml"`
	TraceID    string           `json:"trace_id"`
}

type ssoCallbackRequest struct {
	APIVersion string `json:"api_version,omitempty"`
	IDToken    string `json:"id_token,omitempty"`
	Assertion  string `json:"assertion,omitempty"`
}

type ssoPrincipalResponse struct {
	APIVersion       string `json:"api_version"`
	TenantID         string `json:"tenant_id"`
	AppID            string `json:"app_id"`
	Role             string `json:"role"`
	Subject          string `json:"subject"`
	Email            string `json:"email,omitempty"`
	SessionToken     string `json:"session_token,omitempty"`
	SessionExpiresAt string `json:"session_expires_at,omitempty"`
	TraceID          string `json:"trace_id"`
}

type ssoLogoutResponse struct {
	APIVersion string `json:"api_version"`
	Revoked    bool   `json:"revoked"`
	TraceID    string `json:"trace_id"`
}

func (s *Server) handleSSOConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getSSOConfig(w, r)
	case http.MethodPut:
		s.putSSOConfig(w, r)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPut)
	}
}

func (s *Server) getSSOConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return
	}
	if s.sso == nil {
		s.writeNotImplemented(w, r, "single sign-on is not configured")
		return
	}
	tenantID, _ := requestScope(r)
	oidc := make([]sso.OIDCConfig, 0)
	saml := make([]sso.SAMLConfig, 0)
	if cfg, found, err := s.sso.GetOIDC(r.Context(), tenantID); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read OIDC configuration"))
		return
	} else if found {
		oidc = append(oidc, cfg)
	}
	if cfg, found, err := s.sso.GetSAML(r.Context(), tenantID); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read SAML configuration"))
		return
	} else if found {
		saml = append(saml, cfg)
	}
	s.writeJSON(w, http.StatusOK, ssoConfigResponse{
		APIVersion: s.apiVersion,
		TenantID:   tenantID,
		OIDC:       oidc,
		SAML:       saml,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

func (s *Server) putSSOConfig(w http.ResponseWriter, r *http.Request) {
	if s.sso == nil {
		s.writeNotImplemented(w, r, "single sign-on is not configured")
		return
	}
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var request ssoConfigRequest
	if !s.decodeBody(w, r, raw, &request) {
		return
	}
	apiVersion, ok := s.resolveAPIVersion(w, r, request.APIVersion)
	if !ok {
		return
	}
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return
	}
	tenantID, _ := requestScope(r)

	// SSO configurations carry secret references (ClientSecretRef) and IdP PEM
	// public keys, never plaintext secrets, so payloadpolicy.Validate is not
	// applied here.
	switch strings.ToLower(strings.TrimSpace(request.Type)) {
	case "oidc":
		if request.OIDC == nil {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-OIDC-001", "oidc configuration is required when type=oidc"))
			return
		}
		// Defense in depth: an OIDC config without an issuer cannot bind a
		// token to a trusted IdP, so reject it at the configuration boundary.
		if strings.TrimSpace(request.OIDC.Issuer) == "" {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-OIDC-ISSUER-001", "oidc.Issuer is required"))
			return
		}
		if err := s.sso.SetOIDC(r.Context(), tenantID, *request.OIDC); err != nil {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-OIDC-002", err.Error()))
			return
		}
	case "saml":
		if request.SAML == nil {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-SAML-001", "saml configuration is required when type=saml"))
			return
		}
		// Defense in depth: a SAML config without an IdP signing certificate
		// cannot verify assertion signatures, so reject it up front.
		if strings.TrimSpace(request.SAML.IdPCertPEM) == "" {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-SAML-CERT-001", "saml.IdPCertPEM is required"))
			return
		}
		if err := s.sso.SetSAML(r.Context(), tenantID, *request.SAML); err != nil {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-SAML-002", err.Error()))
			return
		}
	default:
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-TYPE-001", "type must be oidc or saml"))
		return
	}

	s.getSSOConfigWithVersion(w, r, apiVersion)
}

func (s *Server) getSSOConfigWithVersion(w http.ResponseWriter, r *http.Request, apiVersion string) {
	tenantID, _ := requestScope(r)
	oidc := make([]sso.OIDCConfig, 0)
	saml := make([]sso.SAMLConfig, 0)
	if cfg, found, err := s.sso.GetOIDC(r.Context(), tenantID); err == nil && found {
		oidc = append(oidc, cfg)
	}
	if cfg, found, err := s.sso.GetSAML(r.Context(), tenantID); err == nil && found {
		saml = append(saml, cfg)
	}
	s.writeJSON(w, http.StatusOK, ssoConfigResponse{
		APIVersion: apiVersion,
		TenantID:   tenantID,
		OIDC:       oidc,
		SAML:       saml,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

// handleSSOOIDCAuthorize initiates the OIDC authorization-code flow.
// It generates a cryptographically random state+nonce pair, stores state in
// the AuthCodeFlow StateStore (TTL 10 min), and redirects the browser to the
// configured IdP authorization URL.
//
// Returns 501 when SSOAuthFlow is not configured.
// Returns 400 when OIDC is not configured for the tenant.
func (s *Server) handleSSOOIDCAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if s.ssoAuthFlow == nil {
		s.writeNotImplemented(w, r, "OIDC authorization-code flow is not configured")
		return
	}
	if s.sso == nil {
		s.writeNotImplemented(w, r, "single sign-on is not configured")
		return
	}

	tenantID, _ := requestScope(r)
	cfg, found, err := s.sso.GetOIDC(r.Context(), tenantID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read OIDC configuration"))
		return
	}
	if !found {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-OIDC-UNCONFIGURED-001", "no OIDC configuration for tenant"))
		return
	}
	if cfg.AuthorizationURL == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-OIDC-AUTHURL-001", "OIDC configuration is missing authorization_url"))
		return
	}

	redirectURI := strings.TrimSpace(r.URL.Query().Get("redirect_uri"))

	// Generate a random nonce for the ID token nonce claim.
	nonceBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, nonceBytes); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to generate nonce"))
		return
	}
	nonce := hex.EncodeToString(nonceBytes)

	state, err := s.ssoAuthFlow.GenerateState(nonce)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to generate state"))
		return
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", cfg.ClientID)
	params.Set("scope", "openid")
	params.Set("state", state)
	params.Set("nonce", nonce)
	if redirectURI != "" {
		params.Set("redirect_uri", redirectURI)
	}

	authURL := cfg.AuthorizationURL
	if strings.Contains(authURL, "?") {
		authURL += "&" + params.Encode()
	} else {
		authURL += "?" + params.Encode()
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleSSOOIDCCallback handles both the authorization-code callback (GET with
// code+state) and the legacy direct id_token verification (POST). The POST
// path is preserved for backward compatibility.
//
// GET flow (authorization-code):
//  1. Validate code+state query params.
//  2. Consume state from AuthCodeFlow.StateStore (verifies not replayed/expired).
//  3. Exchange code for tokens via cfg.TokenURL.
//  4. Verify the returned id_token using the existing RS256/JWKS verifier.
//  5. Map claims to a UBAG principal and mint a session.
//
// POST flow (legacy direct id_token):
//   - Unchanged from the original implementation.
func (s *Server) handleSSOOIDCCallback(w http.ResponseWriter, r *http.Request) {
	// Route by method: GET = authorization-code flow, POST = direct token flow.
	switch r.Method {
	case http.MethodGet:
		s.handleSSOOIDCCallbackGET(w, r)
	case http.MethodPost:
		s.handleSSOOIDCCallbackPOST(w, r)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

// handleSSOOIDCCallbackGET implements the authorization-code callback path.
func (s *Server) handleSSOOIDCCallbackGET(w http.ResponseWriter, r *http.Request) {
	if s.ssoAuthFlow == nil {
		s.writeNotImplemented(w, r, "OIDC authorization-code flow is not configured")
		return
	}
	if s.sso == nil {
		s.writeNotImplemented(w, r, "single sign-on is not configured")
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-AUTHCODE-001", "code and state are required"))
		return
	}

	_, err := s.ssoAuthFlow.ValidateCallback(state, code)
	if err != nil {
		s.writeError(w, r, http.StatusUnauthorized, authError("UBAG-AUTH-SSO-STATE-001", "invalid or expired state"))
		return
	}

	tenantID, appID := requestScope(r)
	cfg, found, cfgErr := s.sso.GetOIDC(r.Context(), tenantID)
	if cfgErr != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read OIDC configuration"))
		return
	}
	if !found {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-OIDC-UNCONFIGURED-001", "no OIDC configuration for tenant"))
		return
	}

	redirectURI := r.URL.Query().Get("redirect_uri")
	// NOTE: In production, cfg.ClientSecretRef would be resolved via a secret
	// store (e.g., Vault). For now we use it directly per the task spec.
	clientSecret := cfg.ClientSecretRef

	idTokenRaw, _, exchangeErr := s.ssoAuthFlow.Exchanger.Exchange(
		r.Context(), cfg.TokenURL, code, cfg.ClientID, clientSecret, redirectURI,
	)
	if exchangeErr != nil {
		s.writeError(w, r, http.StatusUnauthorized, authError("UBAG-AUTH-SSO-CODE-001", "code exchange failed"))
		return
	}

	claims, verifyErr := sso.VerifyIDToken(r.Context(), idTokenRaw, cfg, time.Now().UTC())
	if verifyErr != nil {
		s.writeError(w, r, http.StatusUnauthorized, authError("UBAG-AUTH-SSO-OIDC-001", "id_token verification failed"))
		return
	}

	principal, mapErr := sso.MapPrincipal(claims.Attributes(), cfg.AttributeMapping)
	if mapErr != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-PRINCIPAL-001", mapErr.Error()))
		return
	}

	s.writeSSOPrincipal(w, r, s.apiVersion, tenantID, appID, principal)
}

// handleSSOOIDCCallbackPOST is the original direct id_token verification path.
// The id_token is the verification input, so payloadpolicy.Validate (which
// rejects an "id_token" key) is intentionally NOT applied to this body.
func (s *Server) handleSSOOIDCCallbackPOST(w http.ResponseWriter, r *http.Request) {
	if s.sso == nil {
		s.writeNotImplemented(w, r, "single sign-on is not configured")
		return
	}
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var request ssoCallbackRequest
	if !s.decodeBody(w, r, raw, &request) {
		return
	}
	apiVersion, ok := s.resolveAPIVersion(w, r, request.APIVersion)
	if !ok {
		return
	}
	if strings.TrimSpace(request.IDToken) == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-ID-TOKEN-001", "id_token is required"))
		return
	}
	tenantID, appID := requestScope(r)
	cfg, found, err := s.sso.GetOIDC(r.Context(), tenantID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read OIDC configuration"))
		return
	}
	if !found {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-OIDC-UNCONFIGURED-001", "no OIDC configuration for tenant"))
		return
	}
	claims, err := sso.VerifyIDToken(r.Context(), request.IDToken, cfg, time.Now().UTC())
	if err != nil {
		s.writeError(w, r, http.StatusUnauthorized, authError("UBAG-AUTH-SSO-OIDC-001", "id_token verification failed"))
		return
	}
	principal, err := sso.MapPrincipal(claims.Attributes(), cfg.AttributeMapping)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-PRINCIPAL-001", err.Error()))
		return
	}
	s.writeSSOPrincipal(w, r, apiVersion, tenantID, appID, principal)
}

func (s *Server) handleSSOSAMLACS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if s.sso == nil {
		s.writeNotImplemented(w, r, "single sign-on is not configured")
		return
	}
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var request ssoCallbackRequest
	if !s.decodeBody(w, r, raw, &request) {
		return
	}
	apiVersion, ok := s.resolveAPIVersion(w, r, request.APIVersion)
	if !ok {
		return
	}
	if strings.TrimSpace(request.Assertion) == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-ASSERTION-001", "assertion is required"))
		return
	}
	tenantID, appID := requestScope(r)
	cfg, found, err := s.sso.GetSAML(r.Context(), tenantID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read SAML configuration"))
		return
	}
	if !found {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-SAML-UNCONFIGURED-001", "no SAML configuration for tenant"))
		return
	}
	assertion, err := sso.ParseAndVerifyAssertion(r.Context(), []byte(request.Assertion), cfg, time.Now().UTC())
	if err != nil {
		s.writeError(w, r, http.StatusUnauthorized, authError("UBAG-AUTH-SSO-SAML-001", "SAML assertion verification failed"))
		return
	}
	principal, err := sso.MapPrincipal(assertion.Attributes, cfg.AttributeMapping)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SSO-PRINCIPAL-001", err.Error()))
		return
	}
	s.writeSSOPrincipal(w, r, apiVersion, tenantID, appID, principal)
}

func (s *Server) writeSSOPrincipal(w http.ResponseWriter, r *http.Request, apiVersion, tenantID, appID string, principal sso.Principal) {
	resolvedTenant := firstNonEmpty(principal.TenantID, tenantID)
	resolvedApp := firstNonEmpty(principal.AppID, appID)

	response := ssoPrincipalResponse{
		APIVersion: apiVersion,
		TenantID:   resolvedTenant,
		AppID:      resolvedApp,
		Role:       principal.Role,
		Subject:    principal.Subject,
		Email:      principal.Email,
		TraceID:    traceIDFromContext(r.Context()),
	}

	// Mint a server-side session for the verified principal when sessions are
	// enabled. This is additive: it does not affect the static app-secret path.
	if s.sessions != nil {
		now := time.Now().UTC()
		ttl := s.sessionTTL
		if ttl <= 0 {
			ttl = time.Hour
		}
		sess, token, err := s.sessions.Create(r.Context(), session.Session{
			TenantID:  resolvedTenant,
			AppID:     resolvedApp,
			Role:      principal.Role,
			Subject:   principal.Subject,
			Email:     principal.Email,
			IssuedAt:  now,
			ExpiresAt: now.Add(ttl),
		})
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to mint session"))
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    token,
			Path:     "/",
			Expires:  sess.ExpiresAt,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
		response.SessionToken = token
		response.SessionExpiresAt = sess.ExpiresAt.Format(time.RFC3339)
		s.emitSessionAudit(r, resolvedTenant, resolvedApp, principal.Role, principal.Subject, "session:mint", "allow", sess.ID)
	}

	s.writeJSON(w, http.StatusOK, response)
}

// handleSSOLogout revokes the session presented via the session cookie or
// bearer token and clears the session cookie. It is idempotent: revoking an
// unknown or already-revoked session returns revoked=false with HTTP 200.
func (s *Server) handleSSOLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if s.sessions == nil {
		s.writeNotImplemented(w, r, "session management is not configured")
		return
	}

	revoked := false
	if token := sessionTokenFromRequest(r); token != "" {
		ok, err := s.sessions.Revoke(r.Context(), token, time.Now())
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to revoke session"))
			return
		}
		revoked = ok
	}

	// Clear the cookie regardless of whether a session was found.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	if revoked {
		if principal, ok := principalFromContext(r.Context()); ok {
			s.emitSessionAudit(r, principal.TenantID, principal.AppID, principal.Role, principal.Subject, "session:logout", "allow", "")
		}
	}

	s.writeJSON(w, http.StatusOK, ssoLogoutResponse{
		APIVersion: s.apiVersion,
		Revoked:    revoked,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

// emitSessionAudit appends a best-effort, nil-safe audit record for a session
// lifecycle event (mint or logout). Store errors are intentionally ignored.
func (s *Server) emitSessionAudit(r *http.Request, tenantID, appID, role, subject, action, outcome, sessionID string) {
	if s == nil || s.audit == nil {
		return
	}
	actor := subject
	if actor == "" {
		actor = role
	}
	attributes := map[string]any{"role": role}
	if sessionID != "" {
		attributes["session_id"] = sessionID
	}
	_, _ = s.audit.Append(r.Context(), audit.Record{
		TenantID:   tenantID,
		AppID:      appID,
		Actor:      actor,
		Action:     action,
		Resource:   r.URL.Path,
		Outcome:    outcome,
		OccurredAt: time.Now(),
		Attributes: attributes,
	})
}
