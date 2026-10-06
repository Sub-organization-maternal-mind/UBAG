package executor

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubag/ubag/apps/gateway/internal/helpermetrics"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// Attempt reconcile (P4.18) against a fake helper, the real memory ledger and the
// real fenced ingest. Each scenario starts from the ledger a crashed gateway leaves
// behind: an attempt on node_a, its lease lapsed (nothing renews it any more).

const reconcileFingerprint = "fp_reconcile_test_0001"

type fakeNodeEndpoints struct {
	endpoint string
	err      error
}

func (n fakeNodeEndpoints) GetAllocation(_ context.Context, id string) (nodes.Allocation, error) {
	if n.err != nil {
		return nodes.Allocation{}, n.err
	}
	return nodes.Allocation{NodeID: id, Endpoint: n.endpoint}, nil
}

type reconcileFixture struct {
	*remoteFixture
	cfg nodes.ReconcileConfig

	mu    sync.Mutex
	dials []HelperPlacement
}

func newReconcileFixture(t *testing.T, mut ...func(*RemoteConfig)) *reconcileFixture {
	t.Helper()
	rf := &reconcileFixture{cfg: nodes.ReconcileConfig{Window: 600 * time.Millisecond, Margin: 5 * time.Millisecond, Poll: 30 * time.Millisecond}}
	rf.remoteFixture = newRemoteFixture(t, append([]func(*RemoteConfig){
		fastLease,
		func(c *RemoteConfig) {
			c.Nodes = fakeNodeEndpoints{endpoint: "10.0.0.2:7443"}
			c.PollEvery = 10 * time.Millisecond
			c.Dialer = HelperDialFunc(func(_ context.Context, p HelperPlacement) (HelperConn, error) {
				rf.mu.Lock()
				rf.dials = append(rf.dials, p)
				rf.mu.Unlock()
				return rf.helper, nil
			})
		},
	}, mut...)...)
	return rf
}

// reconcileConsumer is the usual consumer with the ledger-first gate in front.
func (rf *reconcileFixture) reconcileConsumer(lease *fakeWorkerLease) *WorkerConsumer {
	c := rf.consumer(lease, nil)
	c.Reconcile = &nodes.Reconciler{Ledger: rf.store, Inspector: rf.runner, Config: rf.cfg}
	return c
}

// seed leaves the ledger a crashed gateway would: attempt att_crashed on node_a,
// optionally past the submission boundary, with its lease already lapsed.
func (rf *reconcileFixture) seed(job jobstore.Job, submitted bool) jobstore.Attempt {
	rf.t.Helper()
	att, err := rf.store.BeginAttempt(rf.t.Context(), jobstore.BeginAttemptRequest{
		JobID: job.ID, AttemptID: "att_crashed", NodeID: "node_a", TTL: 30 * time.Millisecond,
		InputFingerprint: reconcileFingerprint, WorkloadVersion: testWorkload,
	})
	if err != nil {
		rf.t.Fatal(err)
	}
	if submitted {
		if _, err := rf.store.MarkSubmitted(rf.t.Context(), att.Ref()); err != nil {
			rf.t.Fatal(err)
		}
	}
	time.Sleep(70 * time.Millisecond) // the lease lapsed: the holder is gone
	return att
}

func (rf *reconcileFixture) answer(fn func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error)) {
	rf.helper.mu.Lock()
	rf.helper.inspect = fn
	rf.helper.mu.Unlock()
}

func (rf *reconcileFixture) dialed() []HelperPlacement {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return slices.Clone(rf.dials)
}

func reconcileOutcome() *helperv1.AttemptOutcome {
	return &helperv1.AttemptOutcome{Status: stCompleted, Submitted: true, ResultJson: `{"type":"text","text":"finished while the gateway was down"}`}
}

// finishedAnswer is a helper that ended the attempt: its last event is the terminal.
func finishedAnswer(gen uint64, fp string, o *helperv1.AttemptOutcome, lastSeq uint64) *helperv1.InspectAttemptResponse {
	return &helperv1.InspectAttemptResponse{
		State: helperv1.AttemptState_ATTEMPT_STATE_FINISHED, LeaseGeneration: gen, InputFingerprint: fp,
		LastSequence: lastSeq, Submitted: true, Outcome: o,
	}
}

func runningAnswer(gen uint64, fp string, submitted bool) *helperv1.InspectAttemptResponse {
	return &helperv1.InspectAttemptResponse{
		State: helperv1.AttemptState_ATTEMPT_STATE_SUBMITTED, LeaseGeneration: gen, InputFingerprint: fp, LastSequence: 3, Submitted: submitted,
	}
}

