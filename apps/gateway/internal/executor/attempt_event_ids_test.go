package executor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

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
