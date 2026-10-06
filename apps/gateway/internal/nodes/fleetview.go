package nodes

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

// The operator read view of the fleet (GET /v1/fleet/nodes and /v1/fleet/summary,
// P6.2). It is read-only and joins what placement already reads: the grants as
// placement may use them, the helper state and the slots this process reserved.
// The wire shapes match the closed FleetNode and FleetSummary schemas, so there
// is deliberately no address, hostname, endpoint or certificate field anywhere
// here (the node's endpoint and pins stay inside Allocation and the registry).

// FleetNode is one node as the operator sees it (OpenAPI FleetNode).
type FleetNode struct {
	NodeID           string           `json:"node_id"`
	Label            string           `json:"label"`
	Region           string           `json:"region"`
	State            string           `json:"state"`
	IneligibleReason *string          `json:"ineligible_reason"`
	HeartbeatAt      *time.Time       `json:"heartbeat_at"`
	Grant            FleetGrant       `json:"grant"`
	Usage            FleetUsage       `json:"usage"`
	Pressure         FleetPressure    `json:"pressure"`
	Readiness        []FleetReadiness `json:"readiness"`
}

// FleetGrant is the grant a node runs under (OpenAPI FleetGrant).
type FleetGrant struct {
	Generation          int64     `json:"generation"`
	State               string    `json:"state"`
	ReservationState    string    `json:"reservation_state"`
	ValidUntil          time.Time `json:"valid_until"`
	MaxBrowserWorkloads int       `json:"max_browser_workloads"`
	CPUMillis           int       `json:"cpu_millis"`
	MemoryBytes         int64     `json:"memory_bytes"`
	VoiceCapable        bool      `json:"voice_capable"`
}

// FleetUsage is the slots in use and the admission limit (OpenAPI FleetUsage).
type FleetUsage struct {
	WorkloadsInUse int `json:"workloads_in_use"`
	AdmissionLimit int `json:"admission_limit"`
}

// FleetPressure is the pressure hysteresis state (OpenAPI FleetPressure).
type FleetPressure struct {
	AdmissionReduced bool       `json:"admission_reduced"`
	RecoverAt        *time.Time `json:"recover_at"`
}

// FleetReadiness is the aggregate provider-session readiness of a node (OpenAPI
// FleetReadiness). It is empty until the gateway consumes the read-only readiness
// probe.
type FleetReadiness struct {
	Target       string    `json:"target"`
	SessionState string    `json:"session_state"`
	Count        int       `json:"count"`
	CheckedAt    time.Time `json:"checked_at"`
}

// FleetSummary is the whole fleet in one object (OpenAPI FleetSummary minus the
// envelope fields the handler adds).
type FleetSummary struct {
	NodesTotal           int            `json:"nodes_total"`
	NodesByState         map[string]int `json:"nodes_by_state"`
	WorkloadLimitTotal   int            `json:"workload_limit_total"`
	WorkloadsInUseTotal  int            `json:"workloads_in_use_total"`
	NodesPressureReduced int            `json:"nodes_pressure_reduced"`
	HeldByReason         map[string]int `json:"held_by_reason"`
}

// Node states of FleetNode.State.
const (
	FleetEligible           = "eligible"
	FleetIneligible         = "ineligible"
	FleetDraining           = "draining"
	FleetLost               = "lost"
	FleetUnknownReservation = "unknown_reservation"
)

// FleetViewConfig wires a FleetView. Allocations and State are the placer's
// inputs (Poller.Current, or the store's list, and Store.GetState); Used is
// Placer.Used (nil: nothing reserved); Held returns the queued jobs held per fine
// reason (nil: none).
type FleetViewConfig struct {
	Allocations func(ctx context.Context, now time.Time) ([]Allocation, error)
	State       func(ctx context.Context, nodeID string) (HelperState, error)
	Used        func(nodeID string) int
	Held        func() map[string]int
	Now         func() time.Time
}

// FleetView renders the operator fleet view. It holds no state.
type FleetView struct{ cfg FleetViewConfig }

