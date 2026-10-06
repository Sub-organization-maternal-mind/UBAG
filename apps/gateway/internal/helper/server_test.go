package helper

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

const (
	stRunning   = helperv1.AttemptState_ATTEMPT_STATE_RUNNING
	stSubmitted = helperv1.AttemptState_ATTEMPT_STATE_SUBMITTED
	stFinished  = helperv1.AttemptState_ATTEMPT_STATE_FINISHED
	stUnknown   = helperv1.AttemptState_ATTEMPT_STATE_UNKNOWN

	stCompleted = helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED
	stFailed    = helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED
	stTimedOut  = helperv1.AttemptStatus_ATTEMPT_STATUS_TIMED_OUT
	stCancelled = helperv1.AttemptStatus_ATTEMPT_STATUS_CANCELLED
)

func TestHandshakeAndNodeCrossCheck(t *testing.T) {
	r := newRig(t, newFakeRunner(blockingScript))
	resp, err := r.client.Handshake(r.ctx(), &helperv1.HandshakeRequest{ProtocolVersion: ProtocolVersion, PrimaryWorkloadVersion: "w0"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetProtocolVersion() != ProtocolVersion || resp.GetNodeId() != "helper-1" || resp.GetWorkloadVersion() != "w1" ||
		resp.GetRegistryDigest() != testDigest || resp.GetHelperVersion() != "dev" || !resp.GetServerTime().IsValid() {
		t.Fatalf("unexpected handshake: %v", resp)
	}
	if !contains(strings.Join(resp.GetFeatures(), ","), "streaming_events") {
		t.Fatalf("features: %v", resp.GetFeatures())
	}
	if _, err := r.client.Handshake(r.ctx(), &helperv1.HandshakeRequest{ProtocolVersion: "ubag.helper.v0"}); codeOf(err) != "FailedPrecondition" {
		t.Fatalf("protocol mismatch: %v", err)
	}
	// A claimed node id is only a cross-check against this node, never a
	// selector: empty passes, anything else must be this node.
	if _, err := r.client.Handshake(r.ctx(), &helperv1.HandshakeRequest{ProtocolVersion: ProtocolVersion, NodeId: "helper-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.client.Handshake(r.ctx(), &helperv1.HandshakeRequest{ProtocolVersion: ProtocolVersion, NodeId: "helper-2"}); codeOf(err) != "PermissionDenied" {
		t.Fatalf("node mismatch: %v", err)
	}
	if _, err := r.client.ReportCapacity(r.ctx(), &helperv1.ReportCapacityRequest{NodeId: "helper-2"}); codeOf(err) != "PermissionDenied" {
		t.Fatalf("capacity node mismatch: %v", err)
	}
	if _, err := r.client.Drain(r.ctx(), &helperv1.DrainRequest{NodeId: "helper-2"}); codeOf(err) != "PermissionDenied" {
		t.Fatalf("drain node mismatch: %v", err)
	}
}

func TestCapacityReportsSlotsAndHostStats(t *testing.T) {
	r := newRig(t, newFakeRunner(blockingScript))
	c := r.capacity()
	if c.GetNodeId() != "helper-1" || c.GetBrowsersMax() != 2 || c.GetBrowsersActive() != 0 || c.GetAttemptsActive() != 0 ||
		c.GetDraining() || c.GetWorkloadVersion() != "w1" || c.GetRegistryDigest() != testDigest ||
		c.GetCpuMillisTotal() != 2000 || c.GetCpuMillisUsed() != 500 || c.GetMemoryBytesTotal() != 4<<30 || c.GetMemoryBytesUsed() != 1<<30 {
		t.Fatalf("idle capacity: %v", c)
	}

	rs := r.open(r.runReq("att_a", 1))
	rs.admitted()
	c = r.capacity()
	if c.GetBrowsersActive() != 1 || c.GetAttemptsActive() != 1 || len(c.GetSlots()) != 1 {
		t.Fatalf("busy capacity: %v", c)
	}
	slot := c.GetSlots()[0]
	if slot.GetProvider() != "chatgpt_web" || slot.GetIdentityRef() != "ident-1" ||
		slot.GetState() != helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_BUSY || slot.GetActiveAttemptId() != "att_a" {
		t.Fatalf("busy slot: %v", slot)
	}

	// Finish it: the slot goes IDLE and capacity is free again.
	if _, err := r.client.CancelAttempt(r.ctx(), &helperv1.CancelAttemptRequest{Fence: r.fence("att_a", 1)}); err != nil {
		t.Fatal(err)
	}
	r.waitReleased("att_a")
	c = r.capacity()
	if c.GetBrowsersActive() != 0 || len(c.GetSlots()) != 1 || c.GetSlots()[0].GetState() != helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_IDLE {
		t.Fatalf("after finish: %v", c)
	}

	// No usable host reading means zero totals (the primary reads that as
	// worst-case pressure), never an idle-looking node.
	r.host.mu.Lock()
	r.host.fail = true
	r.host.mu.Unlock()
	if c = r.capacity(); c.GetCpuMillisTotal() != 0 || c.GetMemoryBytesTotal() != 0 || c.GetCpuMillisUsed() != 0 {
		t.Fatalf("failed sampler must report zero totals: %v", c)
	}
}

func TestRunAttemptStreamsOrderedEventsThroughTheTerminal(t *testing.T) {
	runner := newFakeRunner(func(_ context.Context, _ AttemptSpec, emit EmitFunc) error {
		for _, e := range []Event{
			ev(evStarted, `{"a":1}`), ev(evOpened, ""), ev(evSubmitted, `{"s":true}`),
			ev(evToken, `{"t":"he"}`), ev(evToken, `{"t":"llo"}`), terminal(completedOutcome()),
		} {
			if err := emit(e); err != nil {
				return err
			}
		}
		return nil
	})
	r := newRig(t, runner)
	evs, err := r.open(r.runReq("att_a", 3)).all()
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 6 {
		t.Fatalf("want 6 events, got %d: %v", len(evs), evs)
	}
	wantTypes := []helperv1.AttemptEventType{evStarted, evOpened, evSubmitted, evToken, evToken, evTerminal}
	for i, e := range evs {
		if e.GetSequence() != uint64(i+1) || e.GetAttemptId() != "att_a" || e.GetLeaseGeneration() != 3 ||
			e.GetType() != wantTypes[i] || !e.GetCreatedAt().IsValid() {
			t.Fatalf("event %d: %v", i, e)
		}
	}
	if evs[0].GetDataJson() != `{"a":1}` || evs[4].GetDataJson() != `{"t":"llo"}` {
		t.Fatalf("data not passed through: %v / %v", evs[0], evs[4])
	}
	if o := terminalOf(t, evs); o.GetStatus() != stCompleted || o.GetResultJson() != `{"text":"ok"}` || !o.GetSubmitted() || o.GetReconcileRequired() {
		t.Fatalf("outcome: %v", o)
	}
	r.waitReleased("att_a")
	in := r.inspect("job-1", "att_a")
	if in.GetState() != stFinished || in.GetLastSequence() != 6 || !in.GetSubmitted() || in.GetLeaseGeneration() != 3 ||
		in.GetInputFingerprint() != "fp-1" || in.GetOutcome().GetStatus() != stCompleted {
		t.Fatalf("inspect: %v", in)
	}
	// The runner received exactly the validated request, nothing more.
	spec := runner.specs[0]
	if spec.JobID != "job-1" || spec.AttemptID != "att_a" || spec.Generation != 3 || spec.Provider != "chatgpt_web" ||
		spec.IdentityRef != "ident-1" || spec.InputJSON != `{"prompt":"hello"}` || spec.Deadline != 900*time.Second {
		t.Fatalf("spec: %+v", spec)
	}
}

// The helper's own knowledge of the submission boundary wins over the runner's
// claim (decision D4): after prompt_submitted a failure is submitted and needs
// reconciliation, before it the failure stays retryable.
func TestTerminalNormalisesTheSubmissionBoundary(t *testing.T) {
	for _, tc := range []struct {
		name          string
		pre           []Event
		status        helperv1.AttemptStatus
		wantSubmitted bool
		wantReconcile bool
	}{
		{"failed after submit", []Event{ev(evSubmitted, "")}, stFailed, true, true},
		{"failed before submit", nil, stFailed, false, false},
		{"completed after submit", []Event{ev(evSubmitted, "")}, stCompleted, true, false},
		{"cancelled after submit", []Event{ev(evSubmitted, "")}, stCancelled, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := newFakeRunner(func(_ context.Context, _ AttemptSpec, emit EmitFunc) error {
				for _, e := range tc.pre {
					_ = emit(e)
				}
				// The runner claims the safest thing: not submitted, no reconcile.
				return emit(terminal(&helperv1.AttemptOutcome{Status: tc.status, ResultJson: `{"x":1}`, ErrorCode: "boom"}))
			})
			r := newRig(t, runner)
			evs, err := r.open(r.runReq("att_a", 1)).all()
			if err != nil {
				t.Fatal(err)
			}
			if o := terminalOf(t, evs); o.GetSubmitted() != tc.wantSubmitted || o.GetReconcileRequired() != tc.wantReconcile {
				t.Fatalf("outcome: %v", o)
			}
		})
	}
}

func TestLeaseExpiryWithoutRenewKillsTheAttempt(t *testing.T) {
	runner := newFakeRunner(blockingScript)
	r := newRig(t, runner)
	rs := r.open(r.runReq("att_a", 1))
	rs.admitted()

	r.clock.Advance(119 * time.Second)
	if in := r.inspect("job-1", "att_a"); in.GetState() != stRunning {
		t.Fatalf("still inside the lease, got %v", in)
	}
	r.clock.Advance(2 * time.Second) // 121 s without a renewal

	evs, err := rs.all()
	if err != nil {
		t.Fatal(err)
	}
	o := terminalOf(t, evs)
	if o.GetStatus() != stFailed || o.GetErrorCode() != "helper_lease_expired" || o.GetStreamEndReason() != "lease_expired" {
		t.Fatalf("outcome: %v", o)
	}
	r.waitReleased("att_a")
	if c := runner.cause("att_a"); c != errLeaseExpired {
		t.Fatalf("the runner must see its context cancelled by the lease expiry, got %v", c)
	}
	if in := r.inspect("job-1", "att_a"); in.GetState() != stFinished {
		t.Fatalf("inspect: %v", in)
	}
	// The identity is free again.
	r.open(r.runReq("att_b", 2)).admitted()
}

func TestRenewExtendsTheLeaseAndALateRenewDoesNotResurrect(t *testing.T) {
	release := make(chan struct{})
	runner := newFakeRunner(func(ctx context.Context, _ AttemptSpec, emit EmitFunc) error {
		_ = emit(ev(evStarted, ""))
		<-release // a stubborn runner: ignores its context until released
		return nil
	})
	r := newRig(t, runner)
	rs := r.open(r.runReq("att_a", 1))
	rs.admitted()

	// Renewing at 100 s moves the expiry to 220 s, so 200 s in it is still alive.
	r.clock.Advance(100 * time.Second)
	f := r.fence("att_a", 1)
	resp, err := r.client.RenewAttempt(r.ctx(), &helperv1.RenewAttemptRequest{Fence: f})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetLeaseGeneration() != 1 || resp.GetState() != stRunning || !resp.GetLeaseExpiresAt().AsTime().Equal(f.GetLeaseExpiresAt().AsTime()) {
		t.Fatalf("renew: %v", resp)
	}
	r.clock.Advance(100 * time.Second)
	if in := r.inspect("job-1", "att_a"); in.GetState() != stRunning {
		t.Fatalf("renewed lease must survive its original expiry: %v", in)
	}
	// A renew can never shorten the lease.
	short := r.fence("att_a", 1)
	short.LeaseExpiresAt = timestamppb.New(r.clock.Now().Add(time.Second))
	if resp, err = r.client.RenewAttempt(r.ctx(), &helperv1.RenewAttemptRequest{Fence: short}); err != nil ||
		!resp.GetLeaseExpiresAt().AsTime().After(r.clock.Now().Add(10*time.Second)) {
		t.Fatalf("a shorter renew must not move the expiry back: %v %v", resp, err)
	}

	r.clock.Advance(21 * time.Second) // t=221 s, one second past the renewed expiry (220 s): killed
	late := r.fence("att_a", 1)
	if _, err := r.client.RenewAttempt(r.ctx(), &helperv1.RenewAttemptRequest{Fence: late}); codeOf(err) != "Aborted" {
		t.Fatalf("a renew after the lease lapsed must not resurrect the attempt: %v", err)
	}
	// The runner ignores its cancellation, so after KillGrace (2 s) the helper
	// writes the terminal itself, while the identity stays held until Run really
	// returns.
	r.clock.Advance(3 * time.Second)
	evs, err := rs.all()
	if err != nil {
		t.Fatal(err)
	}
	if o := terminalOf(t, evs); o.GetErrorCode() != "helper_lease_expired" {
		t.Fatalf("outcome: %v", o)
	}
	if _, err := r.open(r.runReq("att_b", 2)).recv(); codeOf(err) != "Unavailable" {
		t.Fatalf("the identity must stay held while the runner has not returned: %v", err)
	}
	close(release)
	r.waitReleased("att_a")
	r.open(r.runReq("att_c", 3)).admitted()
}

func TestCancelAttempt(t *testing.T) {
	r := newRig(t, newFakeRunner(func(ctx context.Context, _ AttemptSpec, emit EmitFunc) error {
		_ = emit(ev(evStarted, ""))
		_ = emit(ev(evSubmitted, ""))
		<-ctx.Done()
		return ctx.Err()
	}))
	rs := r.open(r.runReq("att_a", 1))
	rs.admitted()
	rs.admitted() // prompt_submitted: the boundary is visible

	// Stale, mismatched and foreign fences change nothing.
	for name, mut := range map[string]func(*helperv1.Fence){
		"no generation":     func(f *helperv1.Fence) { f.LeaseGeneration = 0 },
		"newer generation":  func(f *helperv1.Fence) { f.LeaseGeneration = 2 },
		"other job":         func(f *helperv1.Fence) { f.JobId = "job-9" },
		"foreign node":      func(f *helperv1.Fence) { f.NodeId = "helper-2" },
		"fingerprint drift": func(f *helperv1.Fence) { f.InputFingerprint = "other" },
		"unknown attempt":   func(f *helperv1.Fence) { f.AttemptId = "att_zzz" },
	} {
		f := r.fence("att_a", 1)
		mut(f)
		_, err := r.client.CancelAttempt(r.ctx(), &helperv1.CancelAttemptRequest{Fence: f})
		if err == nil {
			t.Fatalf("%s: cancel must be refused", name)
		}
	}
	if in := r.inspect("job-1", "att_a"); in.GetState() != stSubmitted {
		t.Fatalf("a refused cancel must leave the attempt running: %v", in)
	}

	resp, err := r.client.CancelAttempt(r.ctx(), &helperv1.CancelAttemptRequest{Fence: r.fence("att_a", 1), Reason: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetState() != stFinished || !resp.GetSubmitted() {
		t.Fatalf("cancel response: %v", resp)
	}
	evs, err := rs.all()
	if err != nil {
		t.Fatal(err)
	}
	if o := terminalOf(t, evs); o.GetStatus() != stCancelled || o.GetStreamEndReason() != "cancelled" || !o.GetSubmitted() || o.GetReconcileRequired() {
		t.Fatalf("outcome: %v", o)
	}
	// Idempotent.
	if resp, err = r.client.CancelAttempt(r.ctx(), &helperv1.CancelAttemptRequest{Fence: r.fence("att_a", 1)}); err != nil || resp.GetState() != stFinished {
		t.Fatalf("second cancel: %v %v", resp, err)
	}
}

func TestStaleGenerationRejectedAndNewerGenerationSupersedes(t *testing.T) {
	runner := newFakeRunner(blockingScript)
	r := newRig(t, runner)
	rs := r.open(r.runReq("att_a", 5))
	rs.admitted()

	// Lower generation, and a second claim on the same generation, never run.
	if _, err := r.open(r.runReq("att_old", 4)).recv(); codeOf(err) != "Aborted" {
		t.Fatalf("stale generation: %v", err)
	}
	other := r.runReq("att_b", 5)
	other.IdentityRef = "ident-2"
	if _, err := r.open(other).recv(); codeOf(err) != "Aborted" {
		t.Fatalf("same generation, different attempt: %v", err)
	}
	if runner.callCount() != 1 {
		t.Fatalf("a rejected attempt must never reach the runner, calls=%d", runner.callCount())
	}
	for gen, want := range map[uint64]string{4: "Aborted", 6: "Aborted", 5: "OK"} {
		_, err := r.client.RenewAttempt(r.ctx(), &helperv1.RenewAttemptRequest{Fence: r.fence("att_a", gen)})
		if codeOf(err) != want {
			t.Fatalf("renew generation %d: want %s, got %v", gen, want, err)
		}
	}

	// Generation 6 fences the older holder out: it is killed and cannot renew,
	// cancel or re-attach any more. Admission of the successor waits only for
	// the identity to be freed (the primary retries Unavailable).
	var succ *runStream
	eventually(t, "the successor to be admitted once the old holder released", func() bool {
		s := r.open(r.runReq("att_c", 6))
		if _, err := s.recv(); err != nil {
			s.cancel()
			if codeOf(err) != "Unavailable" {
				t.Fatalf("successor admission: %v", err)
			}
			return false
		}
		succ = s
		return true
	})
	old, err := rs.all()
	if err != nil {
		t.Fatal(err)
	}
	if o := terminalOf(t, old); o.GetErrorCode() != "helper_attempt_superseded" || o.GetStreamEndReason() != "superseded" {
		t.Fatalf("superseded outcome: %v", o)
	}
	if c := runner.cause("att_a"); c != errSuperseded {
		t.Fatalf("cause: %v", c)
	}
	for name, call := range map[string]func() error{
		"renew": func() error {
			_, err := r.client.RenewAttempt(r.ctx(), &helperv1.RenewAttemptRequest{Fence: r.fence("att_a", 5)})
			return err
		},
		"cancel": func() error {
			_, err := r.client.CancelAttempt(r.ctx(), &helperv1.CancelAttemptRequest{Fence: r.fence("att_a", 5)})
			return err
		},
		"reattach": func() error { _, err := r.open(r.runReq("att_a", 5)).recv(); return err },
	} {
		if codeOf(call()) != "Aborted" {
			t.Fatalf("%s by the superseded holder must be Aborted: %v", name, call())
		}
	}
	_ = succ
}

func TestInspectAfterRunnerCrashAndRestart(t *testing.T) {
	var mode atomic.Value
	mode.Store("panic")
	runner := newFakeRunner(func(_ context.Context, _ AttemptSpec, emit EmitFunc) error {
		_ = emit(ev(evStarted, ""))
		_ = emit(ev(evSubmitted, ""))
		switch mode.Load().(string) {
		case "panic":
			panic("secret page text must not leave the helper")
		case "error":
			return fmt.Errorf("secret page text must not leave the helper")
		}
		return nil // nil without a terminal
	})
	r := newRig(t, runner)

	for i, tc := range []struct{ mode, code string }{
		{"panic", "helper_runner_crash"}, {"error", "helper_runner_error"}, {"nil", "helper_runner_no_terminal"},
	} {
		mode.Store(tc.mode)
		id := fmt.Sprintf("att_%d", i+1)
		evs, err := r.open(r.runReq(id, uint64(i+1))).all()
		if err != nil {
			t.Fatal(err)
		}
		o := terminalOf(t, evs)
		if o.GetStatus() != stFailed || o.GetErrorCode() != tc.code || !o.GetSubmitted() || !o.GetReconcileRequired() {
			t.Fatalf("%s: outcome %v", tc.mode, o)
		}
		if contains(o.GetErrorMessage(), "secret") {
			t.Fatalf("%s: the runner's own text leaked: %q", tc.mode, o.GetErrorMessage())
		}
		r.waitReleased(id)
		in := r.inspect("job-1", id)
		if in.GetState() != stFinished || !in.GetSubmitted() || in.GetOutcome().GetErrorCode() != tc.code || in.GetLastSequence() != 3 {
			t.Fatalf("%s: inspect after the crash: %v", tc.mode, in)
		}
	}
	// The helper survived and every crashed runner freed its slot.
	if c := r.capacity(); c.GetBrowsersActive() != 0 {
		t.Fatalf("a crashed runner must free its slot: %v", c)
	}

	// A restarted helper has no record: UNKNOWN, never an error, so the primary
	// reconciles from its own ledger.
	r2 := newRig(t, newFakeRunner(blockingScript))
	if in := r2.inspect("job-1", "att_1"); in.GetState() != stUnknown || in.GetOutcome() != nil {
		t.Fatalf("restarted helper: %v", in)
	}
	// Same attempt id under another job is not this attempt either.
	if in := r.inspect("job-other", "att_1"); in.GetState() != stUnknown {
		t.Fatalf("wrong job: %v", in)
	}
}

func TestBrokenStreamDoesNotStopTheAttemptAndReattachReplays(t *testing.T) {
	proceed := make(chan struct{})
	runner := newFakeRunner(func(ctx context.Context, _ AttemptSpec, emit EmitFunc) error {
		_ = emit(ev(evStarted, `{"n":1}`))
		_ = emit(ev(evSubmitted, `{"n":2}`))
		select {
		case <-proceed:
		case <-ctx.Done():
			return ctx.Err()
		}
		_ = emit(ev(evToken, `{"n":3}`))
		return emit(terminal(completedOutcome()))
	})
	r := newRig(t, runner)
	first := r.open(r.runReq("att_a", 1))
	first.admitted()
	first.admitted()
	first.cancel() // the primary's connection drops

	// The attempt is still alive and Inspect says where it is.
	eventually(t, "the attempt to be inspectable as submitted", func() bool {
		return r.inspect("job-1", "att_a").GetState() == stSubmitted
	})
	if in := r.inspect("job-1", "att_a"); in.GetLastSequence() != 2 || !in.GetSubmitted() || in.GetOutcome() != nil {
		t.Fatalf("inspect after the stream broke: %v", in)
	}

	// Re-attach with the same fence: replay from sequence 1, then live. The
	// runner is NOT started again.
	second := r.open(r.runReq("att_a", 1))
	for want := uint64(1); want <= 2; want++ {
		if e := second.admitted(); e.GetSequence() != want {
			t.Fatalf("replay must start at sequence 1: got %v", e)
		}
	}
	close(proceed)
	rest, err := second.all()
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 || rest[0].GetSequence() != 3 || rest[1].GetSequence() != 4 || terminalOf(t, rest).GetStatus() != stCompleted {
		t.Fatalf("live part: %v", rest)
	}
	if runner.callCount() != 1 {
		t.Fatalf("re-attach started the runner again: %d calls", runner.callCount())
	}
	// A finished attempt can still be re-attached to: the full log replays.
	third, err := r.open(r.runReq("att_a", 1)).all()
	if err != nil || len(third) != 4 {
		t.Fatalf("replay of a finished attempt: %v %v", third, err)
	}
	// Re-attach with another fingerprint is refused and shows nothing.
	bad := r.runReq("att_a", 1)
	bad.Fence.InputFingerprint = "different"
	if _, err := r.open(bad).recv(); codeOf(err) != "FailedPrecondition" {
		t.Fatalf("fingerprint drift on re-attach: %v", err)
	}
}

func TestIdentityLockAndCapacity(t *testing.T) {
	r := newRig(t, newFakeRunner(blockingScript), func(c *Config) { c.MaxAttempts = 2 })
	req := func(id, job, ident string, gen uint64) *helperv1.RunAttemptRequest {
		q := r.runReq(id, gen)
		q.Fence.JobId, q.IdentityRef = job, ident
		return q
	}
	r.open(req("att_1", "job-1", "A", 1)).admitted()
	// One active operation per (provider, identity): a second job on the same
	// session waits (Unavailable), whatever the capacity.
	if _, err := r.open(req("att_2", "job-2", "A", 1)).recv(); codeOf(err) != "Unavailable" {
		t.Fatalf("same identity must be refused: %v", err)
	}
	// Another provider session with the same ref is another identity.
	other := req("att_3", "job-3", "A", 1)
	other.Provider = "gemini_web"
	r.open(other).admitted()
	// Capacity (browsers_max=2) is full.
	if _, err := r.open(req("att_4", "job-4", "B", 1)).recv(); codeOf(err) != "Unavailable" {
		t.Fatalf("over capacity must be refused: %v", err)
	}
	if _, err := r.client.CancelAttempt(r.ctx(), &helperv1.CancelAttemptRequest{Fence: &helperv1.Fence{
		JobId: "job-1", AttemptId: "att_1", NodeId: "helper-1", LeaseGeneration: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	r.waitReleased("att_1")
	r.open(req("att_5", "job-5", "B", 1)).admitted()
}

func TestRequestValidation(t *testing.T) {
	runner := newFakeRunner(blockingScript)
	r := newRig(t, runner)
	for _, tc := range []struct {
		name string
		mut  func(*helperv1.RunAttemptRequest)
		want string
	}{
		{"antigravity target", func(q *helperv1.RunAttemptRequest) { q.Target = "antigravity_cli" }, "InvalidArgument"},
		{"antigravity target, any case", func(q *helperv1.RunAttemptRequest) { q.Target = "Antigravity_SDK" }, "InvalidArgument"},
		{"antigravity provider", func(q *helperv1.RunAttemptRequest) { q.Provider = "antigravity_sdk" }, "InvalidArgument"},
		{"no fence", func(q *helperv1.RunAttemptRequest) { q.Fence = nil }, "InvalidArgument"},
		{"bad attempt id", func(q *helperv1.RunAttemptRequest) { q.Fence.AttemptId = "x1" }, "InvalidArgument"},
		{"attempt id with path", func(q *helperv1.RunAttemptRequest) { q.Fence.AttemptId = "att_../x" }, "InvalidArgument"},
		{"zero generation", func(q *helperv1.RunAttemptRequest) { q.Fence.LeaseGeneration = 0 }, "InvalidArgument"},
		{"no fingerprint", func(q *helperv1.RunAttemptRequest) { q.Fence.InputFingerprint = "" }, "InvalidArgument"},
		{"no workload version", func(q *helperv1.RunAttemptRequest) { q.Fence.WorkloadVersion = "" }, "InvalidArgument"},
		{"other workload version", func(q *helperv1.RunAttemptRequest) { q.Fence.WorkloadVersion = "w2" }, "FailedPrecondition"},
		{"fence names another node", func(q *helperv1.RunAttemptRequest) { q.Fence.NodeId = "helper-2" }, "PermissionDenied"},
		{"fence without node", func(q *helperv1.RunAttemptRequest) { q.Fence.NodeId = "" }, "PermissionDenied"},
		{"lease already expired", func(q *helperv1.RunAttemptRequest) {
			q.Fence.LeaseExpiresAt = timestamppb.New(time.Now().Add(-time.Minute))
		}, "Aborted"},
		{"no lease", func(q *helperv1.RunAttemptRequest) { q.Fence.LeaseExpiresAt = nil }, "Aborted"},
		{"no identity_ref", func(q *helperv1.RunAttemptRequest) { q.IdentityRef = "" }, "InvalidArgument"},
		{"identity_ref is a path", func(q *helperv1.RunAttemptRequest) { q.IdentityRef = "../profile" }, "InvalidArgument"},
		{"no provider", func(q *helperv1.RunAttemptRequest) { q.Provider = "" }, "InvalidArgument"},
		{"input is not an object", func(q *helperv1.RunAttemptRequest) { q.InputJson = `["x"]` }, "InvalidArgument"},
		{"input is not json", func(q *helperv1.RunAttemptRequest) { q.InputJson = `{bad` }, "InvalidArgument"},
		{"input empty", func(q *helperv1.RunAttemptRequest) { q.InputJson = "" }, "InvalidArgument"},
		{"input too large", func(q *helperv1.RunAttemptRequest) {
			q.InputJson = `{"p":"` + strings.Repeat("x", maxInputBytes) + `"}`
		}, "InvalidArgument"},
		{"options not an object", func(q *helperv1.RunAttemptRequest) { q.OptionsJson = `3` }, "InvalidArgument"},
		{"negative deadline", func(q *helperv1.RunAttemptRequest) { q.DeadlineSeconds = -1 }, "InvalidArgument"},
		{"deadline beyond the maximum", func(q *helperv1.RunAttemptRequest) { q.DeadlineSeconds = 7200 }, "InvalidArgument"},
		{"asset with a bad digest", func(q *helperv1.RunAttemptRequest) {
			q.Assets = []*helperv1.Asset{{Sha256: "XYZ", SizeBytes: 1}}
		}, "InvalidArgument"},
		{"asset over the size limit", func(q *helperv1.RunAttemptRequest) {
			q.Assets = []*helperv1.Asset{{Sha256: strings.Repeat("a", 64), SizeBytes: maxAssetBytes + 1}}
		}, "InvalidArgument"},
	} {
		req := r.runReq("att_v", 1)
		tc.mut(req)
		if _, err := r.open(req).recv(); codeOf(err) != tc.want {
			t.Errorf("%s: want %s, got %v", tc.name, tc.want, err)
		}
	}
	if runner.callCount() != 0 {
		t.Fatalf("a rejected request must never reach the runner (%d calls)", runner.callCount())
	}
	if c := r.capacity(); c.GetBrowsersActive() != 0 || len(c.GetSlots()) != 0 {
		t.Fatalf("rejected requests must hold nothing: %v", c)
	}
	// Defaults: no deadline means the default budget; a valid asset passes.
	ok := r.runReq("att_ok", 1)
	ok.DeadlineSeconds = 0
	ok.Assets = []*helperv1.Asset{{Sha256: strings.Repeat("a", 64), SizeBytes: 10, Name: "a.pdf", MediaType: "application/pdf"}}
	r.open(ok).admitted()
	if got := runner.specs[0]; got.Deadline != DefaultDeadline || len(got.Assets) != 1 {
		t.Fatalf("spec: %+v", got)
	}
}

func TestNoRunnerRefusesAttemptsAndStillServesTheRest(t *testing.T) {
	r := newRig(t, nil)
	if _, err := r.open(r.runReq("att_a", 1)).recv(); codeOf(err) != "Unavailable" {
		t.Fatalf("no runner: %v", err)
	}
	if c := r.capacity(); c.GetBrowsersMax() != 2 || c.GetBrowsersActive() != 0 {
		t.Fatalf("capacity must still work: %v", c)
	}
	if _, err := r.client.StageManifest(r.ctx(), &helperv1.StageManifestRequest{Fence: r.fence("att_a", 1)}); codeOf(err) != "Unimplemented" {
		t.Fatalf("StageManifest is not served: %v", err)
	}
}

func TestDeadlineCutIsTimedOutWithPartialAndNoResult(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tokens      int
		wantPartial bool
	}{{"tokens streamed", 2, true}, {"nothing streamed", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			runner := newFakeRunner(func(ctx context.Context, _ AttemptSpec, emit EmitFunc) error {
				_ = emit(ev(evStarted, ""))
				_ = emit(ev(evSubmitted, ""))
				for range tc.tokens {
					_ = emit(ev(evToken, `{"t":"x"}`))
				}
				<-ctx.Done()
				return ctx.Err()
			})
			r := newRig(t, runner)
			req := r.runReq("att_a", 1)
			req.DeadlineSeconds = 30
			rs := r.open(req)
			for range 2 + tc.tokens {
				rs.admitted()
			}
			// Keep the lease alive so only the deadline can end it.
			for range 3 {
				r.clock.Advance(10 * time.Second)
				if _, err := r.client.RenewAttempt(r.ctx(), &helperv1.RenewAttemptRequest{Fence: r.fence("att_a", 1)}); err != nil {
					t.Fatalf("renew: %v", err)
				}
			}
			if in := r.inspect("job-1", "att_a"); in.GetState() == stFinished {
				t.Fatal("the runner gets DeadlineGrace to end the attempt itself")
			}
			r.clock.Advance(3 * time.Second) // 33 s > 30 s + 2 s grace
			evs, err := rs.all()
			if err != nil {
				t.Fatal(err)
			}
			o := terminalOf(t, evs)
			if o.GetStatus() != stTimedOut || o.GetStreamEndReason() != "deadline" || o.GetPartial() != tc.wantPartial ||
				o.GetResultJson() != "" || !o.GetSubmitted() || o.GetReconcileRequired() {
				t.Fatalf("outcome: %v", o)
			}
		})
	}
}

func TestOutputBoundsFailTheAttemptAndNeverTruncate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script func(emit EmitFunc) (ended error)
		events uint64 // events in the log, terminal included; 0 = do not check
	}{
		{"one event over the size limit", func(emit EmitFunc) error {
			return emit(ev(evToken, `{"t":"`+strings.Repeat("x", maxEventDataBytes)+`"}`))
		}, 1},
		{"result over the size limit", func(emit EmitFunc) error {
			return emit(terminal(&helperv1.AttemptOutcome{Status: stCompleted, ResultJson: `{"t":"` + strings.Repeat("x", maxEventDataBytes) + `"}`}))
		}, 1},
		{"too many events", func(emit EmitFunc) error {
			for {
				if err := emit(ev(evToken, `{"t":"x"}`)); err != nil {
					return err
				}
			}
		}, maxAttemptEvents},
		{"too many bytes", func(emit EmitFunc) error {
			chunk := `{"t":"` + strings.Repeat("y", 50*1024) + `"}`
			for {
				if err := emit(ev(evToken, chunk)); err != nil {
					return err
				}
			}
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var runnerErr atomic.Value
			r := newRig(t, newFakeRunner(func(_ context.Context, _ AttemptSpec, emit EmitFunc) error {
				err := tc.script(emit)
				if err != nil {
					runnerErr.Store(err)
				}
				return err
			}))
			evs, err := r.open(r.runReq("att_a", 1)).all()
			if err != nil {
				t.Fatal(err)
			}
			o := terminalOf(t, evs)
			if o.GetStatus() != stFailed || o.GetErrorCode() != "helper_output_limit" || o.GetStreamEndReason() != "output_limit" || o.GetResultJson() != "" {
				t.Fatalf("outcome: %v", o)
			}
			if tc.events != 0 && uint64(len(evs)) != tc.events {
				t.Fatalf("want %d events, got %d", tc.events, len(evs))
			}
			for i, e := range evs {
				if e.GetSequence() != uint64(i+1) {
					t.Fatalf("sequence gap at %d: %v", i, e.GetSequence())
				}
			}
			// The runner was told to stop: emit refuses once the attempt ended.
			r.waitReleased("att_a")
			if err, _ := runnerErr.Load().(error); err == nil || !contains(err.Error(), "attempt has ended") {
				t.Fatalf("the runner must see its emit refused, got %v", err)
			}
		})
	}
}

func TestInvalidEventsFailTheAttempt(t *testing.T) {
	for name, e := range map[string]Event{
		"unspecified type":         {Type: helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_UNSPECIFIED},
		"unknown type":             {Type: helperv1.AttemptEventType(99)},
		"terminal without outcome": {Type: evTerminal},
		"terminal with no status":  terminal(&helperv1.AttemptOutcome{}),
		"outcome on a token":       {Type: evToken, Outcome: completedOutcome()},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, newFakeRunner(func(_ context.Context, _ AttemptSpec, emit EmitFunc) error {
				_ = emit(ev(evStarted, ""))
				return emit(e)
			}))
			evs, err := r.open(r.runReq("att_a", 1)).all()
			if err != nil {
				t.Fatal(err)
			}
			if o := terminalOf(t, evs); o.GetStatus() != stFailed || o.GetErrorCode() != "helper_event_invalid" {
				t.Fatalf("outcome: %v", o)
			}
		})
	}
}

