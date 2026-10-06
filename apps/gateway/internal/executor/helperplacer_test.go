package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/conversations"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// placerRig is a FleetPicker over a real memory node store and profile store.
type placerRig struct {
	t        *testing.T
	nodes    *nodes.MemoryStore
	profiles *helperauth.MemoryProfileStore
	picker   *FleetPicker
	gen      map[string]int64
	allocErr error
	listErr  error
}

type failingProfiles struct {
	helperauth.ProfileStore
	err error
}

func (f failingProfiles) List(context.Context, string, string) ([]helperauth.Binding, error) {
	return nil, f.err
}

// newPlacerRig grants every named node (workloads each) with a fresh heartbeat on
// a 4-core / 8 GiB host (the 3 CPU / 5 GiB row of the ceiling table).
func newPlacerRig(t *testing.T, workloads int, ids ...string) *placerRig {
	t.Helper()
	r := &placerRig{t: t, nodes: nodes.NewMemoryStore(), profiles: helperauth.NewMemoryProfileStore(), gen: map[string]int64{}}
	for _, id := range ids {
		r.grant(id, workloads, nodes.StateActive)
		if err := r.nodes.PutState(t.Context(), nodes.HelperState{
			NodeID: id, LastHeartbeat: time.Now(), RampedLimit: 8, HostCores: 4, HostMemoryBytes: 8 << 30,
		}); err != nil {
			t.Fatal(err)
		}
	}
	placer, err := nodes.NewPlacer(nodes.PlacerConfig{
		Allocations: func(ctx context.Context, _ time.Time) ([]nodes.Allocation, error) {
			if r.allocErr != nil {
				return nil, r.allocErr
			}
			return r.nodes.ListAllocations(ctx)
		},
		State:   r.nodes.GetState,
		ViewTTL: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.picker = &FleetPicker{Placer: placer, Profiles: r.profiles, RetryAfter: 7 * time.Millisecond}
	return r
}

func (r *placerRig) grant(id string, workloads int, state string) {
	r.t.Helper()
	r.gen[id]++
	if err := r.nodes.ApplyAllocation(r.t.Context(), nodes.Allocation{
		NodeID: id, Region: "eu-west", Endpoint: "10.8.0." + string(rune('1'+len(r.gen))) + ":7443", URISAN: nodes.NodeURISAN(id),
		CPUMillis: 3000, MemoryBytes: 5 << 30, ReservationState: nodes.ReservationKnown, State: state,
		MaxBrowserWorkloads: workloads, ValidUntil: time.Now().Add(time.Hour), Generation: r.gen[id],
	}, time.Now()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *placerRig) bind(tenant, provider, identity, node string) helperauth.Binding {
	r.t.Helper()
	b, err := r.profiles.Bind(r.t.Context(), tenant, provider, identity, node, time.Now())
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

func (r *placerRig) pick(tenant, target string) (HelperPlacement, error) {
	return r.picker.Pick(r.t.Context(), HelperPickRequest{TenantID: tenant, AppID: "app", JobID: "job_1", Target: target, CommandType: "chat.prompt"})
}

func wantHold(t *testing.T, err error, reason string) {
	t.Helper()
	var hold *HelperRetryError
	if !errors.As(err, &hold) {
		t.Fatalf("err = %v, want a *HelperRetryError (%s)", err, reason)
	}
	if hold.Reason != reason {
		t.Fatalf("hold reason = %q, want %q", hold.Reason, reason)
	}
	if hold.RetryAfter <= 0 {
		t.Fatal("a hold must carry a delay: Retry re-queues instantly on the file spool")
	}
}

func TestPlacerPickerRunsLocallyWhenTheFleetIsNotInvolved(t *testing.T) {
	t.Run("no helper node is eligible", func(t *testing.T) {
		r := newPlacerRig(t, 2) // nothing granted at all
		r.bind("tenant_a", "chatgpt_web", "acct-1", "node-a")
		if _, err := r.pick("tenant_a", "chatgpt_web"); !errors.Is(err, ErrNoHelper) {
			t.Fatalf("err = %v, want ErrNoHelper", err)
		}
	})
	t.Run("the manager is gone: every grant is draining or expired", func(t *testing.T) {
		r := newPlacerRig(t, 2, "node-a")
		r.bind("tenant_a", "chatgpt_web", "acct-1", "node-a")
		r.grant("node-a", 2, nodes.StateDraining)
		if _, err := r.pick("tenant_a", "chatgpt_web"); !errors.Is(err, ErrNoHelper) {
			t.Fatalf("err = %v, want ErrNoHelper: nothing is granted, so nothing new is placed", err)
		}
	})
	t.Run("the tenant has no profile for the target", func(t *testing.T) {
		r := newPlacerRig(t, 2, "node-a")
		r.bind("tenant_a", "chatgpt_web", "acct-1", "node-a")
		if _, err := r.pick("tenant_a", "gemini_web"); !errors.Is(err, ErrNoHelper) {
			t.Fatalf("another provider = %v", err)
		}
		if _, err := r.pick("tenant_b", "chatgpt_web"); !errors.Is(err, ErrNoHelper) {
			t.Fatalf("another tenant must not be given tenant_a's profile: %v", err)
		}
	})
	t.Run("a revoked profile is gone", func(t *testing.T) {
		r := newPlacerRig(t, 2, "node-a")
		b := r.bind("tenant_a", "chatgpt_web", "acct-1", "node-a")
		if ok, err := r.profiles.Revoke(t.Context(), "tenant_a", b.ProfileRef, time.Now()); err != nil || !ok {
			t.Fatal(err)
		}
		if _, err := r.pick("tenant_a", "chatgpt_web"); !errors.Is(err, ErrNoHelper) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestPlacerPickerPlacesOnTheTenantsOwnProfile(t *testing.T) {
	r := newPlacerRig(t, 2, "node-a", "node-b")
	own := r.bind("tenant_a", "chatgpt_web", "acct-1", "node-b")
	r.bind("tenant_b", "chatgpt_web", "acct-9", "node-a") // another tenant's, on the node that sorts first

	p, err := r.pick("tenant_a", "chatgpt_web")
	if err != nil {
		t.Fatal(err)
	}
	if p.NodeID != "node-b" || p.ProfileRef != own.ProfileRef || p.Endpoint == "" || p.Release == nil {
		t.Fatalf("placement = %+v, want tenant_a's own profile on node-b", p)
	}
	if !validPlacement(p) {
		t.Fatalf("a minted profile_ref must pass the runner's placement check: %+v", p)
	}
	if r.picker.Placer.Used("node-b") != 1 || r.picker.Placer.Used("node-a") != 0 {
		t.Fatal("the slot was not reserved on the placed node")
	}
	p.Release()
	if r.picker.Placer.Used("node-b") != 0 {
		t.Fatal("Release must return the slot")
	}
}

func TestPlacerPickerHoldsInsteadOfAssigningTheWrongProfile(t *testing.T) {
	t.Run("the tenant's only profile is on a node that is not eligible", func(t *testing.T) {
		r := newPlacerRig(t, 2, "node-a", "node-b")
		r.bind("tenant_a", "chatgpt_web", "acct-1", "node-b")
		r.bind("tenant_b", "chatgpt_web", "acct-9", "node-a") // node-a is up and has a profile, but not tenant_a's
		r.grant("node-b", 2, nodes.StateDraining)
		_, err := r.pick("tenant_a", "chatgpt_web")
		wantHold(t, err, "no_eligible_node")
		if r.picker.Placer.Used("node-a") != 0 {
			t.Fatal("tenant_a was placed on another tenant's node")
		}
	})
	t.Run("the identity lane is busy", func(t *testing.T) {
		r := newPlacerRig(t, 4, "node-a")
		r.bind("tenant_a", "chatgpt_web", "acct-1", "node-a")
		first, err := r.pick("tenant_a", "chatgpt_web")
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.pick("tenant_a", "chatgpt_web")
		wantHold(t, err, "identity_busy")
		if !errors.Is(err, nodes.ErrLaneBusy) {
			t.Fatalf("the cause must stay visible: %v", err)
		}
		first.Release()
		if _, err := r.pick("tenant_a", "chatgpt_web"); err != nil {
			t.Fatalf("after the first attempt ended the identity is free again: %v", err)
		}
	})
	t.Run("every eligible node is full", func(t *testing.T) {
		r := newPlacerRig(t, 1, "node-a")
		r.bind("tenant_a", "chatgpt_web", "acct-1", "node-a")
		r.bind("tenant_b", "chatgpt_web", "acct-2", "node-a")
		if _, err := r.pick("tenant_a", "chatgpt_web"); err != nil {
			t.Fatal(err)
		}
		_, err := r.pick("tenant_b", "chatgpt_web")
		wantHold(t, err, "no_capacity")
		if !errors.Is(err, nodes.ErrNoCapacity) {
			t.Fatalf("err = %v, want ErrNoCapacity visible", err)
		}
	})
	t.Run("a busy lane on one node does not hide a free one", func(t *testing.T) {
		r := newPlacerRig(t, 1, "node-a", "node-b")
		r.bind("tenant_a", "chatgpt_web", "acct-1", "node-a")
		spare := r.bind("tenant_a", "chatgpt_web", "acct-2", "node-b")
		first, err := r.pick("tenant_a", "chatgpt_web") // takes node-a (first in order)
		if err != nil || first.NodeID != "node-a" {
			t.Fatalf("first = %+v, %v", first, err)
		}
		second, err := r.pick("tenant_a", "chatgpt_web")
		if err != nil || second.NodeID != "node-b" || second.ProfileRef != spare.ProfileRef {
			t.Fatalf("second = %+v, %v, want the spare profile on node-b", second, err)
		}
		_, err = r.pick("tenant_a", "chatgpt_web")
		wantHold(t, err, "identity_busy") // both lanes busy: the identity, not the capacity, is the reason
	})
	t.Run("a revoked node takes nothing and leaves no fleet", func(t *testing.T) {
		r := newPlacerRig(t, 2, "node-a")
		r.bind("tenant_a", "chatgpt_web", "acct-1", "node-a")
		r.grant("node-a", 2, nodes.StateRevoked)
		if _, err := r.pick("tenant_a", "chatgpt_web"); !errors.Is(err, ErrNoHelper) {
			t.Fatalf("err = %v, want ErrNoHelper", err)
		}
		if r.picker.Placer.Used("node-a") != 0 {
			t.Fatal("a revoked node was given a slot")
		}
	})
}

func TestPlacerPickerFailsClosedWhenItCannotTell(t *testing.T) {
	t.Run("the fleet cannot be read", func(t *testing.T) {
		r := newPlacerRig(t, 2, "node-a")
		r.bind("tenant_a", "chatgpt_web", "acct-1", "node-a")
		r.allocErr = errors.New("db down")
		_, err := r.pick("tenant_a", "chatgpt_web")
		wantHold(t, err, "fleet_unreadable") // never ErrNoHelper: a blip must not move work onto this gateway
	})
	t.Run("the profiles cannot be read", func(t *testing.T) {
		r := newPlacerRig(t, 2, "node-a")
		r.picker.Profiles = failingProfiles{ProfileStore: r.profiles, err: errors.New("db down")}
		_, err := r.pick("tenant_a", "chatgpt_web")
		wantHold(t, err, "profiles_unreadable")
	})
}

func TestPlacerPickerConversationAffinity(t *testing.T) {
	setup := func(t *testing.T) (*placerRig, helperauth.Binding, helperauth.Binding) {
		r := newPlacerRig(t, 2, "node-a", "node-b")
		return r, r.bind("tenant_a", "chatgpt_web", "acct-1", "node-a"), r.bind("tenant_a", "chatgpt_web", "acct-2", "node-b")
	}
	conv := func(b helperauth.Binding) *conversations.Conversation {
		return &conversations.Conversation{
			TenantID: "tenant_a", Target: "chatgpt_web", ConversationKey: "k", State: conversations.StateActive,
			ProviderThreadRef: "https://chat.example/c/1", NodeID: b.NodeID, ProfileRef: b.ProfileRef,
		}
	}
	pick := func(r *placerRig, c *conversations.Conversation) (HelperPlacement, error) {
		return r.picker.Pick(r.t.Context(), HelperPickRequest{TenantID: "tenant_a", Target: "chatgpt_web", JobID: "job_1", Conversation: c})
	}

	t.Run("resumes only on its bound node and profile", func(t *testing.T) {
		r, _, onB := setup(t)
		p, err := pick(r, conv(onB))
		if err != nil || p.NodeID != "node-b" || p.ProfileRef != onB.ProfileRef {
			t.Fatalf("placement = %+v, %v, want the bound profile on node-b (node-a sorts first and is free)", p, err)
		}
	})
	t.Run("a bound node that is down is waited for, never relocated", func(t *testing.T) {
		r, _, onB := setup(t)
		r.grant("node-b", 2, nodes.StateDraining)
		_, err := pick(r, conv(onB))
		wantHold(t, err, "affinity_unavailable")
		if r.picker.Placer.Used("node-a") != 0 {
			t.Fatal("the conversation was moved to another node")
		}
	})
	t.Run("a bound node waits even when the whole fleet is down", func(t *testing.T) {
		r, onA, _ := setup(t)
		r.grant("node-a", 2, nodes.StateDraining)
		r.grant("node-b", 2, nodes.StateDraining)
		_, err := pick(r, conv(onA))
		wantHold(t, err, "affinity_unavailable")
	})
	t.Run("a busy bound identity waits for its lane", func(t *testing.T) {
		r, _, onB := setup(t)
		first, err := pick(r, conv(onB))
		if err != nil {
			t.Fatal(err)
		}
		_, err = pick(r, conv(onB))
		wantHold(t, err, "identity_busy")
		first.Release()
	})
	t.Run("a conversation that cannot resume runs where its worker applies on_missing", func(t *testing.T) {
		r, _, onB := setup(t)
		broken := conv(onB)
		broken.State = conversations.StateBroken
		if _, err := pick(r, broken); !errors.Is(err, ErrNoHelper) {
			t.Fatalf("broken conversation = %v, want ErrNoHelper", err)
		}
		moved := conv(onB)
		moved.NodeID = "node-a" // the profile lives on node-b
		if _, err := pick(r, moved); !errors.Is(err, ErrNoHelper) {
			t.Fatalf("a conversation bound to the wrong node for its profile = %v", err)
		}
		local := conv(onB)
		local.NodeID, local.ProfileRef = "", "" // the thread lives on the local worker
		if _, err := pick(r, local); !errors.Is(err, ErrNoHelper) {
			t.Fatalf("a locally bound conversation = %v", err)
		}
	})
	t.Run("a conversation with no thread yet may use any eligible profile", func(t *testing.T) {
		r, onA, _ := setup(t)
		fresh := &conversations.Conversation{TenantID: "tenant_a", Target: "chatgpt_web", ConversationKey: "k", State: conversations.StateActive}
		p, err := pick(r, fresh)
		if err != nil || p.ProfileRef != onA.ProfileRef {
			t.Fatalf("placement = %+v, %v", p, err)
		}
	})
}

// Through the runner: a placement the picker grants passes Place, and a hold
// surfaces as the typed error the consumer answers with a delayed retry.
func TestPlacerPickerThroughTheRunnerPlace(t *testing.T) {
	r := newPlacerRig(t, 1, "node-a")
	r.bind("t1", "chatgpt_web", "acct-1", "node-a")
	f := newRemoteFixture(t, func(c *RemoteConfig) { c.Picker = r.picker })
	env := helperTestEnvelope()
	env.Job.ConversationID, env.Conversation = "", nil

	placed, err := f.runner.Place(t.Context(), env)
	if err != nil || placed == nil || placed.NodeID != "node-a" {
		t.Fatalf("Place = %+v, %v", placed, err)
	}
	if _, err := f.runner.Place(t.Context(), env); err == nil {
		t.Fatal("the node is full: Place must hold the second job")
	} else {
		wantHold(t, err, "identity_busy")
	}
	placed.Release()
	placed.Release()
	if r.picker.Placer.Used("node-a") != 0 {
		t.Fatal("the placement's Release must return the slot (once)")
	}

	// A tenant that is not on the fleet runs here.
	env.TenantID = "tenant_without_profiles"
	if placed, err := f.runner.Place(t.Context(), env); placed != nil || err != nil {
		t.Fatalf("Place = %v, %v, want no placement and no error", placed, err)
	}
}
