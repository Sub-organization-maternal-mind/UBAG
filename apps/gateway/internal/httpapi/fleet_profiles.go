package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// ProfileBindingSource is the operator write side of the tenant-owned profile
// registry (helperauth.ProfileStore), narrowed to the three calls the routes
// need so tests need no helperauth construction.
type ProfileBindingSource interface {
	Bind(ctx context.Context, tenantID, provider, identityRef, nodeID string, now time.Time) (helperauth.Binding, error)
	List(ctx context.Context, tenantID, provider string) ([]helperauth.Binding, error)
	Revoke(ctx context.Context, tenantID, profileRef string, now time.Time) (bool, error)
}

// ProfileNodeRegistry is the bind-time node check: node_id must name a
// registered node this gateway still holds a grant for (nodes.Store).
type ProfileNodeRegistry interface {
	GetAllocation(ctx context.Context, nodeID string) (nodes.Allocation, error)
}

// handleFleetProfiles serves GET /v1/fleet/profiles (listFleetProfiles) and
// POST /v1/fleet/profiles (bindFleetProfile): the operator route that mints and
// lists tenant profile_ref bindings (P4.16 follow-up). Without it the only way
// to call ProfileStore.Bind was a code-level call, so no profile could ever be
// placed.
//
// Authorization runs before the 501 so a role without fleet:manage (everything
// but superadmin) gets 403 whether or not a fleet exists and learns nothing
// about the deployment. Bind additionally requires a verified MFA session like
// the other privileged actions.
func (s *Server) handleFleetProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !s.authorizeGatewayAction(w, r, "fleet:manage") {
			return
		}
		if s.fleetProfiles == nil {
			s.writeNotImplemented(w, r, "profile bindings are not configured")
			return
		}
		s.writeFleetProfileList(w, r)
	case http.MethodPost:
		if !s.authorizeGatewayAction(w, r, "fleet:manage") {
			return
		}
		if s.fleetProfiles == nil {
			s.writeNotImplemented(w, r, "profile bindings are not configured")
			return
		}
		s.writeFleetProfileBind(w, r)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) writeFleetProfileList(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if !validProfileToken(tenantID) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PROFILE-FIELD-001", "tenant_id is required and must be 1..128 printable characters"))
		return
	}
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider != "" && !validProfileToken(provider) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PROFILE-FIELD-001", "provider must be 1..128 printable characters"))
		return
	}
	list, err := s.fleetProfiles.List(r.Context(), tenantID, provider)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read the profile bindings"))
		return
	}
	if list == nil {
		list = []helperauth.Binding{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"api_version": s.apiVersion,
		"kind":        "fleet_profiles",
		"total":       len(list),
		"data":        fleetProfileBindings(list),
		"trace_id":    traceIDFromContext(r.Context()),
	})
}

