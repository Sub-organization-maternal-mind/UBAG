package nodes

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// hostState is a heartbeating helper on a host of the given size.
func hostState(id string, cores int, mem int64) HelperState {
	return HelperState{NodeID: id, LastHeartbeat: t0, HostCores: cores, HostMemoryBytes: mem, RampedLimit: 8}
}

// fleet is a fake grant source and state store with a movable clock.
type fleet struct {
	mu     sync.Mutex
	now    time.Time
	allocs []Allocation
	states map[string]HelperState
	allocE error
	stateE error
	reads  atomic.Int32
}

func newFleet() *fleet { return &fleet{now: t0.Add(time.Second), states: map[string]HelperState{}} }

func (f *fleet) add(a Allocation, st HelperState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allocs = append(f.allocs, a)
	f.states[a.NodeID] = st
}

func (f *fleet) setNow(t time.Time) { f.mu.Lock(); f.now = t; f.mu.Unlock() }

func (f *fleet) placer(t *testing.T, ttl time.Duration) *Placer {
	t.Helper()
	p, err := NewPlacer(PlacerConfig{
		Now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
		Allocations: func(context.Context, time.Time) ([]Allocation, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.reads.Add(1)
			return append([]Allocation(nil), f.allocs...), f.allocE
		},
		State: func(_ context.Context, id string) (HelperState, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.stateE != nil {
				return HelperState{}, f.stateE
			}
			st, ok := f.states[id]
			if !ok {
				return HelperState{}, ErrNotFound
			}
			return st, nil
		},
		ViewTTL: ttl,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func grant(id string, workloads int) Allocation {
	a := goodAlloc(id) // 3000 mCPU, 5 GiB: the 4-core / 8 GiB row of the ceiling table
	a.MaxBrowserWorkloads = workloads
	return a
}

func TestPlacerAdmissionAppliesTheCeilingTable(t *testing.T) {
	const gib = int64(1) << 30
	now := t0.Add(time.Second)
	cases := []struct {
		name   string
		mut    func(*Allocation, *HelperState)
		want   int
		reason string
	}{
		{"a grant inside the 4c/8G row is untouched", nil, 4, ""},
		{"an unknown host size fails closed", func(_ *Allocation, s *HelperState) { s.HostCores, s.HostMemoryBytes = 0, 0 }, 0, ReasonNoCapacity},
		{"a grant of no cpu grants nothing", func(a *Allocation, _ *HelperState) { a.CPUMillis = 0 }, 0, ReasonNoCapacity},
		{"a grant of no memory grants nothing", func(a *Allocation, _ *HelperState) { a.MemoryBytes = 0 }, 0, ReasonNoCapacity},
		{"a grant over the 2c/4G row is cut to its share", func(_ *Allocation, s *HelperState) { s.HostCores, s.HostMemoryBytes = 2, 4*gib }, 2, ""},
		{"memory is the tighter side", func(a *Allocation, s *HelperState) {
			s.HostCores, s.HostMemoryBytes = 2, 4*gib
			a.CPUMillis = 1500 // within the 1.5 CPU row, memory 5 GiB is over 2.5 GiB
		}, 2, ""},
		{"a one workload grant stays at one", func(a *Allocation, s *HelperState) {
			a.MaxBrowserWorkloads = 1
			s.HostCores, s.HostMemoryBytes = 2, 4*gib
		}, 1, ""},
		{"a ceiling never raises the grant", func(a *Allocation, s *HelperState) {
			a.MaxBrowserWorkloads = 2
			s.HostCores, s.HostMemoryBytes = 64, 256*gib
		}, 2, ""},
		{"pressure halves the limit before the table", func(_ *Allocation, s *HelperState) { s.PressureReduced = true }, 2, ""},
		{"a new helper starts at one", func(_ *Allocation, s *HelperState) { s.RampedLimit = 0 }, 1, ""},
		{"three missed heartbeats", func(_ *Allocation, s *HelperState) { s.LastHeartbeat = t0.Add(-time.Minute) }, 0, ReasonHeartbeatMissed},
		{"a draining grant", func(a *Allocation, _ *HelperState) { a.State = StateDraining }, 0, ReasonDraining},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, st := grant("helper-1", 4), hostState("helper-1", 4, 8*gib)
			if c.mut != nil {
				c.mut(&a, &st)
			}
			d := Admission(a, st, now)
			if d.Limit != c.want || d.Eligible != (c.want > 0) || d.Reason != c.reason {
				t.Fatalf("Admission = %+v, want limit %d reason %q", d, c.want, c.reason)
			}
		})
	}
}

func TestPlacerNodesAreTheEligibleOnesInIDOrder(t *testing.T) {
	f := newFleet()
	f.add(grant("helper-b", 2), hostState("helper-b", 4, 8<<30))
	f.add(grant("helper-a", 3), hostState("helper-a", 4, 8<<30))
	dead := grant("helper-c", 4)
	f.add(dead, HelperState{NodeID: "helper-c", LastHeartbeat: t0.Add(-time.Hour), HostCores: 4, HostMemoryBytes: 8 << 30})
	drain := grant("helper-d", 4)
	drain.State = StateDraining
	f.add(drain, hostState("helper-d", 4, 8<<30))
	f.allocs = append(f.allocs, grant("helper-e", 4)) // granted but never heard from: no state row

	p := f.placer(t, -1)
	nodes, err := p.Nodes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].ID != "helper-a" || nodes[0].Limit != 3 || nodes[1].ID != "helper-b" || nodes[1].Limit != 2 {
		t.Fatalf("nodes = %+v, want helper-a(3) then helper-b(2)", nodes)
	}
	if nodes[0].Endpoint == "" || nodes[0].Region == "" {
		t.Fatalf("a node must carry the grant's endpoint and region: %+v", nodes[0])
	}
	if got := p.Capacity(t.Context()); got != 5 {
		t.Fatalf("Capacity = %d, want 5", got)
	}
}

