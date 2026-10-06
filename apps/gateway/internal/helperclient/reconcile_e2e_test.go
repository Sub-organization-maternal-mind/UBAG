package helperclient

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/helper"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// Attempt reconcile (P4.18) against the real ubag-helper service over real mutual
// TLS on loopback. The "lost primary" is played by the test: it leases an attempt
// in the ledger, starts it on the helper, reads until the prompt is submitted and
// then vanishes (no renewal, no cancel). A new RemoteWorkerRunner and a
// nodes.Reconciler then settle it the way a restarted gateway does.

// staticEndpoint is the node store: one node, the rig's address.
type staticEndpoint struct{ addr string }

func (s staticEndpoint) GetAllocation(_ context.Context, id string) (nodes.Allocation, error) {
	return nodes.Allocation{NodeID: id, Endpoint: s.addr}, nil
}

const strandedAttempt = "att_stranded"

// strand plays the lost primary. The ledger lease is ledgerTTL; the lease the helper
// is told is helperTTL; generationOnWire is the generation the helper is given
// (the ledger's own is 1).
func (e *e2e) strand(env executor.DispatchEnvelope, ledgerTTL, helperTTL time.Duration, generationOnWire uint64) jobstore.Attempt {
	e.t.Helper()
	const fingerprint = "fp_e2e_reconcile"
	att, err := e.store.BeginAttempt(e.t.Context(), jobstore.BeginAttemptRequest{
		JobID: env.JobID, AttemptID: strandedAttempt, NodeID: testNode, TTL: ledgerTTL,
		InputFingerprint: fingerprint, WorkloadVersion: "w1",
	})
	if err != nil {
		e.t.Fatal(err)
	}
	conn, err := e.rig.dialer(e.rig.primary, nil, nil).Dial(e.t.Context(), testNode, e.rig.addr)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.t.Context())
	defer cancel()
	stream, err := conn.RunAttempt(ctx, &helperv1.RunAttemptRequest{
		Fence: &helperv1.Fence{
			JobId: env.JobID, AttemptId: strandedAttempt, NodeId: testNode, LeaseGeneration: generationOnWire,
			LeaseExpiresAt: timestamppb.New(time.Now().Add(helperTTL)), InputFingerprint: fingerprint, WorkloadVersion: "w1",
		},
		Provider: "mock", Target: "mock", CommandType: "submit", IdentityRef: "pr_e2e", InputJson: `{"prompt":"hi"}`, DeadlineSeconds: 30,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			e.t.Fatalf("the stream ended before the prompt was submitted: %v", err)
		}
		if resp.GetEvent().GetType() == evSubmitted {
			break
		}
	}
	if _, err := e.store.MarkSubmitted(e.t.Context(), att.Ref()); err != nil {
		e.t.Fatal(err)
	}
	cancel() // the primary is gone: the stream ends, nobody renews or cancels
	_ = conn.Close()
	return att
}

func (e *e2e) reconciler(window time.Duration) *nodes.Reconciler {
	return &nodes.Reconciler{
		Ledger: e.store, Inspector: e.runner,
		Config: nodes.ReconcileConfig{Window: window, Margin: 5 * time.Millisecond, Poll: 50 * time.Millisecond},
	}
}

// scripted is a helper browser that starts, submits and then waits to be released.
func scripted(runs *atomic.Int32, release <-chan struct{}) helper.RunnerFunc {
	return func(ctx context.Context, _ helper.AttemptSpec, emit helper.EmitFunc) error {
		runs.Add(1)
		_ = emit(helper.Event{Type: evStarted})
		_ = emit(helper.Event{Type: evSubmitted})
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		_ = emit(helper.Event{Type: evToken, DataJSON: `{"delta":{"text":"x"}}`})
		return emit(helper.Event{Type: evTerminal, Outcome: &helperv1.AttemptOutcome{
			Status: helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED, ResultJson: `{"type":"text","text":"collected from the real helper"}`}})
	}
}