func TestDrainStopsNewAttemptsAndCancelsAfterTheGrace(t *testing.T) {
	r := newRig(t, newFakeRunner(blockingScript))
	rs := r.open(r.runReq("att_a", 1))
	rs.admitted()

	resp, err := r.client.Drain(r.ctx(), &helperv1.DrainRequest{GraceSeconds: 60, Reason: "grant draining"})
	if err != nil || !resp.GetDraining() || resp.GetAttemptsActive() != 1 {
		t.Fatalf("drain: %v %v", resp, err)
	}
	if !r.capacity().GetDraining() {
		t.Fatal("capacity must report draining")
	}
	other := r.runReq("att_b", 1)
	other.Fence.JobId, other.IdentityRef = "job-2", "ident-2"
	if _, err := r.open(other).recv(); codeOf(err) != "Unavailable" {
		t.Fatalf("a draining helper takes no new attempt: %v", err)
	}
	// Idempotent, and a second call does not restart or extend the clock.
	r.clock.Advance(30 * time.Second)
	if _, err := r.client.Drain(r.ctx(), &helperv1.DrainRequest{GraceSeconds: 600}); err != nil {
		t.Fatal(err)
	}
	// Renew so only the drain can end it.
	if _, err := r.client.RenewAttempt(r.ctx(), &helperv1.RenewAttemptRequest{Fence: r.fence("att_a", 1)}); err != nil {
		t.Fatal(err)
	}
	r.clock.Advance(29 * time.Second)
	if in := r.inspect("job-1", "att_a"); in.GetState() == stFinished {
		t.Fatal("running work must finish or wait out the grace")
	}
	r.clock.Advance(2 * time.Second)
	evs, err := rs.all()
	if err != nil {
		t.Fatal(err)
	}
	if o := terminalOf(t, evs); o.GetStatus() != stCancelled || o.GetStreamEndReason() != "drain_grace_elapsed" {
		t.Fatalf("outcome: %v", o)
	}
}