// NewFleetView validates cfg.
func NewFleetView(cfg FleetViewConfig) (*FleetView, error) {
	if cfg.Allocations == nil || cfg.State == nil {
		return nil, fmt.Errorf("%w: fleet view needs Allocations and State", ErrInvalid)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &FleetView{cfg: cfg}, nil
}

// Nodes returns every node ordered by node id (at most MaxNodes). A store error
// is returned as is: the caller answers 500, never a partial or empty fleet.
func (v *FleetView) Nodes(ctx context.Context) ([]FleetNode, error) {
	now := v.cfg.Now()
	allocs, err := v.cfg.Allocations(ctx, now)
	if err != nil {
		return nil, err
	}
	allocs = slices.Clone(allocs)
	slices.SortFunc(allocs, func(x, y Allocation) int { return strings.Compare(x.NodeID, y.NodeID) })
	if len(allocs) > MaxNodes {
		allocs = allocs[:MaxNodes]
	}
	out := make([]FleetNode, 0, len(allocs))
	for _, a := range allocs {
		st, err := v.cfg.State(ctx, a.NodeID)
		switch {
		case errors.Is(err, ErrNotFound):
			st = HelperState{NodeID: a.NodeID} // never heard from
		case err != nil:
			return nil, err
		}
		out = append(out, v.node(a, st, now))
	}
	return out, nil
}

func (v *FleetView) node(a Allocation, st HelperState, now time.Time) FleetNode {
	d := Admission(a, st, now)
	n := FleetNode{
		NodeID: a.NodeID, Label: a.NodeID, Region: a.Region, State: FleetEligible,
		Grant: FleetGrant{
			Generation: a.Generation, State: a.State, ReservationState: a.ReservationState, ValidUntil: a.ValidUntil.UTC(),
			MaxBrowserWorkloads: a.MaxBrowserWorkloads, CPUMillis: a.CPUMillis, MemoryBytes: a.MemoryBytes, VoiceCapable: a.VoiceCapable,
		},
		Usage:     FleetUsage{AdmissionLimit: d.Limit},
		Pressure:  FleetPressure{AdmissionReduced: st.PressureReduced},
		Readiness: []FleetReadiness{},
	}
	if v.cfg.Used != nil {
		n.Usage.WorkloadsInUse = v.cfg.Used(a.NodeID)
	}
	if !st.LastHeartbeat.IsZero() {
		t := st.LastHeartbeat.UTC()
		n.HeartbeatAt = &t
	}
	if st.PressureReduced && !st.PressureCalmSince.IsZero() {
		t := st.PressureCalmSince.Add(recoverHold).UTC()
		n.Pressure.RecoverAt = &t
	}
	if d.Eligible {
		return n
	}
	switch d.Reason {
	case ReasonDraining:
		n.State = FleetDraining
	case ReasonReservationUnk:
		n.State = FleetUnknownReservation
	case ReasonHeartbeatMissed:
		n.State = FleetLost
	default: // revoked, grant_expired, no_capacity
		n.State = FleetIneligible
		reason := d.Reason
		n.IneligibleReason = &reason
	}
	return n
}

// Summary aggregates Nodes plus the held-job counts.
func (v *FleetView) Summary(ctx context.Context) (FleetSummary, error) {
	list, err := v.Nodes(ctx)
	if err != nil {
		return FleetSummary{}, err
	}
	s := FleetSummary{
		NodesTotal: len(list),
		NodesByState: map[string]int{
			FleetEligible: 0, FleetIneligible: 0, FleetDraining: 0, FleetLost: 0, FleetUnknownReservation: 0,
		},
		HeldByReason: map[string]int{},
	}
	for _, n := range list {
		s.NodesByState[n.State]++
		s.WorkloadLimitTotal += n.Usage.AdmissionLimit
		s.WorkloadsInUseTotal += n.Usage.WorkloadsInUse
		if n.Pressure.AdmissionReduced {
			s.NodesPressureReduced++
		}
	}
	if v.cfg.Held != nil {
		for k, c := range v.cfg.Held() {
			if c > 0 {
				s.HeldByReason[k] = c
			}
		}
	}
	return s, nil
}

// HelperHosts returns the lowercase hosts of every granted helper endpoint, so a
// browser instance reached through one is never reported with its remote endpoint.
func (v *FleetView) HelperHosts(ctx context.Context) (map[string]bool, error) {
	allocs, err := v.cfg.Allocations(ctx, v.cfg.Now())
	if err != nil {
		return nil, err
	}
	hosts := make(map[string]bool, len(allocs))
	for _, a := range allocs {
		if h, _, err := net.SplitHostPort(a.Endpoint); err == nil {
			hosts[strings.ToLower(h)] = true
		}
	}
	return hosts, nil
}
