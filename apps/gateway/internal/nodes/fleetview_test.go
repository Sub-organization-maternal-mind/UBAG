package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func newView(t *testing.T, f *fleet, used func(string) int, held func() map[string]int) *FleetView {
	t.Helper()
	v, err := NewFleetView(FleetViewConfig{
		Now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
		Allocations: func(context.Context, time.Time) ([]Allocation, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
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
		Used: used, Held: held,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFleetViewMapsDecisionsToNodeStates(t *testing.T) {
	f := newFleet()
	f.add(grant("a-eligible", 4), hostState("a-eligible", 4, 8<<30))

	draining := grant("b-draining", 4)
	draining.State = StateDraining
	f.add(draining, hostState("b-draining", 4, 8<<30))

	unk := grant("c-unknown", 4)
	unk.ReservationState = ReservationUnknown
	f.add(unk, hostState("c-unknown", 4, 8<<30))

	silent := hostState("d-lost", 4, 8<<30)
	silent.LastHeartbeat = t0.Add(-time.Hour)
	f.add(grant("d-lost", 4), silent)

	expired := grant("e-expired", 4)
	expired.ValidUntil = t0
	f.add(expired, hostState("e-expired", 4, 8<<30))

	revoked := grant("f-revoked", 4)
	revoked.State = StateRevoked
	f.add(revoked, hostState("f-revoked", 4, 8<<30))

	f.add(grant("g-nohost", 4), hostState("g-nohost", 0, 0)) // unknown host size: no capacity
	f.add(grant("h-silent", 4), HelperState{})               // present in the grants, never heard from
	f.states = map[string]HelperState{
		"a-eligible": f.states["a-eligible"], "b-draining": f.states["b-draining"], "c-unknown": f.states["c-unknown"],
		"d-lost": f.states["d-lost"], "e-expired": f.states["e-expired"], "f-revoked": f.states["f-revoked"], "g-nohost": f.states["g-nohost"],
	} // h-silent has no state row at all

	list, err := newView(t, f, nil, nil).Nodes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	type want struct{ state, reason string }
	wants := map[string]want{
		"a-eligible": {FleetEligible, ""}, "b-draining": {FleetDraining, ""}, "c-unknown": {FleetUnknownReservation, ""},
		"d-lost": {FleetLost, ""}, "e-expired": {FleetIneligible, "grant_expired"}, "f-revoked": {FleetIneligible, "revoked"},
		"g-nohost": {FleetIneligible, "no_capacity"}, "h-silent": {FleetLost, ""},
	}
	if len(list) != len(wants) {
		t.Fatalf("%d nodes, want %d", len(list), len(wants))
	}
	for i, n := range list {
		w := wants[n.NodeID]
		got := ""
		if n.IneligibleReason != nil {
			got = *n.IneligibleReason
		}
		if n.State != w.state || got != w.reason {
			t.Errorf("%s: state=%s reason=%q, want %s %q", n.NodeID, n.State, got, w.state, w.reason)
		}
		if i > 0 && list[i-1].NodeID >= n.NodeID {
			t.Errorf("nodes not ordered by id: %s then %s", list[i-1].NodeID, n.NodeID)
		}
		if n.State != FleetEligible && n.Usage.AdmissionLimit != 0 {
			t.Errorf("%s: ineligible node has admission limit %d", n.NodeID, n.Usage.AdmissionLimit)
		}
	}
	if list[7].NodeID != "h-silent" || list[7].HeartbeatAt != nil {
		t.Errorf("a node never heard from must have a null heartbeat: %+v", list[7])
	}
}

func TestFleetViewUsagePressureAndSummary(t *testing.T) {
	f := newFleet()
	calm := t0.Add(-time.Minute)
	st := hostState("n1", 4, 8<<30)
	st.PressureReduced, st.PressureCalmSince = true, calm
	f.add(grant("n1", 4), st)
	f.add(grant("n2", 4), hostState("n2", 4, 8<<30))

	v := newView(t, f, func(id string) int {
		if id == "n2" {
			return 3
		}
		return 0
	}, func() map[string]int { return map[string]int{"identity_busy": 2, "no_capacity": 0} })
	list, err := v.Nodes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n1, n2 := list[0], list[1]
	if !n1.Pressure.AdmissionReduced || n1.Pressure.RecoverAt == nil || !n1.Pressure.RecoverAt.Equal(calm.Add(recoverHold)) {
		t.Errorf("pressure = %+v, want reduced with the recovery window end", n1.Pressure)
	}
	if n1.Usage.AdmissionLimit != 2 { // 4 granted, 8 ramped, halved by pressure
		t.Errorf("n1 admission limit = %d, want 2", n1.Usage.AdmissionLimit)
	}
	if n2.Usage.WorkloadsInUse != 3 || n2.Usage.AdmissionLimit != 4 || n2.Pressure.RecoverAt != nil {
		t.Errorf("n2 = %+v", n2)
	}
	sum, err := v.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if sum.NodesTotal != 2 || sum.NodesByState[FleetEligible] != 2 || sum.WorkloadLimitTotal != 6 || sum.WorkloadsInUseTotal != 3 ||
		sum.NodesPressureReduced != 1 || len(sum.HeldByReason) != 1 || sum.HeldByReason["identity_busy"] != 2 {
		t.Errorf("summary = %+v", sum)
	}
	for _, s := range []string{FleetEligible, FleetIneligible, FleetDraining, FleetLost, FleetUnknownReservation} {
		if _, ok := sum.NodesByState[s]; !ok {
			t.Errorf("nodes_by_state is missing %q (the schema requires every key)", s)
		}
	}
}

// The wire shape carries no address, hostname, endpoint or certificate detail,
// whatever the allocation holds.
func TestFleetViewNeverSerializesAnAddress(t *testing.T) {
	f := newFleet()
	a := grant("n1", 4)
	a.NATIP, a.VoiceCapable, a.UDPPortMin, a.UDPPortMax = "203.0.113.7", true, 40000, 40100
	a.SPKISHA256 = strings.Repeat("ab", 32)
	f.add(a, hostState("n1", 4, 8<<30))
	v := newView(t, f, nil, nil)
	list, _ := v.Nodes(t.Context())
	sum, _ := v.Summary(t.Context())
	raw, _ := json.Marshal(map[string]any{"nodes": list, "summary": sum})
	for _, banned := range []string{"10.8.0.2", "7443", "203.0.113.7", "spiffe", strings.Repeat("ab", 32), "endpoint", "nat_ip", "spki"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("fleet view leaks %q: %s", banned, raw)
		}
	}
}

func TestFleetViewReadErrorsAreNotAnEmptyFleet(t *testing.T) {
	f := newFleet()
	f.add(grant("n1", 4), hostState("n1", 4, 8<<30))
	v := newView(t, f, nil, nil)
	boom := errors.New("store down")
	f.stateE = boom
	if _, err := v.Nodes(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("state error = %v", err)
	}
	f.stateE, f.allocE = nil, boom
	if _, err := v.Summary(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("allocation error = %v", err)
	}
	if _, err := v.HelperHosts(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("hosts error = %v", err)
	}
	if _, err := NewFleetView(FleetViewConfig{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty config = %v", err)
	}
}

func TestFleetViewHelperHosts(t *testing.T) {
	f := newFleet()
	a := grant("n1", 4)
	a.Endpoint = "Helper.Internal:7443"
	f.add(a, hostState("n1", 4, 8<<30))
	f.add(grant("n2", 4), hostState("n2", 4, 8<<30))
	hosts, err := newView(t, f, nil, nil).HelperHosts(t.Context())
	if err != nil || len(hosts) != 2 || !hosts["helper.internal"] || !hosts["10.8.0.2"] {
		t.Fatalf("hosts = %v err = %v", hosts, err)
	}
}
