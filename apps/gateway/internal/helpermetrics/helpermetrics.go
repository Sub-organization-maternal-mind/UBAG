// Package helpermetrics holds the helper-plane and lease metrics (P4.23): heartbeat
// age, drain state and admission-by-reason per helper node (computed at scrape
// from the node store, so they cost nothing while UBAG_HELPER_NODES is off),
// plus process-wide counters for lease renewal failures, fenced writers and
// helper policy violations. It is a leaf (imports only nodes) so the executor
// can record without importing the HTTP layer. Every label is a bounded set.
package helpermetrics

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// Lease kinds for RecordLeaseRenewFailure.
const (
	LeaseQueue   = "queue"   // spool/NATS delivery lease (P4.3)
	LeaseExec    = "exec"    // execution lease token (admission)
	LeaseAttempt = "attempt" // attempt ledger lease (helper dispatcher)
	LeaseVoice   = "voice"   // helper-hosted voice call: media lease and the node's own lease (P5.11)
)

// Renewal failure reasons.
const (
	ReasonLost  = "lost"  // the lease no longer belongs to the holder
	ReasonError = "error" // transient store/IO error
)

var (
	leaseKinds   = []string{LeaseQueue, LeaseExec, LeaseAttempt, LeaseVoice}
	leaseReasons = []string{ReasonLost, ReasonError}
	// The reason sets mirror the executor's fixed audit reasons. Anything else
	// (helper-influenced text such as "data names another job_id") is folded
	// into "other" so a helper cannot grow the label set.
	fencedReasons    = []string{"stale_generation", "attempt_expired", "attempt_mismatch", "commit_fenced"}
	violationReasons = []string{"job_scope", "unknown_attempt", "node_mismatch", "fingerprint_mismatch", "helper_output_limit", "helper_event_invalid"}
	// Placement outcomes (P4.17): where a leased job went, or why it was held. No
	// tenant, job, node or profile ever becomes a label.
	placementOutcomes = []string{
		"placed", "local_no_fleet", "local_no_profile", "local_conversation",
		"held_identity_busy", "held_no_capacity", "held_no_node", "held_affinity", "held_error",
	}
	// Probe results mirror nodes.ProbeOK, nodes.ProbeError and nodes.ProbeIncompatible.
	probeResults = []string{"ok", "error", "incompatible"}
	// Voice placement outcomes (P5.9): where one helper-hosted voice candidate went.
	// Host health (node_unavailable), media capability (not_voice_capable), capacity
	// and lane refusals are separate buckets, and provider readiness (the account's
	// login state) is never one of them: it is decided before a node is asked.
	voicePlacementOutcomes = []string{
		"placed", "node_unavailable", "not_voice_capable", "no_capacity", "identity_busy", "wan_endpoint", "error",
	}
)

var (
	mu         sync.Mutex
	leaseFail  = map[[2]string]int64{}
	fencedRej  = map[string]int64{}
	violations = map[string]int64{}
	reconciled = map[nodes.ReconcileOutcome]int64{}
	placements = map[string]int64{}
	probes     = map[string]int64{}
	voicePlace = map[string]int64{}
)

func norm(set []string, v string) string {
	for _, s := range set {
		if s == v {
			return v
		}
	}
	return "other"
}

// RecordLeaseRenewFailure counts a failed lease renewal. Unknown kinds/reasons
// are folded into "other" (bounded labels).
func RecordLeaseRenewFailure(kind, reason string) {
	mu.Lock()
	leaseFail[[2]string{norm(leaseKinds, kind), norm(leaseReasons, reason)}]++
	mu.Unlock()
}

// RecordFencedReject counts a helper write rejected as stale/fenced.
func RecordFencedReject(reason string) {
	mu.Lock()
	fencedRej[norm(fencedReasons, reason)]++
	mu.Unlock()
}

// RecordPolicyViolation counts a helper write rejected or failed for a policy
// violation (wrong scope, bad content, over budget).
func RecordPolicyViolation(reason string) {
	mu.Lock()
	violations[norm(violationReasons, reason)]++
	mu.Unlock()
}