// The helper finished the attempt while the primary was gone. The restarted
// primary reads the finished outcome with InspectAttempt and commits it; the helper
// ran the browser script once (nothing was started again).
func TestReconcileCollectsAFinishedAttemptFromTheRealHelper(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	e := newE2E(t, scripted(&runs, release))
	job, env := e.job()
	att := e.strand(env, 150*time.Millisecond, 30*time.Second, 1)
	close(release) // the answer arrives while nobody is connected
	time.Sleep(300 * time.Millisecond)

	rec := e.reconciler(time.Minute)
	plan, err := rec.Reconcile(t.Context(), job.ID)
	if err != nil || plan.Action != nodes.ReconcileResume || plan.Reason != nodes.ReconcileHelperFinished || plan.Attempt.AttemptID != att.AttemptID {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	if err := e.runner.Resume(t.Context(), env, plan.Attempt, plan.Deadline); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := e.status(job.ID); got != jobstore.StatusCompleted {
		t.Fatalf("job status = %s", got)
	}
	stored, _, _ := e.store.Get(t.Context(), job.ID)
	if result, _ := stored.Result.(map[string]any); result == nil {
		t.Fatalf("result = %#v", stored.Result)
	} else if output, _ := result["output"].(map[string]any); output["text"] != "collected from the real helper" {
		t.Fatalf("result = %#v", stored.Result)
	}
	if attempts, _ := e.store.ListAttempts(t.Context(), job.ID); len(attempts) != 1 || attempts[0].State != jobstore.AttemptFinished || attempts[0].AttemptID != strandedAttempt {
		t.Fatalf("attempts = %+v", attempts)
	}
	if runs.Load() != 1 {
		t.Fatalf("the helper's browser ran %d times: the attempt was started again", runs.Load())
	}
}

// The helper is still running the attempt, and its own lease is about to lapse (it
// was told 300 ms). The restarted primary takes the lease over on both sides; the
// attempt outlives its original lease and finishes. Without the renewals the helper
// would have killed it.
func TestReconcileAdoptsARunningAttemptOnTheRealHelperAndKeepsItAlive(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	e := newE2E(t, scripted(&runs, release), func(c *executor.RemoteConfig) {
		c.LeaseTTL, c.RenewEvery, c.MaxClockSkew, c.PollEvery = 600*time.Millisecond, 100*time.Millisecond, 50*time.Millisecond, 20*time.Millisecond
	})
	job, env := e.job()
	e.strand(env, 300*time.Millisecond, 300*time.Millisecond, 1)

	plan, err := e.reconciler(time.Minute).Reconcile(t.Context(), job.ID)
	if err != nil || plan.Action != nodes.ReconcileResume || plan.Reason != nodes.ReconcileHelperHolds {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	errc := make(chan error, 1)
	go func() { errc <- e.runner.Resume(t.Context(), env, plan.Attempt, plan.Deadline) }()

	time.Sleep(900 * time.Millisecond) // three times the lease the helper was originally given
	select {
	case err := <-errc:
		t.Fatalf("Resume returned while the attempt was still running: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Resume: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Resume did not return")
	}
	if got := e.status(job.ID); got != jobstore.StatusCompleted {
		t.Fatalf("job status = %s: the helper let the attempt lapse despite the adoption", got)
	}
	if runs.Load() != 1 {
		t.Fatalf("the helper's browser ran %d times", runs.Load())
	}
}

// A helper that has no record of the attempt (it restarted, or never had it) says
// UNKNOWN, not an error: the submitted attempt is failed for reconciling and the job
// is never run again, by the ledger's own rule too.
func TestReconcileRealHelperThatForgotTheAttemptFailsClosed(t *testing.T) {
	var runs atomic.Int32
	e := newE2E(t, scripted(&runs, make(chan struct{})))
	job, env := e.job()
	att, err := e.store.BeginAttempt(t.Context(), jobstore.BeginAttemptRequest{
		JobID: env.JobID, AttemptID: "att_ghost", NodeID: testNode, TTL: 30 * time.Millisecond,
		InputFingerprint: "fp_e2e_reconcile", WorkloadVersion: "w1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.MarkSubmitted(t.Context(), att.Ref()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)

	plan, err := e.reconciler(time.Minute).Reconcile(t.Context(), job.ID)
	if err != nil || plan.Action != nodes.ReconcileFailClosed || plan.Reason != nodes.ReconcileNoRecord {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	_, err = e.store.BeginAttempt(t.Context(), jobstore.BeginAttemptRequest{
		JobID: env.JobID, AttemptID: "att_again", NodeID: testNode, ExpectedGeneration: 1,
		InputFingerprint: "fp_e2e_reconcile", WorkloadVersion: "w1",
	})
	if !errors.Is(err, jobstore.ErrAttemptSubmitted) {
		t.Fatalf("a successor attempt: err = %v, want the ledger's refusal", err)
	}
	if runs.Load() != 0 {
		t.Fatal("the helper ran something")
	}
}

// The helper holds the attempt under another lease generation than the ledger's: a
// stale answer. The reconciler rejects it, and so does Resume if it is handed the
// attempt directly: nothing from that helper reaches the job.
func TestReconcileRealHelperStaleGenerationIsRejected(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	e := newE2E(t, scripted(&runs, release))
	job, env := e.job()
	att := e.strand(env, 150*time.Millisecond, 30*time.Second, 2) // the ledger says generation 1
	close(release)
	time.Sleep(300 * time.Millisecond)

	plan, err := e.reconciler(time.Minute).Reconcile(t.Context(), job.ID)
	if err != nil || plan.Action != nodes.ReconcileFailClosed || plan.Reason != nodes.ReconcileStale {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	err = e.runner.Resume(t.Context(), env, att, plan.Deadline)
	if !errors.Is(err, executor.ErrAmbiguous) {
		t.Fatalf("Resume = %v, want a failed-closed attempt", err)
	}
	if got := e.status(job.ID); jobstore.TerminalStatus(got) {
		t.Fatalf("job status = %s: nothing from the stale answer may touch the job", got)
	}
	events, _, _ := e.store.ListEvents(t.Context(), job.ID, 0, 100)
	for _, ev := range events {
		if _, fromHelper := ev.Data["helper"]; fromHelper || ev.Type != "queued" {
			t.Fatalf("an event of the stale helper reached the job: %+v", ev)
		}
	}
}
