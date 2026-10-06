package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/helpermetrics"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

const (
	evStarted   = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_STARTED
	evSubmitted = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_PROMPT_SUBMITTED
	evToken     = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TOKEN
	evManual    = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_MANUAL_ACTION_REQUIRED
	evTerminal  = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL

	stCompleted = helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED
	stFailed    = helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED
	stTimedOut  = helperv1.AttemptStatus_ATTEMPT_STATUS_TIMED_OUT
	stCancelled = helperv1.AttemptStatus_ATTEMPT_STATUS_CANCELLED
)

type ingestFixture struct {
	store *jobstore.MemoryStore
	audit *audit.MemoryStore
	job   jobstore.Job
	att   jobstore.Attempt
}

// newIngestFixture creates a tenant_a job and leases attempt att_one
// (generation 1, node_a) for the given TTL (0 = default 120 s).
func newIngestFixture(t *testing.T, ttl time.Duration) *ingestFixture {
	t.Helper()
	store := jobstore.NewMemoryStore()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", Target: "mock",
		CommandType: "submit", Input: map[string]any{"prompt": "hello"}, TraceID: "trace_helper_ingest",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f := &ingestFixture{store: store, audit: audit.NewMemoryStore(), job: job}
	f.att = f.begin(t, "att_one", 0, ttl)
	return f
}

func (f *ingestFixture) begin(t *testing.T, id string, expected uint64, ttl time.Duration) jobstore.Attempt {
	t.Helper()
	a, err := f.store.BeginAttempt(context.Background(), jobstore.BeginAttemptRequest{
		JobID: f.job.ID, AttemptID: id, NodeID: "node_a", ExpectedGeneration: expected,
		TTL: ttl, InputFingerprint: "fp1", WorkloadVersion: "w1",
	})
	if err != nil {
		t.Fatalf("BeginAttempt %s: %v", id, err)
	}
	return a
}

func (f *ingestFixture) binding() HelperIngestBinding {
	return HelperIngestBinding{TenantID: "tenant_a", AppID: "app_a", JobID: f.job.ID, AttemptID: "att_one", Generation: 1, NodeID: "node_a"}
}

func (f *ingestFixture) config(limits HelperIngestLimits) HelperIngestConfig {
	return HelperIngestConfig{Store: f.store, Audit: f.audit, Limits: limits}
}

func (f *ingestFixture) open(t *testing.T) *HelperIngest {
	t.Helper()
	return f.openWith(t, HelperIngestLimits{}, f.binding())
}

func (f *ingestFixture) openWith(t *testing.T, limits HelperIngestLimits, b HelperIngestBinding) *HelperIngest {
	t.Helper()
	g, err := OpenHelperIngest(context.Background(), f.config(limits), b)
	if err != nil {
		t.Fatalf("OpenHelperIngest: %v", err)
	}
	return g
}

func (f *ingestFixture) supersede(t *testing.T) {
	t.Helper()
	time.Sleep(60 * time.Millisecond) // the 20 ms lease of att_one lapses
	f.begin(t, "att_two", 1, 0)
}

func (f *ingestFixture) job2(t *testing.T) jobstore.Job {
	t.Helper()
	job, found, err := f.store.Get(context.Background(), f.job.ID)
	if err != nil || !found {
		t.Fatalf("Get found=%v err=%v", found, err)
	}
	return job
}

func (f *ingestFixture) events(t *testing.T) []jobstore.Event {
	t.Helper()
	events, _, err := f.store.ListEvents(context.Background(), f.job.ID, 0, 5000)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return events
}

func (f *ingestFixture) lastData(t *testing.T) map[string]any {
	t.Helper()
	events := f.events(t)
	return events[len(events)-1].Data
}

func (f *ingestFixture) auditRecords(t *testing.T, tenant string) []audit.Record {
	t.Helper()
	records, err := f.audit.List(context.Background(), audit.Filter{TenantID: tenant})
	if err != nil {
		t.Fatalf("audit List: %v", err)
	}
	return records
}

func (f *ingestFixture) attempt(t *testing.T, id string) jobstore.Attempt {
	t.Helper()
	list, err := f.store.ListAttempts(context.Background(), f.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.AttemptID == id {
			return a
		}
	}
	t.Fatalf("attempt %s not found", id)
	return jobstore.Attempt{}
}

func hev(seq uint64, typ helperv1.AttemptEventType, data string) *helperv1.AttemptEvent {
	return &helperv1.AttemptEvent{AttemptId: "att_one", Sequence: seq, LeaseGeneration: 1, Type: typ, DataJson: data}
}