func (s *Server) writeFleetProfileBind(w http.ResponseWriter, r *http.Request) {
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var body struct {
		// APIVersion and IdempotencyKey are accepted for parity with the
		// idempotent-mutation convention and ignored: the bind is naturally
		// idempotent and needs no key.
		APIVersion     string `json:"api_version,omitempty"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
		TenantID       string `json:"tenant_id"`
		Provider       string `json:"provider"`
		IdentityRef    string `json:"identity_ref"`
		NodeID         string `json:"node_id"`
	}
	if !s.decodeBody(w, r, raw, &body) {
		return
	}
	tenantID, provider, identityRef, nodeID := strings.TrimSpace(body.TenantID),
		strings.TrimSpace(body.Provider), strings.TrimSpace(body.IdentityRef), strings.TrimSpace(body.NodeID)
	if !validProfileToken(tenantID) || !validProfileToken(provider) || !validProfileToken(identityRef) || !nodes.ValidNodeID(nodeID) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PROFILE-FIELD-001", "tenant_id, provider and identity_ref must be 1..128 printable characters and node_id a valid node identifier"))
		return
	}
	// The bind-time node check the P4.16 follow-up asks for: the node must be
	// registered and not revoked. Draining is accepted (the login can be
	// performed while it drains; placement waits until it is eligible again).
	// Fail closed when no registry is wired: an unverifiable node is refused.
	if s.fleetNodes == nil {
		s.writeError(w, r, http.StatusNotImplemented, validationError("UBAG-VALIDATION-NODE-UNKNOWN-001", "no node registry is configured"))
		return
	}
	a, err := s.fleetNodes.GetAllocation(r.Context(), nodeID)
	if err != nil {
		if errors.Is(err, nodes.ErrNotFound) {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-NODE-UNKNOWN-001", "node_id is not a registered fleet node"))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read the fleet node"))
		return
	}
	if a.State == nodes.StateRevoked {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-NODE-UNKNOWN-001", "node_id names a revoked fleet node"))
		return
	}

	now := time.Now()
	binding, err := s.fleetProfiles.Bind(r.Context(), tenantID, provider, identityRef, nodeID, now)
	if err != nil {
		switch {
		case errors.Is(err, helperauth.ErrInvalidProfile):
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PROFILE-FIELD-001", "profile binding fields are invalid"))
		case errors.Is(err, helperauth.ErrTooManyProfiles):
			s.writeError(w, r, http.StatusConflict, validationError("UBAG-CONFLICT-PROFILE-LIMIT-001", "profile limit reached for the tenant and provider"))
		default:
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to bind the profile"))
		}
		return
	}
	principal, _ := principalFromContext(r.Context())
	actor := principal.Subject
	if actor == "" {
		actor = principal.Role
	}
	_, appID := requestScope(r)
	// The event describes the target tenant's resource, so it goes to the
	// target tenant's chain, where its auditors can see who bound (and later
	// revoked) their profiles. The binding is idempotent, so a failed audit
	// append is retried safely by repeating the request; refusing to
	// acknowledge an unaudited bind keeps every accepted bind in the chain.
	if _, err := s.audit.Append(r.Context(), audit.Record{
		TenantID:   tenantID,
		AppID:      appID,
		Actor:      actor,
		Action:     audit.EventProfileBound,
		Resource:   "profile/" + binding.ProfileRef,
		Outcome:    "success",
		OccurredAt: now,
		Attributes: map[string]any{
			"profile_ref":  binding.ProfileRef,
			"tenant_id":    tenantID,
			"provider":     provider,
			"identity_ref": identityRef,
			"node_id":      nodeID,
		},
	}); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to record the profile.bound audit event; repeat the request (the bind is idempotent)"))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"api_version": s.apiVersion,
		"kind":        "fleet_profile",
		"data":        fleetProfileBinding(binding),
		"trace_id":    traceIDFromContext(r.Context()),
	})
}

// handleFleetProfileRevoke serves POST /v1/fleet/profiles/{profile_ref}/revoke
// (revokeFleetProfile). Idempotent and tenant-scoped: another tenant's ref is
// indistinguishable from an unknown one, both answer revoked=false.
func (s *Server) handleFleetProfileRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if !s.authorizeGatewayAction(w, r, "fleet:manage") {
		return
	}
	if s.fleetProfiles == nil {
		s.writeNotImplemented(w, r, "profile bindings are not configured")
		return
	}
	profileRef := chi.URLParam(r, "profile_ref")
	if !validProfileToken(profileRef) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PROFILE-FIELD-001", "profile_ref is required and must be 1..128 printable characters"))
		return
	}
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var body struct {
		APIVersion     string `json:"api_version,omitempty"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
		TenantID       string `json:"tenant_id"`
	}
	if !s.decodeBody(w, r, raw, &body) {
		return
	}
	tenantID := strings.TrimSpace(body.TenantID)
	if !validProfileToken(tenantID) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PROFILE-FIELD-001", "tenant_id is required and must be 1..128 printable characters"))
		return
	}

	now := time.Now()
	revoked, err := s.fleetProfiles.Revoke(r.Context(), tenantID, profileRef, now)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to revoke the profile binding"))
		return
	}
	principal, _ := principalFromContext(r.Context())
	actor := principal.Subject
	if actor == "" {
		actor = principal.Role
	}
	_, appID := requestScope(r)
	if _, err := s.audit.Append(r.Context(), audit.Record{
		TenantID:   tenantID,
		AppID:      appID,
		Actor:      actor,
		Action:     audit.EventProfileRevoked,
		Resource:   "profile/" + profileRef,
		Outcome:    "success",
		OccurredAt: now,
		Attributes: map[string]any{
			"profile_ref": profileRef,
			"tenant_id":   tenantID,
			"revoked":     revoked,
		},
	}); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to record the profile.revoked audit event"))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"api_version": s.apiVersion,
		"kind":        "fleet_profile_revoked",
		"revoked":     revoked,
		"trace_id":    traceIDFromContext(r.Context()),
	})
}

// validProfileToken mirrors helperauth's own field validation closely enough
// to reject junk at the edge; the store re-validates the canonical form.
func validProfileToken(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

func fleetProfileBindings(in []helperauth.Binding) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, b := range in {
		out = append(out, fleetProfileBinding(b))
	}
	return out
}

func fleetProfileBinding(b helperauth.Binding) map[string]any {
	m := map[string]any{
		"profile_ref":  b.ProfileRef,
		"tenant_id":    b.TenantID,
		"provider":     b.Provider,
		"identity_ref": b.IdentityRef,
		"node_id":      b.NodeID,
		"state":        b.State,
		"created_at":   b.CreatedAt.UTC(),
	}
	if !b.RevokedAt.IsZero() {
		m["revoked_at"] = b.RevokedAt.UTC()
	}
	return m
}
