package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// FleetSource is the read side of the shared helper fleet (nodes.FleetView).
type FleetSource interface {
	// Nodes returns every node ordered by node id.
	Nodes(ctx context.Context) ([]nodes.FleetNode, error)
	Summary(ctx context.Context) (nodes.FleetSummary, error)
	// HelperHosts is the set of lowercase hosts the helper nodes are reached at.
	HelperHosts(ctx context.Context) (map[string]bool, error)
}

// QueueHoldSource is what the gateway knows about queued jobs its consumer is
// holding back (executor.HoldBoard).
type QueueHoldSource interface {
	Lookup(jobID string) (executor.HoldInfo, bool)
	// Counts returns the live holds per fine reason; an empty tenantID is every tenant.
	Counts(tenantID, appID string) map[string]int
}

// maxFleetNodes mirrors the OpenAPI limit on GET /v1/fleet/nodes.
const maxFleetNodes = nodes.MaxNodes

// handleFleetRead serves GET /v1/fleet/nodes and GET /v1/fleet/summary, the
// operator read view of the shared helper fleet (OpenAPI listFleetNodes and
// getFleetSummary).
//
// Authorization runs before the 501 so a role without fleet:read (anything but
// operator and admin) gets 403 whether or not a fleet exists, and learns nothing
// about the deployment. A deployment with no fleet source (UBAG_HELPER_NODES off)
// answers 501. Nothing here is tenant-scoped, and nothing returned carries an
// address, hostname or endpoint.
func (s *Server) handleFleetRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if !s.authorizeGatewayAction(w, r, "fleet:read") {
		return
	}
	if s.fleet == nil {
		s.writeNotImplemented(w, r, "fleet source is not configured")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/summary") {
		s.writeFleetSummary(w, r)
		return
	}
	s.writeFleetNodes(w, r)
}

func (s *Server) writeFleetNodes(w http.ResponseWriter, r *http.Request) {
	limit := maxFleetNodes
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxFleetNodes {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-LIMIT-001", "limit must be an integer from 1 to 256"))
			return
		}
		limit = n
	}
	list, err := s.fleet.Nodes(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read the fleet"))
		return
	}
	total := len(list)
	if total > limit {
		list = list[:limit]
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"api_version": s.apiVersion,
		"kind":        "fleet_nodes",
		"total":       total,
		"data":        list,
		"trace_id":    traceIDFromContext(r.Context()),
	})
}

func (s *Server) writeFleetSummary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.fleet.Summary(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read the fleet"))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"api_version":            s.apiVersion,
		"kind":                   "fleet_summary",
		"nodes_total":            sum.NodesTotal,
		"nodes_by_state":         sum.NodesByState,
		"workload_limit_total":   sum.WorkloadLimitTotal,
		"workloads_in_use_total": sum.WorkloadsInUseTotal,
		"nodes_pressure_reduced": sum.NodesPressureReduced,
		"held_by_reason":         sum.HeldByReason,
		"trace_id":               traceIDFromContext(r.Context()),
	})
}

// applyQueueReason fills the coarse queue_reason of a queued job from the holds
// the consumer reports. The fine reason never leaves this function: a tenant sees
// the coarse value only, and operators read the fine counts from the fleet summary.
// A queued job the consumer is not holding is waiting for a worker. Every other
// status, and a gateway with no hold source, leaves the fields absent.
func (s *Server) applyQueueReason(resp *jobResponse, job jobstore.Job) {
	if s.queueHolds == nil || job.Status != jobstore.StatusQueued {
		return
	}
	resp.QueueReason = executor.QueueWaitingForWorker
	since := job.UpdatedAt
	if h, ok := s.queueHolds.Lookup(job.ID); ok {
		resp.QueueReason, since = executor.CoarseQueueReason(h.Fine), h.Since
	}
	since = since.UTC()
	resp.QueueReasonSince = &since
}

// queuedByReason groups the caller's queued jobs by coarse queue reason for the
// jobs summary: the holds the consumer reports for this scope, and every other
// queued job as waiting for a worker (never negative: a hold can briefly outlive
// a job that was cancelled). It is empty without a hold source.
func (s *Server) queuedByReason(tenantID, appID string, queued int) map[string]int {
	out := map[string]int{}
	if s.queueHolds == nil || queued <= 0 {
		return out
	}
	held := 0
	for fine, n := range s.queueHolds.Counts(tenantID, appID) {
		out[executor.CoarseQueueReason(fine)] += n
		held += n
	}
	if rest := queued - held; rest > 0 {
		out[executor.QueueWaitingForWorker] += rest
	}
	return out
}

// helperHostedEndpoint reports whether a browser instance's remote endpoint is on
// a helper node. Such an endpoint is a WireGuard address inside the fleet and is
// never returned, whatever UBAG_REDACT_REMOTE_ENDPOINT says. Fail closed: a fleet
// that cannot be read redacts every endpoint.
func helperHostedEndpointFunc(ctx context.Context, fleet FleetSource) func(endpoint string) bool {
	if fleet == nil {
		return func(string) bool { return false }
	}
	hosts, err := fleet.HelperHosts(ctx)
	if err != nil {
		return func(string) bool { return true }
	}
	return func(endpoint string) bool {
		raw := strings.TrimSpace(endpoint)
		if raw == "" {
			return false
		}
		if !strings.Contains(raw, "://") {
			raw = "//" + raw
		}
		u, err := url.Parse(raw)
		return err != nil || hosts[strings.ToLower(u.Hostname())]
	}
}
