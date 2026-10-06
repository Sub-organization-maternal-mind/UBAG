package executor

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/prototext"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// ---- helpers ----------------------------------------------------------------

func (f *remoteFixture) waitEvent(jobID, eventType string) {
	f.t.Helper()
	waitFor(f.t, 10*time.Second, "the "+eventType+" event to be committed", func() bool {
		return slices.Contains(f.eventTypes(jobID), eventType)
	})
}

func (f *remoteFixture) auditRecords() []audit.Record {
	f.t.Helper()
	records, err := f.audit.List(context.Background(), audit.Filter{TenantID: "tenant_a"})
	if err != nil {
		f.t.Fatal(err)
	}
	return records
}

// assertPlacementsReleased checks the picker's reservation was returned once
// per placement, on whatever path the run took.
func (f *remoteFixture) assertPlacementsReleased() {
	f.t.Helper()
	if placed, released := f.picker.stats(); placed != released {
		f.t.Fatalf("placements = %d, released = %d: a reservation leaked or was returned twice", placed, released)
	}
}

// helperData returns the data of the stored events the helper produced.
func (f *remoteFixture) helperEvents(jobID string) []jobstore.Event {
	f.t.Helper()
	events, _, err := f.store.ListEvents(context.Background(), jobID, 0, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []jobstore.Event
	for _, e := range events {
		if _, ok := e.Data["helper"]; ok {
			out = append(out, e)
		}
	}
	return out
}

func terminalData(t *testing.T, events []jobstore.Event) map[string]any {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	return events[len(events)-1].Data
}

// ---- the happy path ---------------------------------------------------------

func TestRemoteRunCompletesTheJobThroughTheFencedIngest(t *testing.T) {
	f := newRemoteFixture(t)
	job, lease := f.newJob()
	done := runOnceAsync(t.Context(), f.consumer(lease, nil))

	f.helper.waitRun(t, 1)
	f.helper.send(evStarted, `{"phase":"start"}`)
	f.helper.send(evSubmitted, `{}`)
	f.helper.send(evToken, `{"delta":{"text":"hi"}}`)
	f.helper.sendCompleted()
	if res := awaitRun(t, done); !res.processed || res.err != nil {
		t.Fatalf("RunOnce = %+v", res)
	}

	if !lease.completed || lease.failed || lease.retried || lease.cancelled {
		t.Fatalf("lease = %+v, want completed only", lease)
	}
	if f.localRuns.Load() != 0 {
		t.Fatal("the local runner ran a job that was placed on a helper")
	}
	got := f.job(job.ID)
	if got.Status != jobstore.StatusCompleted {
		t.Fatalf("job status = %s", got.Status)
	}
	// The store normalises the result under output; the text is the helper's.
	result, _ := got.Result.(map[string]any)
	if output, _ := result["output"].(map[string]any); output["text"] != "from the helper" {
		t.Fatalf("result = %#v", got.Result)
	}
	// Every helper event carries the node-namespaced provenance the gateway stamped.
	events := f.helperEvents(job.ID)
	var types []string
	for _, e := range events {
		types = append(types, e.Type)
		h, _ := e.Data["helper"].(map[string]any)
		if h["node_id"] != "node_a" || e.Data["attempt_id"] == "" {
			t.Fatalf("event %s lacks provenance: %#v", e.Type, e.Data)
		}
	}
	if want := []string{"running", "prompt_submitted", "token", "completed"}; !slices.Equal(types, want) {
		t.Fatalf("helper events = %v, want %v", types, want)
	}
	attempts := f.attempts(job.ID)
	if len(attempts) != 1 || attempts[0].Generation != 1 || attempts[0].NodeID != "node_a" ||
		attempts[0].State != jobstore.AttemptFinished || !attempts[0].Submitted() {
		t.Fatalf("attempts = %+v", attempts)
	}
	if f.helper.closes.Load() != 1 {
		t.Fatalf("connection closed %d times, want 1", f.helper.closes.Load())
	}
	f.assertPlacementsReleased()
}

func TestRemoteRunSendsTheProjectedAttemptAndNothingElse(t *testing.T) {
	f := newRemoteFixture(t, func(c *RemoteConfig) { c.MaxRuntime = 90 * time.Second })
	job, lease := f.newJobWith(func(r *jobstore.CreateRequest) {
		r.Options = map[string]any{"temperature": 0.2, "user_data_dir": `C:\profiles\x`, "account_binding_id": "acct-9"}
		r.Callbacks = map[string]any{"webhook_url": "https://example.test/hook"}
		r.Context = map[string]any{"account_binding_id": "acct-9"}
		r.Client = map[string]any{"sdk": "ts"}
	})
	before := time.Now()
	done := runOnceAsync(t.Context(), f.consumer(lease, nil))
	req := f.helper.waitRun(t, 1)

	fence := req.GetFence()
	if fence.GetJobId() != job.ID || fence.GetNodeId() != "node_a" || fence.GetLeaseGeneration() != 1 ||
		fence.GetWorkloadVersion() != testWorkload || !regexp.MustCompile(`^att_[0-9a-f]{32}$`).MatchString(fence.GetAttemptId()) ||
		!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(fence.GetInputFingerprint()) {
		t.Fatalf("fence = %v", fence)
	}
	// The helper's lease ends before the ledger's: 120 s minus the 10 s skew allowance.
	if exp := fence.GetLeaseExpiresAt().AsTime(); exp.Before(before.Add(105*time.Second)) || exp.After(time.Now().Add(111*time.Second)) {
		t.Fatalf("fence lease expiry = %s, want about now+110s", exp)
	}
	if req.GetProvider() != "mock" || req.GetTarget() != "mock" || req.GetCommandType() != "submit" ||
		req.GetIdentityRef() != "pr_abc123" || req.GetDeadlineSeconds() != 90 || req.GetTraceId() != job.TraceID {
		t.Fatalf("request = %v", req)
	}
	if req.GetInputJson() != `{"prompt":"hi"}` || req.GetOptionsJson() != `{"temperature":0.2}` {
		t.Fatalf("bodies = %q / %q", req.GetInputJson(), req.GetOptionsJson())
	}
	// Nothing identity- or callback-bearing crosses: no tenant, app, callbacks,
	// client, context, profile paths or caller labels.
	wire := prototext.Format(req)
	for _, leak := range []string{"tenant_a", "app_a", "webhook", "example.test", "acct-9", "profiles", `"sdk"`} {
		if strings.Contains(wire, leak) {
			t.Fatalf("the request leaks %q:\n%s", leak, wire)
		}
	}
	f.helper.send(evStarted, `{}`)
	f.helper.sendCompleted()
	awaitRun(t, done)
}

func TestRemoteDeadlineIsBoundedByTheHelperContract(t *testing.T) {
	f := newRemoteFixture(t, func(c *RemoteConfig) { c.MaxRuntime = 3 * time.Hour })
	_, lease := f.newJob()
	done := runOnceAsync(t.Context(), f.consumer(lease, nil))
	if got := f.helper.waitRun(t, 1).GetDeadlineSeconds(); got != 3600 {
		t.Fatalf("deadline_seconds = %d, want the helper's 1 h bound", got)
	}
	f.helper.sendCompleted()
	awaitRun(t, done)
}

// ---- placement --------------------------------------------------------------

func TestRemoteFallsBackToTheLocalRunnerWhenNothingIsPlaced(t *testing.T) {
	t.Run("the picker has no helper", func(t *testing.T) {
		f := newRemoteFixture(t)
		f.picker.setErr(ErrNoHelper)
		job, lease := f.newJob()
		if res := awaitRun(t, runOnceAsync(t.Context(), f.consumer(lease, nil))); res.err != nil {
			t.Fatal(res.err)
		}
		if f.localRuns.Load() != 1 || !lease.completed || f.job(job.ID).Status != jobstore.StatusCompleted {
			t.Fatalf("local runs = %d, lease = %+v", f.localRuns.Load(), lease)
		}
		if runs, _, _ := f.helper.counts(); runs != 0 || len(f.attempts(job.ID)) != 0 {
			t.Fatal("a helper attempt was started for a job that ran locally")
		}
	})
	t.Run("dispatch is off", func(t *testing.T) {
		f := newRemoteFixture(t)
		job, lease := f.newJob()
		consumer := f.consumer(lease, nil)
		consumer.Remote = nil // UBAG_HELPER_DISPATCH unset
		if res := awaitRun(t, runOnceAsync(t.Context(), consumer)); res.err != nil {
			t.Fatal(res.err)
		}
		if f.localRuns.Load() != 1 || !lease.completed || f.job(job.ID).Status != jobstore.StatusCompleted {
			t.Fatalf("local runs = %d, lease = %+v", f.localRuns.Load(), lease)
		}
		if picks, _ := f.picker.stats(); picks != 0 {
			t.Fatal("the picker was consulted with dispatch off")
		}
	})
}

func TestRemoteIneligibleJobsAreNeverOfferedToThePicker(t *testing.T) {
	base := func() DispatchEnvelope {
		env := helperTestEnvelope()
		env.Job.ConversationID, env.Conversation = "", nil
		env.Job.Target, env.Job.CommandType = "chatgpt_web", "chat.prompt"
		return env
	}
	if !helperEligible(base()) {
		t.Fatal("the baseline envelope must be eligible")
	}
	cases := []struct {
		name string
		mut  func(*DispatchEnvelope)
	}{
		{"a conversation id", func(e *DispatchEnvelope) { e.Job.ConversationID = "conv" }},
		{"a conversation block", func(e *DispatchEnvelope) { e.Conversation = &DispatchConversation{Key: "k"} }},
		{"declared attachments", func(e *DispatchEnvelope) {
			e.Job.Input["attachments"] = []any{map[string]any{"key": "a", "kind": "document", "content_type": "text/plain"}}
		}},
		{"a legacy audio key", func(e *DispatchEnvelope) { e.Job.Input["audio_artifact_key"] = "clip" }},
		{"malformed attachments", func(e *DispatchEnvelope) { e.Job.Input["attachments"] = "nope" }},
		{"an antigravity target", func(e *DispatchEnvelope) { e.Job.Target = "antigravity_sdk" }},
		{"an antigravity target in any case", func(e *DispatchEnvelope) { e.Job.Target = "Antigravity_cli" }},
		{"a gateway voice command", func(e *DispatchEnvelope) { e.Job.CommandType = "voice.activate" }},
		{"a target the helper would refuse", func(e *DispatchEnvelope) { e.Job.Target = "bad target" }},
		{"a command type the helper would refuse", func(e *DispatchEnvelope) { e.Job.CommandType = strings.Repeat("a", 65) }},
		{"a trace id the helper would refuse", func(e *DispatchEnvelope) { e.TraceID = strings.Repeat("t", 129) }},
		{"a payload the policy refuses", func(e *DispatchEnvelope) { e.Job.Input["password"] = "x" }},
		{"an oversized input", func(e *DispatchEnvelope) { e.Job.Input["blob"] = strings.Repeat("x", helperMaxInputBytes+1) }},
		{"oversized options", func(e *DispatchEnvelope) { e.Job.Options["blob"] = strings.Repeat("x", helperMaxOptionsBytes+1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := base()
			tc.mut(&env)
			if helperEligible(env) {
				t.Fatal("job was judged helper-eligible")
			}
		})
	}

	// An ineligible job runs locally without the picker or the helper being touched.
	f := newRemoteFixture(t)
	job, lease := f.newJobWith(func(r *jobstore.CreateRequest) { r.CommandType = "voice.activate" })
	if res := awaitRun(t, runOnceAsync(t.Context(), f.consumer(lease, nil))); res.err != nil {
		t.Fatal(res.err)
	}
	if picks := len(f.picker.picks); picks != 0 || f.localRuns.Load() != 1 || f.job(job.ID).Status != jobstore.StatusCompleted {
		t.Fatalf("picks = %d, local runs = %d", picks, f.localRuns.Load())
	}
}

func TestRemotePickerHoldsTheJobBackAndNeverRunsItLocally(t *testing.T) {
	cases := map[string]error{
		"the picker asks to wait":       &HelperRetryError{Reason: "identity_busy", RetryAfter: 10 * time.Millisecond},
		"the picker fails unexpectedly": errors.New("manager exploded"),
	}
	for name, pickErr := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.picker.setErr(pickErr)
			job, lease := f.newJob()
			if res := awaitRun(t, runOnceAsync(t.Context(), f.consumer(lease, nil))); !res.processed || res.err != nil {
				t.Fatalf("RunOnce = %+v", res)
			}
			if !lease.retried || lease.completed || lease.failed {
				t.Fatalf("lease = %+v, want retried only", lease)
			}
			if f.localRuns.Load() != 0 || f.job(job.ID).Status != jobstore.StatusQueued || len(f.attempts(job.ID)) != 0 {
				t.Fatal("a held-back job must stay queued, unrun and unleased in the ledger")
			}
		})
	}
}