func TestPlacerUnreadableFleetIsAnErrorNotAnEmptyFleet(t *testing.T) {
	f := newFleet()
	f.add(grant("helper-a", 2), hostState("helper-a", 4, 8<<30))
	p := f.placer(t, -1)

	f.allocE = errors.New("db down")
	if nodes, err := p.Nodes(t.Context()); err == nil || nodes != nil {
		t.Fatalf("grant read failure = %v, %v, want an error", nodes, err)
	}
	f.allocE, f.stateE = nil, errors.New("db down")
	if _, err := p.Nodes(t.Context()); err == nil {
		t.Fatal("a state read failure must be an error, not a node that is silently left out")
	}
	if _, err := p.Reserve(t.Context(), "helper-a", "lane"); err == nil || errors.Is(err, ErrNodeNotEligible) {
		t.Fatalf("Reserve on an unreadable fleet = %v, want the read error", err)
	}
	if got := p.Capacity(t.Context()); got != 0 {
		t.Fatalf("Capacity on an unreadable fleet = %d, want 0", got)
	}
}

func TestPlacerViewIsCachedForTheTTLOnly(t *testing.T) {
	f := newFleet()
	f.add(grant("helper-a", 2), hostState("helper-a", 4, 8<<30))
	p := f.placer(t, time.Second)

	for range 5 {
		if nodes, _ := p.Nodes(t.Context()); len(nodes) != 1 {
			t.Fatal("helper-a must be eligible")
		}
	}
	if f.reads.Load() != 1 {
		t.Fatalf("grants read %d times within the TTL, want 1", f.reads.Load())
	}
	f.mu.Lock()
	st := f.states["helper-a"]
	st.LastHeartbeat = t0.Add(-time.Hour) // the node went silent
	f.states["helper-a"] = st
	f.mu.Unlock()
	if nodes, _ := p.Nodes(t.Context()); len(nodes) != 1 {
		t.Fatal("inside the TTL the cached view still answers")
	}
	f.setNow(t0.Add(3 * time.Second))
	if nodes, _ := p.Nodes(t.Context()); len(nodes) != 0 {
		t.Fatalf("after the TTL the silent node must be gone, got %+v", nodes)
	}
}

