// Package voiceplace places helper-hosted voice calls (P5.9, ADR-0017; behind
// UBAG_HELPER_VOICE, default off).
//
// A voice session is admitted against the voice store by (provider account,
// browser environment) pairs the primary's topology offers. When the account's
// browser profile lives on a Helper Node (the tenant's profile registry binds the
// account to a node), the call must also be hosted by that node, and this package
// answers whether it can be:
//
//   - the node is eligible for placements right now (nodes.Placer: an active
//     grant with a KNOWN reservation, a live heartbeat, no drain, within its
//     admission limit), which is host health;
//   - the node is voice capable (the manager opened a bounded public UDP range and
//     a NAT address for its media);
//   - the call takes ONE node workload slot and the account's identity lane, the
//     same pair a text job on that profile would take, so a call and a job on one
//     account exclude each other and a call counts once against the node's limit.
//
// Provider readiness, the account's login state, is NOT decided here and never
// feeds back into the node: the caller's topology read has already left out
// accounts that are not authenticated, and a node refusing a call says nothing
// about the account. The two are separate facts with separate outcomes.
//
// A helper-bound account is never offered to the primary's own browsers: when its
// node cannot take the call the candidate is dropped and the session queues.
package voiceplace

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/helpermetrics"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	"github.com/ubag/ubag/apps/gateway/internal/voice"
)

// Outcomes of one helper-hosted candidate (also the bounded metric labels in
// helpermetrics). Provider readiness is deliberately not one of them.
const (
	Placed          = "placed"
	NodeUnavailable = "node_unavailable"  // host health: not granted, draining, silent, reservation unknown, unknown node
	NotVoiceCapable = "not_voice_capable" // healthy host without a media UDP range and NAT address
	NoCapacity      = "no_capacity"       // every workload slot of the node is taken
	IdentityBusy    = "identity_busy"     // a job or another call holds the account's lane
	WANEndpoint     = "wan_endpoint"      // the helper-bound environment carries a CDP endpoint in the topology
	Failed          = "error"             // the fleet or the profile registry could not be read
)

// Candidate is one (account, environment) pair the topology offers.
type Candidate struct {
	voice.Placement
	// CDPEndpoint says the environment's instance registered a CDP endpoint in the
	// primary's topology: a browser the primary drives over the network. A
	// helper-hosted environment is loopback on its node and carries none (D7: the
	// browser, audio and media are co-located), so a helper-bound account whose
	// environment has one is refused rather than driven across the WAN.
	CDPEndpoint bool
}

// Placer holds the node slots of live helper-hosted sessions. The zero value is
// not usable; a nil *Placer is the flag-off placer (every candidate passes
// through, nothing is held).
//
// ponytail: slots and lanes are in this process (nodes.Placer is one primary
// replica per fleet, ADR-0016). After a restart a surviving node-bound session no
// longer holds its slot here, and the helper's own admission is the backstop; move
// both into the shared admission store before running more than one primary.
type Placer struct {
	Fleet    *nodes.Placer
	Profiles helperauth.ProfileStore

	mu    sync.Mutex
	holds map[string]nodes.Reservation // session id -> node slot + identity lane
}

// Admission is the node side of one voice admission in flight: the candidates the
// store may use and the reservations taken for the helper-hosted ones. Settle
// must be called exactly once after the store call returns.
type Admission struct {
	p          *Placer
	dry        bool // Available: nothing is taken and nothing is counted
	placements []voice.Placement
	held       map[voice.Placement]held
	outcomes   []string
}

type held struct {
	nodeID string
	res    nodes.Reservation
}

// Admit decides, for every candidate, whether it may be offered to the voice
// store. A candidate whose account has no profile on a node passes through
// (primary-hosted, as before); a helper-bound one is kept only with a node slot
// and the account's lane reserved, and dropped otherwise.
func (p *Placer) Admit(ctx context.Context, tenantID, target string, cands []Candidate) *Admission {
	return p.evaluate(ctx, tenantID, target, cands, true)
}

