package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
	voice "github.com/ubag/ubag/apps/gateway/internal/voice"
	"github.com/ubag/ubag/apps/gateway/internal/voiceplace"
)

// Node-aware voice placement (P5.9, UBAG_HELPER_VOICE): a call whose account lives
// on a Helper Node is hosted by that node, takes one workload slot and the
// account's lane, and no response ever names the node, its address or its profile.

const (
	placementTenant = "tenant_edge"
	placementTarget = "chatgpt_web"
	placementNodeIP = "203.0.113.7"
	placementWire   = "10.8.0.2:7443"
)

// placementFleet is a fake grant source and node state store.
type placementFleet struct {
	mu     sync.Mutex
	allocs map[string]nodes.Allocation
	states map[string]nodes.HelperState
}

func newPlacementFleet() *placementFleet {
	return &placementFleet{allocs: map[string]nodes.Allocation{}, states: map[string]nodes.HelperState{}}
}

// node adds a healthy, voice-capable node with `workloads` slots.
func (f *placementFleet) node(id string, workloads int) {
	const gib = int64(1) << 30
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allocs[id] = nodes.Allocation{
		NodeID: id, Region: "eu", Endpoint: placementWire, URISAN: nodes.NodeURISAN(id),
		CPUMillis: 3000, MemoryBytes: 5 * gib, ReservationState: nodes.ReservationKnown, State: nodes.StateActive,
		MaxBrowserWorkloads: workloads, VoiceCapable: true, UDPPortMin: 40000, UDPPortMax: 40063, NATIP: placementNodeIP,
		ValidUntil: time.Now().Add(time.Hour), Generation: 1,
	}
	f.states[id] = nodes.HelperState{NodeID: id, LastHeartbeat: time.Now(), RampedLimit: 8, HostCores: 4, HostMemoryBytes: 8 * gib}
}

func (f *placementFleet) alloc(id string, mutate func(*nodes.Allocation)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.allocs[id]
	mutate(&a)
	f.allocs[id] = a
}

