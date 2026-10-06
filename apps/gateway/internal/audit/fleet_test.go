package audit

import (
	"context"
	"errors"
	"testing"
)

type failStore struct{ *MemoryStore }

func (failStore) Append(context.Context, Record) (Record, error) { return Record{}, errors.New("boom") }

func TestFleetGuardRefusesOnAuditError(t *testing.T) {
	f := &Fleet{Store: failStore{NewMemoryStore()}}
	ran := false
	for _, rec := range []Record{
		f.NodeEvent(EventNodeEnrolled, "n1", "admin", "allow", nil),
		f.AttemptEvent("t1", EventAttemptGranted, "n1", "j1", "allow", nil),
		f.NodeEvent(EventViewerOpened, "n1", "op", "allow", nil),
	} {
		err := f.Guard(context.Background(), rec, func() error { ran = true; return nil })
		if !errors.Is(err, ErrAuditUnavailable) {
			t.Fatalf("%s: err = %v, want ErrAuditUnavailable", rec.Action, err)
		}
	}
	if ran {
		t.Fatal("action ran despite audit failure")
	}
	if err := (*Fleet)(nil).Guard(context.Background(), Record{}, func() error { ran = true; return nil }); !errors.Is(err, ErrAuditUnavailable) || ran {
		t.Fatal("nil Fleet must fail closed")
	}
}

func TestFleetMixedSequenceVerifiesAndRoutesTenants(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()
	f := &Fleet{Store: st}
	agg := NewRejectAggregator(f, 2)
	for _, rec := range []Record{
		f.NodeEvent(EventNodeEnrolled, "n1", "admin", "allow", nil),
		f.AttemptEvent("tenant-a", EventAttemptGranted, "n1", "j1", "allow", nil),
		f.NodeEvent(EventNodeCertRotated, "n1", "node:n1", "allow", nil),
		f.AttemptEvent("tenant-a", EventAttemptCommitted, "n1", "j1", "allow", nil),
		f.NodeEvent(EventNodeRevoked, "n1", "admin", "allow", nil),
	} {
		if err := f.Guard(ctx, rec, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 50; i++ { // flood: one entry per key, not per attempt
		agg.Observe("unknown_spki", "n1")
	}
	agg.Observe("revoked", "n2")
	agg.Observe("revoked", "n3") // over maxKeys=2 -> "other"
	if err := agg.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	fleet, _ := st.List(ctx, Filter{TenantID: FleetTenant})
	jobs, _ := st.List(ctx, Filter{TenantID: "tenant-a"})
	if len(jobs) != 2 || len(fleet) != 3+3 {
		t.Fatalf("fleet=%d jobs=%d, want 6 and 2", len(fleet), len(jobs))
	}
	if !VerifyChain(fleet) || !VerifyChain(jobs) {
		t.Fatal("chain does not verify")
	}
	var n1 any
	for _, r := range fleet {
		if r.Action == EventNodeAuthRejected && r.Resource == "node/n1" {
			n1 = r.Attributes["count"]
		}
	}
	if n1 != 50 {
		t.Fatalf("aggregated count = %v, want 50", n1)
	}
}

func TestFleetRejectFlushRequeuesOnStoreError(t *testing.T) {
	f := &Fleet{Store: failStore{NewMemoryStore()}}
	agg := NewRejectAggregator(f, 0)
	agg.Observe("revoked", "n1")
	if err := agg.Flush(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if agg.counts[[2]string{"revoked", "n1"}] != 1 {
		t.Fatal("count not re-queued")
	}
}
