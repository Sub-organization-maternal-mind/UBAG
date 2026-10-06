package nodes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// The capacity half of helper placement (ADR-0016, UBAG_HELPER_DISPATCH, default
// off). The Placer answers two questions and nothing else: which helper nodes may
// take a placement right now, and, for one node and one identity lane, is there a
// free slot. Which tenant profile runs where is helperauth's rule (EligibleProfiles)
// and the executor's FleetPicker joins the two; this package knows no tenant, no
// job and no profile, only opaque lane keys.

var (
	// ErrNoCapacity: the node is eligible but every workload slot it may run is
	// taken. The caller holds the job and tries again; it never queues past it.
	ErrNoCapacity = errors.New("nodes: no free workload slot on the node")
	// ErrLaneBusy: another attempt is running on the same identity lane (one
	// active operation per physical provider session, decision D6).
	ErrLaneBusy = errors.New("nodes: identity lane is busy")
	// ErrNodeNotEligible: the node may not take placements now (not granted,
	// draining, expired, no heartbeat, unknown).
	ErrNodeNotEligible = errors.New("nodes: node is not eligible for placements")
)

// Admission is the full placement verdict for one helper: Evaluate (grant state,
// reservation, validity, heartbeat, pressure, ramp) and then the UBAG-side
// ceiling table as defence in depth (decision D3): the effective cap is
// min(manager grant, table), and the manager's grant is only ever clamped down.
func Admission(a Allocation, st HelperState, now time.Time) Decision {
	d := Evaluate(a.Grant(), st.LastHeartbeat, now, st.Pressure(), st.RampedLimit)
	if !d.Eligible {
		return d
	}
	if d.Limit = clampToCeiling(d.Limit, a, st); d.Limit <= 0 {
		return Decision{Reason: ReasonNoCapacity}
	}
	return d
}

// clampToCeiling scales a workload limit by the share of the grant the table
// allows. A host of unknown size has ceiling 0 and so no capacity (fail closed);
// a grant within the table is untouched; one over it is cut in proportion to the
// tighter of CPU and memory, never below one workload while the table allows any.
func clampToCeiling(limit int, a Allocation, st HelperState) int {
	cpu, mem := EffectiveCap(a.CPUMillis, a.MemoryBytes, CeilingFor(st.HostCores, st.HostMemoryBytes))
	if cpu <= 0 || mem <= 0 || limit <= 0 {
		return 0
	}
	out := limit
	if cpu < a.CPUMillis {
		out = min(out, max(1, int(int64(limit)*int64(cpu)/int64(a.CPUMillis))))
	}
	if mem < a.MemoryBytes {
		out = min(out, max(1, int(int64(limit)*mem/a.MemoryBytes)))
	}
	return out
}

// Node is one helper that may take placements now.
type Node struct {
	ID       string
	Region   string
	Endpoint string
	// Limit is the concurrent browser workloads admitted (>= 1).
	Limit int
	// VoiceCapable is the grant's own flag: the manager opened a bounded public UDP
	// range and a NAT address for this node's media (Allocation.Validate requires
	// both with it). It says nothing about host health: being listed here at all is
	// the health verdict (voice placement, P5.9, ADR-0017).
	VoiceCapable bool
}

// PlacerConfig wires a Placer to its inputs. Allocations is normally
// Poller.Current (grants as placement may use them: stale and absent grants
// already draining) or, with no manager poller, a wrapper over
// Store.ListAllocations; State is Store.GetState.
type PlacerConfig struct {
	Allocations func(ctx context.Context, now time.Time) ([]Allocation, error)
	State       func(ctx context.Context, nodeID string) (HelperState, error)
	// Now is the clock (tests inject one); nil means time.Now.
	Now func() time.Time
	// ViewTTL is how long the eligible set is reused: heartbeats arrive every 15 s,
	// so a second of staleness costs nothing and keeps a backlog of held jobs from
	// reading every node on every retry. 0 means 1 s; negative disables the cache.
	ViewTTL time.Duration
}

// Reservation is a granted slot on one node and one identity lane.
type Reservation struct {
	Node Node
	// Release returns the slot and the lane; idempotent and safe to call from any goroutine.
	Release func()
}