func TestRemotePlaceRejectsAMalformedPlacement(t *testing.T) {
	for name, mut := range map[string]func(*HelperPlacement){
		"empty node":    func(p *HelperPlacement) { p.NodeID = "" },
		"bad node":      func(p *HelperPlacement) { p.NodeID = "../x" },
		"no endpoint":   func(p *HelperPlacement) { p.Endpoint = "" },
		"colon in ref":  func(p *HelperPlacement) { p.ProfileRef = "a:b" },
		"path-like ref": func(p *HelperPlacement) { p.ProfileRef = "/etc/passwd" },
		"empty ref":     func(p *HelperPlacement) { p.ProfileRef = "" },
		"overlong ref":  func(p *HelperPlacement) { p.ProfileRef = strings.Repeat("a", 129) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newRemoteFixture(t)
			mut(&f.picker.place)
			placed, err := f.runner.Place(t.Context(), helperEnvEligible())
			var retry *HelperRetryError
			if placed != nil || !errors.As(err, &retry) || retry.Reason != "placement_invalid" {
				t.Fatalf("Place = %v, %v", placed, err)
			}
			f.assertPlacementsReleased()
		})
	}
	var none *RemoteWorkerRunner
	if placed, err := none.Place(t.Context(), helperEnvEligible()); placed != nil || err != nil {
		t.Fatalf("a nil runner must place nothing, got %v, %v", placed, err)
	}
}