// Available is Admit without taking anything: the candidates whose node could take
// a call now. It cannot see a busy identity lane, so it is advisory, like the
// capability view it feeds.
func (p *Placer) Available(ctx context.Context, tenantID, target string, cands []Candidate) []voice.Placement {
	return p.evaluate(ctx, tenantID, target, cands, false).placements
}

func (p *Placer) evaluate(ctx context.Context, tenantID, target string, cands []Candidate, take bool) *Admission {
	a := &Admission{p: p, dry: !take, held: map[voice.Placement]held{}}
	if p == nil {
		for _, c := range cands {
			a.placements = append(a.placements, c.Placement)
		}
		return a
	}
	if p.Fleet == nil || p.Profiles == nil {
		slog.Error("voice node placement is not configured; no voice placement is offered")
		a.record(Failed)
		return a
	}
	bindings, err := p.Profiles.List(ctx, tenantID, target)
	if err != nil {
		// Without the registry nothing can be told apart from a helper-bound
		// account, and a helper-bound account must never run on the primary.
		slog.Warn("voice node placement: profile registry unreadable; no voice placement is offered", "error", err)
		a.record(Failed)
		return a
	}
	var view []nodes.Node
	viewRead := false
	for _, c := range cands {
		own := boundTo(bindings, tenantID, target, c.Identity)
		if len(own) == 0 {
			a.placements = append(a.placements, c.Placement) // primary-hosted
			continue
		}
		if c.CDPEndpoint {
			a.record(WANEndpoint)
			continue
		}
		if !viewRead {
			if view, err = p.Fleet.Nodes(ctx); err != nil {
				slog.Warn("voice node placement: fleet unreadable", "error", err)
			}
			viewRead = true
		}
		if err != nil {
			a.record(Failed)
			continue
		}
		h, outcome := p.place(ctx, view, own, take)
		a.record(outcome)
		if outcome != Placed {
			continue
		}
		a.placements = append(a.placements, c.Placement)
		if take {
			a.held[c.Placement] = h
		}
	}
	return a
}

// place reserves (or, dry, only checks) one node among the account's profiles. The
// first node that can host wins; otherwise the refusal of the last one is the
// outcome. A node that is not in the view is, for this call, unavailable.
func (p *Placer) place(ctx context.Context, view []nodes.Node, own []helperauth.Binding, take bool) (held, string) {
	outcome := NodeUnavailable
	for _, b := range own {
		i := slices.IndexFunc(view, func(n nodes.Node) bool { return n.ID == b.NodeID })
		if i < 0 {
			outcome = NodeUnavailable
			continue
		}
		node := view[i]
		if !node.VoiceCapable {
			outcome = NotVoiceCapable
			continue
		}
		if !take {
			if p.Fleet.Used(node.ID) >= node.Limit {
				outcome = NoCapacity
				continue
			}
			return held{nodeID: node.ID}, Placed
		}
		res, err := p.Fleet.Reserve(ctx, node.ID, b.ProfileRef)
		switch {
		case err == nil:
			if !res.Node.VoiceCapable { // the grant changed between the two reads
				res.Release()
				outcome = NotVoiceCapable
				continue
			}
			return held{nodeID: node.ID, res: res}, Placed
		case errors.Is(err, nodes.ErrLaneBusy):
			outcome = IdentityBusy
		case errors.Is(err, nodes.ErrNoCapacity):
			outcome = NoCapacity
		case errors.Is(err, nodes.ErrNodeNotEligible):
			outcome = NodeUnavailable
		default:
			slog.Warn("voice node placement: reserving a node slot failed", "error", err)
			outcome = Failed
		}
	}
	return held{}, outcome
}

// boundTo returns the tenant's active profiles of the account (identity) for the
// target: the nodes whose browser holds its login.
func boundTo(all []helperauth.Binding, tenantID, target, identity string) []helperauth.Binding {
	var own []helperauth.Binding
	for _, b := range all {
		if b.TenantID == tenantID && b.Provider == target && b.State == helperauth.ProfileActive &&
			b.IdentityRef == identity && b.NodeID != "" && b.ProfileRef != "" {
			own = append(own, b)
		}
	}
	return own
}

