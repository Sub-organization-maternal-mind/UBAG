package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/compliance"
)

type privacyRequestBody struct {
	SubjectRef string `json:"subject_ref"`
}

type privacyRequestReceipt struct {
	APIVersion string    `json:"api_version"`
	RequestID  string    `json:"request_id"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Receipt    string    `json:"receipt"`
	CreatedAt  time.Time `json:"created_at"`
	TraceID    string    `json:"trace_id"`
}

func (s *Server) handlePrivacyExport(w http.ResponseWriter, r *http.Request) {
	s.handlePrivacyRequest(w, r, compliance.KindExport)
}

func (s *Server) handlePrivacyErase(w http.ResponseWriter, r *http.Request) {
	s.handlePrivacyRequest(w, r, compliance.KindErase)
}

func (s *Server) handlePrivacyRequest(w http.ResponseWriter, r *http.Request, kind compliance.RequestKind) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if s.privacyStore == nil {
		s.writeError(w, r, http.StatusNotImplemented, validationError("UBAG-COMPLIANCE-DISABLED-001", "privacy request handling is not enabled on this server"))
		return
	}
	if !s.authorizeGatewayAction(w, r, "data:export") {
		return
	}

	var body privacyRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.SubjectRef) == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PRIVACY-001", "subject_ref is required"))
		return
	}

	tenantID, _ := requestScope(r)
	req, err := s.privacyStore.Create(r.Context(), compliance.PrivacyRequest{
		TenantID:   tenantID,
		SubjectRef: strings.TrimSpace(body.SubjectRef),
		Kind:       kind,
	})
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-PRIVACY-002", err.Error()))
		return
	}

	s.writeJSON(w, http.StatusAccepted, privacyRequestReceipt{
		APIVersion: s.apiVersion,
		RequestID:  req.ID,
		Kind:       string(req.Kind),
		Status:     string(req.Status),
		Receipt:    req.Receipt,
		CreatedAt:  req.CreatedAt,
		TraceID:    traceIDFromContext(r.Context()),
	})
}