func (rf *reconcileFixture) noRunAttempt() {
	rf.t.Helper()
	if runs, _, _ := rf.helper.counts(); runs != 0 {
		rf.t.Fatalf("RunAttempt was called %d times: an attempt that was already submitted was started again", runs)
	}
	if rf.localRuns.Load() != 0 {
		rf.t.Fatal("the local runner ran a job whose prompt had already left")
	}
	if placed, _ := rf.picker.stats(); placed != 0 {
		rf.t.Fatal("the picker was asked to place a job that already has a submitted attempt")
	}
}

// ---- lost after submission: the gateway restarted --------------------------------

// The helper finished the attempt while the gateway was down. The restarted
// gateway collects the outcome from InspectAttempt through the fenced ingest: the
// job completes, nothing is started again, and the lapsed (but unreaped) attempt
// still accepts the finished work.
func TestReconcileGateCollectsAFinishedAttemptAfterAGatewayRestart(t *testing.T) {
	rf := newReconcileFixture(t)
	job, lease := rf.newJob()
	rf.seed(job, true)
	var asked []*helperv1.InspectAttemptRequest
	rf.answer(func(req *helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) {
		asked = append(asked, req)
		return finishedAnswer(1, reconcileFingerprint, reconcileOutcome(), 6), nil
	})

	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); !res.processed || res.err != nil {
		t.Fatalf("RunOnce = %+v", res)
	}
	if !lease.completed || lease.failed || lease.retried {
		t.Fatalf("lease = %+v, want completed only", lease)
	}
	got := rf.job(job.ID)
	if got.Status != jobstore.StatusCompleted {
		t.Fatalf("job status = %s", got.Status)
	}
	if result, _ := got.Result.(map[string]any); result == nil {
		t.Fatalf("result = %#v", got.Result)
	} else if output, _ := result["output"].(map[string]any); output["text"] != "finished while the gateway was down" {
		t.Fatalf("result = %#v", got.Result)
	}
	rf.noRunAttempt()

	// The node asked is the one the ledger recorded, at the endpoint the node store
	// last accepted (the manager is not involved); the question names the old attempt.
	if dials := rf.dialed(); len(dials) == 0 || dials[0].NodeID != "node_a" || dials[0].Endpoint != "10.0.0.2:7443" {
		t.Fatalf("dials = %+v", dials)
	}
	if len(asked) == 0 || asked[0].GetJobId() != job.ID || asked[0].GetAttemptId() != "att_crashed" {
		t.Fatalf("inspections = %v", asked)
	}
	// One terminal event, stamped with the old attempt's provenance, and the attempt
	// is finished: the same shape as a streamed run.
	events := rf.helperEvents(job.ID)
	if len(events) != 1 || events[0].Type != "completed" || events[0].Data["attempt_id"] != "att_crashed" {
		t.Fatalf("helper events = %+v", events)
	}
	if h, _ := events[0].Data["helper"].(map[string]any); h["node_id"] != "node_a" {
		t.Fatalf("provenance = %#v", events[0].Data["helper"])
	}
	if attempts := rf.attempts(job.ID); len(attempts) != 1 || attempts[0].State != jobstore.AttemptFinished || !attempts[0].Submitted() {
		t.Fatalf("attempts = %+v", attempts)
	}
	rf.assertPlacementsReleased()
}

