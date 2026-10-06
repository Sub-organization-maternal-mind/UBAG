package nodes

import "time"

const (
	HeartbeatInterval = 15 * time.Second
	// MissedHeartbeatsLimit consecutive missed heartbeats stop assignment.
	MissedHeartbeatsLimit = 3
)

// Reasons a helper is not eligible for new placements.
const (
	ReasonRevoked         = "revoked"
	ReasonDraining        = "draining"
	ReasonReservationUnk  = "reservation_unknown"
	ReasonGrantExpired    = "grant_expired"
	ReasonHeartbeatMissed = "heartbeat_missed"
	ReasonNoCapacity      = "no_capacity"
)

// Grant is the subset of a node-allocation grant the placement logic reads.
type Grant struct {
	State               string // active | draining | revoked
	ReservationState    string // known | unknown
	MaxBrowserWorkloads int
	ValidUntil          time.Time
}

// Decision is the placement verdict for one helper.
type Decision struct {
	Eligible bool
	Reason   string // set when !Eligible
	// Limit is the concurrent browser workloads allowed (0 when ineligible).
	Limit int
}

// Evaluate decides whether a helper may take new placements and how many
// concurrent workloads it may run. ramped is the limit earned so far (<=0 for
// a helper with no history, which starts at NewHelperWorkloads). Existing work
// is never touched here: ineligible only means "no new assignment".
func Evaluate(g Grant, lastHeartbeat, now time.Time, p Pressure, ramped int) Decision {
	no := func(r string) Decision { return Decision{Reason: r} }
	switch {
	case g.State == "revoked":
		return no(ReasonRevoked)
	case g.State != "active": // draining, and fail closed on anything unknown
		return no(ReasonDraining)
	case g.ReservationState != "known":
		return no(ReasonReservationUnk)
	case g.ValidUntil.IsZero() || !now.Before(g.ValidUntil):
		return no(ReasonGrantExpired)
	case lastHeartbeat.IsZero() || now.Sub(lastHeartbeat) >= MissedHeartbeatsLimit*HeartbeatInterval:
		return no(ReasonHeartbeatMissed)
	}
	if ramped <= 0 {
		ramped = NewHelperWorkloads
	}
	limit := p.AdmissionLimit(min(g.MaxBrowserWorkloads, ramped))
	if limit <= 0 {
		return no(ReasonNoCapacity)
	}
	return Decision{Eligible: true, Limit: limit}
}
