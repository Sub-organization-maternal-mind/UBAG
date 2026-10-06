package voice

import (
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

// A session bound to a Helper Node reserves that node's audio environment, which
// is no browser of the primary (P5.9): the store reports the node with the holder,
// and the lane probe does not treat the holder as an unidentifiable browser that
// might be the lane in question.
func testPlacementLeaseHoldersCarryTheNode(t *testing.T, st Store) {
	ctx := t.Context()
	reserveAt(t, st, "n-a", "tenant_a", leaseNow, time.Hour, Placement{"acct-1", "browser-1"})
	reserveAt(t, st, "n-b", "tenant_a", leaseNow, time.Hour, Placement{"acct-2", "helper-env-1"})
	if _, err := st.BindNode(ctx, "tenant_a", "n-b", "node-a", time.Minute, leaseNow); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListLeaseHolders(ctx, leaseNow)
	if err != nil {
		t.Fatal(err)
	}
	nodeOf := map[string]string{}
	for _, h := range got {
		nodeOf[h.SessionID] = h.NodeID
	}
	if len(nodeOf) != 2 || nodeOf["n-a"] != "" || nodeOf["n-b"] != "node-a" {
		t.Fatalf("holders' nodes = %v, want n-a on the primary and n-b on node-a", nodeOf)
	}
	// A held termination keeps its node, so the primary lane still does not see it.
	if err := st.BeginTerminate(ctx, "tenant_a", "n-b", leaseNow, "terminated_by_client", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	got, err = st.ListLeaseHolders(ctx, leaseNow.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, h := range got {
		if h.SessionID == "n-b" {
			seen = true
			if h.NodeID != "node-a" {
				t.Fatalf("a held termination lost its node: %+v", h)
			}
		}
	}
	if !seen {
		t.Fatal("a held termination must still reserve its environment")
	}
}

func TestPlacementLeaseHoldersCarryTheNode(t *testing.T) {
	eachStore(t, testPlacementLeaseHoldersCarryTheNode)
}

func TestPlacementLaneProbeIgnoresNodeHostedHolders(t *testing.T) {
	store := NewMemoryStore()
	lane := topology.BrowserLaneKey("http://browser:9222")
	// Neither environment is in the primary's topology with an endpoint.
	probe := &LaneProbe{Store: store, Topology: instancesFake{}, Now: func() time.Time { return leaseNow }}

	reserveAt(t, store, "h-1", "tenant_a", leaseNow, time.Hour, Placement{"acct-2", "helper-env-1"})
	if held, err := probe.VoiceHoldsLane(t.Context(), lane); err != nil || !held {
		t.Fatalf("an unbound holder with an unknown browser must hold the lane (fail closed): held=%v err=%v", held, err)
	}
	if _, err := store.BindNode(t.Context(), "tenant_a", "h-1", "node-a", time.Minute, leaseNow); err != nil {
		t.Fatal(err)
	}
	if held, err := probe.VoiceHoldsLane(t.Context(), lane); err != nil || held {
		t.Fatalf("a session on a helper node holds no primary lane: held=%v err=%v", held, err)
	}
	// ... while a primary-hosted session of unknown browser still does.
	reserveAt(t, store, "p-1", "tenant_a", leaseNow, time.Hour, Placement{"acct-1", "browser-1"})
	if held, err := probe.VoiceHoldsLane(t.Context(), lane); err != nil || !held {
		t.Fatalf("the fail-closed rule for primary sessions is unchanged: held=%v err=%v", held, err)
	}
}