// The helper is still running the attempt. The restarted gateway takes its lease
// over (ledger first, then helper, under the attempt's own fence) and waits for the
// outcome. It never calls RunAttempt, which would start a new run on a helper that
// had forgotten the attempt.
func TestReconcileGateAdoptsARunningAttemptRenewsItAndCollectsTheOutcome(t *testing.T) {
	rf := newReconcileFixture(t)
	job, lease := rf.newJob()
	rf.seed(job, true)
	var (
		mu    sync.Mutex
		calls int
	)
	rf.answer(func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		if calls++; calls <= 3 {
			return runningAnswer(1, reconcileFingerprint, true), nil
		}
		return finishedAnswer(1, reconcileFingerprint, reconcileOutcome(), 9), nil
	})

	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); res.err != nil {
		t.Fatal(res.err)
	}
	if !lease.completed || rf.job(job.ID).Status != jobstore.StatusCompleted {
		t.Fatalf("lease = %+v, job = %s", lease, rf.job(job.ID).Status)
	}
	rf.noRunAttempt()

	// The lease was taken over under the old attempt's identity: the fence names the
	// ledger's attempt, generation, input and workload, and the ledger was renewed.
	rf.helper.mu.Lock()
	renews := slices.Clone(rf.helper.renews)
	rf.helper.mu.Unlock()
	if len(renews) == 0 {
		t.Fatal("the helper's lease was never renewed: it would have killed the attempt")
	}
	fence := renews[0].GetFence()
	if fence.GetAttemptId() != "att_crashed" || fence.GetLeaseGeneration() != 1 || fence.GetNodeId() != "node_a" ||
		fence.GetInputFingerprint() != reconcileFingerprint || fence.GetWorkloadVersion() != testWorkload {
		t.Fatalf("renewal fence = %v", fence)
	}
	if ttls := rf.store.renewTTLs(); len(ttls) == 0 || ttls[0] != 300*time.Millisecond {
		t.Fatalf("ledger renewals = %v", ttls)
	}
	if attempts := rf.attempts(job.ID); len(attempts) != 1 || attempts[0].State != jobstore.AttemptFinished {
		t.Fatalf("attempts = %+v", attempts)
	}
}

// A helper that restarted has no record. The prompt may be in the provider's
// conversation, so the job fails for reconciling: never run again, never retried.
func TestReconcileGateFailsClosedWhenTheHelperForgotASubmittedAttempt(t *testing.T) {
	rf := newReconcileFixture(t) // the fake answers UNKNOWN by default
	job, lease := rf.newJob()
	rf.seed(job, true)

	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); !res.processed || res.err != nil {
		t.Fatalf("RunOnce = %+v", res)
	}
	if lease.retried || !lease.failed || lease.completed {
		t.Fatalf("lease = %+v, want failed and never retried", lease)
	}
	if got := rf.job(job.ID); got.Status != jobstore.StatusFailedTerminal {
		t.Fatalf("job status = %s, want failed_terminal", got.Status)
	}
	data := terminalData(t, mustEvents(t, rf.remoteFixture, job.ID))
	if data["submitted"] != true || data["reconcile_required"] != true || data["retryable"] != false ||
		data["reason"] != "attempt_reconcile_helper_no_record" {
		t.Fatalf("terminal data = %#v", data)
	}
	rf.noRunAttempt()

	// The decision is counted, so an operator can alert on jobs that need reconciling.
	var scrape bytes.Buffer
	helpermetrics.Write(t.Context(), &scrape, nil, time.Now())
	if m := regexp.MustCompile(`ubag_helper_reconcile_total\{action="fail_closed",reason="helper_no_record"\} (\d+)`).FindStringSubmatch(scrape.String()); m == nil || m[1] == "0" {
		t.Fatalf("the fail-closed decision was not counted:\n%s", scrape.String())
	}

	// A redelivery finds the job terminal and does nothing.
	lease2 := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_again", envelope: lease.envelope}
	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease2))); res.err != nil {
		t.Fatal(res.err)
	}
	rf.noRunAttempt()
}

// A helper that cannot be reached holds the job for the bounded window and then
// fails it closed. The manager plays no part: nothing here asks it anything.
func TestReconcileGateWaitsWhileTheHelperIsUnreachableThenFailsClosedAfterTheWindow(t *testing.T) {
	rf := newReconcileFixture(t)
	job, lease := rf.newJob()
	rf.seed(job, true)
	rf.helper.setDead(true)

	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); !res.processed || res.err != nil {
		t.Fatalf("RunOnce = %+v", res)
	}
	if !lease.retried || lease.failed || lease.completed {
		t.Fatalf("inside the window: lease = %+v, want the job held back", lease)
	}
	if st := rf.job(job.ID).Status; jobstore.TerminalStatus(st) {
		t.Fatalf("inside the window the job must stay open, status = %s", st)
	}
	rf.noRunAttempt()

	time.Sleep(rf.cfg.Window + 50*time.Millisecond) // the window ends
	lease2 := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_again", envelope: lease.envelope}
	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease2))); res.err != nil {
		t.Fatal(res.err)
	}
	if lease2.retried || !lease2.failed {
		t.Fatalf("after the window: lease = %+v, want failed", lease2)
	}
	data := terminalData(t, mustEvents(t, rf.remoteFixture, job.ID))
	if rf.job(job.ID).Status != jobstore.StatusFailedTerminal || data["reconcile_required"] != true ||
		data["reason"] != "attempt_reconcile_helper_unreachable" {
		t.Fatalf("job = %s, terminal data = %#v", rf.job(job.ID).Status, data)
	}
	rf.noRunAttempt()
}