func hterm(seq uint64, out *helperv1.AttemptOutcome) *helperv1.AttemptEvent {
	ev := hev(seq, evTerminal, "")
	ev.Outcome = out
	return ev
}

func completedOutcome() *helperv1.AttemptOutcome {
	return &helperv1.AttemptOutcome{Status: stCompleted, Submitted: true, ResultJson: `{"text":"hello"}`}
}

func accept(t *testing.T, g *HelperIngest, evs ...*helperv1.AttemptEvent) jobstore.Job {
	t.Helper()
	var job jobstore.Job
	for _, ev := range evs {
		var err error
		if job, err = g.Accept(context.Background(), ev); err != nil {
			t.Fatalf("Accept seq %d: %v", ev.GetSequence(), err)
		}
	}
	return job
}

func wantIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

func TestHelperIngestCommitsAnAttemptEndToEnd(t *testing.T) {
	f := newIngestFixture(t, 0)
	g := f.open(t)
	job := accept(t, g,
		hev(1, evStarted, `{"status":"running"}`),
		hev(2, evSubmitted, ``),
		hev(3, evToken, `{"delta":{"text":"hi"}}`),
		hterm(4, completedOutcome()),
	)
	if job.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %s", job.Status)
	}
	result, _ := job.Result.(map[string]any)
	if output, _ := result["output"].(map[string]any); output["text"] != "hello" {
		t.Fatalf("result = %#v", job.Result)
	}
	if !g.Done() {
		t.Fatal("session not done after the terminal event")
	}
	a := f.attempt(t, "att_one")
	if a.State != jobstore.AttemptFinished || !a.Submitted() {
		t.Fatalf("ledger row = %#v, want finished and submitted (MarkSubmitted ran at prompt_submitted)", a)
	}
	for _, event := range f.events(t) {
		meta, _ := event.Data["worker_event"].(map[string]any)
		if meta == nil {
			continue // the job's own queued event
		}
		if _, leaked := event.Data["helper"]; leaked || strings.Contains(fmt.Sprint(event.Data), "node_a") {
			t.Fatalf("event %s leaks the fleet node: %#v", event.Type, event.Data)
		}
		if event.Data["attempt_id"] != "att_one" || !strings.HasPrefix(meta["event_id"].(string), "att_one:") {
			t.Fatalf("event %s lacks node-namespaced provenance: %#v", event.Type, event.Data)
		}
	}
	_, err := g.Accept(context.Background(), hev(5, evToken, `{}`))
	wantIs(t, err, ErrHelperIngestClosed)
}

func TestHelperIngestStaleGenerationIsRejectedAndAudited(t *testing.T) {
	f := newIngestFixture(t, 20*time.Millisecond)
	g := f.open(t)
	accept(t, g, hev(1, evStarted, `{}`))
	f.supersede(t)

	before, eventsBefore := f.job2(t), len(f.events(t))
	_, err := g.Accept(context.Background(), hterm(2, completedOutcome()))
	wantIs(t, err, jobstore.ErrAttemptFenced)
	if !strings.Contains(err.Error(), HelperFencedErrorCode) {
		t.Fatalf("err = %v, want the %s code", err, HelperFencedErrorCode)
	}
	if after := f.job2(t); !reflect.DeepEqual(before, after) || len(f.events(t)) != eventsBefore {
		t.Fatalf("a fenced commit touched the job:\nbefore %#v\nafter  %#v", before, after)
	}
	if before.Status == jobstore.StatusCompleted || before.Result != nil {
		t.Fatalf("job = %s result=%v", before.Status, before.Result)
	}
	if !g.Done() {
		t.Fatal("a fenced writer's session must end")
	}
	_, err = g.Accept(context.Background(), hev(3, evToken, `{}`))
	wantIs(t, err, ErrHelperIngestClosed)

	records := f.auditRecords(t, "tenant_a")
	if len(records) != 1 || records[0].Action != "attempt.fenced_rejected" || records[0].Outcome != "denied" ||
		records[0].Actor != "node:node_a" || records[0].Attributes["error_code"] != HelperFencedErrorCode {
		t.Fatalf("audit = %#v, want one attempt.fenced_rejected record citing %s", records, HelperFencedErrorCode)
	}

	// A new session for the superseded lease cannot even open.
	_, err = OpenHelperIngest(context.Background(), f.config(HelperIngestLimits{}), f.binding())
	wantIs(t, err, jobstore.ErrAttemptFenced)
	// The successor's own writer is unaffected.
	b := f.binding()
	b.AttemptID, b.Generation = "att_two", 2
	g2 := f.openWith(t, HelperIngestLimits{}, b)
	if _, err := g2.Accept(context.Background(), &helperv1.AttemptEvent{AttemptId: "att_two", Sequence: 1, LeaseGeneration: 2, Type: evStarted}); err != nil {
		t.Fatalf("successor: %v", err)
	}
}