func TestDrainWithoutAGraceDefaultsAndNeverMeansCancelNow(t *testing.T) {
	r := newRig(t, newFakeRunner(blockingScript), func(c *Config) { c.LeaseMaxTTL = time.Hour })
	rs := r.open(r.runReq("att_a", 1))
	rs.admitted()
	if _, err := r.client.Drain(r.ctx(), &helperv1.DrainRequest{}); err != nil {
		t.Fatal(err)
	}
	renew := func() {
		f := r.fence("att_a", 1)
		f.LeaseExpiresAt = timestamppb.New(r.clock.Now().Add(time.Hour))
		if _, err := r.client.RenewAttempt(r.ctx(), &helperv1.RenewAttemptRequest{Fence: f}); err != nil {
			t.Fatal(err)
		}
	}
	renew()
	r.clock.Advance(DefaultDrainGrace - time.Second)
	if in := r.inspect("job-1", "att_a"); in.GetState() == stFinished {
		t.Fatal("a missing grace must default to 5 minutes, not cancel now")
	}
	r.clock.Advance(2 * time.Second)
	evs, err := rs.all()
	if err != nil || terminalOf(t, evs).GetStreamEndReason() != "drain_grace_elapsed" {
		t.Fatalf("%v %v", evs, err)
	}
}

