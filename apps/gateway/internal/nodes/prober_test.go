package nodes

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeReporter struct {
	mu      sync.Mutex
	reports map[string]Report
	errs    map[string]error
	calls   map[string]int
	hold    func(ctx context.Context)
	hang    map[string]bool // reports that never answer until their deadline
	cur     atomic.Int32
	peak    atomic.Int32
}

func newFakeReporter() *fakeReporter {
	return &fakeReporter{reports: map[string]Report{}, errs: map[string]error{}, calls: map[string]int{}}
}

func healthyReport() Report {
	return Report{
		CPUMillisTotal: 4000, CPUMillisUsed: 400, MemoryBytesTotal: 8 << 30, MemoryBytesUsed: 1 << 30,
		BrowsersMax: 4, BrowsersActive: 0, WorkloadVersion: "w1", RegistryDigest: "d1",
	}
}

func (r *fakeReporter) Report(ctx context.Context, a Allocation) (Report, error) {
	n := r.cur.Add(1)
	defer r.cur.Add(-1)
	for {
		p := r.peak.Load()
		if n <= p || r.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if r.hold != nil {
		r.hold(ctx)
	}
	r.mu.Lock()
	hang := r.hang[a.NodeID]
	r.mu.Unlock()
	if hang {
		<-ctx.Done()
		return Report{}, ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[a.NodeID]++
	if err := r.errs[a.NodeID]; err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	return r.reports[a.NodeID], nil
}

func (r *fakeReporter) set(id string, rep Report) { r.mu.Lock(); r.reports[id] = rep; r.mu.Unlock() }
func (r *fakeReporter) fail(id string, err error) { r.mu.Lock(); r.errs[id] = err; r.mu.Unlock() }
func (r *fakeReporter) callsTo(id string) int     { r.mu.Lock(); defer r.mu.Unlock(); return r.calls[id] }

type proberRig struct {
	store    *MemoryStore
	rep      *fakeReporter
	prober   *Prober
	now      time.Time
	results  []string
	resultMu sync.Mutex
}

func newProberRig(t *testing.T, mut func(*ProberConfig), ids ...string) *proberRig {
	t.Helper()
	g := &proberRig{store: NewMemoryStore(), rep: newFakeReporter(), now: t0.Add(time.Second)}
	for _, id := range ids {
		if err := g.store.ApplyAllocation(t.Context(), grant(id, 4), g.now); err != nil {
			t.Fatal(err)
		}
		g.rep.set(id, healthyReport())
	}
	cfg := ProberConfig{
		Store: g.store, Reporter: g.rep, WorkloadVersion: "w1", RegistryDigest: "d1",
		Now: func() time.Time { return g.now },
		OnProbe: func(r string) {
			g.resultMu.Lock()
			g.results = append(g.results, r)
			g.resultMu.Unlock()
		},
	}
	if mut != nil {
		mut(&cfg)
	}
	g.prober = NewProber(cfg)
	return g
}

func (g *proberRig) tick(t *testing.T) {
	t.Helper()
	if err := g.prober.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	g.now = g.now.Add(HeartbeatInterval)
}

func (g *proberRig) state(t *testing.T, id string) HelperState {
	t.Helper()
	st, err := g.store.GetState(t.Context(), id)
	if err != nil {
		t.Fatalf("GetState(%s): %v", id, err)
	}
	return st
}

func (g *proberRig) verdict(t *testing.T, id string) Decision {
	t.Helper()
	a, err := g.store.GetAllocation(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return Admission(a, g.state(t, id), g.now)
}

func TestProberWritesTheHeartbeatHostSizeAndStartsANewHelperAtOne(t *testing.T) {
	g := newProberRig(t, nil, "helper-a")
	stamp := g.now
	g.tick(t)
	st := g.state(t, "helper-a")
	if !st.LastHeartbeat.Equal(stamp) {
		t.Fatalf("heartbeat = %v, want the primary's clock %v", st.LastHeartbeat, stamp)
	}
	if st.HostCores != 4 || st.HostMemoryBytes != 8<<30 || st.RampedLimit != 1 || st.PressureReduced {
		t.Fatalf("state = %+v", st)
	}
	if d := g.verdict(t, "helper-a"); !d.Eligible || d.Limit != 1 {
		t.Fatalf("a new helper must be eligible at one workload, got %+v", d)
	}
}

func TestProberRampsOneWorkloadPerEightSaturatedHealthySamples(t *testing.T) {
	g := newProberRig(t, nil, "helper-a")
	busy := healthyReport()
	busy.BrowsersActive = 1 // the helper runs at its current limit
	g.rep.set("helper-a", busy)
	for range RampSamples - 1 {
		g.tick(t)
	}
	if got := g.state(t, "helper-a").RampedLimit; got != 1 {
		t.Fatalf("limit after %d samples = %d, want 1", RampSamples-1, got)
	}
	g.tick(t)
	if got := g.state(t, "helper-a").RampedLimit; got != 2 {
		t.Fatalf("limit after %d samples = %d, want 2", RampSamples, got)
	}
	// An idle sample restarts the run: no jump while the helper is not saturated.
	g.rep.set("helper-a", healthyReport())
	for range 2 * RampSamples {
		g.tick(t)
	}
	if got := g.state(t, "helper-a").RampedLimit; got != 2 {
		t.Fatalf("an unsaturated helper ramped to %d", got)
	}
}

func TestProberPressureHalvesAdmissionAndSticksUntilItRecovers(t *testing.T) {
	g := newProberRig(t, nil, "helper-a")
	// Earn the full limit first so the halving is visible.
	st := HelperState{NodeID: "helper-a", LastHeartbeat: g.now, RampedLimit: 4, HostCores: 4, HostMemoryBytes: 8 << 30}
	if err := g.store.PutState(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	g.tick(t)
	if d := g.verdict(t, "helper-a"); d.Limit != 4 {
		t.Fatalf("limit = %d, want 4", d.Limit)
	}
	hot := healthyReport()
	hot.CPUMillisUsed = 3600 // 90% of the helper's own cgroup
	g.rep.set("helper-a", hot)
	g.tick(t)
	if s := g.state(t, "helper-a"); !s.PressureReduced {
		t.Fatalf("90%% CPU must reduce admission: %+v", s)
	}
	if d := g.verdict(t, "helper-a"); d.Limit != 2 {
		t.Fatalf("a reduced helper admits half: %+v", d)
	}
	g.rep.set("helper-a", healthyReport())
	g.tick(t)
	if s := g.state(t, "helper-a"); !s.PressureReduced {
		t.Fatal("recovery needs two minutes of calm, not one sample")
	}
	for range 9 { // 9 x 15 s > 2 min
		g.tick(t)
	}
	if s := g.state(t, "helper-a"); s.PressureReduced {
		t.Fatalf("still reduced after two calm minutes: %+v", s)
	}
}

func TestProberSilenceFailsClosed(t *testing.T) {
	g := newProberRig(t, nil, "helper-a")
	g.tick(t)
	last := g.state(t, "helper-a").LastHeartbeat

	g.rep.fail("helper-a", errors.New("dial: connection refused"))
	for range 3 {
		g.tick(t)
	}
	if got := g.state(t, "helper-a").LastHeartbeat; !got.Equal(last) {
		t.Fatalf("a failed report moved the heartbeat from %v to %v", last, got)
	}
	if d := g.verdict(t, "helper-a"); d.Eligible || d.Reason != ReasonHeartbeatMissed {
		t.Fatalf("after three missed beats the node must stop taking placements, got %+v", d)
	}
	g.rep.fail("helper-a", nil)
	g.tick(t)
	if d := g.verdict(t, "helper-a"); !d.Eligible {
		t.Fatalf("a report must bring the node back: %+v", d)
	}
}

func TestProberRefusesAHelperOnAnotherWorkloadOrRegistry(t *testing.T) {
	for name, mut := range map[string]func(*Report){
		"another workload version": func(r *Report) { r.WorkloadVersion = "w2" },
		"another registry digest":  func(r *Report) { r.RegistryDigest = "d2" },
		"no registry digest":       func(r *Report) { r.RegistryDigest = "" },
	} {
		t.Run(name, func(t *testing.T) {
			g := newProberRig(t, nil, "helper-a")
			rep := healthyReport()
			mut(&rep)
			g.rep.set("helper-a", rep)
			g.tick(t)
			if _, err := g.store.GetState(t.Context(), "helper-a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("an incompatible helper got a heartbeat: %v", err)
			}
			if len(g.results) != 1 || g.results[0] != ProbeIncompatible {
				t.Fatalf("results = %v", g.results)
			}
		})
	}
	t.Run("with nothing configured every helper is accepted", func(t *testing.T) {
		g := newProberRig(t, func(c *ProberConfig) { c.WorkloadVersion, c.RegistryDigest = "", "" }, "helper-a")
		rep := healthyReport()
		rep.WorkloadVersion = "anything"
		g.rep.set("helper-a", rep)
		g.tick(t)
		if g.state(t, "helper-a").LastHeartbeat.IsZero() {
			t.Fatal("no heartbeat")
		}
	})
}

func TestProberAnUnusableReportEntersPressureAndLeavesTheHostSizeUnknown(t *testing.T) {
	g := newProberRig(t, nil, "helper-a")
	g.rep.set("helper-a", Report{WorkloadVersion: "w1", RegistryDigest: "d1"}) // the helper's sampler failed: zero totals
	g.tick(t)
	st := g.state(t, "helper-a")
	if !st.PressureReduced || st.HostCores != 0 || st.HostMemoryBytes != 0 {
		t.Fatalf("state = %+v, want pressure and an unknown host size", st)
	}
	if d := g.verdict(t, "helper-a"); d.Eligible || d.Reason != ReasonNoCapacity {
		t.Fatalf("an unknown host size has a ceiling of zero: %+v", d)
	}
}

func TestProberSkipsRevokedNodesAndBoundsParallelism(t *testing.T) {
	g := newProberRig(t, func(c *ProberConfig) { c.Parallel = 2 }, "helper-a", "helper-b", "helper-c", "helper-d", "helper-r")
	rev := grant("helper-r", 4)
	rev.State, rev.Generation = StateRevoked, rev.Generation+1
	if err := g.store.ApplyAllocation(t.Context(), rev, g.now); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	var entered atomic.Int32
	g.rep.hold = func(ctx context.Context) {
		if entered.Add(1) == 2 {
			close(gate) // the second concurrent report is in: the third must not be
		}
		select {
		case <-gate:
			time.Sleep(20 * time.Millisecond) // give a third report the chance to overlap
		case <-ctx.Done():
		}
	}
	g.tick(t)
	if g.rep.callsTo("helper-r") != 0 {
		t.Fatal("a revoked node was probed")
	}
	for _, id := range []string{"helper-a", "helper-b", "helper-c", "helper-d"} {
		if g.rep.callsTo(id) != 1 {
			t.Fatalf("%s probed %d times", id, g.rep.callsTo(id))
		}
	}
	if peak := g.rep.peak.Load(); peak > 2 {
		t.Fatalf("%d reports in flight, want at most the 2 allowed", peak)
	}
}

func TestProberAHungHelperTimesOutAndDoesNotStallTheRound(t *testing.T) {
	g := newProberRig(t, func(c *ProberConfig) { c.Timeout = 30 * time.Millisecond }, "helper-a", "helper-b")
	g.rep.hang = map[string]bool{"helper-a": true}
	done := make(chan struct{})
	go func() { _ = g.prober.Tick(t.Context()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a hung helper stalled the probe round")
	}
	g.resultMu.Lock()
	defer g.resultMu.Unlock()
	if len(g.results) != 2 {
		t.Fatalf("results = %v", g.results)
	}
}

func TestProberStoreFailuresAreCountedAndNeverPanic(t *testing.T) {
	g := newProberRig(t, nil, "helper-a")
	g.prober.cfg.Store = failingStore{Store: g.store}
	g.tick(t)
	if len(g.results) != 1 || g.results[0] != ProbeError {
		t.Fatalf("results = %v", g.results)
	}
	if err := g.prober.Tick(t.Context()); err != nil {
		t.Fatalf("a state write failure must not fail the round: %v", err)
	}
}

type failingStore struct{ Store }

func (failingStore) PutState(context.Context, HelperState) error { return errors.New("store down") }
