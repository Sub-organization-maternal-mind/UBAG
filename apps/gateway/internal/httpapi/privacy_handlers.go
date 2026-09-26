package httpapi

import (
	"encoding/json"
	"io"
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

// privacyActionFor maps a privacy request kind to its RBAC action. Erasure is
// deliberately distinct from export.
func privacyActionFor(kind compliance.RequestKind) string {
	if kind == compliance.KindErase {
		return "data:erase"
	}
	return "data:export"
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
	// Erasure is gated on its own action, not on data:export. Sharing the
	// export permission meant a principal granted read-only export rights
	// could trigger GDPR Art. 17 erasure, which is destructive and
	// irreversible. Export and erase are now separately grantable; admin
	// holds both.
	if !s.authorizeGatewayAction(w, r, privacyActionFor(kind)) {
		return
	}

	var body privacyRequestBody
	// Bounded read. This handler used json.NewDecoder(r.Body) directly, so a
	// caller with job:read+data:export could stream an unbounded body into
	// memory. The limit matches every other JSON handler in the package.
	if err := json.NewDecoder(io.LimitReader(r.Body, s.maxBody)).Decode(&body); err != nil ||
		strings.TrimSpace(body.SubjectRef) == "" {
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