// An answer about another generation (or another input) is rejected: nothing in it
// reaches the job, and the job is failed for reconciling.
func TestReconcileGateRejectsAStaleHelperResult(t *testing.T) {
	for name, answer := range map[string]*helperv1.InspectAttemptResponse{
		"another lease generation": finishedAnswer(2, reconcileFingerprint, reconcileOutcome(), 6),
		"another input":            finishedAnswer(1, "fp_somebody_else", reconcileOutcome(), 6),
		"no outcome":               finishedAnswer(1, reconcileFingerprint, nil, 6),
	} {
		t.Run(name, func(t *testing.T) {
			rf := newReconcileFixture(t)
			job, lease := rf.newJob()
			rf.seed(job, true)
			rf.answer(func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) { return answer, nil })

			if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); res.err != nil {
				t.Fatal(res.err)
			}
			if !lease.failed || lease.completed || lease.retried {
				t.Fatalf("lease = %+v", lease)
			}
			if got := rf.job(job.ID); got.Status != jobstore.StatusFailedTerminal || got.Result != nil {
				t.Fatalf("job = %s, result = %#v: the stale answer must not reach the job", got.Status, got.Result)
			}
			if data := terminalData(t, mustEvents(t, rf.remoteFixture, job.ID)); data["reason"] != "attempt_reconcile_stale_helper_result" || data["reconcile_required"] != true {
				t.Fatalf("terminal data = %#v", data)
			}
			if len(rf.helperEvents(job.ID)) != 0 {
				t.Fatal("an event from the stale answer was committed")
			}
			rf.noRunAttempt()
		})
	}
}

// The helper answers consistently and then, mid-resume, answers for another
// generation: the resume stops, commits nothing and fails the job closed.
func TestReconcileResumeStopsWhenTheHelperTurnsStaleMidWay(t *testing.T) {
	rf := newReconcileFixture(t)
	job, lease := rf.newJob()
	rf.seed(job, true)
	var (
		mu    sync.Mutex
		calls int
	)
	rf.answer(func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		if calls++; calls <= 2 {
			return runningAnswer(1, reconcileFingerprint, true), nil
		}
		return finishedAnswer(7, reconcileFingerprint, reconcileOutcome(), 6), nil
	})
	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); res.err != nil {
		t.Fatal(res.err)
	}
	if !lease.failed || lease.retried || rf.job(job.ID).Status != jobstore.StatusFailedTerminal || len(rf.helperEvents(job.ID)) != 0 {
		t.Fatalf("lease = %+v, job = %s", lease, rf.job(job.ID).Status)
	}
	rf.helper.waitCancels(t, 1) // the helper is told to stop the attempt it should not be running
	rf.noRunAttempt()
}

// The fence is closed (a reaper that expired the attempt): nothing the helper holds
// can be committed, and the job is failed for reconciling without asking anyone.
func TestReconcileGateFailsClosedWhenTheFenceIsAlreadyClosed(t *testing.T) {
	rf := newReconcileFixture(t)
	job, lease := rf.newJob()
	rf.seed(job, true)
	if expired, err := rf.store.ExpireAttempts(t.Context(), 0); err != nil || len(expired) != 1 {
		t.Fatalf("ExpireAttempts = %v, %v", expired, err)
	}
	rf.answer(func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) {
		t.Error("the helper was asked about an attempt whose fence is closed")
		return finishedAnswer(1, reconcileFingerprint, reconcileOutcome(), 6), nil
	})
	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); res.err != nil {
		t.Fatal(res.err)
	}
	if data := terminalData(t, mustEvents(t, rf.remoteFixture, job.ID)); !lease.failed || data["reason"] != "attempt_reconcile_attempt_fenced" {
		t.Fatalf("lease = %+v, terminal data = %#v", lease, data)
	}
	rf.noRunAttempt()
}

