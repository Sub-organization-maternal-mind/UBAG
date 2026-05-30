package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
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

type auditExportRequest struct {
	APIVersion string `json:"api_version,omitempty"`
	// IdempotencyKey is accepted for parity with the idempotent-mutation
	// convention used by other POST endpoints and the SDKs. Audit export is a
	// read, so the key is ignored.
	IdempotencyKey string                 `json:"idempotency_key,omitempty"`
	Since          string                 `json:"since,omitempty"`
	Until          string                 `json:"until,omitempty"`
	Limit          int                    `json:"limit,omitempty"`
	Range          *auditExportRangeInput `json:"range,omitempty"`
}

// auditExportRangeInput is an optional sequence-bounded filter. When provided,
// records outside [FromSequence, ToSequence] are excluded after the time-range
// query. A nil bound is treated as unbounded.
type auditExportRangeInput struct {
	FromSequence *int64 `json:"from_sequence,omitempty"`
	ToSequence   *int64 `json:"to_sequence,omitempty"`
}

type auditRecordPayload struct {
	ID         string         `json:"id"`
	Seq        int64          `json:"seq"`
	TenantID   string         `json:"tenant_id"`
	AppID      string         `json:"app_id"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	Resource   string         `json:"resource"`
	Outcome    string         `json:"outcome"`
	OccurredAt time.Time      `json:"occurred_at"`
	Attributes map[string]any `json:"attributes,omitempty"`
	PrevHash   string         `json:"prev_hash"`
	RecordHash string         `json:"record_hash"`
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
	ChainValid bool                 `json:"chain_valid"`
	HeadHash   string               `json:"head_hash"`
	Count      int                  `json:"count"`
	Records    []auditRecordPayload `json:"records"`
	TraceID    string               `json:"trace_id"`
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
	if s.audit == nil && s.siemExporter == nil {
		s.writeNotImplemented(w, r, "audit export is not configured")
		return
	}

	// The filter body is optional. An empty body exports the full tenant chain.
	var request auditExportRequest
	if raw, hasBody, ok := s.readOptionalBody(w, r); !ok {
		return
	} else if hasBody {
		if !s.decodeBody(w, r, raw, &request) {
			return
		}
	}
	apiVersion := s.apiVersion
	if strings.TrimSpace(request.APIVersion) != "" {
		resolved, ok := s.resolveAPIVersion(w, r, request.APIVersion)
		if !ok {
			return
		}
		apiVersion = resolved
	}

	tenantID, _ := requestScope(r)
	resp := auditExportResponse{
		APIVersion: apiVersion,
		Status:     "accepted",
		ChainValid: true,
		Records:    []auditRecordPayload{},
		TraceID:    traceIDFromContext(r.Context()),
	}

	if s.audit != nil {
		filter := audit.Filter{TenantID: tenantID, Limit: request.Limit}
		if since, ok := parseExportTime(request.Since); ok {
			filter.Since = since
		}
		if until, ok := parseExportTime(request.Until); ok {
			filter.Until = until
		}
		records, err := s.audit.List(r.Context(), filter)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read audit records"))
			return
		}
		// Verify the chain over the full time-range result before applying the
		// optional sequence window, so slicing the window never breaks the
		// prev_hash linkage used by VerifyChain.
		resp.ChainValid = audit.VerifyChain(records)
		records = filterAuditBySequence(records, request.Range)
		resp.Records = auditRecordsToPayload(records)
		resp.Count = len(records)
		if head, _, err := s.audit.Head(r.Context(), tenantID); err == nil {
			resp.HeadHash = head
		}
	}

	if s.siemExporter != nil {
		stats := s.siemExporter.Stats()
		resp.Stats.Enqueued = stats.Enqueued
		resp.Stats.Exported = stats.Exported
		resp.Stats.Dropped = stats.Dropped
		resp.Stats.Failed = stats.Failed
	}

	// Forwarding to a SIEM sink is asynchronous, so the request is accepted
	// (202) when an exporter is present; a persistence-only export is a
	// completed read (200).
	status := http.StatusOK
	if s.siemExporter != nil {
		status = http.StatusAccepted
	}
	s.writeJSON(w, status, resp)
}

// parseExportTime parses an RFC3339 timestamp filter bound. It returns ok=false
// for empty or unparseable input so the bound is treated as unbounded.
func parseExportTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

// filterAuditBySequence applies an optional [from_sequence, to_sequence] window
// to records already ordered by Seq ascending. Nil bounds are unbounded. A nil
// window returns the records unchanged.
func filterAuditBySequence(records []audit.Record, window *auditExportRangeInput) []audit.Record {
	if window == nil || (window.FromSequence == nil && window.ToSequence == nil) {
		return records
	}
	out := make([]audit.Record, 0, len(records))
	for _, rec := range records {
		if window.FromSequence != nil && rec.Seq < *window.FromSequence {
			continue
		}
		if window.ToSequence != nil && rec.Seq > *window.ToSequence {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func auditRecordsToPayload(records []audit.Record) []auditRecordPayload {
	out := make([]auditRecordPayload, 0, len(records))
	for _, rec := range records {
		out = append(out, auditRecordPayload{
			ID:         rec.ID,
			Seq:        rec.Seq,
			TenantID:   rec.TenantID,
			AppID:      rec.AppID,
			Actor:      rec.Actor,
			Action:     rec.Action,
			Resource:   rec.Resource,
			Outcome:    rec.Outcome,
			OccurredAt: rec.OccurredAt,
			Attributes: rec.Attributes,
			PrevHash:   rec.PrevHash,
			RecordHash: rec.RecordHash,
		})
	}
	return out
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
