package httpapi

import "net/http"

// handleFleetRead serves GET /v1/fleet/nodes and GET /v1/fleet/summary, the
// operator read view of the shared helper fleet (OpenAPI listFleetNodes and
// getFleetSummary).
//
// The contract and the SDK methods land first; no fleet source is wired into the
// server yet (roadmap P6.2), so a caller that may read the fleet gets 501 today,
// the same degradation as the topology and concurrency routes. Authorization runs
// before the 501 so a role without fleet:read (anything but operator and admin)
// gets 403 whether or not a fleet exists, and learns nothing about the
// deployment.
func (s *Server) handleFleetRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if !s.authorizeGatewayAction(w, r, "fleet:read") {
		return
	}
	s.writeNotImplemented(w, r, "fleet source is not configured")
}
