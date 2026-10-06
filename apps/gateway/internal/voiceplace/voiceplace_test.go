package voiceplace

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	"github.com/ubag/ubag/apps/gateway/internal/voice"
)

const (
	gib    = int64(1) << 30
	tenant = "tenant_a"
	target = "chatgpt_web"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// fleetFix is a fake grant source and node state store over a movable clock.
type fleetFix struct {
	mu     sync.Mutex
	now    time.Time
	allocs []nodes.Allocation
	states map[string]nodes.HelperState
	err    error
}

func newFleet() *fleetFix { return &fleetFix{now: t0, states: map[string]nodes.HelperState{}} }

// node adds a healthy, voice-capable node (a grant inside the 4c/8G ceiling row
// with `workloads` slots) and lets the test spoil it.
func (f *fleetFix) node(id string, workloads int, mutate func(*nodes.Allocation, *nodes.HelperState)) {
	a := nodes.Allocation{
		NodeID: id, Region: "eu", Endpoint: "10.8.0.2:7443", URISAN: nodes.NodeURISAN(id),
		CPUMillis: 3000, MemoryBytes: 5 * gib, ReservationState: nodes.ReservationKnown, State: nodes.StateActive,
		MaxBrowserWorkloads: workloads, VoiceCapable: true, UDPPortMin: 40000, UDPPortMax: 40063, NATIP: "203.0.113.7",
		ValidUntil: t0.Add(time.Hour), Generation: 1,
	}
	st := nodes.HelperState{NodeID: id, LastHeartbeat: t0, RampedLimit: 8, HostCores: 4, HostMemoryBytes: 8 * gib}
	if mutate != nil {
		mutate(&a, &st)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allocs = append(f.allocs, a)
	f.states[id] = st
}

func (f *fleetFix) placer(t *testing.T) *nodes.Placer {
	t.Helper()
	p, err := nodes.NewPlacer(nodes.PlacerConfig{
		ViewTTL: -1, // read every time: tests move the fleet under it
		Now:     func() time.Time { return f.now },
		Allocations: func(context.Context, time.Time) ([]nodes.Allocation, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return slices.Clone(f.allocs), f.err
		},
		State: func(_ context.Context, id string) (nodes.HelperState, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			st, ok := f.states[id]
			if !ok {
				return nodes.HelperState{}, nodes.ErrNotFound
			}
			return st, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type env struct {
	fleet    *fleetFix
	nodes    *nodes.Placer
	profiles *helperauth.MemoryProfileStore
	placer   *Placer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	f := newFleet()
	e := &env{fleet: f, nodes: f.placer(t), profiles: helperauth.NewMemoryProfileStore()}
	e.placer = &Placer{Fleet: e.nodes, Profiles: e.profiles}
	return e
}

// bind records the tenant's login of `identity` on `node` and returns its profile_ref.
func (e *env) bind(t *testing.T, identity, node string) string {
	t.Helper()
	b, err := e.profiles.Bind(t.Context(), tenant, target, identity, node, t0)
	if err != nil {
		t.Fatal(err)
	}
	return b.ProfileRef
}

func cand(identity, instance string) Candidate {
	return Candidate{Placement: voice.Placement{Identity: identity, Instance: instance}}
}

func won(id, identity, instance string) voice.Session {
	return voice.Session{ID: id, TenantID: tenant, Target: target, IdentityRef: identity, InstanceRef: instance, Status: voice.StatusConnecting}
}

func TestPlacementTakesOneNodeSlotAndTheAccountsLane(t *testing.T) {
	e := newEnv(t)
	e.fleet.node("node-a", 2, nil)
	profile := e.bind(t, "acct-1", "node-a")

	adm := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-1", "env-1")})
	if got := adm.Placements(); len(got) != 1 || got[0] != (voice.Placement{Identity: "acct-1", Instance: "env-1"}) {
		t.Fatalf("placements = %v", got)
	}
	if got := adm.Outcomes(); !slices.Equal(got, []string{Placed}) {
		t.Fatalf("outcomes = %v", got)
	}
	// Reserved for the admission already: one call is one workload on the node, and
	// a text job on the same profile cannot take the account's lane.
	if e.nodes.Used("node-a") != 1 {
		t.Fatalf("a call must count as one node workload, used = %d", e.nodes.Used("node-a"))
	}
	if _, err := e.nodes.Reserve(t.Context(), "node-a", profile); !errors.Is(err, nodes.ErrLaneBusy) {
		t.Fatalf("a job on the call's account must be refused the lane, err = %v", err)
	}

	if node := adm.Settle(won("s1", "acct-1", "env-1")); node != "node-a" {
		t.Fatalf("settle returned node %q", node)
	}
	if e.placer.Held() != 1 || e.nodes.Used("node-a") != 1 {
		t.Fatalf("the winner keeps its slot: held=%d used=%d", e.placer.Held(), e.nodes.Used("node-a"))
	}
	// A second call (another account) takes the second slot; a third finds the node full.
	e.bind(t, "acct-2", "node-a")
	e.bind(t, "acct-3", "node-a")
	adm2 := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-2", "env-2")})
	adm2.Settle(won("s2", "acct-2", "env-2"))
	adm3 := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-3", "env-3")})
	if len(adm3.Placements()) != 0 || !slices.Equal(adm3.Outcomes(), []string{NoCapacity}) {
		t.Fatalf("a full node takes no third call: %v %v", adm3.Placements(), adm3.Outcomes())
	}
}

func TestPlacementSettleKeepsOnlyTheWinnersReservation(t *testing.T) {
	e := newEnv(t)
	e.fleet.node("node-a", 2, nil)
	first := e.bind(t, "acct-1", "node-a")
	e.bind(t, "acct-2", "node-a")

	adm := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-1", "env-1"), cand("acct-2", "env-2")})
	if len(adm.Placements()) != 2 || e.nodes.Used("node-a") != 2 {
		t.Fatalf("both candidates are held while the store decides: %v used=%d", adm.Placements(), e.nodes.Used("node-a"))
	}
	// The store gave the call to the second account.
	if node := adm.Settle(won("s1", "acct-2", "env-2")); node != "node-a" {
		t.Fatalf("settle returned node %q", node)
	}
	if e.nodes.Used("node-a") != 1 || e.placer.Held() != 1 {
		t.Fatalf("the loser's slot must be returned: used=%d held=%d", e.nodes.Used("node-a"), e.placer.Held())
	}
	if res, err := e.nodes.Reserve(t.Context(), "node-a", first); err != nil {
		t.Fatalf("the loser's lane must be free again: %v", err)
	} else {
		res.Release()
	}
	// A failed or queued store call settles with no session and keeps nothing.
	adm = e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-1", "env-1")})
	if node := adm.Settle(voice.Session{}); node != "" || e.nodes.Used("node-a") != 1 {
		t.Fatalf("an unsettled admission leaked a slot: node=%q used=%d", node, e.nodes.Used("node-a"))
	}
}

func TestPlacementPassesPrimaryHostedAccountsThrough(t *testing.T) {
	e := newEnv(t) // no node at all
	adm := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-local", "browser-1")})
	if got := adm.Placements(); len(got) != 1 || len(adm.Outcomes()) != 0 {
		t.Fatalf("an account with no profile on a node is hosted on the primary as before: %v %v", got, adm.Outcomes())
	}
	if node := adm.Settle(won("s1", "acct-local", "browser-1")); node != "" || e.placer.Held() != 0 {
		t.Fatalf("primary-hosted: node=%q held=%d", node, e.placer.Held())
	}

	// Another tenant's login of the same account name is not this tenant's profile.
	if _, err := e.profiles.Bind(t.Context(), "tenant_b", target, "acct-local", "node-a", t0); err != nil {
		t.Fatal(err)
	}
	adm = e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-local", "browser-1")})
	if len(adm.Placements()) != 1 {
		t.Fatalf("another tenant's profile must not move this tenant's account: %v", adm.Placements())
	}

	// The flag-off placer is nil: everything passes, nothing is held, nothing panics.
	var off *Placer
	adm = off.Admit(t.Context(), tenant, target, []Candidate{cand("a", "b"), cand("c", "d")})
	if len(adm.Placements()) != 2 || adm.Settle(won("s2", "a", "b")) != "" || off.Held() != 0 || off.ReleaseEnded(t.Context(), voice.NewMemoryStore(), t0) != 0 {
		t.Fatal("a nil placer must be a pass-through")
	}
	var none *Admission
	if none.Settle(voice.Session{}) != "" || none.Placements() != nil || none.Outcomes() != nil {
		t.Fatal("a nil admission must be inert")
	}
}

func TestPlacementRefusesWhatTheNodeCannotHost(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*nodes.Allocation, *nodes.HelperState)
		want   string
	}{
		{"a reservation the manager has not confirmed", func(a *nodes.Allocation, _ *nodes.HelperState) { a.ReservationState = nodes.ReservationUnknown }, NodeUnavailable},
		{"a draining node", func(a *nodes.Allocation, _ *nodes.HelperState) { a.State = nodes.StateDraining }, NodeUnavailable},
		{"a revoked node", func(a *nodes.Allocation, _ *nodes.HelperState) { a.State = nodes.StateRevoked }, NodeUnavailable},
		{"three missed heartbeats", func(_ *nodes.Allocation, s *nodes.HelperState) { s.LastHeartbeat = t0.Add(-time.Minute) }, NodeUnavailable},
		{"an expired grant", func(a *nodes.Allocation, _ *nodes.HelperState) { a.ValidUntil = t0.Add(-time.Second) }, NodeUnavailable},
		{"a host of unknown size (the ceiling fails closed)", func(_ *nodes.Allocation, s *nodes.HelperState) { s.HostCores, s.HostMemoryBytes = 0, 0 }, NodeUnavailable},
		{"a healthy node with no media range", func(a *nodes.Allocation, _ *nodes.HelperState) {
			a.VoiceCapable, a.UDPPortMin, a.UDPPortMax, a.NATIP = false, 0, 0, ""
		}, NotVoiceCapable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.fleet.node("node-a", 2, c.mutate)
			e.bind(t, "acct-1", "node-a")
			adm := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-1", "env-1")})
			if len(adm.Placements()) != 0 || !slices.Equal(adm.Outcomes(), []string{c.want}) {
				t.Fatalf("placements=%v outcomes=%v, want none and %s", adm.Placements(), adm.Outcomes(), c.want)
			}
			if e.nodes.Used("node-a") != 0 || e.placer.Held() != 0 {
				t.Fatal("a refused candidate must take nothing")
			}
		})
	}

	t.Run("a node the fleet has never heard of", func(t *testing.T) {
		e := newEnv(t)
		e.bind(t, "acct-1", "node-ghost")
		adm := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-1", "env-1")})
		if len(adm.Placements()) != 0 || !slices.Equal(adm.Outcomes(), []string{NodeUnavailable}) {
			t.Fatalf("placements=%v outcomes=%v", adm.Placements(), adm.Outcomes())
		}
	})

	t.Run("a job on the same account holds its lane", func(t *testing.T) {
		e := newEnv(t)
		e.fleet.node("node-a", 2, nil)
		profile := e.bind(t, "acct-1", "node-a")
		job, err := e.nodes.Reserve(t.Context(), "node-a", profile)
		if err != nil {
			t.Fatal(err)
		}
		adm := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-1", "env-1")})
		if len(adm.Placements()) != 0 || !slices.Equal(adm.Outcomes(), []string{IdentityBusy}) {
			t.Fatalf("placements=%v outcomes=%v", adm.Placements(), adm.Outcomes())
		}
		job.Release()
		if adm = e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-1", "env-1")}); len(adm.Placements()) != 1 {
			t.Fatalf("the lane is free again: %v", adm.Outcomes())
		}
	})

	t.Run("a helper-bound environment with a CDP endpoint in the topology", func(t *testing.T) {
		e := newEnv(t)
		e.fleet.node("node-a", 2, nil)
		e.bind(t, "acct-1", "node-a")
		c := cand("acct-1", "env-1")
		c.CDPEndpoint = true
		adm := e.placer.Admit(t.Context(), tenant, target, []Candidate{c})
		if len(adm.Placements()) != 0 || !slices.Equal(adm.Outcomes(), []string{WANEndpoint}) || e.nodes.Used("node-a") != 0 {
			t.Fatalf("a WAN CDP endpoint must never be driven: %v %v", adm.Placements(), adm.Outcomes())
		}
	})

	t.Run("an unreadable fleet or registry offers no helper-bound placement", func(t *testing.T) {
		e := newEnv(t)
		e.fleet.node("node-a", 2, nil)
		e.bind(t, "acct-1", "node-a")
		e.fleet.err = errors.New("grants unreadable")
		adm := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-1", "env-1"), cand("acct-local", "browser-1")})
		if got := adm.Placements(); len(got) != 1 || got[0].Identity != "acct-local" || !slices.Equal(adm.Outcomes(), []string{Failed}) {
			t.Fatalf("placements=%v outcomes=%v", got, adm.Outcomes())
		}
		// Without the registry nothing can be told apart from a helper-bound account.
		broken := &Placer{Fleet: e.nodes, Profiles: failingProfiles{e.profiles}}
		adm = broken.Admit(t.Context(), tenant, target, []Candidate{cand("acct-local", "browser-1")})
		if len(adm.Placements()) != 0 || !slices.Equal(adm.Outcomes(), []string{Failed}) {
			t.Fatalf("an unreadable registry must offer nothing: %v %v", adm.Placements(), adm.Outcomes())
		}
		unset := &Placer{}
		if adm = unset.Admit(t.Context(), tenant, target, []Candidate{cand("acct-local", "browser-1")}); len(adm.Placements()) != 0 {
			t.Fatal("a placer with no fleet must fail closed")
		}
	})
}

type failingProfiles struct{ helperauth.ProfileStore }

func (failingProfiles) List(context.Context, string, string) ([]helperauth.Binding, error) {
	return nil, errors.New("registry down")
}

// An account bound on two nodes goes to the first that can host it.
func TestPlacementFallsBackToAnotherNodeOfTheSameAccount(t *testing.T) {
	e := newEnv(t)
	e.fleet.node("node-a", 2, func(a *nodes.Allocation, _ *nodes.HelperState) { a.ReservationState = nodes.ReservationUnknown })
	e.fleet.node("node-b", 2, nil)
	e.bind(t, "acct-1", "node-a")
	e.bind(t, "acct-1", "node-b")
	adm := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-1", "env-1")})
	if node := adm.Settle(won("s1", "acct-1", "env-1")); node != "node-b" {
		t.Fatalf("placed on %q, want node-b (node-a's reservation is unknown)", node)
	}
}

// Available answers from the same rules but takes nothing and counts nothing.
func TestPlacementAvailableIsReadOnly(t *testing.T) {
	e := newEnv(t)
	e.fleet.node("node-a", 1, nil)
	e.bind(t, "acct-1", "node-a")
	cands := []Candidate{cand("acct-1", "env-1"), cand("acct-local", "browser-1")}
	if got := e.placer.Available(t.Context(), tenant, target, cands); len(got) != 2 {
		t.Fatalf("available = %v", got)
	}
	if e.nodes.Used("node-a") != 0 || e.placer.Held() != 0 {
		t.Fatal("Available reserved something")
	}
	adm := e.placer.Admit(t.Context(), tenant, target, cands[:1])
	adm.Settle(won("s1", "acct-1", "env-1"))
	if got := e.placer.Available(t.Context(), tenant, target, cands); len(got) != 1 || got[0].Identity != "acct-local" {
		t.Fatalf("a full node is not available, the primary-hosted account is: %v", got)
	}
}

// The voice store is the truth for who still reserves an environment: a call's
// slot returns when the session ends, not before, and a terminating hold keeps it.
func TestPlacementReleaseEndedFollowsTheVoiceStore(t *testing.T) {
	e := newEnv(t)
	e.fleet.node("node-a", 2, nil)
	e.bind(t, "acct-1", "node-a")
	store := voice.NewMemoryStore()

	adm := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-1", "env-1")})
	s, err := store.Reserve(t.Context(), voice.ReserveRequest{
		SessionID: "s1", TenantID: tenant, Target: target, Placements: adm.Placements(), LeaseTTL: time.Hour, Now: t0,
	})
	if err != nil || s.Status != voice.StatusConnecting {
		t.Fatalf("reserve: %+v %v", s, err)
	}
	if adm.Settle(s) != "node-a" {
		t.Fatal("settle")
	}
	if n := e.placer.ReleaseEnded(t.Context(), store, t0); n != 0 || e.nodes.Used("node-a") != 1 {
		t.Fatalf("a live session keeps its slot: released=%d used=%d", n, e.nodes.Used("node-a"))
	}

	// Terminated with a hold: the provider UI is still being torn down on the node.
	if err := store.BeginTerminate(t.Context(), tenant, "s1", t0, "terminated_by_client", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := e.placer.ReleaseEnded(t.Context(), store, t0.Add(time.Second)); n != 0 || e.nodes.Used("node-a") != 1 {
		t.Fatalf("a terminating hold keeps the slot: released=%d used=%d", n, e.nodes.Used("node-a"))
	}
	// A store that cannot be read frees nothing.
	if n := e.placer.ReleaseEnded(t.Context(), listFails{store}, t0.Add(time.Minute)); n != 0 || e.nodes.Used("node-a") != 1 {
		t.Fatalf("an unreadable store must keep the slot: released=%d used=%d", n, e.nodes.Used("node-a"))
	}
	// The deactivate ack (or the hold's cap) ends the reservation, and the slot returns.
	if err := store.ReleaseHold(t.Context(), tenant, "s1", voice.LeaseFence{}, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if n := e.placer.ReleaseEnded(t.Context(), store, t0.Add(3*time.Second)); n != 1 || e.nodes.Used("node-a") != 0 || e.placer.Held() != 0 {
		t.Fatalf("released=%d used=%d held=%d", n, e.nodes.Used("node-a"), e.placer.Held())
	}
	if n := e.placer.ReleaseEnded(t.Context(), store, t0.Add(4*time.Second)); n != 0 {
		t.Fatalf("release is idempotent, got %d", n)
	}
}

// A session settled while the store is being read is not judged "ended" from that
// older read: the slot of a call that has just been admitted must not be freed.
func TestPlacementReleaseEndedNeverFreesAFreshAdmission(t *testing.T) {
	e := newEnv(t)
	e.fleet.node("node-a", 2, nil)
	e.bind(t, "acct-1", "node-a")
	store := voice.NewMemoryStore()
	var fresh *Admission
	hooked := listHook{Store: store, before: func() {
		// Mid-read: the admission commits and settles, after the snapshot the store returns.
		fresh = e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-1", "env-1")})
		fresh.Settle(won("fresh", "acct-1", "env-1"))
	}}
	// One older hold so ReleaseEnded actually reads the store.
	e.bind(t, "acct-2", "node-a")
	old := e.placer.Admit(t.Context(), tenant, target, []Candidate{cand("acct-2", "env-2")})
	old.Settle(won("old", "acct-2", "env-2"))

	n := e.placer.ReleaseEnded(t.Context(), hooked, t0)
	if n != 1 || e.placer.Held() != 1 {
		t.Fatalf("released=%d held=%d: only the old, unreserved session may be freed", n, e.placer.Held())
	}
	e.placer.mu.Lock()
	_, kept := e.placer.holds["fresh"]
	e.placer.mu.Unlock()
	if !kept {
		t.Fatal("the fresh admission lost its slot to an older snapshot")
	}
}

type listFails struct{ voice.Store }

func (listFails) ListLeaseHolders(context.Context, time.Time) ([]voice.LeaseHolder, error) {
	return nil, errors.New("store down")
}

// listHook runs before ListLeaseHolders returns the (older) answer.
type listHook struct {
	voice.Store
	before func()
}

func (h listHook) ListLeaseHolders(ctx context.Context, now time.Time) ([]voice.LeaseHolder, error) {
	got, err := h.Store.ListLeaseHolders(ctx, now) // the snapshot predates the hook
	h.before()
	return got, err
}
