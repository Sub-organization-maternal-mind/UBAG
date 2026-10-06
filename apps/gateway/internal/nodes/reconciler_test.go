package nodes

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

var (
	rcT0  = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	rcCfg = ReconcileConfig{Window: 10 * time.Minute, Margin: 15 * time.Second, Poll: 10 * time.Second}
)

const rcFingerprint = "fp_aaaaaaaa"

// rcAttempt is the job's one attempt: generation 1 on node_a, lease lapsing at
// lapse, submitted or not.
func rcAttempt(state jobstore.AttemptState, lapse time.Time, submitted bool) jobstore.Attempt {
	a := jobstore.Attempt{
		JobID: "job_1", AttemptID: "att_one", Generation: 1, NodeID: "node_a", State: state,
		LeaseExpiresAt: lapse, InputFingerprint: rcFingerprint, WorkloadVersion: "w1", CreatedAt: rcT0.Add(-time.Hour),
	}
	if submitted {
		at := rcT0.Add(-30 * time.Minute)
		a.SubmittedAt = &at
	}
	return a
}

// rcView is a consistent helper answer for rcAttempt.
func rcView(state HelperAttemptState, submitted bool) *HelperView {
	v := &HelperView{State: state, Generation: 1, Fingerprint: rcFingerprint, Submitted: submitted}
	if state == HelperAttemptFinished {
		v.HasOutcome, v.LastSequence = true, 7
	}
	return v
}

// ---- the policy -------------------------------------------------------------

