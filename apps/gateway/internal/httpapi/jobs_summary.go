package httpapi

import (
	"net/http"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// handleJobsSummary serves GET /v1/jobs/summary: true (uncapped) per-status
// counts for the caller's tenant/app scope, so dashboards never derive queue
// depth from a paginated list page.
func (s *Server) handleJobsSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}
	tenantID, appID := requestScope(r)
	scope := jobstore.ListFilter{TenantID: tenantID, AppID: appID}

	counts := map[string]int{}
	total := 0
	if metrics, ok := s.jobs.(jobstore.MetricsStore); ok {
		byStatus, n, err := metrics.CountsByStatus(r.Context(), scope)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to summarize jobs"))
			return
		}
		for status, count := range byStatus {
			counts[string(status)] = count
		}
		total = n
	} else {
		// ponytail: bounded scan fallback for stores without MetricsStore (all shipped stores implement it).
		scope.Limit = jobstore.UnboundedScanLimit
		jobs, err := s.jobs.List(r.Context(), scope)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to summarize jobs"))
			return
		}
		for _, job := range jobs {
			counts[string(job.Status)]++
		}
		total = len(jobs)
	}

	// Oldest queued job = first row of the ascending (created_at, id) queued set.
	var oldestQueued *string
	if counts[string(jobstore.StatusQueued)] > 0 {
		oldest, err := s.jobs.List(r.Context(), jobstore.ListFilter{
			TenantID: tenantID, AppID: appID, Status: string(jobstore.StatusQueued), Limit: 1,
		})
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to summarize jobs"))
			return
		}
		if len(oldest) > 0 {
			value := oldest[0].CreatedAt.UTC().Format(time.RFC3339Nano)
			oldestQueued = &value
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"api_version":      s.apiVersion,
		"kind":             "jobs_summary",
		"total":            total,
		"counts_by_status": counts,
		// Empty until the gateway computes a queue reason (roadmap P6.2).
		"queued_by_reason": map[string]int{},
		"oldest_queued_at": oldestQueued,
		"trace_id":         traceIDFromContext(r.Context()),
	})
}
