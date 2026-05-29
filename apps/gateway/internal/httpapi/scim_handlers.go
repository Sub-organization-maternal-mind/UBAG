package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/ubag/ubag/apps/gateway/internal/scim"
)

// SCIM is an external provisioning protocol (RFC 7644). It uses its own
// content type and error schema, and clients do NOT send Idempotency-Key
// headers, so the gateway's idempotency requirement is intentionally waived
// for these routes.

func (s *Server) writeSCIM(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", scim.ContentType)
	w.Header().Set("Ubag-Api-Version-Used", s.apiVersion)
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func (s *Server) writeSCIMError(w http.ResponseWriter, err error) {
	if scimErr, ok := scim.AsError(err); ok {
		s.writeSCIM(w, scimErr.StatusCode(), scimErr)
		return
	}
	s.writeSCIM(w, http.StatusInternalServerError, scim.NewError(http.StatusInternalServerError, "", err.Error()))
}

func (s *Server) scimGuard(w http.ResponseWriter, r *http.Request) (string, bool) {
	if s.scim == nil {
		s.writeNotImplemented(w, r, "SCIM provisioning is not configured")
		return "", false
	}
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return "", false
	}
	tenantID, _ := requestScope(r)
	return tenantID, true
}

func (s *Server) scimListParams(r *http.Request) scim.ListParams {
	q := r.URL.Query()
	params := scim.ListParams{Filter: strings.TrimSpace(q.Get("filter"))}
	if v, err := strconv.Atoi(strings.TrimSpace(q.Get("startIndex"))); err == nil {
		params.StartIndex = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(q.Get("count"))); err == nil {
		params.Count = v
	}
	return params
}

func (s *Server) handleSCIMUsers(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.scimGuard(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		resp, err := s.scim.ListUsers(r.Context(), tenantID, s.scimListParams(r))
		if err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusOK, resp)
	case http.MethodPost:
		raw, ok := s.readBody(w, r)
		if !ok {
			return
		}
		var user scim.User
		if err := json.Unmarshal(raw, &user); err != nil {
			s.writeSCIMError(w, scim.NewError(http.StatusBadRequest, "invalidSyntax", "request body must be valid SCIM JSON"))
			return
		}
		created, err := s.scim.CreateUser(r.Context(), tenantID, user)
		if err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusCreated, created)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleSCIMUserByID(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.scimGuard(w, r)
	if !ok {
		return
	}
	tail := splitRouteTail(r.URL.Path, "/v1/scim/v2/Users/")
	if len(tail) != 1 || tail[0] == "" {
		s.writeNotFound(w, r)
		return
	}
	id := tail[0]
	switch r.Method {
	case http.MethodGet:
		user, err := s.scim.GetUser(r.Context(), tenantID, id)
		if err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusOK, user)
	case http.MethodPut:
		raw, ok := s.readBody(w, r)
		if !ok {
			return
		}
		var user scim.User
		if err := json.Unmarshal(raw, &user); err != nil {
			s.writeSCIMError(w, scim.NewError(http.StatusBadRequest, "invalidSyntax", "request body must be valid SCIM JSON"))
			return
		}
		updated, err := s.scim.ReplaceUser(r.Context(), tenantID, id, user)
		if err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusOK, updated)
	case http.MethodPatch:
		raw, ok := s.readBody(w, r)
		if !ok {
			return
		}
		var patch scim.PatchRequest
		if err := json.Unmarshal(raw, &patch); err != nil {
			s.writeSCIMError(w, scim.NewError(http.StatusBadRequest, "invalidSyntax", "request body must be valid SCIM JSON"))
			return
		}
		updated, err := s.scim.PatchUser(r.Context(), tenantID, id, patch.Operations)
		if err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusOK, updated)
	case http.MethodDelete:
		if err := s.scim.DeleteUser(r.Context(), tenantID, id); err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusNoContent, nil)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete)
	}
}

func (s *Server) handleSCIMGroups(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.scimGuard(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		resp, err := s.scim.ListGroups(r.Context(), tenantID, s.scimListParams(r))
		if err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusOK, resp)
	case http.MethodPost:
		raw, ok := s.readBody(w, r)
		if !ok {
			return
		}
		var group scim.Group
		if err := json.Unmarshal(raw, &group); err != nil {
			s.writeSCIMError(w, scim.NewError(http.StatusBadRequest, "invalidSyntax", "request body must be valid SCIM JSON"))
			return
		}
		created, err := s.scim.CreateGroup(r.Context(), tenantID, group)
		if err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusCreated, created)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleSCIMGroupByID(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.scimGuard(w, r)
	if !ok {
		return
	}
	tail := splitRouteTail(r.URL.Path, "/v1/scim/v2/Groups/")
	if len(tail) != 1 || tail[0] == "" {
		s.writeNotFound(w, r)
		return
	}
	id := tail[0]
	switch r.Method {
	case http.MethodGet:
		group, err := s.scim.GetGroup(r.Context(), tenantID, id)
		if err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusOK, group)
	case http.MethodPut:
		raw, ok := s.readBody(w, r)
		if !ok {
			return
		}
		var group scim.Group
		if err := json.Unmarshal(raw, &group); err != nil {
			s.writeSCIMError(w, scim.NewError(http.StatusBadRequest, "invalidSyntax", "request body must be valid SCIM JSON"))
			return
		}
		updated, err := s.scim.ReplaceGroup(r.Context(), tenantID, id, group)
		if err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusOK, updated)
	case http.MethodPatch:
		raw, ok := s.readBody(w, r)
		if !ok {
			return
		}
		var patch scim.PatchRequest
		if err := json.Unmarshal(raw, &patch); err != nil {
			s.writeSCIMError(w, scim.NewError(http.StatusBadRequest, "invalidSyntax", "request body must be valid SCIM JSON"))
			return
		}
		updated, err := s.scim.PatchGroup(r.Context(), tenantID, id, patch.Operations)
		if err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusOK, updated)
	case http.MethodDelete:
		if err := s.scim.DeleteGroup(r.Context(), tenantID, id); err != nil {
			s.writeSCIMError(w, err)
			return
		}
		s.writeSCIM(w, http.StatusNoContent, nil)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete)
	}
}
