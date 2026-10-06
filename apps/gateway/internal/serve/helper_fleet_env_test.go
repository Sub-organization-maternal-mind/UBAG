package serve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

func TestFleetPollerFromEnv(t *testing.T) {
	store := nodes.NewMemoryStore()
	cases := []struct {
		name    string
		store   nodes.Store
		env     map[string]string
		want    bool
		wantErr string
	}{
		{name: "no node store, nothing is polled", env: map[string]string{"UBAG_FLEET_MANAGER_URL": "https://manager.example/v1/allocations"}},
		{name: "no manager url, nothing is polled", store: store},
		{name: "configured", store: store, env: map[string]string{"UBAG_FLEET_MANAGER_URL": "https://manager.example/v1/allocations"}, want: true},
		{name: "credentials in the url are refused", store: store, env: map[string]string{"UBAG_FLEET_MANAGER_URL": "https://u:p@manager.example/x"}, wantErr: "UBAG_FLEET_MANAGER_URL"},
		{name: "a bad scheme is refused", store: store, env: map[string]string{"UBAG_FLEET_MANAGER_URL": "ftp://manager.example/x"}, wantErr: "UBAG_FLEET_MANAGER_URL"},
		{name: "a bad poll interval fails closed", store: store, env: map[string]string{
			"UBAG_FLEET_MANAGER_URL": "https://manager.example/x", "UBAG_FLEET_POLL_SECONDS": "1"}, wantErr: "UBAG_FLEET_POLL_SECONDS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"UBAG_FLEET_MANAGER_URL", "UBAG_FLEET_POLL_SECONDS", "UBAG_FLEET_GRANT_STALE_GRACE_SECONDS"} {
				t.Setenv(k, tc.env[k])
			}
			poller, err := newFleetPollerFromEnv(tc.store)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if (poller != nil) != tc.want {
				t.Fatalf("poller = %v, want one: %v", poller, tc.want)
			}
		})
	}
}

func TestHelperFleetFromEnv(t *testing.T) {
	ctx := context.Background()
	t.Run("off is inert", func(t *testing.T) {
		t.Setenv("UBAG_HELPER_DISPATCH", "")
		if f, err := newHelperFleetFromEnv(ctx, nodes.NewMemoryStore(), nil, "memory", nil); f != nil || err != nil {
			t.Fatalf("fleet = %v, err = %v", f, err)
		}
	})
	t.Run("on without a node store leaves the prerequisite error to dispatch", func(t *testing.T) {
		t.Setenv("UBAG_HELPER_DISPATCH", "true")
		if f, err := newHelperFleetFromEnv(ctx, nil, nil, "memory", nil); f != nil || err != nil {
			t.Fatalf("fleet = %v, err = %v", f, err)
		}
	})
	t.Run("on with the memory store", func(t *testing.T) {
		t.Setenv("UBAG_HELPER_DISPATCH", "true")
		f, err := newHelperFleetFromEnv(ctx, nodes.NewMemoryStore(), nil, "memory", nil)
		if err != nil || f == nil || f.picker == nil || f.placer == nil {
			t.Fatalf("fleet = %+v, err = %v", f, err)
		}
		if got := f.placer.Capacity(ctx); got != 0 {
			t.Fatalf("capacity with nothing granted = %d", got)
		}
	})
	t.Run("postgres needs a handle, and an unknown store is refused", func(t *testing.T) {
		t.Setenv("UBAG_HELPER_DISPATCH", "true")
		if _, err := newHelperFleetFromEnv(ctx, nodes.NewMemoryStore(), nil, "postgres", nil); err == nil || !strings.Contains(err.Error(), "Postgres") {
			t.Fatalf("err = %v", err)
		}
		if _, err := newHelperFleetFromEnv(ctx, nodes.NewMemoryStore(), nil, "sqlite", nil); err == nil || !strings.Contains(err.Error(), "UBAG_GATEWAY_STORE") {
			t.Fatalf("err = %v", err)
		}
	})
}