// The primary was lost after the helper submitted the prompt but before it read the
// event: the ledger says unsubmitted, the helper says otherwise. The boundary is
// recorded and the finished outcome is collected, not run again.
func TestReconcileGateRecordsASubmissionOnlyTheHelperSaw(t *testing.T) {
	rf := newReconcileFixture(t)
	job, lease := rf.newJob()
	seeded := rf.seed(job, false)
	if seeded.Submitted() {
		t.Fatal("the ledger must be behind the helper for this scenario")
	}
	rf.answer(func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) {
		return finishedAnswer(1, reconcileFingerprint, reconcileOutcome(), 4), nil
	})
	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); res.err != nil {
		t.Fatal(res.err)
	}
	if !lease.completed || rf.job(job.ID).Status != jobstore.StatusCompleted {
		t.Fatalf("lease = %+v, job = %s", lease, rf.job(job.ID).Status)
	}
	if attempts := rf.attempts(job.ID); len(attempts) != 1 || !attempts[0].Submitted() || attempts[0].State != jobstore.AttemptFinished {
		t.Fatalf("attempts = %+v: the boundary the helper saw must be in the ledger", attempts)
	}
	rf.noRunAttempt()
}

// A helper that failed the attempt after submission (its own lease ran out while
// the gateway was down) hands back that failure: the job ends failed_terminal with
// reconcile_required, exactly like a streamed one, and is not retried.
func TestReconcileGateCollectsAFailureTheHelperRecorded(t *testing.T) {
	rf := newReconcileFixture(t)
	job, lease := rf.newJob()
	rf.seed(job, true)
	rf.answer(func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) {
		return finishedAnswer(1, reconcileFingerprint, &helperv1.AttemptOutcome{
			Status: stFailed, Submitted: true, ReconcileRequired: true, StreamEndReason: "lease_expired", ErrorCode: "helper_lease_expired",
		}, 5), nil
	})
	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); res.err != nil {
		t.Fatal(res.err)
	}
	if !lease.failed || lease.retried || rf.job(job.ID).Status != jobstore.StatusFailedTerminal {
		t.Fatalf("lease = %+v, job = %s", lease, rf.job(job.ID).Status)
	}
	data := terminalData(t, mustEvents(t, rf.remoteFixture, job.ID))
	if data["reconcile_required"] != true || data["stream_end_reason"] != "lease_expired" || data["error_code"] != "helper_lease_expired" {
		t.Fatalf("terminal data = %#v", data)
	}
	rf.noRunAttempt()
}

// Resuming is the attempt's own business: a helper whose workload version or
// adapter registry no longer matches this primary (a deploy happened) is still the
// helper that holds the attempt. A new placement would refuse it.
func TestReconcileResumeToleratesAHelperWorkloadThatMovedOn(t *testing.T) {
	rf := newReconcileFixture(t)
	rf.helper.hs.WorkloadVersion, rf.helper.hs.RegistryDigest = "w2", "b"+testDigest[1:]
	job, lease := rf.newJob()
	rf.seed(job, true)
	rf.answer(func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) {
		return finishedAnswer(1, reconcileFingerprint, reconcileOutcome(), 6), nil
	})
	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); res.err != nil {
		t.Fatal(res.err)
	}
	if !lease.completed || rf.job(job.ID).Status != jobstore.StatusCompleted {
		t.Fatalf("lease = %+v, job = %s", lease, rf.job(job.ID).Status)
	}
	// ... but it is not a helper a new job may be placed on.
	if r := rf.runner.incompatible(rf.helper.hs, "node_a", false); r != "helper_workload_version" {
		t.Fatalf("a new placement on it: %q", r)
	}
}

// A skewed clock makes a lease meaningless, resumed or not.
func TestReconcileResumeStillRefusesAHelperWithADriftingClock(t *testing.T) {
	rf := newReconcileFixture(t)
	rf.helper.serverTime = func() time.Time { return time.Now().Add(time.Minute) }
	job, lease := rf.newJob()
	rf.seed(job, true)
	rf.answer(func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) {
		return finishedAnswer(1, reconcileFingerprint, reconcileOutcome(), 6), nil
	})
	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); res.err != nil {
		t.Fatal(res.err)
	}
	// The gate saw the finished answer (Inspect needs no handshake), but the resume
	// could not trust the helper's lease clock: the job is held, not completed.
	if !lease.retried || lease.completed || jobstore.TerminalStatus(rf.job(job.ID).Status) {
		t.Fatalf("lease = %+v, job = %s", lease, rf.job(job.ID).Status)
	}
	rf.noRunAttempt()

	// It does not hold the job for ever: when the window ends the job is failed for
	// reconciling, the same as a helper that never answered.
	time.Sleep(rf.cfg.Window + 50*time.Millisecond)
	lease2 := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_again", envelope: lease.envelope}
	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease2))); res.err != nil {
		t.Fatal(res.err)
	}
	data := terminalData(t, mustEvents(t, rf.remoteFixture, job.ID))
	if lease2.retried || !lease2.failed || data["reason"] != "attempt_reconcile_helper_unreachable" || data["reconcile_required"] != true {
		t.Fatalf("after the window: lease = %+v, terminal data = %#v", lease2, data)
	}
	rf.noRunAttempt()
}