func (a *Admission) record(outcome string) {
	a.outcomes = append(a.outcomes, outcome)
	if !a.dry {
		helpermetrics.RecordVoicePlacement(outcome)
	}
}

// Placements are the candidates the voice store may admit, in the caller's order.
func (a *Admission) Placements() []voice.Placement {
	if a == nil {
		return nil
	}
	return a.placements
}

// Outcomes lists the outcome of every helper-hosted candidate (and of a failed
// read), in order; primary-hosted candidates are not counted.
func (a *Admission) Outcomes() []string {
	if a == nil {
		return nil
	}
	return slices.Clone(a.outcomes)
}

// Settle ends the admission with the session the store returned (the zero Session
// when the call failed). The reservation of the pair the session won is kept for
// the session's life and its node id returned; every other reservation is
// released. A queued session, an error and a primary-hosted win all return "".
func (a *Admission) Settle(won voice.Session) (nodeID string) {
	if a == nil {
		return ""
	}
	winner := voice.Placement{Identity: won.IdentityRef, Instance: won.InstanceRef}
	for pl, h := range a.held {
		if pl == winner && winner.Identity != "" && won.ID != "" {
			a.p.mu.Lock()
			if a.p.holds == nil {
				a.p.holds = map[string]nodes.Reservation{}
			}
			a.p.holds[won.ID] = h.res
			a.p.mu.Unlock()
			nodeID = h.nodeID
			continue
		}
		h.res.Release()
	}
	a.held = nil
	return nodeID
}

// ProfileRef is the node-side identity of a helper-hosted account: the tenant's ACTIVE
// profile_ref for (target, identity) on node. It is what a text job on that profile
// takes on the node's identity gate, so a call the node admits under it excludes a
// job on the same account there (P5.11). voice.ErrNoProfile when the account has no
// active profile on the node (revoked, or never bound); the registry is tenant-scoped,
// so another tenant's profile never matches.
func (p *Placer) ProfileRef(ctx context.Context, tenantID, target, identityRef, nodeID string) (string, error) {
	if p == nil || p.Profiles == nil {
		return "", voice.ErrNoProfile
	}
	bindings, err := p.Profiles.List(ctx, tenantID, target)
	if err != nil {
		return "", err
	}
	for _, b := range boundTo(bindings, tenantID, target, identityRef) {
		if b.NodeID == nodeID {
			return b.ProfileRef, nil
		}
	}
	return "", voice.ErrNoProfile
}

// Held reports how many sessions this process holds a node slot for.
func (p *Placer) Held() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.holds)
}

// ReleaseEnded frees the node slot and identity lane of every session that no
// longer reserves its environment: ended, swept for a lapsed lease, or past its
// terminating hold (the voice store is the truth, the same one the job consumer's
// lane probe reads). A store error frees nothing (fail closed). It returns how
// many sessions were released and is safe to call from any goroutine.
func (p *Placer) ReleaseEnded(ctx context.Context, store voice.Store, now time.Time) int {
	if p == nil || store == nil {
		return 0
	}
	// The sessions to judge are read BEFORE the store: a session settled after the
	// store read is not judged, so a fresh admission is never released as "ended".
	p.mu.Lock()
	ids := make([]string, 0, len(p.holds))
	for id := range p.holds {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	if len(ids) == 0 {
		return 0
	}
	holders, err := store.ListLeaseHolders(ctx, now)
	if err != nil {
		slog.Warn("voice node placement: lease holders unreadable; node slots stay held", "error", err)
		return 0
	}
	live := make(map[string]struct{}, len(holders))
	for _, h := range holders {
		live[h.SessionID] = struct{}{}
	}
	var release []nodes.Reservation
	p.mu.Lock()
	for _, id := range ids {
		if _, ok := live[id]; ok {
			continue
		}
		if r, ok := p.holds[id]; ok {
			release = append(release, r)
			delete(p.holds, id)
		}
	}
	p.mu.Unlock()
	for _, r := range release {
		r.Release()
	}
	return len(release)
}