// RecordReconcile counts one attempt-reconcile decision (P4.18). A pair the
// policy cannot produce is folded into action="other", reason="other" (bounded
// labels).
func RecordReconcile(action, reason string) {
	o := nodes.ReconcileOutcome{Action: nodes.ReconcileAction(action), Reason: reason}
	if !slices.Contains(nodes.ReconcileOutcomes, o) {
		o = nodes.ReconcileOutcome{Action: "other", Reason: "other"}
	}
	mu.Lock()
	reconciled[o]++
	mu.Unlock()
}

// RecordPlacement counts one placement decision by outcome (bounded labels).
func RecordPlacement(outcome string) {
	mu.Lock()
	placements[norm(placementOutcomes, outcome)]++
	mu.Unlock()
}

// RecordVoicePlacement counts one helper-hosted voice placement decision by
// outcome (bounded labels).
func RecordVoicePlacement(outcome string) {
	mu.Lock()
	voicePlace[norm(voicePlacementOutcomes, outcome)]++
	mu.Unlock()
}

// RecordProbe counts one helper capacity report by result (bounded labels).
func RecordProbe(result string) {
	mu.Lock()
	probes[norm(probeResults, result)]++
	mu.Unlock()
}

// NodeSource is the read side of nodes.Store the scrape needs.
type NodeSource interface {
	ListAllocations(ctx context.Context) ([]nodes.Allocation, error)
	GetState(ctx context.Context, nodeID string) (nodes.HelperState, error)
}

// admissionReasons is every value of the `admission` label, in output order.
var admissionReasons = []string{
	"eligible", nodes.ReasonRevoked, nodes.ReasonDraining, nodes.ReasonReservationUnk,
	nodes.ReasonGrantExpired, nodes.ReasonHeartbeatMissed, nodes.ReasonNoCapacity,
}

func header(w io.Writer, name, kind, help string) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}

func writeReasons(w io.Writer, name string, m map[string]int64, fixed []string) {
	writeLabeled(w, name, "reason", m, fixed)
}

func writeLabeled(w io.Writer, name, label string, m map[string]int64, fixed []string) {
	for _, r := range append(append([]string{}, fixed...), "other") {
		_, _ = fmt.Fprintf(w, "%s{%s=\"%s\"} %d\n", name, label, r, m[r])
	}
}