func TestHelperIngestEventLevelFencing(t *testing.T) {
	f := newIngestFixture(t, 0)
	g := f.open(t)
	before, eventsBefore := f.job2(t), len(f.events(t))

	wrongGen := hev(1, evStarted, `{}`)
	wrongGen.LeaseGeneration = 2
	_, err := g.Accept(context.Background(), wrongGen)
	wantIs(t, err, jobstore.ErrAttemptStale)
	wrongAttempt := hev(1, evStarted, `{}`)
	wrongAttempt.AttemptId = "att_other"
	_, err = g.Accept(context.Background(), wrongAttempt)
	wantIs(t, err, jobstore.ErrAttemptFenced)

	if after := f.job2(t); !reflect.DeepEqual(before, after) || len(f.events(t)) != eventsBefore {
		t.Fatal("a mismatching event touched the job")
	}
	if g.Done() {
		t.Fatal("a mismatching event must not end the legitimate session")
	}
	// One audit record per distinct reason, not per event.
	for range 5 {
		_, _ = g.Accept(context.Background(), wrongGen)
	}
	if records := f.auditRecords(t, "tenant_a"); len(records) != 2 {
		t.Fatalf("audit records = %d, want 2 (one per reason)", len(records))
	}
	accept(t, g, hev(1, evStarted, `{}`))
}

func TestHelperIngestRejectsCrossTenantAndForeignNode(t *testing.T) {
	f := newIngestFixture(t, 0)
	before, eventsBefore := f.job2(t), len(f.events(t))

	cases := map[string]func(*HelperIngestBinding){
		"another tenant's job id": func(b *HelperIngestBinding) { b.TenantID = "tenant_b" },
		"another app":             func(b *HelperIngestBinding) { b.AppID = "app_b" },
		"a job that does not exist": func(b *HelperIngestBinding) {
			b.JobID = "job_missing"
		},
		"a node that does not own the attempt": func(b *HelperIngestBinding) { b.NodeID = "node_b" },
		"an attempt the ledger never granted":  func(b *HelperIngestBinding) { b.AttemptID = "att_unknown" },
		"a different input fingerprint":        func(b *HelperIngestBinding) { b.InputFingerprint = "fp_other" },
		"a different workload version":         func(b *HelperIngestBinding) { b.WorkloadVersion = "w_other" },
	}
	for name, mutate := range cases {
		b := f.binding()
		mutate(&b)
		_, err := OpenHelperIngest(context.Background(), f.config(HelperIngestLimits{}), b)
		if !errors.Is(err, ErrHelperIngestScope) {
			t.Errorf("%s: err = %v, want ErrHelperIngestScope", name, err)
		}
	}
	if after := f.job2(t); !reflect.DeepEqual(before, after) || len(f.events(t)) != eventsBefore {
		t.Fatal("a refused open touched the job")
	}
	// The cross-tenant claim is audited under the tenant the primary dispatched for.
	var found bool
	for _, r := range f.auditRecords(t, "tenant_b") {
		found = found || (r.Action == "helper.policy_violation" && r.Attributes["reason"] == "job_scope" && r.Actor == "node:node_a")
	}
	if !found {
		t.Fatal("cross-tenant claim was not audited as helper.policy_violation / job_scope")
	}
	if _, err := OpenHelperIngest(context.Background(), HelperIngestConfig{}, f.binding()); err == nil {
		t.Fatal("open without a store succeeded")
	}
	if _, err := OpenHelperIngest(context.Background(), f.config(HelperIngestLimits{}), HelperIngestBinding{}); !errors.Is(err, ErrHelperIngestScope) {
		t.Fatalf("empty binding: %v", err)
	}
}

