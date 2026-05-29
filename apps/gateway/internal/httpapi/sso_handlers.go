package httpapi

import (
	"net/http"
	"strings"
	"time"

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
	APIVersion string `json:"api_version"`
	TenantID   string `json:"tenant_id"`
	AppID      string `json:"app_id"`
	Role       string `json:"role"`
	Subject    string `json:"subject"`
	Email      string `json:"email,omitempty"`
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

// handleSSOOIDCCallback verifies an OIDC id_token and resolves a principal.
// The id_token is the verification input, so payloadpolicy.Validate (which
// rejects an "id_token" key) is intentionally NOT applied to this body.
func (s *Server) handleSSOOIDCCallback(w http.ResponseWriter, r *http.Request) {
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
	// NOTE: session/token minting from the verified principal is a documented
	// follow-up; this endpoint currently returns the resolved principal only.
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
	s.writeJSON(w, http.StatusOK, ssoPrincipalResponse{
		APIVersion: apiVersion,
		TenantID:   resolvedTenant,
		AppID:      resolvedApp,
		Role:       principal.Role,
		Subject:    principal.Subject,
		Email:      principal.Email,
		TraceID:    traceIDFromContext(r.Context()),
	})
}