// Cancel reaches a resumed attempt: the job is cancelled and the helper is told to
// stop under the attempt's own fence.
func TestReconcileResumeStopsTheHelperWhenTheJobIsCancelled(t *testing.T) {
	rf := newReconcileFixture(t)
	job, lease := rf.newJob()
	rf.seed(job, true)
	rf.answer(func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) {
		return runningAnswer(1, reconcileFingerprint, true), nil // never finishes
	})
	done := runOnceAsync(t.Context(), rf.reconcileConsumer(lease))
	rf.helper.waitRenews(t, 1)
	if _, found, err := rf.store.UpdateStatus(t.Context(), job.ID, jobstore.StatusCanceled); err != nil || !found {
		t.Fatalf("cancel = %v, %v", found, err)
	}
	if res := awaitRun(t, done); res.err != nil {
		t.Fatal(res.err)
	}
	rf.helper.waitCancels(t, 1)
	rf.helper.mu.Lock()
	cancelled := rf.helper.cancels[0].GetFence()
	rf.helper.mu.Unlock()
	if cancelled.GetAttemptId() != "att_crashed" || cancelled.GetLeaseGeneration() != 1 {
		t.Fatalf("cancel fence = %v", cancelled)
	}
	if !lease.cancelled || lease.failed {
		t.Fatalf("lease = %+v", lease)
	}
	rf.noRunAttempt()
}

// ---- lost before submission: reassigned at generation+1 --------------------------

// The first attempt lapsed before it submitted anything. The job is held while its
// lease might still be live, and once it has lapsed the next attempt takes the
// next generation on a freshly placed helper.
func TestReconcileGateLostBeforeSubmissionReassignsAtGenerationPlusOne(t *testing.T) {
	rf := newReconcileFixture(t)
	job, lease := rf.newJob()
	// The next attempt must be allowed to follow this one, so it carries the input's
	// real fingerprint (the ledger pins it across attempts).
	spec, body, err := projectHelperJob(lease.envelope, "att_x", "pr_abc123")
	if err != nil {
		t.Fatal(err)
	}
	// A lease that is still held: the gate waits instead of racing it.
	if _, err := rf.store.BeginAttempt(t.Context(), jobstore.BeginAttemptRequest{
		JobID: job.ID, AttemptID: "att_crashed", NodeID: "node_a", TTL: 400 * time.Millisecond,
		InputFingerprint: helperFingerprint(job.ID, spec.Target, spec.CommandType, body), WorkloadVersion: testWorkload,
	}); err != nil {
		t.Fatal(err)
	}
	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); res.err != nil {
		t.Fatal(res.err)
	}
	if !lease.retried || lease.failed || lease.completed {
		t.Fatalf("while the lease is held: %+v", lease)
	}
	if runs, _, _ := rf.helper.counts(); runs != 0 || rf.localRuns.Load() != 0 {
		t.Fatal("the job was started while its attempt lease could still be live")
	}

	waitFor(t, 5*time.Second, "the first lease to lapse", func() bool { return time.Now().After(rf.attempts(job.ID)[0].LeaseExpiresAt.Add(20 * time.Millisecond)) })
	lease2 := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_again", envelope: lease.envelope}
	done := runOnceAsync(t.Context(), rf.reconcileConsumer(lease2))
	req := rf.helper.waitRun(t, 1)
	if req.GetFence().GetLeaseGeneration() != 2 || req.GetFence().GetAttemptId() == "att_crashed" {
		t.Fatalf("the new attempt's fence = %v, want generation 2 under a fresh attempt id", req.GetFence())
	}
	rf.helper.send(evStarted, `{}`)
	rf.helper.sendCompleted()
	if res := awaitRun(t, done); res.err != nil {
		t.Fatal(res.err)
	}
	if !lease2.completed || rf.job(job.ID).Status != jobstore.StatusCompleted {
		t.Fatalf("lease = %+v, job = %s", lease2, rf.job(job.ID).Status)
	}
	attempts := rf.attempts(job.ID)
	if len(attempts) != 2 || attempts[0].State != jobstore.AttemptExpired || attempts[1].Generation != 2 || attempts[1].State != jobstore.AttemptFinished {
		t.Fatalf("attempts = %+v", attempts)
	}
}