func (f *placementFleet) state(id string, mutate func(*nodes.HelperState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.states[id]
	mutate(&s)
	f.states[id] = s
}

func (f *placementFleet) placer(t *testing.T) *nodes.Placer {
	t.Helper()
	p, err := nodes.NewPlacer(nodes.PlacerConfig{
		ViewTTL: -1,
		Allocations: func(context.Context, time.Time) ([]nodes.Allocation, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			out := make([]nodes.Allocation, 0, len(f.allocs))
			for _, a := range f.allocs {
				out = append(out, a)
			}
			return out, nil
		},
		State: func(_ context.Context, id string) (nodes.HelperState, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if s, ok := f.states[id]; ok {
				return s, nil
			}
			return nodes.HelperState{}, nodes.ErrNotFound
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// nodeMedia is a negotiator that terminates media on the node (what P5.11 provides).
type nodeMedia struct{ *fakeMediaNegotiator }

func (nodeMedia) HostsNodeSessions() bool { return true }

type placementEnv struct {
	srv      *Server
	h        http.Handler
	fleet    *placementFleet
	nodes    *nodes.Placer
	profiles *helperauth.MemoryProfileStore
	vp       *voiceplace.Placer
	topo     *topology.MemoryStore
	media    *fakeMediaNegotiator
}

// placementServer serves one helper-hosted environment (acct-helper-N on
// helper-env-N, no CDP endpoint) per name in accounts, each bound to node-a.
func placementServer(t *testing.T, accounts int, mutate ...func(*Config, *placementEnv)) *placementEnv {
	t.Helper()
	e := &placementEnv{fleet: newPlacementFleet(), profiles: helperauth.NewMemoryProfileStore(), topo: topology.NewMemoryStore()}
	e.fleet.node("node-a", 2)
	e.nodes = e.fleet.placer(t)
	e.vp = &voiceplace.Placer{Fleet: e.nodes, Profiles: e.profiles}
	for _, n := range []string{"1", "2", "3"}[:accounts] {
		e.topo.AddInstance(topology.BrowserInstance{InstanceID: "helper-env-" + n, TenantID: placementTenant, State: "ready"})
		e.topo.AddContext(topology.ProviderContext{ContextID: "ctx-h" + n, InstanceID: "helper-env-" + n, TenantID: placementTenant,
			TargetID: placementTarget, IdentityRef: "acct-helper-" + n, LoginState: "authenticated"})
		if _, err := e.profiles.Bind(t.Context(), placementTenant, placementTarget, "acct-helper-"+n, "node-a", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var media *fakeMediaNegotiator
	srv, h, _ := voiceTestServer(t, func(c *Config) {
		c.Topology = e.topo
		c.VoiceNodes = e.vp
		media = &fakeMediaNegotiator{answer: "v=0\r\no=- answer\r\n"}
		c.VoiceMedia = nodeMedia{media}
		for _, fn := range mutate {
			fn(c, e)
		}
	})
	e.srv, e.h, e.media = srv, h, media
	return e
}

func placementBody(identity string) string {
	return `{"target":"` + placementTarget + `","identity_ref":"` + identity + `"}`
}

// create posts a session and returns the status code, the raw body and the session.
func (e *placementEnv) create(t *testing.T, identity string) (int, string, voice.Session) {
	t.Helper()
	rec := doJSON(e.h, http.MethodPost, "/v1/voice/sessions", placementBody(identity), authHeaders(""))
	var out struct {
		Session voice.Session `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, rec.Body.String(), out.Session
}

func (e *placementEnv) stored(t *testing.T, id string) voice.Session {
	t.Helper()
	s, ok, err := e.srv.voice.Get(t.Context(), placementTenant, id)
	if err != nil || !ok {
		t.Fatalf("session %s: found=%v err=%v", id, ok, err)
	}
	return s
}

func (e *placementEnv) connect(t *testing.T, id string) (int, string) {
	t.Helper()
	rec := doJSON(e.h, http.MethodPost, "/v1/voice/sessions/"+id+"/connect", `{"sdp_offer":"v=0"}`, authHeaders(""))
	return rec.Code, rec.Body.String()
}

func (e *placementEnv) terminate(t *testing.T, id string) {
	t.Helper()
	if rec := doJSON(e.h, http.MethodPost, "/v1/voice/sessions/"+id+"/terminate", `{}`, authHeaders("")); rec.Code != http.StatusOK {
		t.Fatalf("terminate = %d %s", rec.Code, rec.Body.String())
	}
}

// assertNoInternalNames: a client must never read the node, its wire address, its
// media address, the profile handle or the lease state.
func assertNoInternalNames(t *testing.T, e *placementEnv, what, body string) {
	t.Helper()
	names := []string{"node-a", "10.8.0.2", placementNodeIP, "node_id", "lease_generation", "media_lease", "profile_ref"}
	bindings, err := e.profiles.List(t.Context(), placementTenant, placementTarget)
	if err != nil || len(bindings) == 0 {
		t.Fatalf("bindings: %v %v", bindings, err)
	}
	for _, b := range bindings {
		names = append(names, b.ProfileRef)
	}
	for _, name := range names {
		if strings.Contains(body, name) {
			t.Fatalf("%s leaks %q: %s", what, name, body)
		}
	}
}

func TestVoicePlacementHostsAHelperBoundAccountOnItsNode(t *testing.T) {
	e := placementServer(t, 1)
	code, body, s := e.create(t, "acct-helper-1")
	if code != http.StatusCreated || s.Status != voice.StatusConnecting {
		t.Fatalf("create = %d %s", code, body)
	}
	assertNoInternalNames(t, e, "create response", body)

	stored := e.stored(t, s.ID)
	if stored.NodeID != "node-a" || stored.LeaseGeneration != 1 || !stored.MediaLeaseExpires.After(time.Now()) {
		t.Fatalf("the session is not bound to its node: %+v", stored)
	}
	if e.nodes.Used("node-a") != 1 {
		t.Fatalf("one call is one node workload, used = %d", e.nodes.Used("node-a"))
	}

	rec := doJSON(e.h, http.MethodGet, "/v1/voice/sessions/"+s.ID, "", authHeaders(""))
	assertNoInternalNames(t, e, "GET session", rec.Body.String())
	rec = doJSON(e.h, http.MethodGet, "/v1/voice/sessions", "", authHeaders(""))
	assertNoInternalNames(t, e, "list sessions", rec.Body.String())

	// The offer reaches a negotiator that terminates media on the node, with the
	// fence it needs; the answer and the credential name nobody.
	code, body = e.connect(t, s.ID)
	if code != http.StatusOK || len(e.media.sessions) != 1 {
		t.Fatalf("connect = %d %s", code, body)
	}
	if got := e.media.sessions[0]; got.NodeID != "node-a" || got.LeaseGeneration != 1 {
		t.Fatalf("the negotiator must see the node and the generation: %+v", got)
	}
	assertNoInternalNames(t, e, "connect response", body)

	e.terminate(t, s.ID)
	if e.nodes.Used("node-a") != 0 || e.vp.Held() != 0 {
		t.Fatalf("terminate must return the slot at once: used=%d held=%d", e.nodes.Used("node-a"), e.vp.Held())
	}
}

func TestVoicePlacementNeverHostsANodeSessionOnThePrimaryHub(t *testing.T) {
	// The negotiator is the primary's own hub (it does not declare node hosting).
	e := placementServer(t, 1, func(c *Config, e *placementEnv) {
		c.VoiceMedia = &fakeMediaNegotiator{answer: "v=0\r\no=- primary\r\n"}
	})
	_, _, s := e.create(t, "acct-helper-1")
	hub := e.srv.voiceMedia.(*fakeMediaNegotiator)
	code, body := e.connect(t, s.ID)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "UBAG-VOICE-MEDIA-UNAVAILABLE-007") || len(hub.offers) != 0 {
		t.Fatalf("a node-bound session must not reach the primary hub: %d %s offers=%d", code, body, len(hub.offers))
	}
	assertNoInternalNames(t, e, "refusal", body)
}

func TestVoicePlacementProviderActivationRunsOnTheNodeNotThePrimary(t *testing.T) {
	var sim *voiceSimExecutor
	e := placementServer(t, 1, func(c *Config, e *placementEnv) {
		jobs := jobstore.NewMemoryStore()
		sim = &voiceSimExecutor{recordingExecutor: &recordingExecutor{}, store: jobs, state: "activated"}
		c.Jobs, c.Executor, c.VoiceProviderActivation = jobs, sim, true
	})
	_, _, s := e.create(t, "acct-helper-1")
	if code, body := e.connect(t, s.ID); code != http.StatusOK {
		t.Fatalf("connect = %d %s", code, body)
	}
	// A (wrongly) started primary activation fails at once for want of a CDP endpoint
	// and ends the session; the node activates its own browser as part of the offer.
	time.Sleep(100 * time.Millisecond)
	if jobs := sim.jobs(); len(jobs) != 0 {
		t.Fatalf("the primary must not run control jobs for a helper-hosted environment, got %d", len(jobs))
	}
	if got := e.stored(t, s.ID); got.Status != voice.StatusConnecting {
		t.Fatalf("the session must stay up, got %s (%s)", got.Status, got.LastError)
	}
}

func TestVoicePlacementOneCallIsOneNodeWorkload(t *testing.T) {
	e := placementServer(t, 2, func(c *Config, e *placementEnv) { e.fleet.node("node-a", 1) })
	code, _, first := e.create(t, "acct-helper-1")
	if code != http.StatusCreated {
		t.Fatalf("first call = %d", code)
	}
	// A second account on the same node: the node's only workload slot is taken.
	code, body, second := e.create(t, "acct-helper-2")
	if code != http.StatusAccepted || second.Status != voice.StatusQueued {
		t.Fatalf("second call = %d %s, want it queued", code, body)
	}
	assertNoInternalNames(t, e, "queued response", body)
	if e.nodes.Used("node-a") != 1 {
		t.Fatalf("used = %d", e.nodes.Used("node-a"))
	}
	if code, body := e.connect(t, second.ID); code != http.StatusConflict {
		t.Fatalf("a queued call cannot claim a full node: %d %s", code, body)
	}

	e.terminate(t, first.ID)
	if code, body := e.connect(t, second.ID); code != http.StatusOK {
		t.Fatalf("the queued call must claim the freed slot: %d %s", code, body)
	}
	if got := e.stored(t, second.ID); got.NodeID != "node-a" || got.LeaseGeneration != 1 || e.nodes.Used("node-a") != 1 {
		t.Fatalf("claimed session: %+v used=%d", got, e.nodes.Used("node-a"))
	}
}

func TestVoicePlacementCallAndJobExcludeEachOtherOnOneAccount(t *testing.T) {
	e := placementServer(t, 1)
	bindings, err := e.profiles.List(t.Context(), placementTenant, placementTarget)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings = %v %v", bindings, err)
	}
	lane := bindings[0].ProfileRef

	// A job is running on the account: the call queues.
	job, err := e.nodes.Reserve(t.Context(), "node-a", lane)
	if err != nil {
		t.Fatal(err)
	}
	code, _, s := e.create(t, "acct-helper-1")
	if code != http.StatusAccepted || s.Status != voice.StatusQueued {
		t.Fatalf("the call must queue behind a job on its account: %d %+v", code, s)
	}
	job.Release()
	if code, body := e.connect(t, s.ID); code != http.StatusOK {
		t.Fatalf("connect once the job ended = %d %s", code, body)
	}
	// ... and a job cannot take the account from a live call.
	if _, err := e.nodes.Reserve(t.Context(), "node-a", lane); !errors.Is(err, nodes.ErrLaneBusy) {
		t.Fatalf("a job on a live call's account must wait, err = %v", err)
	}
}

func TestVoicePlacementUnknownReservationNodeIsIneligible(t *testing.T) {
	e := placementServer(t, 1, func(c *Config, e *placementEnv) {
		e.fleet.alloc("node-a", func(a *nodes.Allocation) { a.ReservationState = nodes.ReservationUnknown })
	})
	code, body, s := e.create(t, "acct-helper-1")
	if code != http.StatusAccepted || s.Status != voice.StatusQueued {
		t.Fatalf("a node whose reservation is unknown must not host: %d %s", code, body)
	}
	if e.nodes.Used("node-a") != 0 {
		t.Fatal("nothing may be reserved on an ineligible node")
	}
	// The manager confirms the reservation: the queued call is promoted at connect.
	e.fleet.alloc("node-a", func(a *nodes.Allocation) { a.ReservationState = nodes.ReservationKnown })
	if code, body := e.connect(t, s.ID); code != http.StatusOK {
		t.Fatalf("connect after the reservation is known = %d %s", code, body)
	}
}

func TestVoicePlacementNeedsAVoiceCapableNode(t *testing.T) {
	e := placementServer(t, 1, func(c *Config, e *placementEnv) {
		e.fleet.alloc("node-a", func(a *nodes.Allocation) {
			a.VoiceCapable, a.UDPPortMin, a.UDPPortMax, a.NATIP = false, 0, 0, ""
		})
	})
	if code, body, _ := e.create(t, "acct-helper-1"); code != http.StatusAccepted {
		t.Fatalf("a healthy node without a media range must not host voice: %d %s", code, body)
	}
	// It still takes jobs: being unable to host voice is not a health problem.
	if nodesNow, err := e.nodes.Nodes(t.Context()); err != nil || len(nodesNow) != 1 {
		t.Fatalf("the node must stay eligible for jobs: %v %v", nodesNow, err)
	}
}

// Provider readiness (the account's login state) and host health (the node's
// grant and heartbeat) are separate facts: neither is derived from, or written
// back to, the other, and each refusal leaves the other side untouched.
func TestVoicePlacementProviderReadinessIsSeparateFromHostHealth(t *testing.T) {
	loginState := func(e *placementEnv) string {
		t.Helper()
		contexts, err := e.topo.ListContexts(t.Context(), topology.ContextFilter{TenantID: placementTenant})
		if err != nil || len(contexts) != 1 {
			t.Fatalf("contexts = %v %v", contexts, err)
		}
		return contexts[0].LoginState
	}

	t.Run("a healthy node does not make an unauthenticated account ready", func(t *testing.T) {
		e := placementServer(t, 1)
		e.topo.AddContext(topology.ProviderContext{ContextID: "ctx-h1", InstanceID: "helper-env-1", TenantID: placementTenant,
			TargetID: placementTarget, IdentityRef: "acct-helper-1", LoginState: "login_required"})
		if code, body, _ := e.create(t, "acct-helper-1"); code != http.StatusAccepted {
			t.Fatalf("an account that is not logged in must queue: %d %s", code, body)
		}
		if e.nodes.Used("node-a") != 0 {
			t.Fatal("no node slot may be taken for an account that is not ready")
		}
		if nodesNow, _ := e.nodes.Nodes(t.Context()); len(nodesNow) != 1 {
			t.Fatal("a provider that is not ready must not make the node unhealthy")
		}
		// The operator logs in out of band; the very same node now hosts the call.
		e.topo.AddContext(topology.ProviderContext{ContextID: "ctx-h1", InstanceID: "helper-env-1", TenantID: placementTenant,
			TargetID: placementTarget, IdentityRef: "acct-helper-1", LoginState: "authenticated"})
		if code, body, _ := e.create(t, "acct-helper-1"); code != http.StatusCreated {
			t.Fatalf("authenticated account on a healthy node = %d %s", code, body)
		}
	})

	t.Run("a silent node does not make an authenticated account unready", func(t *testing.T) {
		e := placementServer(t, 1, func(c *Config, e *placementEnv) {
			e.fleet.state("node-a", func(s *nodes.HelperState) { s.LastHeartbeat = time.Now().Add(-5 * time.Minute) })
		})
		code, body, s := e.create(t, "acct-helper-1")
		if code != http.StatusAccepted || s.Status != voice.StatusQueued {
			t.Fatalf("a node with missed heartbeats must not host: %d %s", code, body)
		}
		if got := loginState(e); got != "authenticated" {
			t.Fatalf("host health must never rewrite the account's login state, got %q", got)
		}
		e.fleet.state("node-a", func(s *nodes.HelperState) { s.LastHeartbeat = time.Now() })
		if code, body := e.connect(t, s.ID); code != http.StatusOK {
			t.Fatalf("the node is back: %d %s", code, body)
		}
	})
}

func TestVoicePlacementKeepsAHelperBoundAccountOffThePrimary(t *testing.T) {
	// The node is down, and the primary has a free environment for the same account
	// name: the call still queues, it is never hosted on the primary's browser.
	e := placementServer(t, 1, func(c *Config, e *placementEnv) {
		e.fleet.alloc("node-a", func(a *nodes.Allocation) { a.State = nodes.StateDraining })
	})
	code, _, s := e.create(t, "acct-helper-1")
	if code != http.StatusAccepted || s.Status != voice.StatusQueued || s.InstanceRef != "" {
		t.Fatalf("a helper-bound account must queue while its node is down: %d %+v", code, s)
	}

	// With helper-hosted voice off the same account is hosted exactly as before.
	off := placementServer(t, 1, func(c *Config, e *placementEnv) { c.VoiceNodes = nil })
	code, _, s = off.create(t, "acct-helper-1")
	if code != http.StatusCreated || off.stored(t, s.ID).NodeID != "" || off.nodes.Used("node-a") != 0 {
		t.Fatalf("flag off must be the previous behaviour: %d %+v", code, off.stored(t, s.ID))
	}
}

func TestVoicePlacementRefusesAWANCDPEndpoint(t *testing.T) {
	e := placementServer(t, 1)
	e.topo.AddInstance(topology.BrowserInstance{InstanceID: "helper-env-1", TenantID: placementTenant, State: "ready",
		RemoteEndpoint: "http://198.51.100.9:9222"})
	code, body, _ := e.create(t, "acct-helper-1")
	if code != http.StatusAccepted || e.nodes.Used("node-a") != 0 {
		t.Fatalf("a helper-bound environment with a WAN CDP endpoint must never be placed: %d %s", code, body)
	}
}

func TestVoicePlacementPrimaryHostedAccountsAreUntouched(t *testing.T) {
	e := placementServer(t, 1, func(c *Config, e *placementEnv) {
		e.topo.AddInstance(topology.BrowserInstance{InstanceID: "browser-1", TenantID: placementTenant, State: "ready"})
		e.topo.AddContext(topology.ProviderContext{ContextID: "ctx-p1", InstanceID: "browser-1", TenantID: placementTenant,
			TargetID: placementTarget, IdentityRef: "acct-local", LoginState: "authenticated"})
		// Node-hosted calls need a negotiator that can host them; the primary's own
		// account must work with the primary's hub alone.
		c.VoiceMedia = &fakeMediaNegotiator{answer: "v=0\r\no=- primary\r\n"}
	})
	code, _, s := e.create(t, "acct-local")
	if code != http.StatusCreated || e.stored(t, s.ID).NodeID != "" || e.vp.Held() != 0 {
		t.Fatalf("a primary-hosted account is not placed on a node: %d %+v", code, e.stored(t, s.ID))
	}
	if code, body := e.connect(t, s.ID); code != http.StatusOK {
		t.Fatalf("primary-hosted connect = %d %s", code, body)
	}
}

func TestVoicePlacementReleasesTheSlotOfASweptSession(t *testing.T) {
	e := placementServer(t, 1)
	_, _, s := e.create(t, "acct-helper-1")
	if e.nodes.Used("node-a") != 1 {
		t.Fatal("setup")
	}
	if swept, err := e.srv.voice.SweepExpired(t.Context(), time.Now().Add(24*time.Hour)); err != nil || !slices.Contains(swept, s.ID) {
		t.Fatalf("sweep = %v %v", swept, err)
	}
	// The periodic release in serve (or any ending path) returns the slot.
	e.srv.releaseVoiceNodeHolds(t.Context())
	if e.nodes.Used("node-a") != 0 || e.vp.Held() != 0 {
		t.Fatalf("a swept session must return its slot: used=%d held=%d", e.nodes.Used("node-a"), e.vp.Held())
	}
}

// bindFails fails BindNode: the session cannot reach its node.
type bindFails struct{ voice.Store }

func (bindFails) BindNode(context.Context, string, string, string, time.Duration, time.Time) (voice.Session, error) {
	return voice.Session{}, errors.New("store down")
}

func TestVoicePlacementEndsASessionThatCannotBeBound(t *testing.T) {
	e := placementServer(t, 1, func(c *Config, e *placementEnv) { c.VoiceStore = bindFails{c.VoiceStore} })
	rec := doJSON(e.h, http.MethodPost, "/v1/voice/sessions", placementBody("acct-helper-1"), authHeaders(""))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "UBAG-VOICE-MEDIA-UNAVAILABLE-007") {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	assertNoInternalNames(t, e, "bind failure", rec.Body.String())
	if e.nodes.Used("node-a") != 0 || e.vp.Held() != 0 {
		t.Fatalf("a session that cannot be bound must not keep its slot: used=%d held=%d", e.nodes.Used("node-a"), e.vp.Held())
	}
	sessions, err := e.srv.voice.List(t.Context(), placementTenant, "", 10)
	if err != nil || len(sessions) != 1 || sessions[0].Status != voice.StatusTerminated {
		t.Fatalf("the unbound session must be terminated, not left holding leases: %+v %v", sessions, err)
	}
}

func TestVoicePlacementCapabilitiesCountOnlyHostableAccounts(t *testing.T) {
	free := func(e *placementEnv) float64 {
		t.Helper()
		rec := doJSON(e.h, http.MethodGet, "/v1/capabilities", "", authHeaders(""))
		var response collectionResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		for _, entry := range response.Data {
			if entry["target"] == placementTarget {
				v, _ := entry["voice"].(map[string]any)
				n, _ := v["free_resources"].(float64)
				return n
			}
		}
		t.Fatal("target missing")
		return 0
	}
	e := placementServer(t, 1, func(c *Config, e *placementEnv) { c.VoiceProviderActivation = true })
	if got := free(e); got != 1 {
		t.Fatalf("a hostable account is free: %v", got)
	}
	if e.nodes.Used("node-a") != 0 {
		t.Fatal("a capability read must not reserve anything")
	}
	e.fleet.alloc("node-a", func(a *nodes.Allocation) { a.State = nodes.StateDraining })
	if got := free(e); got != 0 {
		t.Fatalf("an account whose node is draining must not be advertised: %v", got)
	}
}
