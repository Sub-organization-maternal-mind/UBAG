package helperauth

import (
	"context"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth/authtest"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// TestFleetRejectionsAggregateIntoAudit wires OnReject to the fleet aggregator:
// repeated rejections land as one node.auth_rejected record with a count.
func TestFleetRejectionsAggregateIntoAudit(t *testing.T) {
	st := audit.NewMemoryStore()
	agg := audit.NewRejectAggregator(&audit.Fleet{Store: st}, 8)
	e := newEnv(t, false, &fakeHelper{})
	e.auth.OnReject = func(r Reason, n string) { agg.Observe(string(r), n) }
	// Trusted CA, but the node is not in the registry.
	ghost := e.ca.Issue(t, authtest.Spec{NodeID: "ghost"})
	for i := 0; i < 3; i++ {
		if _, err := e.dial(&ghost.TLS).Handshake(ctx5(t), &helperv1.HandshakeRequest{}); err == nil {
			t.Fatal("unregistered node was accepted")
		}
	}
	if err := agg.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	recs, _ := st.List(context.Background(), audit.Filter{TenantID: audit.FleetTenant})
	if len(recs) != 1 || recs[0].Action != audit.EventNodeAuthRejected || recs[0].Attributes["count"] != 3 {
		t.Fatalf("records = %+v, want one aggregated entry with count 3", recs)
	}
	if !audit.VerifyChain(recs) {
		t.Fatal("chain does not verify")
	}
}