func TestHelperIngestRejectsIdentityClaimsInsideData(t *testing.T) {
	f := newIngestFixture(t, 0)
	g := f.open(t)
	for _, claim := range []string{`{"tenant_id":"tenant_b"}`, `{"job_id":"job_other"}`, `{"node_id":"node_b"}`, `{"attempt_id":"att_other"}`, `{"app_id":"app_b"}`} {
		_, err := g.Accept(context.Background(), hev(1, evStarted, claim))
		wantIs(t, err, ErrHelperIngestScope)
	}
	if g.Done() || f.job2(t).Status != jobstore.StatusQueued {
		t.Fatal("an identity claim must reject the event, not fail the attempt")
	}
	// The bound value itself is harmless, and anything else the helper put under
	// the gateway's provenance keys is dropped.
	accept(t, g, hev(1, evStarted, `{"tenant_id":"tenant_a","helper":{"node_id":"node_b"}}`))
	data := f.lastData(t)
	if _, present := data["helper"]; present {
		t.Fatalf("helper-supplied provenance survived: %#v", data["helper"])
	}
	if _, present := data["tenant_id"]; present {
		t.Fatalf("identity key leaked into the event: %#v", data)
	}
}

func TestHelperIngestDuplicateCommitIsIdempotent(t *testing.T) {
	f := newIngestFixture(t, 0)
	steps := []*helperv1.AttemptEvent{
		hev(1, evStarted, `{}`), hev(2, evSubmitted, ``), hev(3, evToken, `{"delta":{"text":"hi"}}`),
	}
	g := f.open(t)
	accept(t, g, steps...)
	count := len(f.events(t))

	accept(t, g, steps[2], steps[1]) // in-session replays
	if got := len(f.events(t)); got != count {
		t.Fatalf("in-session replay changed the event log: %d -> %d", count, got)
	}

	// A reconnect after a gateway restart: a fresh session replays from 1.
	g2 := f.open(t)
	accept(t, g2, steps...)
	final := hterm(4, completedOutcome())
	job := accept(t, g2, final)
	if job.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %s", job.Status)
	}
	count = len(f.events(t))

	// Lost ack: the helper replays everything, terminal included.
	g3 := f.open(t)
	job = accept(t, g3, append(steps, final)...)
	if job.Status != jobstore.StatusCompleted || len(f.events(t)) != count {
		t.Fatalf("replay after completion: status=%s events %d -> %d", job.Status, count, len(f.events(t)))
	}
	// ResumeAfter lets a reconnect skip what it knows is committed.
	b := f.binding()
	b.ResumeAfter = 3
	if _, err := f.openWith(t, HelperIngestLimits{}, b).Accept(context.Background(), final); err != nil {
		t.Fatalf("resume: %v", err)
	}
}

func TestHelperIngestSequenceGap(t *testing.T) {
	f := newIngestFixture(t, 0)
	g := f.open(t)
	before := f.job2(t)
	for _, seq := range []uint64{0, 2} {
		_, err := g.Accept(context.Background(), hev(seq, evStarted, `{}`))
		wantIs(t, err, ErrHelperSequenceGap)
	}
	if after := f.job2(t); !reflect.DeepEqual(before, after) || g.Done() {
		t.Fatal("a sequence gap must reject without failing the attempt")
	}
	accept(t, g, hev(1, evStarted, `{}`), hev(2, evToken, `{}`))
	_, err := g.Accept(context.Background(), hev(4, evToken, `{}`))
	wantIs(t, err, ErrHelperSequenceGap)

	b := f.binding()
	b.ResumeAfter = 2
	g2 := f.openWith(t, HelperIngestLimits{}, b)
	if _, err := g2.Accept(context.Background(), hev(2, evToken, `{}`)); err != nil { // replay below the resume point
		t.Fatalf("replay: %v", err)
	}
	accept(t, g2, hev(3, evToken, `{}`))
}

// assertAttemptFailed checks that a content failure ended the job failed_terminal
// (never completed), left no result, and closed the session.
func assertAttemptFailed(t *testing.T, f *ingestFixture, g *HelperIngest, job jobstore.Job, err error, kind error, class string, reconcile bool) {
	t.Helper()
	wantIs(t, err, kind)
	if job.Status != jobstore.StatusFailedTerminal || job.Result != nil {
		t.Fatalf("job = %s result=%v, want failed_terminal with no result", job.Status, job.Result)
	}
	if got := f.job2(t); got.Status != jobstore.StatusFailedTerminal || got.Result != nil {
		t.Fatalf("stored job = %s result=%v", got.Status, got.Result)
	}
	data := f.lastData(t)
	if data["error_class"] != class || data["retryable"] != false || data["reconcile_required"] != reconcile || data["submitted"] != reconcile {
		t.Fatalf("failure event = %#v, want class %s reconcile_required=%v", data, class, reconcile)
	}
	if !g.Done() {
		t.Fatal("session not done after the attempt failed")
	}
	if a := f.attempt(t, "att_one"); a.State != jobstore.AttemptFinished {
		t.Fatalf("attempt = %s, want finished", a.State)
	}
	_, again := g.Accept(context.Background(), hev(99, evToken, `{}`))
	wantIs(t, again, ErrHelperIngestClosed)
	var violation bool
	for _, r := range f.auditRecords(t, "tenant_a") {
		violation = violation || (r.Action == "helper.policy_violation" && r.Attributes["reason"] == class)
	}
	if !violation {
		t.Fatalf("no helper.policy_violation / %s audit record", class)
	}
}