func helperEnvEligible() DispatchEnvelope {
	env := helperTestEnvelope()
	env.Job.ConversationID, env.Conversation = "", nil
	return env
}

func TestRemoteIncompatibleHelperHoldsTheJobBeforeAnythingIsLeased(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*fakeHelper)
	}{
		{"another workload version", func(h *fakeHelper) { h.hs.WorkloadVersion = "w2" }},
		{"another adapter registry", func(h *fakeHelper) { h.hs.RegistryDigest = strings.Repeat("b", 64) }},
		{"no registry digest", func(h *fakeHelper) { h.hs.RegistryDigest = "" }},
		{"another protocol", func(h *fakeHelper) { h.hs.ProtocolVersion = "ubag.helper.v2" }},
		{"another node", func(h *fakeHelper) { h.hs.NodeId = "node_b" }},
		{"a drifting clock", func(h *fakeHelper) { h.serverTime = func() time.Time { return time.Now().Add(time.Minute) } }},
		{"no handshake answer", func(h *fakeHelper) { h.handshakeErr = status.Error(codes.Unavailable, "down") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			tc.mut(f.helper)
			job, lease := f.newJob()
			if res := awaitRun(t, runOnceAsync(t.Context(), f.consumer(lease, nil))); res.err != nil {
				t.Fatal(res.err)
			}
			if !lease.retried || lease.failed || lease.completed {
				t.Fatalf("lease = %+v, want retried", lease)
			}
			if runs, _, _ := f.helper.counts(); runs != 0 || len(f.attempts(job.ID)) != 0 || f.localRuns.Load() != 0 {
				t.Fatal("an unusable helper must not be leased, run or bypassed")
			}
			if f.helper.closes.Load() != 1 {
				t.Fatal("the connection to a refused helper was not closed")
			}
			f.assertPlacementsReleased()
		})
	}

	t.Run("the dial fails", func(t *testing.T) {
		f := newRemoteFixture(t, func(c *RemoteConfig) {
			c.Dialer = HelperDialFunc(func(context.Context, HelperPlacement) (HelperConn, error) { return nil, errors.New("no route") })
		})
		job, lease := f.newJob()
		if res := awaitRun(t, runOnceAsync(t.Context(), f.consumer(lease, nil))); res.err != nil {
			t.Fatal(res.err)
		}
		if !lease.retried || len(f.attempts(job.ID)) != 0 || f.localRuns.Load() != 0 {
			t.Fatalf("lease = %+v", lease)
		}
		f.assertPlacementsReleased()
	})
}

// ---- idempotent, fenced commits ---------------------------------------------

