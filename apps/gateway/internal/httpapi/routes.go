package httpapi

import "net/http"

// routeTable is the one declaration of the gateway's HTTP surface: every
// registered route, its canonical pattern, and the dispatcher-mapped metric
// patterns for the sub-paths chi wildcards resolve. routes() registers from
// it; the metrics middleware and rateLimitAction match against it — no second
// hand-maintained routing table (ADR-0004 discipline at the routing seam).
type routeDecl struct {
	pattern string
	handler func(http.ResponseWriter, *http.Request)
}

// metricPatterns maps the concrete sub-paths a registered chi wildcard
// resolves onto the canonical pattern recorded in metrics. Derived once from
// the route table; replaces the hand-rolled routePattern() switch.
type metricRoute struct {
	matchPrefix string
	segments    int          // exact segment count that selects this entry
	segment3   string        // required 4th segment value ("" = any)
	pattern     string       // canonical metric pattern
}

var jobMetricRoutes = []metricRoute{
	{matchPrefix: "/v1/jobs", segments: 3, segment3: "", pattern: "/v1/jobs/{job_id}"},
	{matchPrefix: "/v1/jobs", segments: 4, segment3: "artifacts", pattern: "/v1/jobs/{job_id}/artifacts"},
	{matchPrefix: "/v1/jobs", segments: 5, segment3: "artifacts", pattern: "/v1/jobs/{job_id}/artifacts/{key}"},
	{matchPrefix: "/v1/jobs", segments: 4, segment3: "events", pattern: "/v1/jobs/{job_id}/events"},
	{matchPrefix: "/v1/jobs", segments: 4, segment3: "cancel", pattern: "/v1/jobs/{job_id}/cancel"},
	{matchPrefix: "/v1/jobs", segments: 4, segment3: "retry", pattern: "/v1/jobs/{job_id}/retry"},
}

// registerRoutes wires every route from the declaration tables below. A new
// route = one row; the method guard, authz action, and nil-501 stay in the
// handler (the declaration carries what the ROUTER needs, not what the
// handler enforces).
func (s *Server) registerRoutes() {
	for _, route := range s.simpleRoutes() {
		s.mux.HandleFunc(route.pattern, route.handler)
	}
}

func (s *Server) simpleRoutes() []routeDecl {
	return []routeDecl{
		{"/v1/health", s.handleHealth},
		{"/v1/ready", s.handleReady},
		{"/v1/version", s.handleVersion},
		{"/v1/metrics", s.handleMetrics},
		{"/v1/events", s.handleEvents},
		{"/v1/stream", s.handleStream},
		{"/v1/workflows", s.handleWorkflows},
		{"/v1/workflows/*", s.handleWorkflowsSubtree},
		{"/v1/templates", s.handleTemplates},
		{"/v1/templates/*", s.handleTemplateRender},
		{"/v1/targets", s.handleCollection("targets", targetCatalog(), "job:read")},
		{"/v1/adapters", s.handleCollection("adapters", adapterCatalog(), "job:read")},
		{"/v1/apps", s.handleCollection("apps", nil, "job:read")},
		{"/v1/devices", s.handleCollection("devices", nil, "job:read")},
		{"/v1/webhooks", s.handleCollection("webhooks", nil, "job:read")},
		{"/v1/webhooks/replay", s.replayWebhook},
		{"/v1/webhooks/secret:rotate", s.rotateWebhookSecret},
		{"/v1/cache", s.handleCache},
		{"/v1/cache/invalidate", s.handleCacheInvalidate},
		{"/v1/rate-limits", s.handleRateLimits},
		{"/v1/audit", s.handleCollection("audit", nil, "audit:read")},
		{"/v1/audit/export", s.handleAuditExport},
		{"/v1/sso/config", s.handleSSOConfig},
		{"/v1/sso/oidc/authorize", s.handleSSOOIDCAuthorize},
		{"/v1/sso/oidc/callback", s.handleSSOOIDCCallback},
		{"/v1/sso/saml/acs", s.handleSSOSAMLACS},
		{"/v1/sso/logout", s.handleSSOLogout},
		{"/v1/scim/v2/Users", s.handleSCIMUsers},
		{"/v1/scim/v2/Users/*", s.handleSCIMUserByID},
		{"/v1/scim/v2/Groups", s.handleSCIMGroups},
		{"/v1/scim/v2/Groups/*", s.handleSCIMGroupByID},
		{"/v1/siem/config", s.handleSIEMConfig},
		{"/v1/alerts", s.handleAlerts},
		{"/v1/alerts/config", s.handleAlertsConfig},
		{"/v1/alerts/*", s.handleAlertsSubtree},
		{"/v1/browser/instances", s.handleBrowserInstances},
		{"/v1/browser/contexts", s.handleBrowserContexts},
		{"/v1/browser/tabs", s.handleBrowserTabs},
		{"/v1/browser/summary", s.handleBrowserSummary},
		{"/v1/concurrency", s.handleConcurrency},
		{"/v1/conversations", s.handleConversations},
		{"/v1/jobs", s.handleJobs},
		{"/v1/jobs/batch", s.handleBatchJobs},
		{"/v1/jobs/*", s.handleJobByID},
		{"/v1/sse/jobs/*", s.handleJobSSE},
		{"/v1/auth/pat", s.handleIssuePAT},
		{"/v1/privacy/export", s.handlePrivacyExport},
		{"/v1/privacy/erase", s.handlePrivacyErase},
		{"/v1/admin/regions/{region}/state", s.handleSetRegionState},
		{"/v1/mfa/enroll", s.handleMFAEnroll},
		{"/v1/mfa/verify", s.handleMFAVerify},
		{"/v1/admin/elevation", s.handleRequestElevation},
		{"/v1/admin/elevation/{id}/approve", s.handleApproveElevation},
	}
}

// routePattern resolves the canonical metric pattern for a concrete request
// path. Single source: the jobMetricRoutes table.
func routePattern(path string) string {
	if path == "/v1/jobs" || path == "/v1/jobs/batch" {
		return path
	}
	segments := splitRouteTail(path, "/")
	if len(segments) < 3 || segments[0] != "v1" || segments[1] != "jobs" {
		return "unmatched"
	}
	for _, rule := range jobMetricRoutes {
		if len(segments) != rule.segments {
			continue
		}
		if rule.segment3 != "" && (len(segments) < 4 || segments[3] != rule.segment3) {
			continue
		}
		return rule.pattern
	}
	return "unmatched"
}