func TestShutdownKillsRunningAttemptsAndRefusesNewOnes(t *testing.T) {
	r := newRig(t, newFakeRunner(blockingScript))
	rs := r.open(r.runReq("att_a", 1))
	rs.admitted()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	evs, err := rs.all()
	if err != nil {
		t.Fatal(err)
	}
	if o := terminalOf(t, evs); o.GetErrorCode() != "helper_shutdown" || o.GetStreamEndReason() != "shutdown" {
		t.Fatalf("outcome: %v", o)
	}
	if _, err := r.open(r.runReq("att_b", 2)).recv(); codeOf(err) != "Unavailable" {
		t.Fatalf("a closed helper takes no attempt: %v", err)
	}
}

func TestFinishedAttemptsAreForgottenAfterRetentionAndBounded(t *testing.T) {
	done := newFakeRunner(func(_ context.Context, _ AttemptSpec, emit EmitFunc) error { return emit(terminal(completedOutcome())) })
	r := newRig(t, done, func(c *Config) { c.Retain = 10 * time.Minute; c.MaxRetained = 2; c.MaxAttempts = 4 })
	for i := 1; i <= 4; i++ {
		req := r.runReq(fmt.Sprintf("att_%d", i), 1)
		req.Fence.JobId = fmt.Sprintf("job-%d", i)
		if _, err := r.open(req).all(); err != nil {
			t.Fatal(err)
		}
		r.waitReleased(fmt.Sprintf("att_%d", i))
		r.clock.Advance(time.Minute)
	}
	r.capacity() // sweeps
	known := 0
	for i := 1; i <= 4; i++ {
		if r.inspect(fmt.Sprintf("job-%d", i), fmt.Sprintf("att_%d", i)).GetState() == stFinished {
			known++
		}
	}
	if known != 2 {
		t.Fatalf("MaxRetained=2 keeps the 2 newest finished attempts, got %d", known)
	}
	r.clock.Advance(30 * time.Minute)
	r.capacity()
	if in := r.inspect("job-4", "att_4"); in.GetState() != stUnknown {
		t.Fatalf("after Retain a finished attempt is forgotten: %v", in)
	}
	r.srv.mu.Lock()
	n, nj := len(r.srv.attempts), len(r.srv.jobs)
	r.srv.mu.Unlock()
	if n != 0 || nj != 0 {
		t.Fatalf("sweep must also drop the per-job fence entries: %d attempts, %d jobs", n, nj)
	}
}

