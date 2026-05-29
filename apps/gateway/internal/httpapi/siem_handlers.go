package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/payloadpolicy"
	"github.com/ubag/ubag/apps/gateway/internal/siem"
)

type siemSinkRequest struct {
	APIVersion string `json:"api_version,omitempty"`
	ID         string `json:"id,omitempty"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Target     string `json:"target,omitempty"`
	Network    string `json:"network,omitempty"`
	SecretRef  string `json:"secret_ref,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type siemSinkPayload struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Target    string    `json:"target,omitempty"`
	Network   string    `json:"network,omitempty"`
	SecretRef string    `json:"secret_ref,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type siemConfigResponse struct {
	APIVersion string            `json:"api_version"`
	TenantID   string            `json:"tenant_id"`
	Sinks      []siemSinkPayload `json:"sinks"`
	TraceID    string            `json:"trace_id"`
}

type auditExportResponse struct {
	APIVersion string `json:"api_version"`
	Status     string `json:"status"`
	Stats      struct {
		Enqueued int `json:"enqueued"`
		Exported int `json:"exported"`
		Dropped  int `json:"dropped"`
		Failed   int `json:"failed"`
	} `json:"stats"`
	TraceID string `json:"trace_id"`
}

func (s *Server) handleSIEMConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getSIEMConfig(w, r)
	case http.MethodPut:
		s.putSIEMConfig(w, r)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPut)
	}
}

func (s *Server) getSIEMConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return
	}
	if s.siemConfig == nil {
		s.writeNotImplemented(w, r, "SIEM export is not configured")
		return
	}
	tenantID, _ := requestScope(r)
	sinks, err := s.siemConfig.List(r.Context(), tenantID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list SIEM sinks"))
		return
	}
	s.writeJSON(w, http.StatusOK, siemConfigResponse{
		APIVersion: s.apiVersion,
		TenantID:   tenantID,
		Sinks:      siemSinksToPayload(sinks),
		TraceID:    traceIDFromContext(r.Context()),
	})
}

func (s *Server) putSIEMConfig(w http.ResponseWriter, r *http.Request) {
	if s.siemConfig == nil {
		s.writeNotImplemented(w, r, "SIEM export is not configured")
		return
	}
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var request siemSinkRequest
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

	// Reject any plaintext secrets in the sink configuration; only secret_ref
	// references are permitted.
	var bodyMap map[string]any
	if err := json.Unmarshal(raw, &bodyMap); err == nil {
		if err := payloadpolicy.Validate(bodyMap); err != nil {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SIEM-PAYLOAD-SAFETY-001", err.Error()))
			return
		}
	}

	if strings.TrimSpace(request.Name) == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SIEM-NAME-001", "name is required"))
		return
	}
	switch strings.ToLower(strings.TrimSpace(request.Kind)) {
	case "file", "http", "syslog":
	default:
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SIEM-KIND-001", "kind must be file, http, or syslog"))
		return
	}

	tenantID, _ := requestScope(r)
	stored, err := s.siemConfig.Put(r.Context(), siem.SinkConfig{
		ID:        strings.TrimSpace(request.ID),
		TenantID:  tenantID,
		Name:      strings.TrimSpace(request.Name),
		Kind:      strings.ToLower(strings.TrimSpace(request.Kind)),
		Target:    strings.TrimSpace(request.Target),
		Network:   strings.TrimSpace(request.Network),
		SecretRef: strings.TrimSpace(request.SecretRef),
		Enabled:   request.Enabled,
	})
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-SIEM-SINK-001", err.Error()))
		return
	}
	s.writeJSON(w, http.StatusOK, siemConfigResponse{
		APIVersion: apiVersion,
		TenantID:   tenantID,
		Sinks:      siemSinksToPayload([]siem.SinkConfig{stored}),
		TraceID:    traceIDFromContext(r.Context()),
	})
}

func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if !s.authorizeGatewayAction(w, r, "data:export") {
		return
	}
	if s.siemExporter == nil {
		s.writeNotImplemented(w, r, "SIEM export is not configured")
		return
	}
	stats := s.siemExporter.Stats()
	resp := auditExportResponse{
		APIVersion: s.apiVersion,
		Status:     "accepted",
		TraceID:    traceIDFromContext(r.Context()),
	}
	resp.Stats.Enqueued = stats.Enqueued
	resp.Stats.Exported = stats.Exported
	resp.Stats.Dropped = stats.Dropped
	resp.Stats.Failed = stats.Failed
	s.writeJSON(w, http.StatusAccepted, resp)
}

func siemSinksToPayload(sinks []siem.SinkConfig) []siemSinkPayload {
	out := make([]siemSinkPayload, 0, len(sinks))
	for _, sink := range sinks {
		out = append(out, siemSinkPayload{
			ID:        sink.ID,
			Name:      sink.Name,
			Kind:      sink.Kind,
			Target:    sink.Target,
			Network:   sink.Network,
			SecretRef: sink.SecretRef,
			Enabled:   sink.Enabled,
			CreatedAt: sink.CreatedAt.UTC(),
			UpdatedAt: sink.UpdatedAt.UTC(),
		})
	}
	return out
}
