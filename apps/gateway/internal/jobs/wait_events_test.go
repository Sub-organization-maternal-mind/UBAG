package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// Characterization tests for Store.WaitEvents. They pin today's behaviour on
// every store implementation (memory and sqlite here; postgres in
// postgres_wait_events_test.go) so the later streaming-ingestion work, which
// changes who writes events and when, cannot silently change what waiters see.
// Zero behaviour change: these tests only describe the current contract.

var waitEventsJobSeq atomic.Int64

func waitEventsTestJob(t *testing.T, store Store, tenantID string) Job {
	t.Helper()
	n := waitEventsJobSeq.Add(1)
	job, err := store.Create(context.Background(), CreateRequest{
		APIVersion:     "2026-05-22",
		TenantID:       tenantID,
		AppID:          "app_wait_events",
		IdempotencyKey: fmt.Sprintf("idem_wait_events_%d_%d", time.Now().UnixNano(), n),
		Target:         "mock",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		TraceID:        fmt.Sprintf("trace_wait_events_%d", n),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	return job
}

func applyWaitEventsWorkerEvent(t *testing.T, store Store, job Job, eventID, eventType string, sequence int, data map[string]any) {
	t.Helper()
	if _, found, err := store.ApplyWorkerEvent(context.Background(), WorkerEvent{
		EventID: eventID, JobID: job.ID, APIVersion: job.APIVersion, Type: eventType,
		Sequence: sequence, TraceID: job.TraceID, Data: data,
	}); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent %s found=%v err=%v", eventID, found, err)
	}
}

func tokenData(text string) map[string]any {
	return map[string]any{"status": "token_streaming", "delta": map[string]any{"text": text}}
}

type waitResult struct {
	events []Event
	found  bool
	err    error
}

func startWait(ctx context.Context, store Store, jobID string, after, limit int) <-chan waitResult {
	out := make(chan waitResult, 1)
	go func() {
		events, found, err := store.WaitEvents(ctx, jobID, after, limit)
		out <- waitResult{events, found, err}
	}()
	return out
}

// waitEventsContract is the shared WaitEvents characterization suite.
func waitEventsContract(t *testing.T, store Store, tenantID string) {
	t.Helper()
	bg := context.Background()

	t.Run("returns existing events immediately without blocking", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		ctx, cancel := context.WithTimeout(bg, 2*time.Second)
		defer cancel()
		events, found, err := store.WaitEvents(ctx, job.ID, 0, 10)
		if err != nil || !found {
			t.Fatalf("WaitEvents found=%v err=%v", found, err)
		}
		if len(events) != 1 || events[0].Type != "queued" || events[0].Sequence != 1 {
			t.Fatalf("events = %#v, want the single queued event at sequence 1", events)
		}
	})

	t.Run("missing job returns nil false nil without blocking", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(bg, 2*time.Second)
		defer cancel()
		events, found, err := store.WaitEvents(ctx, "job_does_not_exist_wait_events", 0, 10)
		if events != nil || found || err != nil {
			t.Fatalf("WaitEvents = (%#v, %v, %v), want (nil, false, nil)", events, found, err)
		}
	})

	t.Run("blocks until a new event lands then returns it", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		ctx, cancel := context.WithTimeout(bg, 10*time.Second)
		defer cancel()
		result := startWait(ctx, store, job.ID, 1, 10)
		select {
		case r := <-result:
			t.Fatalf("WaitEvents returned before any new event: %#v", r)
		case <-time.After(150 * time.Millisecond):
		}
		applyWaitEventsWorkerEvent(t, store, job, "wait_evt_running_"+job.ID, "running", 2, map[string]any{"status": "running"})
		select {
		case r := <-result:
			if r.err != nil || !r.found || len(r.events) != 1 || r.events[0].Type != "running" || r.events[0].Sequence != 2 {
				t.Fatalf("WaitEvents = %#v, want the running event at sequence 2", r)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("WaitEvents did not wake for a new event")
		}
	})

	t.Run("context cancel returns nil true context.Canceled", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		ctx, cancel := context.WithCancel(bg)
		result := startWait(ctx, store, job.ID, 1, 10)
		time.Sleep(100 * time.Millisecond)
		cancel()
		select {
		case r := <-result:
			if r.events != nil || !r.found || !errors.Is(r.err, context.Canceled) {
				t.Fatalf("WaitEvents = %#v, want (nil, true, context.Canceled)", r)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("WaitEvents did not return after cancel")
		}
	})

	t.Run("context deadline returns nil true DeadlineExceeded", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		ctx, cancel := context.WithTimeout(bg, 150*time.Millisecond)
		defer cancel()
		events, found, err := store.WaitEvents(ctx, job.ID, 1, 10)
		if events != nil || !found || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("WaitEvents = (%#v, %v, %v), want (nil, true, DeadlineExceeded)", events, found, err)
		}
	})

	t.Run("honors limit and ascending sequence order", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		applyWaitEventsWorkerEvent(t, store, job, "wait_evt_lim_run_"+job.ID, "running", 2, map[string]any{"status": "running"})
		applyWaitEventsWorkerEvent(t, store, job, "wait_evt_lim_t1_"+job.ID, "token", 3, tokenData("a"))
		applyWaitEventsWorkerEvent(t, store, job, "wait_evt_lim_t2_"+job.ID, "token", 4, tokenData("b"))

		all, _, err := store.ListEvents(bg, job.ID, 0, 100)
		if err != nil || len(all) != 4 {
			t.Fatalf("ListEvents len=%d err=%v, want 4", len(all), err)
		}

		first, found, err := store.WaitEvents(bg, job.ID, 0, 2)
		if err != nil || !found || len(first) != 2 || first[0].ID != all[0].ID || first[1].ID != all[1].ID {
			t.Fatalf("limit 2 from 0 = %#v err=%v, want first two events in order", first, err)
		}
		rest, _, err := store.WaitEvents(bg, job.ID, first[1].Sequence, 10)
		if err != nil || len(rest) != 2 || rest[0].ID != all[2].ID || rest[1].ID != all[3].ID {
			t.Fatalf("after %d = %#v err=%v, want the remaining two events in order", first[1].Sequence, rest, err)
		}
		for i := 1; i < len(all); i++ {
			if all[i].Sequence <= all[i-1].Sequence {
				t.Fatalf("sequences not strictly ascending: %#v", all)
			}
		}
		// A non-positive limit falls back to the default page, not zero rows.
		defaulted, _, err := store.WaitEvents(bg, job.ID, 0, 0)
		if err != nil || len(defaulted) != 4 {
			t.Fatalf("limit 0 returned %d events err=%v, want all 4 via the default limit", len(defaulted), err)
		}
	})

	t.Run("duplicate worker event adds no event and does not wake waiters", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		applyWaitEventsWorkerEvent(t, store, job, "wait_evt_dup_"+job.ID, "running", 2, map[string]any{"status": "running"})
		before, _, err := store.ListEvents(bg, job.ID, 0, 100)
		if err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
		last := before[len(before)-1].Sequence

		applyWaitEventsWorkerEvent(t, store, job, "wait_evt_dup_"+job.ID, "running", 2, map[string]any{"status": "running"})
		after, _, err := store.ListEvents(bg, job.ID, 0, 100)
		if err != nil || len(after) != len(before) {
			t.Fatalf("duplicate changed event count %d -> %d err=%v", len(before), len(after), err)
		}
		ctx, cancel := context.WithTimeout(bg, 200*time.Millisecond)
		defer cancel()
		events, found, err := store.WaitEvents(ctx, job.ID, last, 10)
		if events != nil || !found || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("WaitEvents after duplicate = (%#v, %v, %v), want (nil, true, DeadlineExceeded)", events, found, err)
		}
	})

	t.Run("terminal event is delivered but the store never closes the wait", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		applyWaitEventsWorkerEvent(t, store, job, "wait_evt_done_"+job.ID, "completed", 2, map[string]any{
			"status": "completed", "result": map[string]any{"type": "text", "text": "done"},
		})
		events, found, err := store.WaitEvents(bg, job.ID, 1, 10)
		if err != nil || !found || len(events) != 1 || events[0].Type != "completed" {
			t.Fatalf("WaitEvents = (%#v, %v, %v), want the completed event", events, found, err)
		}
		// Terminal-ness is a caller concern (SSE/gRPC decide); past the terminal
		// event the store simply waits for more events until ctx ends.
		ctx, cancel := context.WithTimeout(bg, 200*time.Millisecond)
		defer cancel()
		events, found, err = store.WaitEvents(ctx, job.ID, events[0].Sequence, 10)
		if events != nil || !found || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("WaitEvents past terminal = (%#v, %v, %v), want (nil, true, DeadlineExceeded)", events, found, err)
		}
	})
}

func TestMemoryStoreWaitEventsContract(t *testing.T) {
	waitEventsContract(t, NewMemoryStore(), "tenant_wait_events")
}

func TestSQLiteStoreWaitEventsContract(t *testing.T) {
	waitEventsContract(t, newScheduledTestSQLiteStore(t), "tenant_wait_events")
}