// Placer tracks the slots and identity lanes this process has handed out.
//
// ponytail: counts are in-process, so one primary replica per fleet. A helper
// that is busier than this process knows (a restart, a second replica) refuses
// admission and the remote runner holds the job (ADR-0014). Move the lanes into
// the shared admission store before running more than one primary.
type Placer struct {
	cfg PlacerConfig

	mu    sync.Mutex
	used  map[string]int
	lanes map[string]string // lane key -> node id
	view  []Node
	viewT time.Time
}

// NewPlacer validates cfg.
func NewPlacer(cfg PlacerConfig) (*Placer, error) {
	if cfg.Allocations == nil || cfg.State == nil {
		return nil, fmt.Errorf("%w: placer needs Allocations and State", ErrInvalid)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ViewTTL == 0 {
		cfg.ViewTTL = time.Second
	}
	return &Placer{cfg: cfg, used: map[string]int{}, lanes: map[string]string{}}, nil
}

// Nodes returns the helpers that may take placements now, ordered by node id. An
// empty list with a nil error means nothing is eligible (nothing granted, manager
// unreachable past the grace, every node draining or silent). An error means the
// answer could not be read: the caller holds its job instead of assuming "none".
func (p *Placer) Nodes(ctx context.Context) ([]Node, error) {
	now := p.cfg.Now()
	p.mu.Lock()
	if p.cfg.ViewTTL > 0 && p.view != nil && now.Sub(p.viewT) >= 0 && now.Sub(p.viewT) < p.cfg.ViewTTL {
		v := slices.Clone(p.view)
		p.mu.Unlock()
		return v, nil
	}
	p.mu.Unlock()

	allocs, err := p.cfg.Allocations(ctx, now)
	if err != nil {
		return nil, err
	}
	view := []Node{}
	for _, a := range allocs {
		st, err := p.cfg.State(ctx, a.NodeID)
		switch {
		case errors.Is(err, ErrNotFound):
			st = HelperState{NodeID: a.NodeID} // never heard from: no heartbeat, so ineligible
		case err != nil:
			return nil, err
		}
		if d := Admission(a, st, now); d.Eligible {
			view = append(view, Node{ID: a.NodeID, Region: a.Region, Endpoint: a.Endpoint, Limit: d.Limit, VoiceCapable: a.VoiceCapable})
		}
	}
	slices.SortFunc(view, func(x, y Node) int { return strings.Compare(x.ID, y.ID) })
	p.mu.Lock()
	p.view, p.viewT = view, now
	p.mu.Unlock()
	return slices.Clone(view), nil
}

// Capacity is the total workload limit of the eligible helpers (0 when none, or
// when the fleet cannot be read). The consumer sizes its pool from it.
func (p *Placer) Capacity(ctx context.Context) int {
	nodes, err := p.Nodes(ctx)
	if err != nil {
		return 0
	}
	total := 0
	for _, n := range nodes {
		total += n.Limit
	}
	return total
}

// Used reports how many slots this process has reserved on a node.
func (p *Placer) Used(nodeID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.used[nodeID]
}

// Reserve takes one workload slot on the node and the identity lane together, or
// none: ErrNodeNotEligible, ErrLaneBusy or ErrNoCapacity. A limit that shrinks
// below the slots in use evicts nothing; it only refuses new work until usage
// falls under it (ShrinkCap).
func (p *Placer) Reserve(ctx context.Context, nodeID, lane string) (Reservation, error) {
	if lane == "" {
		return Reservation{}, fmt.Errorf("%w: identity lane", ErrInvalid)
	}
	nodes, err := p.Nodes(ctx)
	if err != nil {
		return Reservation{}, err
	}
	i := slices.IndexFunc(nodes, func(n Node) bool { return n.ID == nodeID })
	if i < 0 {
		return Reservation{}, ErrNodeNotEligible
	}
	node := nodes[i]

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, busy := p.lanes[lane]; busy {
		return Reservation{}, ErrLaneBusy
	}
	if p.used[nodeID] >= node.Limit {
		return Reservation{}, ErrNoCapacity
	}
	p.used[nodeID]++
	p.lanes[lane] = nodeID
	var once sync.Once
	return Reservation{Node: node, Release: func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.used[nodeID]--; p.used[nodeID] <= 0 {
				delete(p.used, nodeID)
			}
			delete(p.lanes, lane)
		})
	}}, nil
}