// Write emits the counters and, when src is non-nil, the per-node gauges. A
// source failure is reported as ubag_helper_metrics_source_up 0 (so an alert
// can fire) rather than failing the whole scrape. ponytail: one GetState per
// node per scrape (<= nodes.MaxNodes, body cached 5 s by the caller); batch it
// if the Postgres store ever needs it.
func Write(ctx context.Context, w io.Writer, src NodeSource, now time.Time) {
	mu.Lock()
	lf := make(map[[2]string]int64, len(leaseFail))
	for k, v := range leaseFail {
		lf[k] = v
	}
	fr := make(map[string]int64, len(fencedRej))
	for k, v := range fencedRej {
		fr[k] = v
	}
	pv := make(map[string]int64, len(violations))
	for k, v := range violations {
		pv[k] = v
	}
	rc := make(map[nodes.ReconcileOutcome]int64, len(reconciled))
	for k, v := range reconciled {
		rc[k] = v
	}
	pl := make(map[string]int64, len(placements))
	for k, v := range placements {
		pl[k] = v
	}
	pr := make(map[string]int64, len(probes))
	for k, v := range probes {
		pr[k] = v
	}
	vp := make(map[string]int64, len(voicePlace))
	for k, v := range voicePlace {
		vp[k] = v
	}
	mu.Unlock()

	header(w, "ubag_lease_renew_failures_total", "counter", "Failed lease renewals by lease kind and reason.")
	for _, kind := range append(append([]string{}, leaseKinds...), "other") {
		for _, reason := range append(append([]string{}, leaseReasons...), "other") {
			if (kind == "other" || reason == "other") && lf[[2]string{kind, reason}] == 0 {
				continue
			}
			_, _ = fmt.Fprintf(w, "ubag_lease_renew_failures_total{lease=\"%s\",reason=\"%s\"} %d\n", kind, reason, lf[[2]string{kind, reason}])
		}
	}
	header(w, "ubag_helper_fenced_rejects_total", "counter", "Helper writes rejected as stale or fenced (UBAG-WORKER-NODE-FENCED-005).")
	writeReasons(w, "ubag_helper_fenced_rejects_total", fr, fencedReasons)
	header(w, "ubag_helper_policy_violations_total", "counter", "Helper writes rejected or failed for policy violations.")
	writeReasons(w, "ubag_helper_policy_violations_total", pv, violationReasons)
	header(w, "ubag_helper_reconcile_total", "counter", "Attempt reconcile decisions for leased jobs that already have attempts (P4.18).")
	for _, o := range nodes.ReconcileOutcomes {
		_, _ = fmt.Fprintf(w, "ubag_helper_reconcile_total{action=\"%s\",reason=\"%s\"} %d\n", o.Action, o.Reason, rc[o])
	}
	if other := rc[nodes.ReconcileOutcome{Action: "other", Reason: "other"}]; other > 0 {
		_, _ = fmt.Fprintf(w, "ubag_helper_reconcile_total{action=\"other\",reason=\"other\"} %d\n", other)
	}
	header(w, "ubag_helper_placements_total", "counter", "Helper placement decisions for leased jobs: placed, run on this gateway, or held back.")
	writeLabeled(w, "ubag_helper_placements_total", "outcome", pl, placementOutcomes)
	header(w, "ubag_helper_voice_placements_total", "counter", "Helper-hosted voice placement decisions per candidate account: placed, or why its node could not host it (host health, media capability, capacity, identity lane).")
	writeLabeled(w, "ubag_helper_voice_placements_total", "outcome", vp, voicePlacementOutcomes)
	header(w, "ubag_helper_probes_total", "counter", "Helper capacity reports by result (ok, error, incompatible).")
	writeLabeled(w, "ubag_helper_probes_total", "result", pr, probeResults)

	if src == nil {
		return
	}
	header(w, "ubag_helper_metrics_source_up", "gauge", "1 when the helper node store was readable for this scrape.")
	allocs, err := src.ListAllocations(ctx)
	if err != nil {
		_, _ = fmt.Fprint(w, "ubag_helper_metrics_source_up 0\n")
		return
	}
	_, _ = fmt.Fprint(w, "ubag_helper_metrics_source_up 1\n")

	byReason := map[string]int{}
	var age, drain, limit strings.Builder
	for _, a := range allocs {
		st, serr := src.GetState(ctx, a.NodeID)
		if serr != nil {
			st = nodes.HelperState{NodeID: a.NodeID} // no heartbeat yet or unreadable: fail closed
		}
		d := nodes.Admission(a, st, now) // the verdict placement uses: Evaluate plus the ceiling table
		reason := "eligible"
		if !d.Eligible {
			reason = d.Reason
		}
		byReason[reason]++
		id := promLabel(a.NodeID)
		if !st.LastHeartbeat.IsZero() {
			_, _ = fmt.Fprintf(&age, "ubag_helper_node_heartbeat_age_seconds{node_id=\"%s\"} %.3f\n", id, max(now.Sub(st.LastHeartbeat), 0).Seconds())
		}
		_, _ = fmt.Fprintf(&drain, "ubag_helper_node_drain_state{node_id=\"%s\"} %d\n", id, drainState(a.State))
		_, _ = fmt.Fprintf(&limit, "ubag_helper_node_admission_limit{node_id=\"%s\"} %d\n", id, d.Limit)
	}
	header(w, "ubag_helper_nodes", "gauge", "Helper nodes by current admission verdict (eligible or the refusal reason).")
	for _, r := range admissionReasons {
		_, _ = fmt.Fprintf(w, "ubag_helper_nodes{admission=\"%s\"} %d\n", r, byReason[r])
	}
	header(w, "ubag_helper_node_heartbeat_age_seconds", "gauge", "Seconds since the node's last heartbeat (absent until the first).")
	_, _ = io.WriteString(w, age.String())
	header(w, "ubag_helper_node_drain_state", "gauge", "Grant state: 0 active, 1 draining, 2 revoked.")
	_, _ = io.WriteString(w, drain.String())
	header(w, "ubag_helper_node_admission_limit", "gauge", "Concurrent browser workloads currently admitted (0 = refused).")
	_, _ = io.WriteString(w, limit.String())
}

func drainState(state string) int {
	switch state {
	case nodes.StateActive:
		return 0
	case nodes.StateDraining:
		return 1
	default: // revoked, and fail closed on anything unknown
		return 2
	}
}

// promLabel escapes a label value (node ids are [A-Za-z0-9._-], but never trust it).
func promLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}