func TestRemoteDuplicateCompletionIsANoOp(t *testing.T) {
	f := newRemoteFixture(t)
	job, lease := f.newJob()
	done := runOnceAsync(t.Context(), f.consumer(lease, nil))

	f.helper.waitRun(t, 1)
	f.helper.send(evStarted, `{}`)
	f.helper.send(evSubmitted, `{}`)
	f.waitEvent(job.ID, "prompt_submitted")
	// The stream drops; the attempt lives on its lease. The primary re-attaches
	// and the helper replays from sequence 1: those events are already committed.
	f.helper.breakStreams()
	f.helper.waitRun(t, 2)
	f.helper.send(evToken, `{"delta":{"text":"hi"}}`)
	f.helper.sendCompleted()
	if res := awaitRun(t, done); res.err != nil {
		t.Fatal(res.err)
	}

	count := func(kind string) int {
		n := 0
		for _, typ := range f.eventTypes(job.ID) {
			if typ == kind {
				n++
			}
		}
		return n
	}
	for kind, want := range map[string]int{"running": 1, "prompt_submitted": 1, "token": 1, "completed": 1} {
		if got := count(kind); got != want {
			t.Fatalf("%d %q events stored, want %d (a replay must not duplicate)", got, kind, want)
		}
	}
	if runs, _, _ := f.helper.counts(); runs != 2 {
		t.Fatalf("RunAttempt calls = %d, want 2 (one re-attach)", runs)
	}

	// Feeding the terminal event again, even after the job completed and the
	// attempt finished, changes nothing.
	f.helper.mu.Lock()
	log := f.helper.last.events
	f.helper.mu.Unlock()
	att := f.attempts(job.ID)[0]
	ingest, err := OpenHelperIngest(t.Context(), HelperIngestConfig{Store: f.store, Audit: f.audit},
		HelperIngestBinding{TenantID: "tenant_a", JobID: job.ID, AttemptID: att.AttemptID, Generation: att.Generation, NodeID: "node_a", ResumeAfter: uint64(len(log) - 1)})
	if err != nil {
		t.Fatal(err)
	}
	before, after := f.job(job.ID), jobstore.Job{}
	stored := len(f.eventTypes(job.ID))
	if _, err := ingest.Accept(t.Context(), log[len(log)-1]); err != nil {
		t.Fatalf("duplicate terminal: %v", err)
	}
	after = f.job(job.ID)
	if after.Status != before.Status || !after.UpdatedAt.Equal(before.UpdatedAt) || len(f.eventTypes(job.ID)) != stored {
		t.Fatal("a duplicate completion changed the job")
	}
}

func TestRemoteStaleEventGenerationIsRejectedTheJobUntouchedAndAudited(t *testing.T) {
	f := newRemoteFixture(t)
	job, lease := f.newJob()
	done := runOnceAsync(t.Context(), f.consumer(lease, nil))

	f.helper.waitRun(t, 1)
	f.helper.send(evStarted, `{}`)
	f.waitEvent(job.ID, "running")
	committed := len(f.eventTypes(job.ID))
	f.helper.sendStale(evToken, 0) // names a lease generation that is not the attempt's

	if res := awaitRun(t, done); res.err != nil {
		t.Fatal(res.err)
	}
	if got := len(f.eventTypes(job.ID)); got != committed {
		t.Fatalf("the stale event was committed (%d events, want %d)", got, committed)
	}
	if st := f.job(job.ID).Status; jobstore.TerminalStatus(st) {
		t.Fatalf("a stale writer ended the job: %s", st)
	}
	// The helper is confused, not the lease: the job is held back and the helper stopped.
	if !lease.retried || lease.failed || lease.completed {
		t.Fatalf("lease = %+v, want retried", lease)
	}
	f.helper.waitCancels(t, 1)
	var fenced []audit.Record
	for _, r := range f.auditRecords() {
		if r.Action == "attempt.fenced_rejected" {
			fenced = append(fenced, r)
		}
	}
	if len(fenced) != 1 || fenced[0].Outcome != "denied" || fenced[0].Attributes["error_code"] != HelperFencedErrorCode ||
		fenced[0].Actor != "node:node_a" {
		t.Fatalf("fenced audit records = %+v", fenced)
	}
	f.assertPlacementsReleased()
}

func TestRemoteSupersededLeaseStopsWithoutWritingAnything(t *testing.T) {
	f := newRemoteFixture(t, fastLease)
	// The ledger lease is not extended: it lapses 300 ms after the attempt began,
	// as if this primary could no longer reach the store.
	f.store.setHook(func(jobstore.AttemptRef) (bool, error) { return true, nil })
	job, lease := f.newJob()
	done := runOnceAsync(t.Context(), f.consumer(lease, nil))

	req := f.helper.waitRun(t, 1)
	f.helper.send(evStarted, `{}`)
	f.waitEvent(job.ID, "running")
	committed := len(f.eventTypes(job.ID))

	// Another dispatcher takes the job over once the lease has lapsed: generation 2.
	first := f.attempts(job.ID)[0]
	waitFor(t, 5*time.Second, "the first lease to lapse", func() bool { return time.Now().After(first.LeaseExpiresAt) })
	if _, err := f.store.BeginAttempt(t.Context(), jobstore.BeginAttemptRequest{
		JobID: job.ID, AttemptID: "att_successor", NodeID: "node_b", ExpectedGeneration: 1,
		InputFingerprint: req.GetFence().GetInputFingerprint(), WorkloadVersion: testWorkload,
	}); err != nil {
		t.Fatalf("successor BeginAttempt: %v", err)
	}

	// The old helper keeps talking. Its next event is fenced out.
	f.helper.send(evToken, `{"delta":{"text":"late"}}`)
	if res := awaitRun(t, done); res.err != nil {
		t.Fatal(res.err)
	}
	if got := len(f.eventTypes(job.ID)); got != committed {
		t.Fatalf("a superseded attempt wrote %d events", got-committed)
	}
	if st := f.job(job.ID).Status; jobstore.TerminalStatus(st) {
		t.Fatalf("the superseded attempt ended the job: %s", st)
	}
	if !lease.retried || lease.failed || lease.completed {
		t.Fatalf("lease = %+v, want retried (the successor owns the job)", lease)
	}
	if _, _, cancels := f.helper.counts(); cancels != 0 {
		t.Fatal("a fenced-out dispatcher must not cancel the successor's helper work")
	}
	var reasons []string
	for _, r := range f.auditRecords() {
		if r.Action == "attempt.fenced_rejected" {
			reasons = append(reasons, r.Attributes["reason"].(string))
		}
	}
	if !slices.Equal(reasons, []string{"commit_fenced"}) {
		t.Fatalf("fenced audit reasons = %v", reasons)
	}
}