func TestReconcilePolicyTable(t *testing.T) {
	var (
		held     = rcT0.Add(60 * time.Second)  // lease valid for another minute
		inMargin = rcT0.Add(-5 * time.Second)  // lapsed, still inside the 15 s margin
		settled  = rcT0.Add(-30 * time.Second) // lapsed and the holder has had time to stop
		longGone = rcT0.Add(-11 * time.Minute) // lapsed past the 10 min window
	)
	active, expired, finished := jobstore.AttemptActive, jobstore.AttemptExpired, jobstore.AttemptFinished
	errDown := errors.New("connection refused")

	cases := []struct {
		name       string
		attempts   []jobstore.Attempt
		inspected  bool
		view       *HelperView
		inspectErr error

		wantInspect bool
		want        ReconcileAction
		wantReason  string
		wantNextGen uint64
	}{
		// ---- nothing to reconcile
		{name: "a job without attempts runs", want: ReconcileRun, wantReason: ReconcileNoAttempt, wantNextGen: 1},

		// ---- lost before submission: wait out the lease, then reassign at generation+1
		{name: "unsubmitted, lease still held: wait", attempts: []jobstore.Attempt{rcAttempt(active, held, false)},
			want: ReconcileWait, wantReason: ReconcileLeaseHeld, wantNextGen: 2},
		{name: "unsubmitted, lapsed but inside the margin: wait", attempts: []jobstore.Attempt{rcAttempt(active, inMargin, false)},
			want: ReconcileWait, wantReason: ReconcileLeaseHeld, wantNextGen: 2},
		{name: "unsubmitted, settled: ask the helper first", attempts: []jobstore.Attempt{rcAttempt(active, settled, false)},
			wantInspect: true, wantNextGen: 2},
		{name: "unsubmitted, settled, helper unreachable: reassign at generation+1", attempts: []jobstore.Attempt{rcAttempt(active, settled, false)},
			inspected: true, inspectErr: errDown, want: ReconcileRun, wantReason: ReconcileAttemptLapsed, wantNextGen: 2},
		{name: "unsubmitted, settled, helper has no record: reassign", attempts: []jobstore.Attempt{rcAttempt(active, settled, false)},
			inspected: true, view: &HelperView{State: HelperAttemptUnknown}, want: ReconcileRun, wantReason: ReconcileAttemptLapsed, wantNextGen: 2},
		{name: "unsubmitted, settled, helper finished without submitting: reassign", attempts: []jobstore.Attempt{rcAttempt(active, settled, false)},
			inspected: true, view: rcView(HelperAttemptFinished, false), want: ReconcileRun, wantReason: ReconcileAttemptLapsed, wantNextGen: 2},
		{name: "unsubmitted, settled, node revoked: reassign", attempts: []jobstore.Attempt{rcAttempt(active, settled, false)},
			inspected: true, inspectErr: ErrNodeRevoked, want: ReconcileRun, wantReason: ReconcileAttemptLapsed, wantNextGen: 2},
		{name: "unsubmitted, a stale answer is ignored, not trusted", attempts: []jobstore.Attempt{rcAttempt(active, settled, false)},
			inspected: true, view: &HelperView{State: HelperAttemptRunning, Generation: 9, Fingerprint: rcFingerprint, Submitted: true},
			want: ReconcileRun, wantReason: ReconcileAttemptLapsed, wantNextGen: 2},
		{name: "unsubmitted in the ledger but the helper saw the prompt leave: collect, record the boundary",
			attempts: []jobstore.Attempt{rcAttempt(active, settled, false)}, inspected: true, view: rcView(HelperAttemptFinished, true),
			want: ReconcileResume, wantReason: ReconcileHelperSubmitted, wantNextGen: 2},
		{name: "unsubmitted, helper still running long after its lease: wait", attempts: []jobstore.Attempt{rcAttempt(active, settled, false)},
			inspected: true, view: rcView(HelperAttemptRunning, false), want: ReconcileWait, wantReason: ReconcileHelperRunning, wantNextGen: 2},
		{name: "unsubmitted, helper never stops it: fail closed after the window", attempts: []jobstore.Attempt{rcAttempt(active, longGone, false)},
			inspected: true, view: rcView(HelperAttemptRunning, false), want: ReconcileFailClosed, wantReason: ReconcileNotStopping, wantNextGen: 2},
		{name: "an expired unsubmitted attempt is simply over: run", attempts: []jobstore.Attempt{rcAttempt(expired, settled, false)},
			want: ReconcileRun, wantReason: ReconcileAttemptEnded, wantNextGen: 2},

		// ---- lost after submission: ask the helper, never run
		{name: "submitted, lease still held: ask the helper, do not wait", attempts: []jobstore.Attempt{rcAttempt(active, held, true)},
			wantInspect: true, wantNextGen: 2},
		{name: "submitted, helper unreachable inside the window: wait", attempts: []jobstore.Attempt{rcAttempt(active, settled, true)},
			inspected: true, inspectErr: errDown, want: ReconcileWait, wantReason: ReconcileHelperUnreachable, wantNextGen: 2},
		{name: "submitted, helper unreachable and the lease still valid: wait", attempts: []jobstore.Attempt{rcAttempt(active, held, true)},
			inspected: true, inspectErr: errDown, want: ReconcileWait, wantReason: ReconcileHelperUnreachable, wantNextGen: 2},
		{name: "submitted, helper unreachable for the whole window: fail closed", attempts: []jobstore.Attempt{rcAttempt(active, longGone, true)},
			inspected: true, inspectErr: errDown, want: ReconcileFailClosed, wantReason: ReconcileHelperUnreachable, wantNextGen: 2},
		{name: "submitted, the window ends exactly now: fail closed", attempts: []jobstore.Attempt{rcAttempt(active, rcT0.Add(-rcCfg.Window), true)},
			inspected: true, inspectErr: errDown, want: ReconcileFailClosed, wantReason: ReconcileHelperUnreachable, wantNextGen: 2},
		{name: "submitted, the window has one second left: still waiting", attempts: []jobstore.Attempt{rcAttempt(active, rcT0.Add(-rcCfg.Window+time.Second), true)},
			inspected: true, inspectErr: errDown, want: ReconcileWait, wantReason: ReconcileHelperUnreachable, wantNextGen: 2},
		{name: "submitted, no answer and no error: treated as unreachable", attempts: []jobstore.Attempt{rcAttempt(active, settled, true)},
			inspected: true, want: ReconcileWait, wantReason: ReconcileHelperUnreachable, wantNextGen: 2},
		{name: "submitted, node revoked: fail closed at once", attempts: []jobstore.Attempt{rcAttempt(active, held, true)},
			inspected: true, inspectErr: ErrNodeRevoked, want: ReconcileFailClosed, wantReason: ReconcileNodeRevoked, wantNextGen: 2},
		{name: "submitted, the helper restarted and forgot it: fail closed", attempts: []jobstore.Attempt{rcAttempt(active, settled, true)},
			inspected: true, view: &HelperView{State: HelperAttemptUnknown}, want: ReconcileFailClosed, wantReason: ReconcileNoRecord, wantNextGen: 2},
		{name: "submitted, the helper still runs it: resume", attempts: []jobstore.Attempt{rcAttempt(active, held, true)},
			inspected: true, view: rcView(HelperAttemptRunning, true), want: ReconcileResume, wantReason: ReconcileHelperHolds, wantNextGen: 2},
		{name: "submitted, the helper holds the finished outcome: resume", attempts: []jobstore.Attempt{rcAttempt(active, settled, true)},
			inspected: true, view: rcView(HelperAttemptFinished, true), want: ReconcileResume, wantReason: ReconcileHelperFinished, wantNextGen: 2},

		// ---- stale helper result rejected
		{name: "submitted, the answer is for another lease generation: rejected", attempts: []jobstore.Attempt{rcAttempt(active, settled, true)},
			inspected: true, view: &HelperView{State: HelperAttemptFinished, Generation: 2, Fingerprint: rcFingerprint, HasOutcome: true, LastSequence: 3},
			want: ReconcileFailClosed, wantReason: ReconcileStale, wantNextGen: 2},
		{name: "submitted, the answer is for another input: rejected", attempts: []jobstore.Attempt{rcAttempt(active, settled, true)},
			inspected: true, view: &HelperView{State: HelperAttemptRunning, Generation: 1, Fingerprint: "fp_other"},
			want: ReconcileFailClosed, wantReason: ReconcileStale, wantNextGen: 2},
		{name: "submitted, the answer has no fingerprint: rejected", attempts: []jobstore.Attempt{rcAttempt(active, settled, true)},
			inspected: true, view: &HelperView{State: HelperAttemptRunning, Generation: 1},
			want: ReconcileFailClosed, wantReason: ReconcileStale, wantNextGen: 2},
		{name: "submitted, finished without an outcome: rejected", attempts: []jobstore.Attempt{rcAttempt(active, settled, true)},
			inspected: true, view: &HelperView{State: HelperAttemptFinished, Generation: 1, Fingerprint: rcFingerprint, LastSequence: 4},
			want: ReconcileFailClosed, wantReason: ReconcileStale, wantNextGen: 2},
		{name: "submitted, finished with no last event: rejected", attempts: []jobstore.Attempt{rcAttempt(active, settled, true)},
			inspected: true, view: &HelperView{State: HelperAttemptFinished, Generation: 1, Fingerprint: rcFingerprint, HasOutcome: true},
			want: ReconcileFailClosed, wantReason: ReconcileStale, wantNextGen: 2},

		// ---- the fence is closed: nothing the helper holds can be committed
		{name: "an expired submitted attempt is failed closed", attempts: []jobstore.Attempt{rcAttempt(expired, settled, true)},
			want: ReconcileFailClosed, wantReason: ReconcileFenced, wantNextGen: 2},
		{name: "a finished submitted attempt under a live job is failed closed", attempts: []jobstore.Attempt{rcAttempt(finished, settled, true)},
			want: ReconcileFailClosed, wantReason: ReconcileFenced, wantNextGen: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := PlanReconcile(ReconcileInput{
				Now: rcT0, Attempts: tc.attempts, Inspected: tc.inspected, View: tc.view, InspectErr: tc.inspectErr,
			}, rcCfg)
			if p.NeedInspect != tc.wantInspect {
				t.Fatalf("NeedInspect = %v, want %v (%+v)", p.NeedInspect, tc.wantInspect, p)
			}
			if tc.wantInspect {
				if p.Action != "" || p.Attempt.AttemptID != "att_one" {
					t.Fatalf("a plan that needs the helper must name the attempt and decide nothing: %+v", p)
				}
				return
			}
			if p.Action != tc.want || p.Reason != tc.wantReason {
				t.Fatalf("plan = %s/%s, want %s/%s", p.Action, p.Reason, tc.want, tc.wantReason)
			}
			if p.NextGeneration != tc.wantNextGen {
				t.Fatalf("NextGeneration = %d, want %d", p.NextGeneration, tc.wantNextGen)
			}
			if n := len(tc.attempts); n > 0 {
				if want := tc.attempts[n-1].LeaseExpiresAt.Add(rcCfg.Window); !p.Deadline.Equal(want) {
					t.Fatalf("Deadline = %s, want the lease lapse plus the window (%s)", p.Deadline, want)
				}
			}
			if p.Action == ReconcileWait && (p.RetryAfter < time.Second || p.RetryAfter > rcCfg.Poll) {
				t.Fatalf("RetryAfter = %s, want within [1s, %s]", p.RetryAfter, rcCfg.Poll)
			}
			if !slices.Contains(ReconcileOutcomes, ReconcileOutcome{Action: p.Action, Reason: p.Reason}) {
				t.Fatalf("%s/%s is not in ReconcileOutcomes (the metric label set)", p.Action, p.Reason)
			}
		})
	}
}

