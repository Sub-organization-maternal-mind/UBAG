package helpermetrics

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

func alloc(id, state string, valid time.Time) nodes.Allocation {
	return nodes.Allocation{
		NodeID: id, Region: "eu", Endpoint: "10.0.0.2:7443", URISAN: nodes.NodeURISAN(id),
		CPUMillis: 1500, MemoryBytes: 1 << 31, ReservationState: nodes.ReservationKnown,
		State: state, MaxBrowserWorkloads: 3, ValidUntil: valid, Generation: 1,
	}
}

func render(t *testing.T, src NodeSource, now time.Time) string {
	t.Helper()
	var b bytes.Buffer
	Write(context.Background(), &b, src, now)
	return b.String()
}

func TestCountersBoundedLabels(t *testing.T) {
	RecordLeaseRenewFailure(LeaseAttempt, ReasonLost)
	RecordLeaseRenewFailure("weird-"+strings.Repeat("x", 50), ReasonError)
	RecordFencedReject("stale_generation")
	RecordFencedReject("data names another job_id") // helper-influenced text must not become a label
	RecordPolicyViolation("job_scope")
	out := render(t, nil, time.Now())
	for _, want := range []string{
		`ubag_lease_renew_failures_total{lease="attempt",reason="lost"} `,
		`ubag_lease_renew_failures_total{lease="other",reason="error"} `,
		`ubag_helper_fenced_rejects_total{reason="stale_generation"} `,
		`ubag_helper_fenced_rejects_total{reason="other"} `,
		`ubag_helper_policy_violations_total{reason="job_scope"} `,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "another job_id") || strings.Contains(out, "weird-") {
		t.Fatal("unbounded label value leaked")
	}
	if strings.Contains(out, "ubag_helper_nodes") {
		t.Fatal("node gauges must be absent without a source")
	}
}

func TestNodeGauges(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	st := nodes.NewMemoryStore()
	for _, a := range []nodes.Allocation{
		alloc("ok", nodes.StateActive, now.Add(time.Hour)),
		alloc("stale", nodes.StateActive, now.Add(time.Hour)),
		alloc("never", nodes.StateActive, now.Add(time.Hour)),
		alloc("drain", nodes.StateDraining, now.Add(time.Hour)),
		alloc("expired", nodes.StateActive, now.Add(-time.Minute)),
	} {
		if err := st.ApplyAllocation(ctx, a, now); err != nil {
			t.Fatal(err)
		}
	}
	for id, beat := range map[string]time.Duration{"ok": 5 * time.Second, "stale": 100 * time.Second, "drain": time.Second, "expired": time.Second} {
		if err := st.PutState(ctx, nodes.HelperState{NodeID: id, LastHeartbeat: now.Add(-beat), HostCores: 4, HostMemoryBytes: 8 << 30}); err != nil {
			t.Fatal(err)
		}
	}
	out := render(t, st, now)
	for _, want := range []string{
		"ubag_helper_metrics_source_up 1\n",
		`ubag_helper_nodes{admission="eligible"} 1`,
		`ubag_helper_nodes{admission="heartbeat_missed"} 2`, // stale + never
		`ubag_helper_nodes{admission="draining"} 1`,
		`ubag_helper_nodes{admission="grant_expired"} 1`,
		`ubag_helper_node_heartbeat_age_seconds{node_id="ok"} 5.000`,
		`ubag_helper_node_heartbeat_age_seconds{node_id="stale"} 100.000`,
		`ubag_helper_node_drain_state{node_id="drain"} 1`,
		`ubag_helper_node_drain_state{node_id="ok"} 0`,
		`ubag_helper_node_admission_limit{node_id="stale"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `heartbeat_age_seconds{node_id="never"}`) {
		t.Error("a node that never heartbeated must have no age series")
	}
	if n := strings.Count(out, "# TYPE ubag_helper_node_drain_state"); n != 1 {
		t.Errorf("TYPE emitted %d times", n)
	}
}

type failingSource struct{}

func (failingSource) ListAllocations(context.Context) ([]nodes.Allocation, error) {
	return nil, errors.New("db down")
}
func (failingSource) GetState(context.Context, string) (nodes.HelperState, error) {
	return nodes.HelperState{}, nil
}

func TestSourceFailureIsVisible(t *testing.T) {
	out := render(t, failingSource{}, time.Now())
	if !strings.Contains(out, "ubag_helper_metrics_source_up 0\n") || strings.Contains(out, "ubag_helper_nodes{") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}