func TestPlacerReserveTable(t *testing.T) {
	newP := func(t *testing.T) (*Placer, *fleet) {
		f := newFleet()
		f.add(grant("helper-a", 2), hostState("helper-a", 4, 8<<30))
		f.add(grant("helper-b", 1), hostState("helper-b", 4, 8<<30))
		return f.placer(t, -1), f
	}
	ctx := t.Context()

	t.Run("a free slot and a free lane", func(t *testing.T) {
		p, _ := newP(t)
		r, err := p.Reserve(ctx, "helper-a", "pr_1")
		if err != nil || r.Node.ID != "helper-a" || r.Node.Endpoint == "" || p.Used("helper-a") != 1 {
			t.Fatalf("Reserve = %+v, %v, used %d", r, err, p.Used("helper-a"))
		}
	})
	t.Run("a busy identity lane is refused even when the node has slots", func(t *testing.T) {
		p, _ := newP(t)
		if _, err := p.Reserve(ctx, "helper-a", "pr_1"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Reserve(ctx, "helper-a", "pr_1"); !errors.Is(err, ErrLaneBusy) {
			t.Fatalf("second attempt on one identity = %v, want ErrLaneBusy", err)
		}
		if _, err := p.Reserve(ctx, "helper-b", "pr_1"); !errors.Is(err, ErrLaneBusy) {
			t.Fatalf("the same identity on another node = %v, want ErrLaneBusy", err)
		}
		if _, err := p.Reserve(ctx, "helper-a", "pr_2"); err != nil {
			t.Fatalf("another identity must not be blocked: %v", err)
		}
	})
	t.Run("a full node refuses with no capacity", func(t *testing.T) {
		p, _ := newP(t)
		if _, err := p.Reserve(ctx, "helper-b", "pr_1"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Reserve(ctx, "helper-b", "pr_2"); !errors.Is(err, ErrNoCapacity) {
			t.Fatalf("Reserve past the limit = %v, want ErrNoCapacity", err)
		}
		if p.Used("helper-b") != 1 {
			t.Fatal("a refused reservation must not take a slot")
		}
	})
	t.Run("a refused slot does not leave the lane held", func(t *testing.T) {
		p, _ := newP(t)
		if _, err := p.Reserve(ctx, "helper-b", "pr_1"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Reserve(ctx, "helper-b", "pr_2"); !errors.Is(err, ErrNoCapacity) {
			t.Fatal(err)
		}
		if _, err := p.Reserve(ctx, "helper-a", "pr_2"); err != nil {
			t.Fatalf("pr_2 was refused on a full node and must be free elsewhere: %v", err)
		}
	})
	t.Run("an unknown or ineligible node", func(t *testing.T) {
		p, f := newP(t)
		if _, err := p.Reserve(ctx, "helper-z", "pr_1"); !errors.Is(err, ErrNodeNotEligible) {
			t.Fatalf("unknown node = %v", err)
		}
		f.mu.Lock()
		f.allocs[0].State = StateDraining
		f.mu.Unlock()
		if _, err := p.Reserve(ctx, "helper-a", "pr_1"); !errors.Is(err, ErrNodeNotEligible) {
			t.Fatalf("draining node = %v", err)
		}
	})
	t.Run("release frees the slot and the lane exactly once", func(t *testing.T) {
		p, _ := newP(t)
		r1, _ := p.Reserve(ctx, "helper-b", "pr_1")
		r1.Release()
		r1.Release() // a second call must not free someone else's slot
		r2, err := p.Reserve(ctx, "helper-b", "pr_1")
		if err != nil {
			t.Fatalf("after Release the lane and the slot are free: %v", err)
		}
		r1.Release()
		if _, err := p.Reserve(ctx, "helper-b", "pr_3"); !errors.Is(err, ErrNoCapacity) {
			t.Fatalf("a stale Release freed a slot it did not own: %v", err)
		}
		r2.Release()
		if p.Used("helper-b") != 0 {
			t.Fatal("slots leaked")
		}
	})
	t.Run("a shrink evicts nothing and refuses new work until usage falls", func(t *testing.T) {
		p, f := newP(t)
		r1, _ := p.Reserve(ctx, "helper-a", "pr_1")
		r2, _ := p.Reserve(ctx, "helper-a", "pr_2")
		f.mu.Lock()
		f.allocs[0].MaxBrowserWorkloads = 1 // the manager shrinks the grant under the running work
		f.mu.Unlock()
		if _, err := p.Reserve(ctx, "helper-a", "pr_3"); !errors.Is(err, ErrNoCapacity) {
			t.Fatalf("over the shrunk limit = %v, want ErrNoCapacity", err)
		}
		r1.Release()
		if _, err := p.Reserve(ctx, "helper-a", "pr_3"); !errors.Is(err, ErrNoCapacity) {
			t.Fatalf("one in use against a limit of one = %v, want ErrNoCapacity", err)
		}
		r2.Release()
		if _, err := p.Reserve(ctx, "helper-a", "pr_3"); err != nil {
			t.Fatalf("usage fell under the limit: %v", err)
		}
	})
	t.Run("an empty lane is invalid", func(t *testing.T) {
		p, _ := newP(t)
		if _, err := p.Reserve(ctx, "helper-a", ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("empty lane = %v", err)
		}
	})
}

func TestPlacerReserveNeverOverbooksUnderContention(t *testing.T) {
	f := newFleet()
	f.add(grant("helper-a", 3), hostState("helper-a", 4, 8<<30))
	p := f.placer(t, time.Second)
	var wg sync.WaitGroup
	var won, laneBusy, full atomic.Int32
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lane := "pr_" + string(rune('a'+i%8)) // 8 identities, 8 contenders each
			switch _, err := p.Reserve(t.Context(), "helper-a", lane); {
			case err == nil:
				won.Add(1)
			case errors.Is(err, ErrLaneBusy):
				laneBusy.Add(1)
			case errors.Is(err, ErrNoCapacity):
				full.Add(1)
			default:
				t.Errorf("Reserve: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 3 || p.Used("helper-a") != 3 {
		t.Fatalf("won %d (laneBusy %d, full %d), used %d: want exactly the 3 slots granted", won.Load(), laneBusy.Load(), full.Load(), p.Used("helper-a"))
	}
}

func TestPlacerConfigIsValidated(t *testing.T) {
	if _, err := NewPlacer(PlacerConfig{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty config = %v", err)
	}
}