// ---- the attempt lease: 120 s, renewed every 20 s ----------------------------

func TestRemoteLeaseRenewsEvery20sAndExpiresAt120s(t *testing.T) {
	f := newRemoteFixture(t, func(c *RemoteConfig) { c.RetryDelay = time.Millisecond })
	clock := newFakeRemoteClock()
	f.runner.clock = clock
	f.helper.serverTime = clock.Now
	job, lease := f.newJob()
	done := runOnceAsync(t.Context(), f.consumer(lease, nil))

	f.helper.waitRun(t, 1)
	f.helper.send(evStarted, `{}`)
	f.waitEvent(job.ID, "running")
	clock.waitTicker(t)
	if begun := f.store.begun; len(begun) != 1 || begun[0].TTL != 120*time.Second {
		t.Fatalf("BeginAttempt = %+v, want a 120 s lease", begun)
	}

	// Three healthy renewals, 20 s apart: the ledger is renewed for 120 s each
	// time, and the helper is told a lease that ends 110 s out (the 10 s skew
	// allowance keeps its lease from outliving the ledger's).
	for i := 1; i <= 3; i++ {
		clock.Advance(20 * time.Second)
		f.helper.waitRenews(t, i)
		f.helper.mu.Lock()
		got := f.helper.renews[i-1].GetFence()
		f.helper.mu.Unlock()
		if want := clock.Now().Add(110 * time.Second); !got.GetLeaseExpiresAt().AsTime().Equal(want) {
			t.Fatalf("renewal %d fence expiry = %s, want %s", i, got.GetLeaseExpiresAt().AsTime(), want)
		}
		if got.GetLeaseGeneration() != 1 || got.GetNodeId() != "node_a" {
			t.Fatalf("renewal %d fence = %v", i, got)
		}
	}
	if ttls := f.store.renewTTLs(); len(ttls) != 3 || ttls[0] != 120*time.Second || ttls[2] != 120*time.Second {
		t.Fatalf("ledger renewals = %v, want three of 120s", ttls)
	}

	// The helper stops answering. Renewals keep being tried every 20 s; the helper
	// is declared lost only once 120 s have passed since the last success.
	f.helper.setRenewErr(status.Error(codes.Unavailable, "unreachable"))
	for i := 1; i <= 5; i++ {
		clock.Advance(20 * time.Second)
		f.helper.waitRenews(t, 3+i)
	}
	time.Sleep(50 * time.Millisecond)
	select {
	case res := <-done:
		t.Fatalf("declared lost after only 100 s of failed renewals: %+v", res)
	default:
	}
	clock.Advance(20 * time.Second) // 120 s since the last renewal that worked
	if res := awaitRun(t, done); !res.processed || res.err != nil {
		t.Fatalf("RunOnce = %+v", res)
	}
	if !lease.retried || lease.failed {
		t.Fatalf("lease = %+v, want the job held back for reassignment", lease)
	}
	f.helper.waitCancels(t, 1)
	if att := f.attempts(job.ID)[0]; att.Submitted() {
		t.Fatal("nothing was submitted")
	}
}

// ---- helper lost mid-run ------------------------------------------------------