// With nothing placed (the manager is down: the picker grants nothing new) a job
// whose earlier attempt never submitted runs on this gateway, once that attempt has
// lapsed. A job whose earlier attempt DID submit never does.
func TestReconcileGateManagerDownRunsAnUnsubmittedJobLocallyButNeverASubmittedOne(t *testing.T) {
	t.Run("unsubmitted", func(t *testing.T) {
		rf := newReconcileFixture(t)
		rf.picker.setErr(ErrNoHelper)
		job, lease := rf.newJob()
		rf.seed(job, false)
		if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); res.err != nil {
			t.Fatal(res.err)
		}
		if !lease.completed || rf.localRuns.Load() != 1 || rf.job(job.ID).Status != jobstore.StatusCompleted {
			t.Fatalf("lease = %+v, local runs %d, job = %s", lease, rf.localRuns.Load(), rf.job(job.ID).Status)
		}
	})
	t.Run("submitted", func(t *testing.T) {
		rf := newReconcileFixture(t)
		rf.picker.setErr(ErrNoHelper)
		job, lease := rf.newJob()
		rf.seed(job, true)
		if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease))); res.err != nil {
			t.Fatal(res.err)
		}
		// Without the gate this job would have gone to the local runner and been
		// submitted a second time. With it the helper's answer settles it.
		if rf.localRuns.Load() != 0 || !lease.failed {
			t.Fatalf("lease = %+v, local runs %d", lease, rf.localRuns.Load())
		}
		rf.noRunAttempt()
	})
}

// ---- the gate is transparent for everything else -----------------------------------

func TestReconcileGateLeavesAFreshJobAlone(t *testing.T) {
	rf := newReconcileFixture(t)
	job, lease := rf.newJob()
	done := runOnceAsync(t.Context(), rf.reconcileConsumer(lease))
	req := rf.helper.waitRun(t, 1)
	if req.GetFence().GetLeaseGeneration() != 1 {
		t.Fatalf("fence = %v", req.GetFence())
	}
	rf.helper.send(evStarted, `{}`)
	rf.helper.sendCompleted()
	if res := awaitRun(t, done); res.err != nil || !lease.completed {
		t.Fatalf("RunOnce = %+v, lease = %+v", res, lease)
	}
	if rf.job(job.ID).Status != jobstore.StatusCompleted || rf.localRuns.Load() != 0 {
		t.Fatalf("job = %s", rf.job(job.ID).Status)
	}
	rf.assertPlacementsReleased()

	// Nothing placed: a fresh job runs locally as before.
	rf.picker.setErr(ErrNoHelper)
	_, lease2 := rf.newJob()
	if res := awaitRun(t, runOnceAsync(t.Context(), rf.reconcileConsumer(lease2))); res.err != nil || !lease2.completed || rf.localRuns.Load() != 1 {
		t.Fatalf("RunOnce = %+v, lease = %+v, local runs %d", res, lease2, rf.localRuns.Load())
	}
}

type brokenLedger struct{ err error }

func (l brokenLedger) ListAttempts(context.Context, string) ([]jobstore.Attempt, error) {
	return nil, l.err
}

// A ledger that cannot be read cannot say whether a prompt left: the job is held,
// not run.
func TestReconcileGateHoldsTheJobWhenTheLedgerCannotBeRead(t *testing.T) {
	rf := newReconcileFixture(t)
	job, lease := rf.newJob()
	c := rf.consumer(lease, nil)
	c.Reconcile = &nodes.Reconciler{Ledger: brokenLedger{err: errors.New("postgres down")}, Inspector: rf.runner, Config: rf.cfg}
	if res := awaitRun(t, runOnceAsync(t.Context(), c)); res.err != nil {
		t.Fatal(res.err)
	}
	if !lease.retried || lease.failed || lease.completed || jobstore.TerminalStatus(rf.job(job.ID).Status) {
		t.Fatalf("lease = %+v, job = %s", lease, rf.job(job.ID).Status)
	}
	rf.noRunAttempt()
}

// ---- the building blocks -------------------------------------------------------------

func TestReconcileFailureIsAmbiguousAndNamesItsReason(t *testing.T) {
	err := error(&reconcileFailure{reason: nodes.ReconcileNoRecord})
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatal("an unresolved attempt must be ErrAmbiguous: failed closed, never replayed")
	}
	var f *reconcileFailure
	if !errors.As(err, &f) || f.reason != "helper_no_record" {
		t.Fatalf("reason = %+v", f)
	}
}

