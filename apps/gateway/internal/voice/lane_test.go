package voice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

// ListLeaseHolders is the truth behind the job consumer's lane check, so every
// store must report exactly the sessions that reserve a browser: live ones and
// held terminations, never queued sessions, elapsed holds or released ones.
func testLeaseHoldersFollowLeases(t *testing.T, st Store) {
	ctx := t.Context()
	holders := func(at time.Time) map[string]string {
		t.Helper()
		got, err := st.ListLeaseHolders(ctx, at)
		if err != nil {
			t.Fatalf("ListLeaseHolders: %v", err)
		}
		out := map[string]string{}
		for _, h := range got {
			out[h.SessionID] = h.TenantID + "/" + h.InstanceRef
		}
		return out
	}
	expect := func(at time.Time, want map[string]string) {
		t.Helper()
		got := holders(at)
		if len(got) != len(want) {
			t.Fatalf("holders = %v, want %v", got, want)
		}
		for id, v := range want {
			if got[id] != v {
				t.Fatalf("holders = %v, want %v", got, want)
			}
		}
	}

	expect(leaseNow, map[string]string{})
	reserveAt(t, st, "l-a", "tenant_a", leaseNow, time.Hour, Placement{"acct-1", "browser-1"})
	// Queued: the environment is taken, so the session holds nothing.
	if q := reserveAt(t, st, "l-q", "tenant_b", leaseNow, time.Hour, Placement{"acct-9", "browser-1"}); q.Status != StatusQueued {
		t.Fatalf("setup: %+v", q)
	}
	reserveAt(t, st, "l-b", "tenant_b", leaseNow, time.Hour, Placement{"acct-2", "browser-2"})
	expect(leaseNow, map[string]string{"l-a": "tenant_a/browser-1", "l-b": "tenant_b/browser-2"})

	// A held termination still reserves its environment (Get already reports no
	// leases for it), until the hold elapses.
	now := leaseNow.Add(time.Second)
	if err := st.BeginTerminate(ctx, "tenant_a", "l-a", now, "terminated_by_client", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	expect(now.Add(5*time.Second), map[string]string{"l-a": "tenant_a/browser-1", "l-b": "tenant_b/browser-2"})
	expect(now.Add(11*time.Second), map[string]string{"l-b": "tenant_b/browser-2"})

	// The deactivate ack frees a hold at once; a plain Terminate frees a live one.
	if err := st.BeginTerminate(ctx, "tenant_b", "l-b", now, "terminated_by_client", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	expect(now, map[string]string{"l-a": "tenant_a/browser-1", "l-b": "tenant_b/browser-2"})
	if err := st.ReleaseHold(ctx, "tenant_b", "l-b", LeaseFence{}, now); err != nil {
		t.Fatal(err)
	}
	expect(now, map[string]string{"l-a": "tenant_a/browser-1"})
	reserveAt(t, st, "l-c", "tenant_a", now, time.Hour, Placement{"acct-3", "browser-3"})
	if err := st.Terminate(ctx, "tenant_a", "l-c", now, "gone"); err != nil {
		t.Fatal(err)
	}
	expect(now, map[string]string{"l-a": "tenant_a/browser-1"})
}

func TestLeaseHoldersFollowLeases(t *testing.T) {
	eachStore(t, testLeaseHoldersFollowLeases)
}

type instancesFake struct {
	topology.Store
	instances map[string][]topology.BrowserInstance // tenant -> instances
	err       error
}

func (f instancesFake) ListInstances(_ context.Context, filter topology.InstanceFilter) ([]topology.BrowserInstance, error) {
	return f.instances[filter.TenantID], f.err
}

func TestLaneProbeMatchesTheSessionsBrowser(t *testing.T) {
	store := NewMemoryStore()
	laneOf := topology.BrowserLaneKey("http://browser:9222")
	topo := instancesFake{instances: map[string][]topology.BrowserInstance{
		"tenant_a": {{InstanceID: "browser-1", RemoteEndpoint: "ws://Browser:9222/devtools/browser/x"}, {InstanceID: "browser-2", RemoteEndpoint: "http://other:9222"}},
	}}
	probe := &LaneProbe{Store: store, Topology: topo, Now: func() time.Time { return leaseNow }}
	holds := func(lane string) bool {
		t.Helper()
		held, err := probe.VoiceHoldsLane(t.Context(), lane)
		if err != nil {
			t.Fatalf("VoiceHoldsLane: %v", err)
		}
		return held
	}

	if holds(laneOf) {
		t.Fatal("no session, no hold")
	}
	reserveAt(t, store, "p-1", "tenant_a", leaseNow, time.Hour, Placement{"acct-1", "browser-1"})
	if !holds(laneOf) {
		t.Fatal("a live session must hold its browser's lane (any spelling of the endpoint)")
	}
	if holds(topology.BrowserLaneKey("http://other:9222")) || holds(topology.BrowserLaneKey("http://browser:9333")) {
		t.Fatal("another browser's lane is not held")
	}
	if holds("") {
		t.Fatal("no lane (a local browser) is never held")
	}

	// A held termination keeps the lane while the provider UI is torn down.
	if err := store.BeginTerminate(t.Context(), "tenant_a", "p-1", leaseNow, "terminated_by_client", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if !holds(laneOf) {
		t.Fatal("a terminating hold must keep the lane")
	}
	probe.Now = func() time.Time { return leaseNow.Add(11 * time.Second) }
	if holds(laneOf) {
		t.Fatal("an elapsed hold frees the lane")
	}
}

func TestLaneProbeFailsClosed(t *testing.T) {
	store := NewMemoryStore()
	reserveAt(t, store, "f-1", "tenant_a", leaseNow, time.Hour, Placement{"acct-1", "browser-1"})
	lane := topology.BrowserLaneKey("http://browser:9222")
	probe := func(topo topology.Store) *LaneProbe {
		return &LaneProbe{Store: store, Topology: topo, Now: func() time.Time { return leaseNow }}
	}

	// A holder whose browser cannot be identified might be this one.
	for name, topo := range map[string]topology.Store{
		"instance not in the topology": instancesFake{},
		"instance without an endpoint": instancesFake{instances: map[string][]topology.BrowserInstance{"tenant_a": {{InstanceID: "browser-1"}}}},
		"no topology store":            nil,
	} {
		held, err := probe(topo).VoiceHoldsLane(t.Context(), lane)
		if err != nil || !held {
			t.Errorf("%s: held=%v err=%v, want held (fail closed)", name, held, err)
		}
	}
	// An unreadable topology is an error, never "not held".
	boom := errors.New("topology down")
	if held, err := probe(instancesFake{err: boom}).VoiceHoldsLane(t.Context(), lane); !errors.Is(err, boom) || held {
		t.Errorf("topology error: held=%v err=%v, want the error", held, err)
	}
	// An unreadable voice store is an error too.
	if held, err := (&LaneProbe{Store: failingStore{Store: store}, Topology: instancesFake{}}).VoiceHoldsLane(t.Context(), lane); err == nil || held {
		t.Errorf("store error: held=%v err=%v, want an error", held, err)
	}
	// A nil probe (voice off) holds nothing.
	if held, err := (*LaneProbe)(nil).VoiceHoldsLane(t.Context(), lane); held || err != nil {
		t.Errorf("nil probe: held=%v err=%v", held, err)
	}
}

type failingStore struct{ Store }

func (failingStore) ListLeaseHolders(context.Context, time.Time) ([]LeaseHolder, error) {
	return nil, errors.New("store down")
}

func TestMediaHubAvailabilityFollowsTheRelaySecret(t *testing.T) {
	if (&MediaHub{Dialer: &TCPRelayDialer{}}).MediaAvailable() {
		t.Fatal("a hub with no relay secret must report media unavailable")
	}
	if !(&MediaHub{Dialer: &TCPRelayDialer{Secret: []byte("s")}}).MediaAvailable() {
		t.Fatal("a hub with a relay secret must report media available")
	}
}
