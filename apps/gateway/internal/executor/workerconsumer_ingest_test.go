package executor

import (
	"context"
	"strings"
	"testing"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// Characterization of the consumer's terminal-validation rules before any
// streaming-ingestion change (zero behaviour change). Today the consumer
// buffers the worker's whole event list and validates it BEFORE applying
// anything: no events, malformed JSONL, zero terminals, two terminals, an
// invalid event and a truncated stream must all end the job as
// failed_retryable (lease.Fail), never completed, and none of the worker's own
// events may have been applied to the store (only queued plus the gateway's
// synthesized failure event). Incremental ingestion removes that pre-apply
// gate, so these tests are the net that proves the end state is unchanged.

func ingestTestEvent(envelope DispatchEnvelope, id, eventType string, sequence int, data map[string]any) jobstore.WorkerEvent {
	return jobstore.WorkerEvent{
		EventID: id, JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: eventType,
		Sequence: sequence, TraceID: envelope.TraceID, Data: data,
	}
}

func runIngestConsumer(t *testing.T, runner WorkerRunFunc) (jobstore.Job, *fakeWorkerLease, []jobstore.Event) {
	t.Helper()
	return runIngestWith(t, runner)
}

func runIngestWith(t *testing.T, runner WorkerRunner) (jobstore.Job, *fakeWorkerLease, []jobstore.Event) {
	t.Helper()
	store := jobstore.NewMemoryStore()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", Target: "mock",
		CommandType: "submit", Input: map[string]any{"prompt": "hello"}, TraceID: "trace_ingest_characterization",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	lease := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_ingest", envelope: EnvelopeFromJob(job)}
	consumer := WorkerConsumer{Queue: fakeWorkerQueue{lease: lease}, Jobs: store, Runner: runner}
	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	final, found, err := store.Get(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("Get found=%v err=%v", found, err)
	}
	events, _, err := store.ListEvents(context.Background(), job.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return final, lease, events
}

func eventTypes(events []jobstore.Event) string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return strings.Join(types, ",")
}

func completedIngestData() map[string]any {
	return map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "hello"}}
}

func TestIngestTerminalValidationAlwaysFailsNeverCompletes(t *testing.T) {
	cases := []struct {
		name   string
		runner WorkerRunFunc
	}{
		{
			name:   "no events",
			runner: func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) { return nil, nil },
		},
		{
			name: "empty output parsed as JSONL",
			runner: func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
				return parseWorkerJSONL([]byte("\n\n"))
			},
		},
		{
			name: "malformed JSONL",
			runner: func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
				return parseWorkerJSONL([]byte("{not-json}\n"))
			},
		},
		{
			name: "zero terminal events",
			runner: func(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
				return []jobstore.WorkerEvent{
					ingestTestEvent(e, "ing_run", "running", 1, map[string]any{"status": "running"}),
					ingestTestEvent(e, "ing_tok", "token", 2, map[string]any{"status": "token_streaming", "delta": map[string]any{"text": "a"}}),
				}, nil
			},
		},
		{
			name: "two terminal events completed twice",
			runner: func(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
				return []jobstore.WorkerEvent{
					ingestTestEvent(e, "ing_done_1", "completed", 1, completedIngestData()),
					ingestTestEvent(e, "ing_done_2", "completed", 2, completedIngestData()),
				}, nil
			},
		},
		{
			name: "two terminal events completed then failed",
			runner: func(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
				return []jobstore.WorkerEvent{
					ingestTestEvent(e, "ing_done_a", "completed", 1, completedIngestData()),
					ingestTestEvent(e, "ing_fail_b", "failed", 2, map[string]any{"status": "failed_terminal"}),
				}, nil
			},
		},
		{
			name: "truncated stream cut after valid lines before any terminal",
			runner: func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
				return parseWorkerJSONL([]byte(
					`{"event_id":"ing_t1","type":"running","sequence":1,"data":{"status":"running"}}` + "\n"))
			},
		},
		{
			name: "truncated stream cut mid terminal line",
			runner: func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
				return parseWorkerJSONL([]byte(
					`{"event_id":"ing_t2","type":"running","sequence":1,"data":{"status":"running"}}` + "\n" +
						`{"event_id":"ing_t3","type":"completed","sequence":2,"data":{"status":"comple`))
			},
		},
		{
			name: "event without id or sequence",
			runner: func(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
				return []jobstore.WorkerEvent{ingestTestEvent(e, "", "completed", 0, completedIngestData())}, nil
			},
		},
		{
			name: "event for a different job",
			runner: func(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
				event := ingestTestEvent(e, "ing_other", "completed", 1, completedIngestData())
				event.JobID = "job_some_other_job"
				return []jobstore.WorkerEvent{event}, nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			final, lease, events := runIngestConsumer(t, tc.runner)
			if final.Status != jobstore.StatusFailedRetryable {
				t.Fatalf("status = %s, want %s", final.Status, jobstore.StatusFailedRetryable)
			}
			if final.Status == jobstore.StatusCompleted || final.Result != nil {
				t.Fatalf("job must never complete or carry a result: status=%s result=%#v", final.Status, final.Result)
			}
			if !lease.failed || lease.completed || lease.retried || lease.poisoned {
				t.Fatalf("lease outcome failed=%v completed=%v retried=%v poisoned=%v, want only Fail",
					lease.failed, lease.completed, lease.retried, lease.poisoned)
			}
			// Pre-apply gate: the worker's own events were not applied, so the
			// history is exactly queued + the consumer's own assignment + the
			// gateway-synthesized failure.
			if got := eventTypes(events); got != "queued,assigned,failed" {
				t.Fatalf("event history = %s, want queued,assigned,failed", got)
			}
			if class := events[2].Data["error_class"]; class != "worker_execution" {
				t.Fatalf("failure error_class = %v, want worker_execution", class)
			}
		})
	}
}