func TestReconcileHelperViewMapsEveryInspectState(t *testing.T) {
	for name, tc := range map[string]struct {
		in   *helperv1.InspectAttemptResponse
		want nodes.HelperAttemptState
	}{
		"unspecified": {&helperv1.InspectAttemptResponse{}, nodes.HelperAttemptUnknown},
		"unknown":     {&helperv1.InspectAttemptResponse{State: helperv1.AttemptState_ATTEMPT_STATE_UNKNOWN}, nodes.HelperAttemptUnknown},
		"accepted":    {&helperv1.InspectAttemptResponse{State: helperv1.AttemptState_ATTEMPT_STATE_ACCEPTED}, nodes.HelperAttemptRunning},
		"running":     {&helperv1.InspectAttemptResponse{State: helperv1.AttemptState_ATTEMPT_STATE_RUNNING}, nodes.HelperAttemptRunning},
		"submitted":   {&helperv1.InspectAttemptResponse{State: helperv1.AttemptState_ATTEMPT_STATE_SUBMITTED}, nodes.HelperAttemptRunning},
		"finished":    {&helperv1.InspectAttemptResponse{State: helperv1.AttemptState_ATTEMPT_STATE_FINISHED}, nodes.HelperAttemptFinished},
		"future":      {&helperv1.InspectAttemptResponse{State: 99}, nodes.HelperAttemptUnknown},
	} {
		if got := helperViewOf(tc.in).State; got != tc.want {
			t.Errorf("%s: state = %v, want %v", name, got, tc.want)
		}
	}
	// The outcome's own submitted flag counts: a finished attempt whose record
	// forgot the flag is still submitted.
	v := helperViewOf(finishedAnswer(3, "fp", &helperv1.AttemptOutcome{Status: stFailed, Submitted: true}, 8))
	if !v.Submitted || !v.HasOutcome || v.Generation != 3 || v.Fingerprint != "fp" || v.LastSequence != 8 {
		t.Fatalf("view = %+v", v)
	}
	if v := helperViewOf(&helperv1.InspectAttemptResponse{State: helperv1.AttemptState_ATTEMPT_STATE_FINISHED, Outcome: &helperv1.AttemptOutcome{Submitted: true}}); !v.Submitted {
		t.Fatal("the outcome's submitted flag was dropped")
	}
}

func TestReconcileInspectIsReadOnlyAndFailsWithoutAnEndpoint(t *testing.T) {
	rf := newReconcileFixture(t)
	rf.answer(func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) {
		return runningAnswer(1, reconcileFingerprint, true), nil
	})
	view, err := rf.runner.Inspect(t.Context(), "node_a", "job_1", "att_x")
	if err != nil || view.State != nodes.HelperAttemptRunning || !view.Submitted {
		t.Fatalf("Inspect = %+v, %v", view, err)
	}
	runs, renews, cancels := rf.helper.counts()
	if runs+renews+cancels != 0 {
		t.Fatalf("an inspection called RunAttempt/RenewAttempt/CancelAttempt (%d/%d/%d)", runs, renews, cancels)
	}
	if rf.helper.closes.Load() != 1 {
		t.Fatalf("the connection was closed %d times, want once", rf.helper.closes.Load())
	}

	rf.helper.setDead(true)
	if _, err := rf.runner.Inspect(t.Context(), "node_a", "job_1", "att_x"); status.Code(err) != codes.Unavailable {
		t.Fatalf("a dead helper: err = %v", err)
	}

	noEndpoint := newReconcileFixture(t, func(c *RemoteConfig) { c.Nodes = fakeNodeEndpoints{err: nodes.ErrNotFound} })
	if _, err := noEndpoint.runner.Inspect(t.Context(), "node_a", "job_1", "att_x"); !errors.Is(err, nodes.ErrNotFound) {
		t.Fatalf("a node without an allocation: err = %v", err)
	}
	none := newReconcileFixture(t, func(c *RemoteConfig) { c.Nodes = nil })
	if _, err := none.runner.Inspect(t.Context(), "node_a", "job_1", "att_x"); err == nil {
		t.Fatal("a runner without a node directory must not dial")
	}
	if _, err := (*RemoteWorkerRunner)(nil).Inspect(t.Context(), "node_a", "job_1", "att_x"); err == nil {
		t.Fatal("a nil runner must not answer")
	}
}