// The ledger says a prompt left: whatever the attempt looks like and whatever the
// helper answers, the plan is never to run the job again.
func TestReconcileNeverRunsASubmittedAttempt(t *testing.T) {
	states := []jobstore.AttemptState{jobstore.AttemptActive, jobstore.AttemptExpired, jobstore.AttemptFinished}
	lapses := []time.Time{rcT0.Add(time.Minute), rcT0.Add(-time.Second), rcT0.Add(-time.Minute), rcT0.Add(-time.Hour), rcT0.Add(-48 * time.Hour)}
	errs := []error{nil, errors.New("down"), ErrNodeRevoked}
	var views []*HelperView
	for _, st := range []HelperAttemptState{HelperAttemptUnknown, HelperAttemptRunning, HelperAttemptFinished} {
		for _, gen := range []uint64{0, 1, 2} {
			for _, fp := range []string{"", rcFingerprint, "other"} {
				for _, sub := range []bool{false, true} {
					for _, outcome := range []bool{false, true} {
						views = append(views, &HelperView{State: st, Generation: gen, Fingerprint: fp, Submitted: sub, HasOutcome: outcome, LastSequence: 3})
					}
				}
			}
		}
	}
	views = append(views, nil)
	for _, st := range states {
		for _, lapse := range lapses {
			a := rcAttempt(st, lapse, true)
			first := PlanReconcile(ReconcileInput{Now: rcT0, Attempts: []jobstore.Attempt{a}}, rcCfg)
			if first.Action == ReconcileRun {
				t.Fatalf("%s lapsing %s: run before the helper was even asked", st, lapse)
			}
			for _, e := range errs {
				for _, v := range views {
					p := PlanReconcile(ReconcileInput{Now: rcT0, Attempts: []jobstore.Attempt{a}, Inspected: true, View: v, InspectErr: e}, rcCfg)
					if p.Action == ReconcileRun || (p.Action == "" && !p.NeedInspect) {
						t.Fatalf("%s lapsing %s, err %v, view %+v: plan %+v", st, lapse, e, v, p)
					}
					if !slices.Contains(ReconcileOutcomes, ReconcileOutcome{Action: p.Action, Reason: p.Reason}) {
						t.Fatalf("plan %s/%s is outside the metric label set", p.Action, p.Reason)
					}
				}
			}
		}
	}
}