func TestManualActionLeavesTheSlotNeedingAHuman(t *testing.T) {
	r := newRig(t, newFakeRunner(func(_ context.Context, _ AttemptSpec, emit EmitFunc) error {
		_ = emit(ev(evManual, `{"reason":"login_required"}`))
		return emit(terminal(&helperv1.AttemptOutcome{Status: stFailed, ErrorCode: "manual_action"}))
	}))
	if _, err := r.open(r.runReq("att_a", 1)).all(); err != nil {
		t.Fatal(err)
	}
	r.waitReleased("att_a")
	c := r.capacity()
	if len(c.GetSlots()) != 1 || c.GetSlots()[0].GetState() != helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_MANUAL_ACTION_REQUIRED {
		t.Fatalf("slot after a manual-action stop: %v", c.GetSlots())
	}
}

func TestMetadataBounds(t *testing.T) {
	if _, err := NewServer(Config{NodeID: "../x", WorkloadVersion: "w", RegistryDigest: testDigest}); err == nil {
		t.Error("a bad node id must be refused")
	}
	if _, err := NewServer(Config{NodeID: "n", WorkloadVersion: "", RegistryDigest: testDigest}); err == nil {
		t.Error("a missing workload version must be refused")
	}
	if _, err := NewServer(Config{NodeID: "n", WorkloadVersion: "w", RegistryDigest: "abc"}); err == nil {
		t.Error("a bad registry digest must be refused")
	}
	s, err := NewServer(Config{NodeID: "n", WorkloadVersion: "w", RegistryDigest: testDigest, MaxAttempts: 99})
	if err != nil || s.cfg.MaxAttempts != maxAttemptsCeiling {
		t.Fatalf("MaxAttempts must be capped at %d: %v %v", maxAttemptsCeiling, s.cfg.MaxAttempts, err)
	}
}
