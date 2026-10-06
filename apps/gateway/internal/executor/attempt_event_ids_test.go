package executor

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// The attempt id stamped into events must match job-event.schema.json
// (^att_[A-Za-z0-9]+$) for every queue's lease id shape, and stay stable and
// distinct per lease.
func TestAttemptIDForLeaseMatchesSchemaPattern(t *testing.T) {
	pattern := regexp.MustCompile(`^att_[A-Za-z0-9]+$`)
	seen := map[string]bool{}
	for _, lease := range []string{"1759752000123456789", "stream:consumer:12:3", "nats", "lease_ingest"} {
		id := attemptIDForLease(lease)
		if !pattern.MatchString(id) || len(id) > 128 {
			t.Fatalf("attemptIDForLease(%q) = %q, not schema-conformant", lease, id)
		}
		if id != attemptIDForLease(lease) || seen[id] {
			t.Fatalf("attemptIDForLease(%q) = %q is unstable or collides", lease, id)
		}
		seen[id] = true
	}
}

// Helper-ingest terminal data must fit the closed job-event schema: stream_end_reason
// in the enum, partial limited to {text, token_events}.
func TestHelperIngestSchemaNormalizers(t *testing.T) {
	cases := []struct {
		reason string
		status helperv1.AttemptStatus
		want   string
	}{
		{"deadline", helperv1.AttemptStatus_ATTEMPT_STATUS_TIMED_OUT, "deadline"},
		{"shutdown", helperv1.AttemptStatus_ATTEMPT_STATUS_CANCELLED, "cancelled"},
		{"lease_expired", helperv1.AttemptStatus_ATTEMPT_STATUS_TIMED_OUT, "deadline"},
		{"manual_login_required", helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED, "error"},
	}
	for _, c := range cases {
		if got := schemaStreamEndReason(c.reason, c.status); got != c.want {
			t.Fatalf("schemaStreamEndReason(%q) = %q, want %q", c.reason, got, c.want)
		}
	}
	got := schemaPartial(map[string]any{"text": "abc", "token_events": float64(4), "truncated": true, "token_count": 9}, 2)
	if len(got) != 2 || got["text"] != "abc" || got["token_events"] != 4 {
		t.Fatalf("schemaPartial = %#v, want only text and token_events", got)
	}
	if got := schemaPartial(nil, 3); len(got) != 1 || got["token_events"] != 3 {
		t.Fatalf("schemaPartial(nil) = %#v, want the session token count", got)
	}
}

func TestAttemptEnvelopeOmittedWhenUnset(t *testing.T) {
	raw, _ := json.Marshal(DispatchEnvelope{})
	if strings.Contains(string(raw), `"attempt"`) {
		t.Fatalf("attempt must be omitted: %s", raw)
	}
}

func TestAttemptFlagAndFailureEventID(t *testing.T) {
	if attemptEventIDsEnabled() {
		t.Fatal("flag must default off")
	}
	t.Setenv("UBAG_WORKER_ATTEMPT_EVENT_IDS", "true")
	if !attemptEventIDsEnabled() {
		t.Fatal("flag should be on")
	}
	lease := &fakeWorkerLease{jobID: "job_1", leaseID: "l1"}
	legacy := failureEventID(lease, DispatchEnvelope{})
	scoped := failureEventID(lease, DispatchEnvelope{Attempt: &DispatchAttempt{ID: "a2"}})
	if legacy != "gateway_worker_failure:job_1:l1" || scoped == legacy {
		t.Fatalf("legacy=%q scoped=%q", legacy, scoped)
	}
}

func TestDropStaleAttemptEvents(t *testing.T) {
	env := DispatchEnvelope{Attempt: &DispatchAttempt{ID: "a2"}}
	events := []jobstore.WorkerEvent{
		{EventID: "1", Data: map[string]any{"attempt_id": "a1"}},
		{EventID: "2", Data: map[string]any{"attempt_id": "a2"}},
		{EventID: "3"},
	}
	if got := dropStaleAttemptEvents(env, events); len(got) != 2 || got[0].EventID != "2" {
		t.Fatalf("got %+v", got)
	}
	if got := dropStaleAttemptEvents(DispatchEnvelope{}, events); len(got) != 3 {
		t.Fatalf("no attempt must keep all, got %d", len(got))
	}
}

// Collision regression: same (job, seq) events from two attempts are both kept
// once ids are attempt-scoped; a same-attempt replay still dedupes.
func TestRetryAttemptEventsNotDeduped(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "t", AppID: "a", Target: "mock",
		CommandType: "submit", Input: map[string]any{"prompt": "x"}, TraceID: "trace_x",
	})
	if err != nil {
		t.Fatal(err)
	}
	apply := func(id string) {
		if _, _, err := store.ApplyWorkerEvent(context.Background(), jobstore.WorkerEvent{
			EventID: id, JobID: job.ID, APIVersion: job.APIVersion, Type: "running",
			TraceID: job.TraceID, Data: map[string]any{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	apply("evt_a1_2")
	apply("evt_a2_2")
	apply("evt_a2_2")
	events, _, _ := store.ListEvents(context.Background(), job.ID, 0, 100)
	n := 0
	for _, e := range events {
		if e.Type == "running" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want 2 progress events, got %d", n)
	}
}