func TestReconcileConfigDefaultsAndBounds(t *testing.T) {
	got := ReconcileConfig{}.normalized()
	if got.Window != DefaultReconcileWindow || got.Margin != DefaultReconcileMargin || got.Poll != DefaultReconcilePoll {
		t.Fatalf("defaults = %+v", got)
	}
	if got := (ReconcileConfig{Window: 1000 * time.Hour}).normalized(); got.Window != MaxReconcileWindow {
		t.Fatalf("window %s is not capped at %s", got.Window, MaxReconcileWindow)
	}
	// A negative or zero value can never shorten the window to nothing.
	if got := (ReconcileConfig{Window: -time.Hour, Margin: -1}).normalized(); got.Window != DefaultReconcileWindow || got.Margin != DefaultReconcileMargin {
		t.Fatalf("negative values were not replaced: %+v", got)
	}
}

// ---- the reconciler -----------------------------------------------------------

type rcLedger struct {
	attempts []jobstore.Attempt
	err      error
}

func (l rcLedger) ListAttempts(context.Context, string) ([]jobstore.Attempt, error) {
	return l.attempts, l.err
}

type rcInspector struct {
	view  HelperView
	err   error
	calls atomic.Int32
	asked []string
}

func (i *rcInspector) Inspect(_ context.Context, nodeID, jobID, attemptID string) (HelperView, error) {
	i.calls.Add(1)
	i.asked = append(i.asked, nodeID+"/"+jobID+"/"+attemptID)
	return i.view, i.err
}

