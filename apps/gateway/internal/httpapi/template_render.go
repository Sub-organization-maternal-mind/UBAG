package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ubag/ubag/apps/gateway/internal/templates"
)

type renderTemplateRequest struct {
	Vars map[string]any `json:"vars"`
}

type renderTemplateResponse struct {
	APIVersion string `json:"api_version"`
	TemplateID string `json:"template_id"`
	Rendered   string `json:"rendered"`
	TraceID    string `json:"trace_id"`
}

// handleTemplateRender dispatches /v1/templates/{id}/render and /v1/templates/{id}.
func (s *Server) handleTemplateRender(w http.ResponseWriter, r *http.Request) {
	// Extract the path segment after /v1/templates/
	tail := strings.TrimPrefix(r.URL.Path, "/v1/templates/")
	parts := strings.SplitN(tail, "/", 2)
	templateID := strings.TrimSpace(parts[0])
	if templateID == "" {
		s.writeNotFound(w, r)
		return
	}

	action := ""
	if len(parts) == 2 {
		action = strings.TrimSpace(parts[1])
	}

	switch {
	case action == "render" && r.Method == http.MethodPost:
		s.renderTemplate(w, r, templateID)
	case action == "" && r.Method == http.MethodGet:
		s.getTemplate(w, r, templateID)
	case action == "render":
		s.writeMethodNotAllowed(w, r, http.MethodPost)
	default:
		s.writeNotFound(w, r)
	}
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request, id string) {
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}
	tenantID, appID := requestScope(r)
	tmpl, ok, err := s.templates.GetScoped(r.Context(), id, tenantID, appID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to fetch template"))
		return
	}
	if !ok {
		s.writeNotFound(w, r)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"api_version": s.apiVersion,
		"template":    tmpl,
		"trace_id":    traceIDFromContext(r.Context()),
	})
}

func (s *Server) renderTemplate(w http.ResponseWriter, r *http.Request, id string) {
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}

	renderer, ok := s.templates.(templates.RenderStore)
	if !ok {
		s.writeError(w, r, http.StatusNotImplemented, validationError("UBAG-TEMPLATE-RENDER-UNSUPPORTED-001", "template rendering is not supported by this store"))
		return
	}

	var req renderTemplateRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JSON-001", "request body must be valid JSON"))
			return
		}
	}
	if req.Vars == nil {
		req.Vars = map[string]any{}
	}

	tenantID, appID := requestScope(r)
	rendered, err := renderer.Render(r.Context(), id, tenantID, appID, req.Vars)
	if err != nil {
		if strings.Contains(err.Error(), templates.ErrNotFound.Error()) {
			s.writeNotFound(w, r)
			return
		}
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-TEMPLATE-RENDER-001", err.Error()))
		return
	}

	s.writeJSON(w, http.StatusOK, renderTemplateResponse{
		APIVersion: s.apiVersion,
		TemplateID: id,
		Rendered:   rendered,
		TraceID:    traceIDFromContext(r.Context()),
	})
}