func TestIngestRunnerErrorFailsRetryableWithoutResult(t *testing.T) {
	final, lease, events := runIngestConsumer(t, func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
		return nil, context.DeadlineExceeded
	})
	if final.Status != jobstore.StatusFailedRetryable || final.Result != nil {
		t.Fatalf("status=%s result=%#v, want failed_retryable and no result", final.Status, final.Result)
	}
	if got := eventTypes(events); !lease.failed || got != "queued,assigned,failed" {
		t.Fatalf("lease.failed=%v history=%s, want Fail and queued,assigned,failed", lease.failed, got)
	}
}

func TestIngestExactlyOneTerminalCompletesAndAppliesAllEvents(t *testing.T) {
	// Positive control for the table above: one terminal event is accepted and
	// every worker event lands in the store in order.
	final, lease, events := runIngestConsumer(t, func(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
		return []jobstore.WorkerEvent{
			ingestTestEvent(e, "ing_ok_run", "running", 1, map[string]any{"status": "running"}),
			ingestTestEvent(e, "ing_ok_done", "completed", 2, completedIngestData()),
		}, nil
	})
	if final.Status != jobstore.StatusCompleted || final.Result == nil {
		t.Fatalf("status=%s result=%#v, want completed with a result", final.Status, final.Result)
	}
	if !lease.completed || lease.failed {
		t.Fatalf("lease completed=%v failed=%v, want Complete only", lease.completed, lease.failed)
	}
	if got := eventTypes(events); got != "queued,assigned,running,completed" {
		t.Fatalf("event history = %s, want queued,assigned,running,completed", got)
	}
}

func TestIngestParseWorkerJSONLTruncationAndBlankLines(t *testing.T) {
	complete := `{"event_id":"p1","type":"running","sequence":1,"data":{"status":"running"}}`
	events, err := parseWorkerJSONL([]byte("\n" + complete + "\n\n" + complete + "\n"))
	if err != nil || len(events) != 2 {
		t.Fatalf("blank lines: events=%d err=%v, want 2 events and no error", len(events), err)
	}
	// A final line cut mid-object (no trailing newline) is malformed, not silently dropped.
	if _, err := parseWorkerJSONL([]byte(complete + "\n" + complete[:len(complete)-8])); err == nil {
		t.Fatal("truncated final line was accepted")
	}
	if _, err := parseWorkerJSONL([]byte(`{"event_id":"p2","sequence":1}` + "\n")); err == nil {
		t.Fatal("event without type was accepted")
	}
	events, err = parseWorkerJSONL(nil)
	if err != nil || len(events) != 0 {
		t.Fatalf("empty output: events=%d err=%v, want 0 events and no error (the consumer fails the job)", len(events), err)
	}
}