func grantNode(t *testing.T, store nodes.Store, id string, workloads int) {
	t.Helper()
	if err := store.ApplyAllocation(t.Context(), nodes.Allocation{
		NodeID: id, Region: "eu-west", Endpoint: "10.8.0.2:7443", URISAN: nodes.NodeURISAN(id),
		CPUMillis: 3000, MemoryBytes: 5 << 30, ReservationState: nodes.ReservationKnown, State: nodes.StateActive,
		MaxBrowserWorkloads: workloads, ValidUntil: time.Now().Add(time.Hour), Generation: 1,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutState(t.Context(), nodes.HelperState{
		NodeID: id, LastHeartbeat: time.Now(), RampedLimit: 8, HostCores: 4, HostMemoryBytes: 8 << 30,
	}); err != nil {
		t.Fatal(err)
	}
}

// The consumer is wired to follow the fleet: holds wait off the worker and the
// worker count is the local pool plus the granted capacity.
func TestHelperFleetWiresTheConsumer(t *testing.T) {
	t.Setenv("UBAG_HELPER_DISPATCH", "true")
	store := nodes.NewMemoryStore()
	grantNode(t, store, "node-a", 3)
	fleet, err := newHelperFleetFromEnv(t.Context(), store, nil, "memory", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &executor.WorkerConsumer{PoolSize: 2}
	fleet.wire(c)
	if c.AsyncHolds != asyncPlacementHolds || c.HelperCapacity == nil {
		t.Fatalf("consumer = %+v", c)
	}
	if got := c.HelperCapacity(); got != 3 {
		t.Fatalf("capacity = %d, want the granted 3", got)
	}
	var none *helperFleet
	none.wire(c) // a nil fleet is a no-op
	none.wire(nil)
}

// With the manager poller, a grant the manager stopped renewing is treated as
// draining: no capacity and no placement (decision D3).
func TestHelperFleetUsesThePollersViewOfTheGrants(t *testing.T) {
	t.Setenv("UBAG_HELPER_DISPATCH", "true")
	store := nodes.NewMemoryStore()
	grantNode(t, store, "node-a", 3)
	poller := nodes.NewPoller(nil, store, nodes.PollerConfig{Interval: time.Minute, StaleGrace: time.Nanosecond}, nil)
	fleet, err := newHelperFleetFromEnv(t.Context(), store, poller, "memory", nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond) // past the stale grace measured from the grant's acceptance
	if got := fleet.placer.Capacity(t.Context()); got != 0 {
		t.Fatalf("capacity = %d, want 0: a stale grant is draining", got)
	}
	generous := nodes.NewPoller(nil, store, nodes.PollerConfig{Interval: time.Minute, StaleGrace: time.Hour}, nil)
	if fleet, err = newHelperFleetFromEnv(t.Context(), store, generous, "memory", nil); err != nil {
		t.Fatal(err)
	}
	if got := fleet.placer.Capacity(t.Context()); got != 3 {
		t.Fatalf("capacity = %d, want 3 inside the grace", got)
	}
}

// A job of a tenant with a bound profile is placed on the granted node.
func TestHelperFleetPicksTheTenantsProfileOnAGrantedNode(t *testing.T) {
	t.Setenv("UBAG_HELPER_DISPATCH", "true")
	store := nodes.NewMemoryStore()
	grantNode(t, store, "node-a", 1)
	fleet, err := newHelperFleetFromEnv(t.Context(), store, nil, "memory", nil)
	if err != nil {
		t.Fatal(err)
	}
	profiles := fleet.picker.Profiles.(*helperauth.MemoryProfileStore)
	bound, err := profiles.Bind(t.Context(), "tenant_a", "chatgpt_web", "acct-1", "node-a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, err := fleet.picker.Pick(t.Context(), executor.HelperPickRequest{TenantID: "tenant_a", Target: "chatgpt_web", JobID: "job_1"})
	if err != nil || p.NodeID != "node-a" || p.ProfileRef != bound.ProfileRef || p.Endpoint != "10.8.0.2:7443" {
		t.Fatalf("placement = %+v, err = %v", p, err)
	}
	p.Release()
}

func TestHelperProberFromEnv(t *testing.T) {
	good := writeDispatchEnv(t)
	for name, mut := range map[string]func(*dispatchEnv){
		"needs the CA":   func(e *dispatchEnv) { e.ca = "" },
		"needs the cert": func(e *dispatchEnv) { e.cert = "" },
		"needs the key":  func(e *dispatchEnv) { e.key = "" },
	} {
		t.Run(name, func(t *testing.T) {
			env := good
			mut(&env)
			setDispatchEnv(t, "true", env)
			if _, err := newHelperProberFromEnv(nodes.NewMemoryStore()); err == nil || !strings.Contains(err.Error(), "UBAG_HELPER_") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("an unreadable adapter registry", func(t *testing.T) {
		env := good
		env.adapters = t.TempDir() + "/missing"
		setDispatchEnv(t, "true", env)
		if _, err := newHelperProberFromEnv(nodes.NewMemoryStore()); err == nil || !strings.Contains(err.Error(), "registry digest") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("configured", func(t *testing.T) {
		setDispatchEnv(t, "true", good)
		prober, err := newHelperProberFromEnv(nodes.NewMemoryStore())
		if err != nil || prober == nil {
			t.Fatalf("prober = %v, err = %v", prober, err)
		}
		// With no allocation there is nothing to dial: a round is a no-op, not a failure.
		if err := prober.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

type fakeCapacityConn struct {
	resp   *helperv1.ReportCapacityResponse
	err    error
	asked  string
	closed bool
}

func (f *fakeCapacityConn) ReportCapacity(_ context.Context, in *helperv1.ReportCapacityRequest, _ ...grpc.CallOption) (*helperv1.ReportCapacityResponse, error) {
	f.asked = in.GetNodeId()
	return f.resp, f.err
}
func (f *fakeCapacityConn) Close() error { f.closed = true; return nil }

func TestHelperReporterMapsTheCapacityReport(t *testing.T) {
	alloc := nodes.Allocation{NodeID: "node-a", Endpoint: "10.8.0.2:7443"}
	report := func(conn *fakeCapacityConn, dialErr error) (nodes.Report, error) {
		return helperReporter{dial: func(_ context.Context, a nodes.Allocation) (capacityConn, error) {
			if a.NodeID != "node-a" {
				t.Fatalf("dialed %q", a.NodeID)
			}
			return conn, dialErr
		}}.Report(t.Context(), alloc)
	}

	conn := &fakeCapacityConn{resp: &helperv1.ReportCapacityResponse{
		NodeId: "node-a", CpuMillisTotal: 4000, CpuMillisUsed: 1200, MemoryBytesTotal: 8 << 30, MemoryBytesUsed: 2 << 30,
		BrowsersMax: 4, BrowsersActive: 2, WorkloadVersion: "w1", RegistryDigest: "d1",
	}}
	got, err := report(conn, nil)
	want := nodes.Report{
		CPUMillisTotal: 4000, CPUMillisUsed: 1200, MemoryBytesTotal: 8 << 30, MemoryBytesUsed: 2 << 30,
		BrowsersMax: 4, BrowsersActive: 2, WorkloadVersion: "w1", RegistryDigest: "d1",
	}
	if err != nil || got != want || conn.asked != "node-a" || !conn.closed {
		t.Fatalf("report = %+v, err = %v, asked %q, closed %v", got, err, conn.asked, conn.closed)
	}

	if _, err := report(&fakeCapacityConn{resp: &helperv1.ReportCapacityResponse{NodeId: "node-b"}}, nil); err == nil {
		t.Fatal("a helper answering for another node must be refused")
	}
	failing := &fakeCapacityConn{err: errors.New("unavailable")}
	if _, err := report(failing, nil); err == nil || !failing.closed {
		t.Fatalf("a failing report = %v, closed %v: the connection must still be closed", err, failing.closed)
	}
	if _, err := report(nil, errors.New("dial refused")); err == nil {
		t.Fatal("a dial failure must be an error")
	}
}