type rcRegistry struct {
	entry RegistryEntry
	err   error
}

func (r rcRegistry) GetRegistry(context.Context, string) (RegistryEntry, error) {
	return r.entry, r.err
}

func rcReconciler(l Ledger, i Inspector, r RegistrySource) *Reconciler {
	return &Reconciler{Ledger: l, Inspector: i, Registry: r, Config: rcCfg, Now: func() time.Time { return rcT0 }}
}

func TestReconcileDoesNotAskTheHelperUnlessTheAnswerDependsOnIt(t *testing.T) {
	insp := &rcInspector{}
	for name, attempts := range map[string][]jobstore.Attempt{
		"no attempts":        nil,
		"a lease still held": {rcAttempt(jobstore.AttemptActive, rcT0.Add(time.Minute), false)},
		"an expired attempt": {rcAttempt(jobstore.AttemptExpired, rcT0.Add(-time.Hour), false)},
		"an expired submit":  {rcAttempt(jobstore.AttemptExpired, rcT0.Add(-time.Hour), true)},
	} {
		if _, err := rcReconciler(rcLedger{attempts: attempts}, insp, nil).Reconcile(t.Context(), "job_1"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if insp.calls.Load() != 0 {
		t.Fatalf("the helper was asked %d times for answers the ledger already decides", insp.calls.Load())
	}
}

func TestReconcileAsksTheNodeThatHoldsTheAttemptAndOnlyRead(t *testing.T) {
	insp := &rcInspector{view: *rcView(HelperAttemptRunning, true)}
	plan, err := rcReconciler(rcLedger{attempts: []jobstore.Attempt{rcAttempt(jobstore.AttemptActive, rcT0.Add(time.Minute), true)}}, insp, nil).
		Reconcile(t.Context(), "job_1")
	if err != nil || plan.Action != ReconcileResume || plan.Reason != ReconcileHelperHolds {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	if !slices.Equal(insp.asked, []string{"node_a/job_1/att_one"}) {
		t.Fatalf("asked = %v", insp.asked)
	}
}

func TestReconcileRevokedNodeIsNeverAsked(t *testing.T) {
	insp := &rcInspector{view: *rcView(HelperAttemptRunning, true)}
	revoked := rcRegistry{entry: RegistryEntry{NodeID: "node_a", RevokedAt: rcT0.Add(-time.Hour)}}
	plan, err := rcReconciler(rcLedger{attempts: []jobstore.Attempt{rcAttempt(jobstore.AttemptActive, rcT0.Add(time.Minute), true)}}, insp, revoked).
		Reconcile(t.Context(), "job_1")
	if err != nil || plan.Action != ReconcileFailClosed || plan.Reason != ReconcileNodeRevoked {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	if insp.calls.Load() != 0 {
		t.Fatal("a revoked node was dialed")
	}
	// A registry that cannot answer says nothing about revocation: the dial refuses
	// such a node on its own, and the inspection is simply unreachable.
	insp = &rcInspector{err: errors.New("dial refused")}
	plan, err = rcReconciler(rcLedger{attempts: []jobstore.Attempt{rcAttempt(jobstore.AttemptActive, rcT0.Add(-time.Minute), true)}}, insp,
		rcRegistry{err: errors.New("store down")}).Reconcile(t.Context(), "job_1")
	if err != nil || plan.Action != ReconcileWait || plan.Reason != ReconcileHelperUnreachable || insp.calls.Load() != 1 {
		t.Fatalf("plan = %+v, err = %v, asked %d", plan, err, insp.calls.Load())
	}
}

func TestReconcileUnreadableLedgerIsAnErrorNotAPlan(t *testing.T) {
	insp := &rcInspector{}
	plan, err := rcReconciler(rcLedger{err: errors.New("postgres down")}, insp, nil).Reconcile(t.Context(), "job_1")
	if err == nil || plan.Action != "" {
		t.Fatalf("plan = %+v, err = %v: a ledger that cannot be read must not look like \"nothing to reconcile\"", plan, err)
	}
	if _, err := (*Reconciler)(nil).Reconcile(t.Context(), "job_1"); err == nil {
		t.Fatal("a nil reconciler must not answer")
	}
	if _, err := (&Reconciler{}).Reconcile(t.Context(), "job_1"); err == nil {
		t.Fatal("a reconciler without a ledger must not answer")
	}
}

// The window is judged on the clock after the helper was asked, and a helper that
// stays silent ends the wait exactly when the window is out.
func TestReconcileHelperStaysUnreachableUntilTheWindowEnds(t *testing.T) {
	now := rcT0
	r := rcReconciler(rcLedger{attempts: []jobstore.Attempt{rcAttempt(jobstore.AttemptActive, rcT0, true)}}, // lapsed just now
		&rcInspector{err: errors.New("no route to host")}, nil)
	r.Now = func() time.Time { return now }
	for _, step := range []struct {
		advance time.Duration
		want    ReconcileAction
	}{{0, ReconcileWait}, {5 * time.Minute, ReconcileWait}, {4*time.Minute + 59*time.Second, ReconcileWait}, {time.Second, ReconcileFailClosed}, {time.Hour, ReconcileFailClosed}} {
		now = now.Add(step.advance)
		plan, err := r.Reconcile(t.Context(), "job_1")
		if err != nil || plan.Action != step.want || plan.Reason != ReconcileHelperUnreachable {
			t.Fatalf("at +%s: plan = %+v, err = %v, want %s", now.Sub(rcT0), plan, err, step.want)
		}
	}
}

// Manager down: the reconciler reads the node store's last accepted state only. A
// grant that lapsed because the manager stopped renewing it ends new placements,
// not the inspection of an attempt that was already placed.
func TestReconcileManagerDownStillSettlesAnAlreadyPlacedAttempt(t *testing.T) {
	ctx := t.Context()
	store := NewMemoryStore()
	a := Allocation{
		NodeID: "node_a", Region: "eu", Endpoint: "10.0.0.2:7443", URISAN: NodeURISAN("node_a"),
		CPUMillis: 1500, MemoryBytes: 1 << 31, ReservationState: ReservationKnown, State: StateActive,
		MaxBrowserWorkloads: 3, ValidUntil: rcT0.Add(-6 * time.Hour), Generation: 1, // lapsed hours ago: nobody renewed it
	}
	if err := store.ApplyAllocation(ctx, a, rcT0.Add(-12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	pin := "a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7"
	if err := store.PutRegistry(ctx, RegistryEntry{NodeID: "node_a", URISAN: NodeURISAN("node_a"), SPKICurrent: pin}, rcT0.Add(-12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if d := Evaluate(a.Grant(), rcT0.Add(-time.Minute), rcT0, Pressure{}, 1); d.Eligible || d.Reason != ReasonGrantExpired {
		t.Fatalf("the lapsed grant must refuse new placements: %+v", d)
	}
	insp := &rcInspector{view: *rcView(HelperAttemptFinished, true)}
	plan, err := rcReconciler(rcLedger{attempts: []jobstore.Attempt{rcAttempt(jobstore.AttemptActive, rcT0.Add(-time.Minute), true)}}, insp, store).
		Reconcile(ctx, "job_1")
	if err != nil || plan.Action != ReconcileResume || plan.Reason != ReconcileHelperFinished {
		t.Fatalf("plan = %+v, err = %v: a lapsed grant must not stop the reconcile", plan, err)
	}
	if insp.calls.Load() != 1 {
		t.Fatalf("the helper was asked %d times", insp.calls.Load())
	}
}

// ---- against the real ledger ----------------------------------------------------

func rcJob(t *testing.T, store *jobstore.MemoryStore) jobstore.Job {
	t.Helper()
	job, err := store.Create(t.Context(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", IdempotencyKey: "reconcile_job_key_0001",
		Target: "mock", CommandType: "submit", Input: map[string]any{"prompt": "hi"}, TraceID: "trace_reconcile",
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func rcBegin(t *testing.T, store *jobstore.MemoryStore, jobID, attemptID string, expected uint64, ttl time.Duration) jobstore.Attempt {
	t.Helper()
	att, err := store.BeginAttempt(t.Context(), jobstore.BeginAttemptRequest{
		JobID: jobID, AttemptID: attemptID, NodeID: "node_a", ExpectedGeneration: expected, TTL: ttl,
		InputFingerprint: rcFingerprint, WorkloadVersion: "w1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return att
}

// Lost before submission: once the lease has lapsed (and the helper cannot say
// otherwise) the job is reassigned, the new attempt takes generation 2, and the
// old holder is fenced the moment it does: nothing it reports can reach the job.
func TestReconcileLostBeforeSubmissionReassignsAtTheNextGenerationAndFencesTheOldHolder(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job := rcJob(t, store)
	first := rcBegin(t, store, job.ID, "att_one", 0, 30*time.Millisecond)

	r := &Reconciler{Ledger: store, Inspector: &rcInspector{err: errors.New("helper gone")}, Config: ReconcileConfig{Margin: time.Millisecond}}
	if plan, err := r.Reconcile(t.Context(), job.ID); err != nil || plan.Action != ReconcileWait || plan.Reason != ReconcileLeaseHeld {
		t.Fatalf("while the lease is held: plan = %+v, err = %v", plan, err)
	}
	time.Sleep(80 * time.Millisecond)
	plan, err := r.Reconcile(t.Context(), job.ID)
	if err != nil || plan.Action != ReconcileRun || plan.Reason != ReconcileAttemptLapsed || plan.NextGeneration != 2 || plan.Attempt.AttemptID != first.AttemptID {
		t.Fatalf("after the lapse: plan = %+v, err = %v", plan, err)
	}

	second := rcBegin(t, store, job.ID, "att_two", plan.Attempt.Generation, 0)
	if second.Generation != plan.NextGeneration {
		t.Fatalf("the new attempt took generation %d, the plan said %d", second.Generation, plan.NextGeneration)
	}
	if _, err := store.RenewAttempt(t.Context(), first.Ref(), 0); !errors.Is(err, jobstore.ErrAttemptFenced) {
		t.Fatalf("the old holder renewing: err = %v, want fenced", err)
	}
	_, err = store.CommitEvents(t.Context(), first.Ref(), []jobstore.WorkerEvent{{
		EventID: "att_one:1", JobID: job.ID, APIVersion: "2026-05-22", Type: "running", TraceID: "trace_reconcile",
		Data: map[string]any{"attempt_id": "att_one"},
	}})
	if !errors.Is(err, jobstore.ErrAttemptFenced) {
		t.Fatalf("the old holder committing: err = %v, want fenced", err)
	}
}

// Lost after submission: whatever the helper answers, the ledger itself refuses a
// successor attempt, and the reconciler never says run.
func TestReconcileLostAfterSubmissionNeverRunsAndTheLedgerAgrees(t *testing.T) {
	for _, tc := range []struct {
		name     string
		insp     *rcInspector
		want     ReconcileAction
		wantWhy  string
		wantCall int32
	}{
		{"helper holds the finished outcome", &rcInspector{view: *rcView(HelperAttemptFinished, true)}, ReconcileResume, ReconcileHelperFinished, 1},
		{"helper still runs it", &rcInspector{view: *rcView(HelperAttemptRunning, true)}, ReconcileResume, ReconcileHelperHolds, 1},
		{"helper forgot it", &rcInspector{view: HelperView{State: HelperAttemptUnknown}}, ReconcileFailClosed, ReconcileNoRecord, 1},
		{"helper answers for another generation", &rcInspector{view: HelperView{State: HelperAttemptFinished, Generation: 5, Fingerprint: rcFingerprint, HasOutcome: true, LastSequence: 2}},
			ReconcileFailClosed, ReconcileStale, 1},
		{"helper unreachable", &rcInspector{err: errors.New("down")}, ReconcileWait, ReconcileHelperUnreachable, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := jobstore.NewMemoryStore()
			job := rcJob(t, store)
			att := rcBegin(t, store, job.ID, "att_one", 0, 30*time.Millisecond)
			if _, err := store.MarkSubmitted(t.Context(), att.Ref()); err != nil {
				t.Fatal(err)
			}
			time.Sleep(80 * time.Millisecond) // the lease lapsed: nothing renews it any more

			r := &Reconciler{Ledger: store, Inspector: tc.insp, Config: ReconcileConfig{Margin: time.Millisecond}}
			plan, err := r.Reconcile(t.Context(), job.ID)
			if err != nil || plan.Action != tc.want || plan.Reason != tc.wantWhy {
				t.Fatalf("plan = %+v, err = %v, want %s/%s", plan, err, tc.want, tc.wantWhy)
			}
			if tc.insp.calls.Load() != tc.wantCall {
				t.Fatalf("the helper was asked %d times, want %d", tc.insp.calls.Load(), tc.wantCall)
			}
			_, err = store.BeginAttempt(t.Context(), jobstore.BeginAttemptRequest{
				JobID: job.ID, AttemptID: "att_two", NodeID: "node_b", ExpectedGeneration: 1,
				InputFingerprint: rcFingerprint, WorkloadVersion: "w1",
			})
			if !errors.Is(err, jobstore.ErrAttemptSubmitted) {
				t.Fatalf("a successor attempt: err = %v, want ErrAttemptSubmitted", err)
			}
		})
	}
}

func TestReconcileViewConsistency(t *testing.T) {
	att := rcAttempt(jobstore.AttemptActive, rcT0, true)
	for name, tc := range map[string]struct {
		v    HelperView
		want bool
	}{
		"running, same generation and input": {*rcView(HelperAttemptRunning, true), true},
		"finished with outcome and sequence": {*rcView(HelperAttemptFinished, true), true},
		"another generation":                 {HelperView{State: HelperAttemptRunning, Generation: 2, Fingerprint: rcFingerprint}, false},
		"generation zero":                    {HelperView{State: HelperAttemptRunning, Fingerprint: rcFingerprint}, false},
		"another input":                      {HelperView{State: HelperAttemptRunning, Generation: 1, Fingerprint: "fp_x"}, false},
		"no input":                           {HelperView{State: HelperAttemptRunning, Generation: 1}, false},
		"finished without an outcome":        {HelperView{State: HelperAttemptFinished, Generation: 1, Fingerprint: rcFingerprint, LastSequence: 2}, false},
		"finished without a sequence":        {HelperView{State: HelperAttemptFinished, Generation: 1, Fingerprint: rcFingerprint, HasOutcome: true}, false},
	} {
		if got := tc.v.Consistent(att); got != tc.want {
			t.Errorf("%s: Consistent = %v, want %v", name, got, tc.want)
		}
	}
	// An attempt the ledger recorded without a fingerprint cannot match any answer.
	noFP := att
	noFP.InputFingerprint = ""
	if (HelperView{State: HelperAttemptRunning, Generation: 1}).Consistent(noFP) {
		t.Error("an empty fingerprint on both sides must not count as a match")
	}
}