func TestRemoteHelperLostBeforeSubmissionIsReassigned(t *testing.T) {
	f := newRemoteFixture(t, fastLease)
	job, lease := f.newJob()
	done := runOnceAsync(t.Context(), f.consumer(lease, nil))

	f.helper.waitRun(t, 1)
	f.helper.send(evStarted, `{}`)
	f.waitEvent(job.ID, "running")
	f.helper.setDead(true) // the node vanishes: the stream breaks and renewals fail

	if res := awaitRun(t, done); !res.processed || res.err != nil {
		t.Fatalf("RunOnce = %+v", res)
	}
	if !lease.retried || lease.failed || lease.completed || f.localRuns.Load() != 0 {
		t.Fatalf("lease = %+v, local runs %d: a job lost before submission is held back, not failed or run locally", lease, f.localRuns.Load())
	}
	if st := f.job(job.ID).Status; jobstore.TerminalStatus(st) {
		t.Fatalf("job status = %s", st)
	}
	first := f.attempts(job.ID)[0]
	if first.Submitted() {
		t.Fatal("the boundary was never crossed")
	}

	// The helper (or another) is back. Once the first attempt's lease has lapsed a
	// new attempt, generation 2, takes the job over and completes it.
	f.helper.setDead(false)
	waitFor(t, 5*time.Second, "the first lease to lapse", func() bool { return time.Now().After(f.attempts(job.ID)[0].LeaseExpiresAt) })
	runsBefore, _, _ := f.helper.counts()
	lease2 := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_again", envelope: lease.envelope}
	done2 := runOnceAsync(t.Context(), f.consumer(lease2, nil))
	f.helper.waitRun(t, runsBefore+1)
	f.helper.send(evStarted, `{}`)
	f.helper.sendCompleted()
	if res := awaitRun(t, done2); res.err != nil {
		t.Fatal(res.err)
	}
	if !lease2.completed || f.job(job.ID).Status != jobstore.StatusCompleted {
		t.Fatalf("second delivery: lease = %+v, job = %s", lease2, f.job(job.ID).Status)
	}
	attempts := f.attempts(job.ID)
	if len(attempts) != 2 || attempts[1].Generation != 2 || attempts[0].State != jobstore.AttemptExpired ||
		attempts[1].State != jobstore.AttemptFinished || attempts[0].AttemptID == attempts[1].AttemptID {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestRemoteHelperLostAfterSubmissionFailsClosedAndIsNeverReplayed(t *testing.T) {
	f := newRemoteFixture(t, fastLease)
	job, lease := f.newJob()
	done := runOnceAsync(t.Context(), f.consumer(lease, nil))

	f.helper.waitRun(t, 1)
	f.helper.send(evStarted, `{}`)
	f.helper.send(evSubmitted, `{}`)
	f.waitEvent(job.ID, "prompt_submitted")
	f.helper.setDead(true)

	if res := awaitRun(t, done); !res.processed || res.err != nil {
		t.Fatalf("RunOnce = %+v", res)
	}
	if lease.retried || !lease.failed || lease.completed {
		t.Fatalf("lease = %+v, want failed and never retried", lease)
	}
	got := f.job(job.ID)
	if got.Status != jobstore.StatusFailedTerminal {
		t.Fatalf("job status = %s, want failed_terminal", got.Status)
	}
	data := terminalData(t, mustEvents(t, f, job.ID))
	if data["submitted"] != true || data["reconcile_required"] != true || data["retryable"] != false {
		t.Fatalf("terminal data = %#v, want submitted, reconcile_required, not retryable", data)
	}
	if attempts := f.attempts(job.ID); len(attempts) != 1 || !attempts[0].Submitted() {
		t.Fatalf("attempts = %+v", attempts)
	}

	// A redelivery of the same job finds it terminal and places nothing.
	placedBefore, _ := f.picker.stats()
	lease2 := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_again", envelope: lease.envelope}
	if res := awaitRun(t, runOnceAsync(t.Context(), f.consumer(lease2, nil))); res.err != nil {
		t.Fatal(res.err)
	}
	if placed, _ := f.picker.stats(); placed != placedBefore || len(f.store.begun) != 1 || f.localRuns.Load() != 0 {
		t.Fatal("a job that failed after submission was dispatched again")
	}
	f.assertPlacementsReleased()
}

func mustEvents(t *testing.T, f *remoteFixture, jobID string) []jobstore.Event {
	t.Helper()
	events, _, err := f.store.ListEvents(context.Background(), jobID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestRemoteEarlierSubmittedAttemptFailsTheJobClosed(t *testing.T) {
	f := newRemoteFixture(t)
	job, lease := f.newJob()
	// A previous attempt (say, before a gateway restart) crossed the boundary and
	// its lease has lapsed. The input is the same, so the ledger's only objection
	// is the submission.
	spec, body, err := projectHelperJob(lease.envelope, "att_x", "pr_abc123")
	if err != nil {
		t.Fatal(err)
	}
	earlier, err := f.store.BeginAttempt(t.Context(), jobstore.BeginAttemptRequest{
		JobID: job.ID, AttemptID: "att_earlier", NodeID: "node_x", TTL: 30 * time.Millisecond,
		InputFingerprint: helperFingerprint(job.ID, spec.Target, spec.CommandType, body), WorkloadVersion: testWorkload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MarkSubmitted(t.Context(), earlier.Ref()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)

	if res := awaitRun(t, runOnceAsync(t.Context(), f.consumer(lease, nil))); res.err != nil {
		t.Fatal(res.err)
	}
	if !lease.failed || lease.retried || f.job(job.ID).Status != jobstore.StatusFailedTerminal {
		t.Fatalf("lease = %+v, job = %s", lease, f.job(job.ID).Status)
	}
	if data := terminalData(t, mustEvents(t, f, job.ID)); data["reconcile_required"] != true {
		t.Fatalf("terminal data = %#v", data)
	}
	if runs, _, _ := f.helper.counts(); runs != 0 {
		t.Fatal("a submitted job was run again on a helper")
	}
	f.assertPlacementsReleased()
}

func TestRemoteHelperRefusingAdmissionIsHeldBackAtOnce(t *testing.T) {
	f := newRemoteFixture(t)
	f.helper.runErr = status.Error(codes.Unavailable, "identity busy") // and InspectAttempt says: no such attempt
	job, lease := f.newJob()
	start := time.Now()
	if res := awaitRun(t, runOnceAsync(t.Context(), f.consumer(lease, nil))); res.err != nil {
		t.Fatal(res.err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %s: a refusal must not wait for the lease to lapse", elapsed)
	}
	if !lease.retried || lease.failed || f.localRuns.Load() != 0 {
		t.Fatalf("lease = %+v", lease)
	}
	if att := f.attempts(job.ID); len(att) != 1 || att[0].Submitted() {
		t.Fatalf("attempts = %+v", att)
	}
	f.assertPlacementsReleased()
}

// ---- output limits -----------------------------------------------------------

func TestRemoteOutputThatBreaksTheContractFailsTheAttemptNeverASuccess(t *testing.T) {
	oversize := `{"delta":{"text":"` + strings.Repeat("x", 2000) + `"}}`
	cases := []struct {
		name  string
		send  func(h *fakeHelper)
		class string
	}{
		{"over-limit event", func(h *fakeHelper) { h.send(evToken, oversize) }, "helper_output_limit"},
		{"an event type outside the allowlist", func(h *fakeHelper) { h.sendOutcome(helperv1.AttemptEventType(99), `{}`, nil) }, "helper_event_invalid"},
		{"a partial completed outcome", func(h *fakeHelper) {
			h.sendOutcome(evTerminal, "", &helperv1.AttemptOutcome{Status: stCompleted, Partial: true, ResultJson: `{"text":"cut"}`})
		}, "helper_event_invalid"},
	}
	for _, submitted := range []bool{false, true} {
		for _, tc := range cases {
			name := tc.name + " before submission"
			if submitted {
				name = tc.name + " after submission"
			}
			t.Run(name, func(t *testing.T) {
				f := newRemoteFixture(t, func(c *RemoteConfig) { c.Limits = HelperIngestLimits{MaxEventBytes: 1024} })
				job, lease := f.newJob()
				done := runOnceAsync(t.Context(), f.consumer(lease, nil))
				f.helper.waitRun(t, 1)
				f.helper.send(evStarted, `{}`)
				if submitted {
					f.helper.send(evSubmitted, `{}`)
				}
				tc.send(f.helper)
				if res := awaitRun(t, done); res.err != nil {
					t.Fatal(res.err)
				}

				if !lease.failed || lease.retried || lease.completed {
					t.Fatalf("lease = %+v, want failed", lease)
				}
				if f.job(job.ID).Status != jobstore.StatusFailedTerminal {
					t.Fatalf("job status = %s, want failed_terminal", f.job(job.ID).Status)
				}
				if slices.Contains(f.eventTypes(job.ID), "completed") {
					t.Fatal("rejected output became a completed event")
				}
				data := terminalData(t, mustEvents(t, f, job.ID))
				if data["error_class"] != tc.class || data["submitted"] != submitted || (data["reconcile_required"] == true) != submitted {
					t.Fatalf("terminal data = %#v, want %s, submitted=%v", data, tc.class, submitted)
				}
				if got := f.job(job.ID).Result; got != nil {
					t.Fatalf("a failed attempt left a result: %#v", got)
				}
				f.helper.waitCancels(t, 1) // the helper is told to stop
				f.helper.mu.Lock()
				reason := f.helper.cancels[0].GetReason()
				f.helper.mu.Unlock()
				if reason != "output_rejected" {
					t.Fatalf("cancel reason = %q", reason)
				}
				violations := 0
				for _, r := range f.auditRecords() {
					if r.Action == "helper.policy_violation" && r.Attributes["reason"] == tc.class {
						violations++
					}
				}
				if violations != 1 {
					t.Fatalf("policy violation audit records = %d", violations)
				}
				f.assertPlacementsReleased()
			})
		}
	}
}

// ---- manager down ------------------------------------------------------------

func TestRemoteManagerDownKeepsInFlightAttemptsAndGrantsNothingNew(t *testing.T) {
	f := newRemoteFixture(t, fastLease)
	job1, lease1 := f.newJob()
	done1 := runOnceAsync(t.Context(), f.consumer(lease1, nil))
	f.helper.waitRun(t, 1)
	f.helper.send(evStarted, `{}`)
	f.waitEvent(job1.ID, "running")

	// The fleet manager becomes unreachable: the picker grants nothing new.
	f.picker.setErr(ErrNoHelper)
	job2, lease2 := f.newJob()
	if res := awaitRun(t, runOnceAsync(t.Context(), f.consumer(lease2, nil))); res.err != nil {
		t.Fatal(res.err)
	}
	if f.localRuns.Load() != 1 || !lease2.completed || len(f.attempts(job2.ID)) != 0 {
		t.Fatalf("the new job must run locally and unfenced: local runs %d, lease %+v", f.localRuns.Load(), lease2)
	}

	// The attempt already granted is untouched: it keeps renewing and finishes.
	_, renews, _ := f.helper.counts()
	f.helper.waitRenews(t, renews+3)
	select {
	case res := <-done1:
		t.Fatalf("the in-flight attempt ended while the manager was down: %+v", res)
	default:
	}
	f.helper.sendCompleted()
	if res := awaitRun(t, done1); res.err != nil {
		t.Fatal(res.err)
	}
	if !lease1.completed || f.job(job1.ID).Status != jobstore.StatusCompleted || f.localRuns.Load() != 1 {
		t.Fatalf("in-flight job: lease %+v, status %s", lease1, f.job(job1.ID).Status)
	}
	f.assertPlacementsReleased()
}

// ---- stopping ---------------------------------------------------------------

func TestRemoteShutdownStopsTheHelperAndNeverLeavesASubmittedJobReplayable(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		name := "before submission"
		if submitted {
			name = "after submission"
		}
		t.Run(name, func(t *testing.T) {
			f := newRemoteFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			job, lease := f.newJob()
			done := runOnceAsync(ctx, f.consumer(lease, nil))
			f.helper.waitRun(t, 1)
			f.helper.send(evStarted, `{}`)
			if submitted {
				f.helper.send(evSubmitted, `{}`)
				f.waitEvent(job.ID, "prompt_submitted")
			}
			cancel() // gateway shutdown
			res := awaitRun(t, done)
			f.helper.waitCancels(t, 1)

			if submitted {
				if res.err != nil || !lease.failed || lease.retried || f.job(job.ID).Status != jobstore.StatusFailedTerminal {
					t.Fatalf("res = %+v, lease = %+v, job = %s", res, lease, f.job(job.ID).Status)
				}
				if data := terminalData(t, mustEvents(t, f, job.ID)); data["reconcile_required"] != true {
					t.Fatalf("terminal data = %#v", data)
				}
			} else if !errors.Is(res.err, context.Canceled) || !lease.retried || lease.failed || jobstore.TerminalStatus(f.job(job.ID).Status) {
				t.Fatalf("res = %+v, lease = %+v, job = %s", res, lease, f.job(job.ID).Status)
			}
			f.assertPlacementsReleased()
		})
	}
}

func TestRemoteJobEndedUnderTheAttemptStopsTheHelper(t *testing.T) {
	f := newRemoteFixture(t)
	job, lease := f.newJob()
	done := runOnceAsync(t.Context(), f.consumer(lease, nil))
	f.helper.waitRun(t, 1)
	f.helper.send(evStarted, `{}`)
	f.waitEvent(job.ID, "running")

	// A cancel written by another gateway process: the store says canceled, the
	// helper does not know yet and sends its next event.
	if _, _, err := f.store.UpdateStatus(t.Context(), job.ID, jobstore.StatusCanceled); err != nil {
		t.Fatal(err)
	}
	f.helper.send(evToken, `{"delta":{"text":"x"}}`)
	if res := awaitRun(t, done); res.err != nil {
		t.Fatal(res.err)
	}
	f.helper.waitCancels(t, 1)
	if !lease.cancelled || lease.failed || lease.retried || f.job(job.ID).Status != jobstore.StatusCanceled {
		t.Fatalf("lease = %+v, job = %s", lease, f.job(job.ID).Status)
	}
}

func TestRemoteCancelAPIStopsTheHelperThroughTheInProcessRegistry(t *testing.T) {
	f := newRemoteFixture(t)
	reg := NewCancelRegistry()
	job, lease := f.newJob()
	done := runOnceAsync(t.Context(), f.consumer(lease, reg))
	f.helper.waitRun(t, 1)
	f.helper.send(evStarted, `{}`)
	waitFor(t, 5*time.Second, "the supervisor to watch the job", func() bool { return reg.Watching() == 1 })

	// The cancel API: CancelJob first (the hint), the canceled status just after.
	start := time.Now()
	api := NewCancelNotifier(NewNoopDispatcher(), reg)
	if err := api.CancelJob(t.Context(), job, "caller_cancelled"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, _, err := f.store.UpdateStatus(t.Context(), job.ID, jobstore.StatusCanceled); err != nil {
		t.Fatal(err)
	}
	f.helper.waitCancels(t, 1)
	// The safety-net store read for a remote attempt is 2 s: this was the hint.
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the helper was stopped after %s, want well under the 2 s safety net", d)
	}
	if res := awaitRun(t, done); res.err != nil {
		t.Fatal(res.err)
	}
	f.helper.mu.Lock()
	reason := f.helper.cancels[0].GetReason()
	f.helper.mu.Unlock()
	if !lease.cancelled || lease.failed || lease.retried || reason != "cancelled" {
		t.Fatalf("lease = %+v, cancel reason = %q", lease, reason)
	}
	if reg.Watching() != 0 {
		t.Fatal("the watcher registration leaked")
	}
}

// ---- configuration -----------------------------------------------------------

func TestRemoteConfigValidationAndDefaults(t *testing.T) {
	good := func() RemoteConfig {
		f := newRemoteFixture(t)
		return RemoteConfig{
			Store: f.store, Picker: f.picker, WorkloadVersion: "w1", RegistryDigest: testDigest,
			Dialer: HelperDialFunc(func(context.Context, HelperPlacement) (HelperConn, error) { return f.helper, nil }),
		}
	}
	runner, err := NewRemoteWorkerRunner(good())
	if err != nil {
		t.Fatal(err)
	}
	if c := runner.cfg; c.LeaseTTL != 120*time.Second || c.RenewEvery != 20*time.Second || c.MaxClockSkew != 10*time.Second ||
		c.MaxRuntime != defaultWorkerMaxRuntime {
		t.Fatalf("defaults = %+v", c)
	}
	bad := map[string]func(*RemoteConfig){
		"no store":             func(c *RemoteConfig) { c.Store = nil },
		"no picker":            func(c *RemoteConfig) { c.Picker = nil },
		"no dialer":            func(c *RemoteConfig) { c.Dialer = nil },
		"no workload version":  func(c *RemoteConfig) { c.WorkloadVersion = "" },
		"no registry digest":   func(c *RemoteConfig) { c.RegistryDigest = "" },
		"renewal too slow":     func(c *RemoteConfig) { c.RenewEvery = 41 * time.Second },
		"skew eats the lease":  func(c *RemoteConfig) { c.MaxClockSkew = 60 * time.Second },
		"lease beyond the cap": func(c *RemoteConfig) { c.LeaseTTL = time.Hour },
	}
	for name, mut := range bad {
		t.Run(name, func(t *testing.T) {
			c := good()
			mut(&c)
			if _, err := NewRemoteWorkerRunner(c); err == nil {
				t.Fatal("config accepted")
			}
		})
	}
}

func TestRemoteFingerprintBindsTheInputAndIsCanonical(t *testing.T) {
	a := helperJobBody{inputJSON: `{"a":1}`, optionsJSON: ""}
	if helperFingerprint("job_1", "mock", "submit", a) != helperFingerprint("job_1", "mock", "submit", a) {
		t.Fatal("not deterministic")
	}
	for name, fp := range map[string]string{
		"job":     helperFingerprint("job_2", "mock", "submit", a),
		"target":  helperFingerprint("job_1", "other", "submit", a),
		"command": helperFingerprint("job_1", "mock", "other", a),
		"input":   helperFingerprint("job_1", "mock", "submit", helperJobBody{inputJSON: `{"a":2}`}),
		"options": helperFingerprint("job_1", "mock", "submit", helperJobBody{inputJSON: `{"a":1}`, optionsJSON: `{"b":1}`}),
		// a boundary shift between fields must not collide
		"shift": helperFingerprint("job_1", "mocks", "ubmit", a),
	} {
		if fp == helperFingerprint("job_1", "mock", "submit", a) {
			t.Fatalf("fingerprint ignores the %s", name)
		}
	}
}
