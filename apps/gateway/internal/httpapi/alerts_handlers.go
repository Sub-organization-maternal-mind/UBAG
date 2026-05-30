package httpapi

import (
	"net/http"

	"github.com/ubag/ubag/apps/gateway/internal/alerts"
)

// handleAlerts serves GET /v1/alerts, listing human-in-the-loop manual-action
// alerts for the authenticated tenant. It returns 501 when the alert subsystem
// is not configured.
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if s.alerts == nil {
		s.writeNotImplemented(w, r, "alerting subsystem is not configured")
		return
	}
	if !s.authorizeGatewayAction(w, r, "alerts:read") {
		return
	}

	query := r.URL.Query()
	limit, ok := s.parseLimit(w, r, query.Get("limit"), 50)
	if !ok {
		return
	}
	status := normalizeAlertStatusFilter(query.Get("status"))

	tenantID, _ := requestScope(r)
	records, err := s.alerts.List(r.Context(), alerts.Filter{TenantID: tenantID, Status: status, Limit: limit})
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list alerts"))
		return
	}

	data := make([]map[string]any, 0, len(records))
	for _, alert := range records {
		data = append(data, alertToResponse(alert))
	}
	s.writeJSON(w, http.StatusOK, collectionResponse{
		APIVersion: s.apiVersion,
		Kind:       "alerts",
		Data:       data,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

// handleAlertsConfig serves GET /v1/alerts/config, returning a secret-free
// description of the alerting configuration. SMTP credentials are NEVER
// included.
func (s *Server) handleAlertsConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if s.alerts == nil {
		s.writeNotImplemented(w, r, "alerting subsystem is not configured")
		return
	}
	if !s.authorizeGatewayAction(w, r, "alerts:read") {
		return
	}
	summary := s.alerts.Config()
	s.writeJSON(w, http.StatusOK, map[string]any{
		"api_version":     s.apiVersion,
		"kind":            "alert_config",
		"sink_type":       summary.SinkType,
		"smtp_configured": summary.SMTPConfigured,
		"smtp_host":       summary.SMTPHost,
		"store_kind":      summary.StoreKind,
		"recipient_count": summary.RecipientCount,
		"recipients":      summary.Recipients,
		"trace_id":        traceIDFromContext(r.Context()),
	})
}

// handleAlertsSubtree serves POST /v1/alerts/{alert_id}/acknowledge and
// POST /v1/alerts/{alert_id}/resolve.
func (s *Server) handleAlertsSubtree(w http.ResponseWriter, r *http.Request) {
	if s.alerts == nil {
		s.writeNotImplemented(w, r, "alerting subsystem is not configured")
		return
	}
	tail := splitRouteTail(r.URL.Path, "/v1/alerts/")
	if len(tail) != 2 || tail[0] == "" {
		s.writeNotFound(w, r)
		return
	}
	alertID := tail[0]
	switch tail[1] {
	case "acknowledge":
		s.transitionAlert(w, r, alertID, alerts.StatusAcknowledged)
	case "resolve":
		s.transitionAlert(w, r, alertID, alerts.StatusResolved)
	default:
		s.writeNotFound(w, r)
	}
}

func (s *Server) transitionAlert(w http.ResponseWriter, r *http.Request, alertID, status string) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if !s.authorizeGatewayAction(w, r, "alerts:manage") {
		return
	}
	tenantID, _ := requestScope(r)

	var (
		alert alerts.Alert
		found bool
		err   error
	)
	switch status {
	case alerts.StatusAcknowledged:
		alert, found, err = s.alerts.Acknowledge(r.Context(), tenantID, alertID)
	case alerts.StatusResolved:
		alert, found, err = s.alerts.Resolve(r.Context(), tenantID, alertID)
	default:
		s.writeNotFound(w, r)
		return
	}
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to update alert"))
		return
	}
	if !found {
		s.writeNotFound(w, r)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"api_version": s.apiVersion,
		"kind":        "alert",
		"data":        alertToResponse(alert),
		"trace_id":    traceIDFromContext(r.Context()),
	})
}

func normalizeAlertStatusFilter(raw string) string {
	switch raw {
	case alerts.StatusOpen, alerts.StatusNotified, alerts.StatusAcknowledged, alerts.StatusResolved, alerts.StatusExpired:
		return raw
	default:
		return ""
	}
}

func alertToResponse(alert alerts.Alert) map[string]any {
	out := map[string]any{
		"alert_id":   alert.AlertID,
		"tenant_id":  alert.TenantID,
		"app_id":     alert.AppID,
		"job_id":     alert.JobID,
		"kind":       alert.Kind,
		"status":     alert.Status,
		"created_at": alert.CreatedAt.UTC(),
	}
	if alert.SessionID != "" {
		out["session_id"] = alert.SessionID
	}
	if alert.TargetID != "" {
		out["target_id"] = alert.TargetID
	}
	if alert.Message != "" {
		out["message"] = alert.Message
	}
	if !alert.NotifiedAt.IsZero() {
		out["notified_at"] = alert.NotifiedAt.UTC()
	}
	if !alert.AckedAt.IsZero() {
		out["acknowledged_at"] = alert.AckedAt.UTC()
	}
	if !alert.ResolvedAt.IsZero() {
		out["resolved_at"] = alert.ResolvedAt.UTC()
	}
	if len(alert.Attributes) > 0 {
		out["attributes"] = alert.Attributes
	}
	return out
}