func TestHelperIngestOverLimitOutputFailsTheAttemptNeverTruncates(t *testing.T) {
	big := func(n int) string { return `{"delta":{"text":"` + strings.Repeat("x", n) + `"}}` }
	cases := []struct {
		name   string
		limits HelperIngestLimits
		drive  func(t *testing.T, g *HelperIngest) (jobstore.Job, error)
	}{
		{"one event over the per-event cap", HelperIngestLimits{MaxEventBytes: 256},
			func(t *testing.T, g *HelperIngest) (jobstore.Job, error) {
				return g.Accept(context.Background(), hev(1, evToken, big(400)))
			}},
		{"the per-event cap is clamped to the store's", HelperIngestLimits{MaxEventBytes: 10 << 20},
			func(t *testing.T, g *HelperIngest) (jobstore.Job, error) {
				return g.Accept(context.Background(), hev(1, evToken, big(DefaultHelperMaxEventBytes)))
			}},
		{"a completed result over the cap is not a truncated success", HelperIngestLimits{MaxEventBytes: 256},
			func(t *testing.T, g *HelperIngest) (jobstore.Job, error) {
				out := completedOutcome()
				out.ResultJson = `{"text":"` + strings.Repeat("y", 400) + `"}`
				return g.Accept(context.Background(), hterm(1, out))
			}},
		{"cumulative bytes over the attempt budget", HelperIngestLimits{MaxAttemptBytes: 300},
			func(t *testing.T, g *HelperIngest) (jobstore.Job, error) {
				accept(t, g, hev(1, evToken, big(150)))
				return g.Accept(context.Background(), hev(2, evToken, big(150)))
			}},
		{"more events than the attempt budget", HelperIngestLimits{MaxAttemptEvents: 2},
			func(t *testing.T, g *HelperIngest) (jobstore.Job, error) {
				accept(t, g, hev(1, evStarted, `{}`), hev(2, evToken, `{}`))
				return g.Accept(context.Background(), hev(3, evToken, `{}`))
			}},
	}
	for _, tc := range cases {
		for _, submitted := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				f := newIngestFixture(t, 0)
				g := f.openWith(t, tc.limits, f.binding())
				if submitted {
					// Mark the boundary through the ledger: the prompt left, so a
					// failure now needs reconciling (D4).
					if _, err := f.store.MarkSubmitted(context.Background(), f.att.Ref()); err != nil {
						t.Fatal(err)
					}
					g = f.openWith(t, tc.limits, f.binding())
				}
				job, err := tc.drive(t, g)
				assertAttemptFailed(t, f, g, job, err, ErrHelperOutputLimit, "helper_output_limit", submitted)
				for _, event := range f.events(t) {
					if event.Type == "completed" {
						t.Fatalf("an over-limit attempt produced a completed event: %#v", event.Data)
					}
				}
			})
		}
	}
}

