package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ubag/ubag/apps/gateway/internal/authz"
	"github.com/ubag/ubag/apps/gateway/internal/pat"
)

// maxPATTTLSseconds caps an explicitly requested PAT lifetime at one year.
const maxPATTTLSseconds = 31536000

type issuePatRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
	AppID    string `json:"app_id,omitempty"`
	Role     string `json:"role,omitempty"`
	// TTLSeconds overrides the server default (0 = server default, -1 = no expiry).
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

type issuePatResponse struct {
	APIVersion string     `json:"api_version"`
	Token      string     `json:"token"`
	TenantID   string     `json:"tenant_id"`
	AppID      string     `json:"app_id"`
	Role       string     `json:"role"`
	IssuedAt   time.Time  `json:"issued_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	TraceID    string     `json:"trace_id"`
}

// handleIssuePAT handles POST /v1/auth/pat. The caller must be authenticated
// with the app secret or another privileged credential. The issued PAT inherits
// the caller's tenant/app scope unless overridden in the request body (admin only).
func (s *Server) handleIssuePAT(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if s.patStore == nil {
		s.writeError(w, r, http.StatusNotImplemented, validationError("UBAG-VALIDATION-PAT-DISABLED-001", "PAT issuance is not enabled on this server"))
		return
	}
	if !s.authorizeGatewayAction(w, r, "auth:pat:issue") {
		return
	}

	principal, hasPrincipal := principalFromContext(r.Context())

	var req issuePatRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JSON-001", "request body must be valid JSON"))
			return
		}
	}

	// Resolve scope: use caller's scope unless a privileged caller provides an
	// explicit override. Issuing a PAT for another tenant/app is what makes PATs
	// usable for per-client identity (one operator mints a scoped token per
	// downstream project), so the override is allowed for admin and superadmin —
	// and only superadmin can actually reach this handler (auth:pat:issue), so
	// without superadmin here no role could both issue and scope a PAT.
	tenantID := s.tenantID
	appID := s.appID
	role := "viewer"
	if hasPrincipal {
		tenantID = principal.TenantID
		appID = principal.AppID
		role = principal.Role
	}
	canOverrideScope := hasPrincipal && (principal.Role == "admin" || principal.Role == "superadmin")
	if req.TenantID != "" && canOverrideScope {
		tenantID = req.TenantID
	}
	if req.AppID != "" && canOverrideScope {
		appID = req.AppID
	}
	if req.Role != "" {
		if !authz.IsValidRole(req.Role) {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PAT-ROLE-001", "role is not a known gateway role"))
			return
		}
		role = req.Role
	}

	// TTL policy: only superadmin may mint a never-expiring PAT (ttl_seconds
	// below 0) — a credential that outlives its operator must not be reachable
	// by an elevated JIT role alone — and every explicit TTL is capped at one
	// year so a leaked PAT cannot silently outlive its rotation cycle.
	if req.TTLSeconds < 0 && (!hasPrincipal || principal.Role != "superadmin") {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PAT-TTL-001", "never-expiring PATs (negative ttl_seconds) require the superadmin role"))
		return
	}
	if req.TTLSeconds > maxPATTTLSseconds {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PAT-TTL-001", "ttl_seconds must not exceed 31536000 (1 year)"))
		return
	}

	ttl := s.patDefaultTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	} else if req.TTLSeconds < 0 {
		ttl = 0 // explicit no-expiry
	}

	token, err := pat.Issue(tenantID, appID, role, ttl)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PAT-001", err.Error()))
		return
	}
	if err := s.patStore.Save(r.Context(), token); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to persist PAT"))
		return
	}

	resp := issuePatResponse{
		APIVersion: s.apiVersion,
		Token:      token.ID,
		TenantID:   token.TenantID,
		AppID:      token.AppID,
		Role:       token.Role,
		IssuedAt:   token.IssuedAt,
		TraceID:    traceIDFromContext(r.Context()),
	}
	if !token.ExpiresAt.IsZero() {
		resp.ExpiresAt = &token.ExpiresAt
	}

	s.writeJSON(w, http.StatusCreated, resp)
}

// handleRevokePAT handles POST /v1/auth/pat/{id}/revoke. It retires an issued
// PAT via the store's Revoke (previously unreachable over HTTP). RBAC:
// auth:pat:issue — the same privileged action that mints PATs (and, when MFA
// is enabled, one that additionally requires a verified MFA session). The
// store is resolved first so revocation stays tenant-scoped: an unknown id and
// a PAT belonging to another tenant both surface as an indistinguishable 404.
// Returns 204 on success.
func (s *Server) handleRevokePAT(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if s.patStore == nil {
		s.writeError(w, r, http.StatusNotImplemented, validationError("UBAG-VALIDATION-PAT-DISABLED-001", "PAT issuance is not enabled on this server"))
		return
	}
	if !s.authorizeGatewayAction(w, r, "auth:pat:issue") {
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PAT-ID-001", "PAT id is required"))
		return
	}

	principal, hasPrincipal := principalFromContext(r.Context())
	token, found, err := s.patStore.Resolve(r.Context(), id, time.Now())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to resolve PAT"))
		return
	}
	if !found || (hasPrincipal && token.TenantID != principal.TenantID) {
		s.writeError(w, r, http.StatusNotFound, validationError("UBAG-VALIDATION-PAT-NOT-FOUND-001", "PAT not found"))
		return
	}

	if err := s.patStore.Revoke(r.Context(), id); err != nil {
		if errors.Is(err, pat.ErrNotConfigured) {
			s.writeError(w, r, http.StatusNotImplemented, validationError("UBAG-VALIDATION-PAT-DISABLED-001", "PAT storage is not enabled on this server"))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to revoke PAT"))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
