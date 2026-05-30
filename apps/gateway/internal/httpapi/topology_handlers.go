package httpapi

import (
	"net/http"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

// handleBrowserInstances serves GET /v1/browser/instances, listing the browser
// instances (engine processes) the worker fleet has registered for the
// authenticated tenant. Read-only observability; returns 501 when the topology
// subsystem is not configured.
func (s *Server) handleBrowserInstances(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if s.topology == nil {
		s.writeNotImplemented(w, r, "browser topology subsystem is not configured")
		return
	}
	if !s.authorizeGatewayAction(w, r, "browser:read") {
		return
	}
	query := r.URL.Query()
	limit, ok := s.parseLimit(w, r, query.Get("limit"), 100)
	if !ok {
		return
	}
	tenantID, _ := requestScope(r)
	records, err := s.topology.ListInstances(r.Context(), topology.InstanceFilter{
		TenantID: tenantID,
		State:    query.Get("state"),
		Limit:    limit,
	})
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list browser instances"))
		return
	}
	data := make([]map[string]any, 0, len(records))
	for _, instance := range records {
		data = append(data, instanceToResponse(instance))
	}
	s.writeJSON(w, http.StatusOK, collectionResponse{
		APIVersion: s.apiVersion,
		Kind:       "browser_instances",
		Data:       data,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

// handleBrowserContexts serves GET /v1/browser/contexts, listing the provider
// contexts (per target+identity isolated browser contexts) for the tenant.
// storage_state_uri is never exposed; only has_storage_state is returned.
func (s *Server) handleBrowserContexts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if s.topology == nil {
		s.writeNotImplemented(w, r, "browser topology subsystem is not configured")
		return
	}
	if !s.authorizeGatewayAction(w, r, "browser:read") {
		return
	}
	query := r.URL.Query()
	limit, ok := s.parseLimit(w, r, query.Get("limit"), 100)
	if !ok {
		return
	}
	tenantID, _ := requestScope(r)
	records, err := s.topology.ListContexts(r.Context(), topology.ContextFilter{
		TenantID:   tenantID,
		InstanceID: query.Get("instance_id"),
		Limit:      limit,
	})
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list provider contexts"))
		return
	}
	data := make([]map[string]any, 0, len(records))
	for _, context := range records {
		data = append(data, contextToResponse(context))
	}
	s.writeJSON(w, http.StatusOK, collectionResponse{
		APIVersion: s.apiVersion,
		Kind:       "provider_contexts",
		Data:       data,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

// handleBrowserTabs serves GET /v1/browser/tabs, listing the browser tabs for
// the tenant. Tabs are tenant-scoped by joining their parent provider context.
func (s *Server) handleBrowserTabs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if s.topology == nil {
		s.writeNotImplemented(w, r, "browser topology subsystem is not configured")
		return
	}
	if !s.authorizeGatewayAction(w, r, "browser:read") {
		return
	}
	query := r.URL.Query()
	limit, ok := s.parseLimit(w, r, query.Get("limit"), 100)
	if !ok {
		return
	}
	tenantID, _ := requestScope(r)
	records, err := s.topology.ListTabs(r.Context(), topology.TabFilter{
		TenantID:  tenantID,
		ContextID: query.Get("context_id"),
		State:     query.Get("state"),
		Limit:     limit,
	})
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list browser tabs"))
		return
	}
	data := make([]map[string]any, 0, len(records))
	for _, tab := range records {
		data = append(data, tabToResponse(tab))
	}
	s.writeJSON(w, http.StatusOK, collectionResponse{
		APIVersion: s.apiVersion,
		Kind:       "browser_tabs",
		Data:       data,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

// handleBrowserSummary serves GET /v1/browser/summary, returning aggregate
// counts of the tenant's browser topology grouped by lifecycle state.
func (s *Server) handleBrowserSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if s.topology == nil {
		s.writeNotImplemented(w, r, "browser topology subsystem is not configured")
		return
	}
	if !s.authorizeGatewayAction(w, r, "browser:read") {
		return
	}
	tenantID, _ := requestScope(r)
	summary, err := s.topology.Summary(r.Context(), tenantID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to summarize browser topology"))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"api_version":             s.apiVersion,
		"kind":                    "browser_topology_summary",
		"tenant_id":               summary.TenantID,
		"total_instances":         summary.TotalInstances,
		"total_contexts":          summary.TotalContexts,
		"total_tabs":              summary.TotalTabs,
		"instances_by_state":      summary.InstancesByState,
		"contexts_by_login_state": summary.ContextsByLoginState,
		"tabs_by_state":           summary.TabsByState,
		"trace_id":                traceIDFromContext(r.Context()),
	})
}

// handleConcurrency serves GET /v1/concurrency, returning the latest adaptive
// (AIMD) concurrency ceilings reported by the worker fleet for the tenant. The
// gateway is read-only here: it never computes or mutates AIMD state.
func (s *Server) handleConcurrency(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if s.concurrency == nil {
		s.writeNotImplemented(w, r, "concurrency observability subsystem is not configured")
		return
	}
	if !s.authorizeGatewayAction(w, r, "concurrency:read") {
		return
	}
	tenantID, _ := requestScope(r)
	views := s.concurrency.List(tenantID)
	data := make([]map[string]any, 0, len(views))
	for _, view := range views {
		data = append(data, concurrencyToResponse(view))
	}
	s.writeJSON(w, http.StatusOK, collectionResponse{
		APIVersion: s.apiVersion,
		Kind:       "concurrency_ceilings",
		Data:       data,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

func instanceToResponse(instance topology.BrowserInstance) map[string]any {
	out := map[string]any{
		"instance_id":     instance.InstanceID,
		"worker_id":       instance.WorkerID,
		"tenant_id":       instance.TenantID,
		"engine":          instance.Engine,
		"remote_endpoint": instance.RemoteEndpoint,
		"state":           instance.State,
		"context_count":   instance.ContextCount,
		"tab_count":       instance.TabCount,
		"created_at":      instance.CreatedAt,
	}
	if instance.RSSBytes != nil {
		out["rss_bytes"] = *instance.RSSBytes
	}
	if instance.RecycleAt != nil {
		out["recycle_at"] = *instance.RecycleAt
	}
	return out
}

func contextToResponse(context topology.ProviderContext) map[string]any {
	// storage_state_uri is intentionally absent from the model; only its
	// presence is surfaced to callers (INV-5 redaction).
	out := map[string]any{
		"context_id":         context.ContextID,
		"instance_id":        context.InstanceID,
		"tenant_id":          context.TenantID,
		"target_id":          context.TargetID,
		"identity_ref":       context.IdentityRef,
		"login_state":        context.LoginState,
		"conversation_model": context.ConversationModel,
		"fingerprint_id":     context.FingerprintID,
		"proxy_id":           context.ProxyID,
		"has_storage_state":  context.HasStorageState,
		"max_tabs":           context.MaxTabs,
		"created_at":         context.CreatedAt,
	}
	if context.LastHealthAt != nil {
		out["last_health_at"] = *context.LastHealthAt
	}
	if context.RecycleAt != nil {
		out["recycle_at"] = *context.RecycleAt
	}
	return out
}

func tabToResponse(tab topology.BrowserTab) map[string]any {
	out := map[string]any{
		"tab_id":          tab.TabID,
		"context_id":      tab.ContextID,
		"state":           tab.State,
		"conversation_id": tab.ConversationID,
		"current_job_id":  tab.CurrentJobID,
		"jobs_completed":  tab.JobsCompleted,
		"created_at":      tab.CreatedAt,
	}
	if tab.RSSBytes != nil {
		out["rss_bytes"] = *tab.RSSBytes
	}
	if tab.LastHealthAt != nil {
		out["last_health_at"] = *tab.LastHealthAt
	}
	if tab.RecycleAt != nil {
		out["recycle_at"] = *tab.RecycleAt
	}
	return out
}

func concurrencyToResponse(view topology.ConcurrencyView) map[string]any {
	return map[string]any{
		"target":             view.Target,
		"identity_ref":       view.IdentityRef,
		"current_cap":        view.CurrentCap,
		"min":                view.Min,
		"max":                view.Max,
		"in_flight":          view.InFlight,
		"last_change_reason": view.LastChangeReason,
		"last_change_at":     view.LastChangeAt,
	}
}