func TestHelperIngestContractViolationsFailTheAttempt(t *testing.T) {
	cases := []struct {
		name string
		ev   *helperv1.AttemptEvent
	}{
		{"unspecified event type", hev(1, helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_UNSPECIFIED, `{}`)},
		{"unknown event type", hev(1, helperv1.AttemptEventType(99), `{}`)},
		{"data that is not JSON", hev(1, evToken, `{not json`)},
		{"data that is an array", hev(1, evToken, `[1,2]`)},
		{"an outcome on a non-terminal event", func() *helperv1.AttemptEvent {
			ev := hev(1, evToken, `{}`)
			ev.Outcome = completedOutcome()
			return ev
		}()},
		{"a terminal event without an outcome", hev(1, evTerminal, `{}`)},
		{"a partial outcome reported as completed", hterm(1, &helperv1.AttemptOutcome{Status: stCompleted, Partial: true, ResultJson: `{"text":"half"}`})},
		{"a completed outcome without a result", hterm(1, &helperv1.AttemptOutcome{Status: stCompleted})},
		{"a completed outcome with a non-object result", hterm(1, &helperv1.AttemptOutcome{Status: stCompleted, ResultJson: `"text"`})},
		{"a deadline cut that smuggles partial text into result", hterm(1, &helperv1.AttemptOutcome{Status: stTimedOut, Partial: true, ResultJson: `{"text":"half"}`})},
		{"an unspecified outcome status", hterm(1, &helperv1.AttemptOutcome{})},
		{"an error code that is not a token", hterm(1, &helperv1.AttemptOutcome{Status: stFailed, ErrorCode: "bad code with spaces"})},
		{"a secret-looking value", hev(1, evToken, `{"delta":{"text":"Bearer abcdefghijklmnopqrstuvwxyz"}}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIngestFixture(t, 0)
			g := f.open(t)
			job, err := g.Accept(context.Background(), tc.ev)
			assertAttemptFailed(t, f, g, job, err, ErrHelperEventInvalid, "helper_event_invalid", false)
		})
	}
}

func TestHelperIngestSanitizesSecretBearingData(t *testing.T) {
	f := newIngestFixture(t, 0)
	g := f.open(t)
	accept(t, g, hev(1, evToken, `{
		"delta": {"text": "hi"},
		"cookie": "sessionid=abc123", "api_key": "sk-live-AAAA",
		"nested": {"password": "hunter2", "note": "fine"},
		"novnc_url": "http://127.0.0.1:6080/vnc.html", "session_id": "sess_helper_1"
	}`))
	data := f.lastData(t)
	if data["cookie"] != "[redacted]" || data["api_key"] != "[redacted]" {
		t.Fatalf("secret keys were not redacted: %#v", data)
	}
	if nested, _ := data["nested"].(map[string]any); nested["password"] != "[redacted]" || nested["note"] != "fine" {
		t.Fatalf("nested = %#v", data["nested"])
	}
	for _, key := range []string{"novnc_url", "session_id"} {
		if _, present := data[key]; present {
			t.Fatalf("%s from a helper reached the job event: %#v", key, data)
		}
	}
	raw, _ := json.Marshal(f.events(t))
	for _, leak := range []string{"sessionid=abc123", "sk-live-AAAA", "hunter2", "127.0.0.1:6080", "sess_helper_1"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("%q reached the event log: %s", leak, raw)
		}
	}
}

func TestHelperIngestTerminalOutcomes(t *testing.T) {
	type want struct {
		status    jobstore.Status
		eventType string
		reconcile bool
	}
	cases := []struct {
		name  string
		steps []*helperv1.AttemptEvent
		want  want
	}{
		{"deadline cut ends timed_out, partial rides in data", []*helperv1.AttemptEvent{
			hev(1, evSubmitted, ``), hev(2, evToken, `{"delta":{"text":"a"}}`), hev(3, evToken, `{"delta":{"text":"b"}}`),
			hterm(4, &helperv1.AttemptOutcome{Status: stTimedOut, Partial: true, StreamEndReason: "deadline"}),
		}, want{jobstore.StatusTimedOut, "timed_out", false}},
		{"failure before submission is retryable", []*helperv1.AttemptEvent{
			hev(1, evStarted, `{}`), hterm(2, &helperv1.AttemptOutcome{Status: stFailed, ErrorCode: "UBAG-ADAPTER-EXTRACT-003"}),
		}, want{jobstore.StatusFailedRetryable, "failed", false}},
		{"failure after the boundary is never retryable even if the helper says submitted=false", []*helperv1.AttemptEvent{
			hev(1, evSubmitted, ``), hterm(2, &helperv1.AttemptOutcome{Status: stFailed, Submitted: false}),
		}, want{jobstore.StatusFailedTerminal, "failed_terminal", true}},
		{"a helper flagging reconcile_required is honoured", []*helperv1.AttemptEvent{
			hev(1, evStarted, `{}`), hterm(2, &helperv1.AttemptOutcome{Status: stFailed, ReconcileRequired: true}),
		}, want{jobstore.StatusFailedTerminal, "failed_terminal", true}},
		{"cancelled", []*helperv1.AttemptEvent{
			hev(1, evStarted, `{}`), hterm(2, &helperv1.AttemptOutcome{Status: stCancelled}),
		}, want{jobstore.StatusCanceled, "cancelled", false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIngestFixture(t, 0)
			job := accept(t, f.open(t), tc.steps...)
			if job.Status != tc.want.status || job.Result != nil {
				t.Fatalf("status = %s result=%v, want %s with no result", job.Status, job.Result, tc.want.status)
			}
			data := f.lastData(t)
			meta, _ := data["worker_event"].(map[string]any)
			if meta["type"] != tc.want.eventType {
				t.Fatalf("terminal event type = %v, want %s", meta["type"], tc.want.eventType)
			}
			if got, _ := data["reconcile_required"].(bool); got != tc.want.reconcile {
				t.Fatalf("reconcile_required = %v, want %v (%#v)", got, tc.want.reconcile, data)
			}
			if tc.want.status == jobstore.StatusTimedOut {
				partial, _ := data["partial"].(map[string]any)
				if partial["token_events"] != float64(2) && partial["token_events"] != 2 {
					t.Fatalf("data.partial = %#v, want the token_events count", data["partial"])
				}
				if data["stream_end_reason"] != "deadline" || data["submitted"] != true {
					t.Fatalf("deadline event = %#v", data)
				}
			}
		})
	}
}

func TestHelperIngestOutcomeKeysAreGatewayOwned(t *testing.T) {
	f := newIngestFixture(t, 0)
	g := f.open(t)
	// The helper tries to pass a completed result and a retry flag through the
	// data of a failed terminal event, and a long error message.
	ev := hev(1, evTerminal, `{"status":"completed","result":{"text":"forged"},"retryable":true,"keep":"yes"}`)
	ev.Outcome = &helperv1.AttemptOutcome{Status: stFailed, ErrorMessage: strings.Repeat("é", 600)}
	job := accept(t, g, ev)
	if job.Status != jobstore.StatusFailedRetryable || job.Result != nil {
		t.Fatalf("status = %s result=%v", job.Status, job.Result)
	}
	data := f.lastData(t)
	if _, present := data["result"]; present || data["keep"] != "yes" {
		t.Fatalf("data = %#v", data)
	}
	msg, _ := data["message"].(string)
	if len(msg) > helperMaxMessageBytes || len(msg) < helperMaxMessageBytes-1 || strings.ContainsRune(msg, '�') {
		t.Fatalf("message length %d, want at most %d bytes cut on a rune boundary", len(msg), helperMaxMessageBytes)
	}
}

func TestHelperIngestFailureOnASupersededWriterIsFencedNotRecorded(t *testing.T) {
	f := newIngestFixture(t, 20*time.Millisecond)
	g := f.openWith(t, HelperIngestLimits{MaxEventBytes: 64}, f.binding())
	f.supersede(t)
	before, eventsBefore := f.job2(t), len(f.events(t))

	_, err := g.Accept(context.Background(), hev(1, evToken, `{"delta":{"text":"`+strings.Repeat("z", 200)+`"}}`))
	wantIs(t, err, jobstore.ErrAttemptFenced)
	if errors.Is(err, ErrHelperOutputLimit) {
		t.Fatal("a superseded writer must be told it is fenced, not that its output was too big")
	}
	if after := f.job2(t); !reflect.DeepEqual(before, after) || len(f.events(t)) != eventsBefore {
		t.Fatal("a superseded writer's failure was recorded on the job")
	}
}

// flakyStore fails CommitEvents with a transient error a set number of times.
type flakyStore struct {
	*jobstore.MemoryStore
	failures int
}

func (s *flakyStore) CommitEvents(ctx context.Context, ref jobstore.AttemptRef, events []jobstore.WorkerEvent) (jobstore.Job, error) {
	if s.failures > 0 {
		s.failures--
		return jobstore.Job{}, errors.New("connection reset")
	}
	return s.MemoryStore.CommitEvents(ctx, ref, events)
}

func TestHelperIngestTransientStoreErrorIsRetryable(t *testing.T) {
	f := newIngestFixture(t, 0)
	flaky := &flakyStore{MemoryStore: f.store, failures: 1}
	g, err := OpenHelperIngest(context.Background(), HelperIngestConfig{Store: flaky, Audit: f.audit}, f.binding())
	if err != nil {
		t.Fatal(err)
	}
	ev := hev(1, evSubmitted, ``)
	_, err = g.Accept(context.Background(), ev)
	if err == nil || errors.Is(err, jobstore.ErrAttemptFenced) || errors.Is(err, ErrHelperEventInvalid) {
		t.Fatalf("err = %v, want a plain transient error", err)
	}
	if g.Done() || f.job2(t).Status != jobstore.StatusQueued {
		t.Fatal("a transient store error must not end or fail the attempt")
	}
	// The ledger already knows the prompt may have left (marked before the commit).
	if !f.attempt(t, "att_one").Submitted() {
		t.Fatal("submission boundary not recorded before the failed commit")
	}
	if _, err := g.Accept(context.Background(), ev); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := len(f.events(t)); n != 2 { // queued + prompt_submitted
		t.Fatalf("events = %d after retry, want 2", n)
	}
}

func TestHelperIngestSessionStaysUsableAfterAFailedCommitOfTheFailure(t *testing.T) {
	f := newIngestFixture(t, 0)
	flaky := &flakyStore{MemoryStore: f.store, failures: 1}
	g, err := OpenHelperIngest(context.Background(), HelperIngestConfig{Store: flaky, Audit: f.audit}, f.binding())
	if err != nil {
		t.Fatal(err)
	}
	bad := hev(1, helperv1.AttemptEventType(77), `{}`)
	_, err = g.Accept(context.Background(), bad)
	if err == nil || g.Done() {
		t.Fatalf("err=%v done=%v: the failure could not be recorded, the session must stay open", err, g.Done())
	}
	job, err := g.Accept(context.Background(), bad)
	wantIs(t, err, ErrHelperEventInvalid)
	if job.Status != jobstore.StatusFailedTerminal || !g.Done() {
		t.Fatalf("retry: status=%s done=%v", job.Status, g.Done())
	}
}

func TestHelperBindingFromFence(t *testing.T) {
	fence := &helperv1.Fence{JobId: "job_1", AttemptId: "att_one", NodeId: "node_a", LeaseGeneration: 3, InputFingerprint: "fp", WorkloadVersion: "w"}
	b, err := HelperBindingFromFence(fence, "tenant_a", "node_a")
	if err != nil || b.NodeID != "node_a" || b.Generation != 3 || b.JobID != "job_1" || b.TenantID != "tenant_a" || b.InputFingerprint != "fp" {
		t.Fatalf("binding = %#v err=%v", b, err)
	}
	for name, args := range map[string]struct {
		fence        *helperv1.Fence
		tenant, node string
	}{
		"nil fence":             {nil, "tenant_a", "node_a"},
		"fence names a node":    {fence, "tenant_a", "node_b"},
		"no authenticated node": {fence, "tenant_a", ""},
		"no tenant":             {fence, "", "node_a"},
		"fence without a node":  {&helperv1.Fence{JobId: "job_1", AttemptId: "att_one"}, "tenant_a", "node_a"},
	} {
		if _, err := HelperBindingFromFence(args.fence, args.tenant, args.node); !errors.Is(err, ErrHelperIngestScope) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestHelperIngestEventTypeAllowlistIsTheProtoMinusTerminal(t *testing.T) {
	// Every non-terminal proto type maps to a type the job store accepts; the
	// allowlist cannot drift from the proto or from the store vocabulary.
	for value, name := range helperv1.AttemptEventType_name {
		typ := helperv1.AttemptEventType(value)
		if typ == helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_UNSPECIFIED || typ == evTerminal {
			if _, listed := helperEventTypes[typ]; listed {
				t.Errorf("%s must not be in the allowlist", name)
			}
			continue
		}
		mapped, listed := helperEventTypes[typ]
		if !listed {
			t.Errorf("%s is not in the allowlist", name)
			continue
		}
		if err := jobstore.ValidateWorkerEventData(mapped, map[string]any{}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		f := newIngestFixture(t, 0)
		if _, _, err := f.store.ApplyWorkerEvent(context.Background(), jobstore.WorkerEvent{
			EventID: "probe", JobID: f.job.ID, APIVersion: f.job.APIVersion, Type: mapped, TraceID: f.job.TraceID,
		}); err != nil {
			t.Errorf("%s maps to %q which the store refuses: %v", name, mapped, err)
		}
	}
}

func TestHelperIngestFencedRejectIsCountedInMetrics(t *testing.T) {
	f := newIngestFixture(t, 20*time.Millisecond)
	g := f.open(t)
	accept(t, g, hev(1, evStarted, `{}`))
	f.supersede(t)
	// Sum the series: which fence reason fires (stale generation vs. commit
	// fence) is the ledger's business, the counter must move either way.
	total := func() (n int) {
		var b strings.Builder
		helpermetrics.Write(context.Background(), &b, nil, time.Now())
		for _, l := range strings.Split(b.String(), "\n") {
			if strings.HasPrefix(l, "ubag_helper_fenced_rejects_total{") {
				var v int
				_, _ = fmt.Sscanf(l[strings.LastIndex(l, " ")+1:], "%d", &v)
				n += v
			}
		}
		return n
	}
	before := total()
	_, err := g.Accept(context.Background(), hterm(2, completedOutcome()))
	wantIs(t, err, jobstore.ErrAttemptFenced)
	if after := total(); after != before+1 {
		t.Fatalf("fenced reject counter %d -> %d, want +1", before, after)
	}
}
