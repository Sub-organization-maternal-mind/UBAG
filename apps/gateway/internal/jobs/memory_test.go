package jobs

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMemoryStoreAppliesWorkerEventsAndStoresNormalizedResult(t *testing.T) {
	store := NewMemoryStore()
	job := createWorkerEventTestJob(t, store, "trace_worker_result")

	events := []WorkerEvent{
		{EventID: "worker_evt_1", JobID: job.ID, APIVersion: job.APIVersion, Type: "running", Sequence: 2, TraceID: job.TraceID, Data: map[string]any{"status": "running"}},
		{EventID: "worker_evt_2", JobID: job.ID, APIVersion: job.APIVersion, Type: "token", Sequence: 3, TraceID: job.TraceID, Data: map[string]any{"status": "token_streaming", "delta": map[string]any{"text": "hello"}}},
		{EventID: "worker_evt_3", JobID: job.ID, APIVersion: job.APIVersion, Type: "completed", Sequence: 4, TraceID: job.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "hello world"}}},
	}
	for _, event := range events {
		if _, found, err := store.ApplyWorkerEvent(context.Background(), event); err != nil || !found {
			t.Fatalf("ApplyWorkerEvent(%s) found=%v err=%v", event.Type, found, err)
		}
	}

	loaded, found, err := store.Get(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("Get found=%v err=%v", found, err)
	}
	if loaded.Status != StatusCompleted {
		t.Fatalf("status = %s, want %s", loaded.Status, StatusCompleted)
	}
	result, ok := loaded.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", loaded.Result)
	}
	output := result["output"].(map[string]any)
	if output["plain_text"] != "hello world" {
		t.Fatalf("plain_text = %v", output["plain_text"])
	}

	storedEvents, found, err := store.ListEvents(context.Background(), job.ID, 0, 10)
	if err != nil || !found {
		t.Fatalf("ListEvents found=%v err=%v", found, err)
	}
	if len(storedEvents) != 4 {
		t.Fatalf("stored event count = %d, want 4", len(storedEvents))
	}
	if storedEvents[1].Sequence != 2 || storedEvents[1].Data["worker_event"] == nil {
		t.Fatalf("worker event metadata missing: %#v", storedEvents[1])
	}
}

func TestMemoryStoreStoresCompletedWithWarningsResult(t *testing.T) {
	store := NewMemoryStore()
	job := createWorkerEventTestJob(t, store, "trace_worker_warnings")

	if _, found, err := store.ApplyWorkerEvent(context.Background(), WorkerEvent{
		EventID:    "worker_evt_warn",
		JobID:      job.ID,
		APIVersion: job.APIVersion,
		Type:       "completed_with_warnings",
		Sequence:   2,
		TraceID:    job.TraceID,
		Data:       map[string]any{"status": "completed_with_warnings", "result": map[string]any{"type": "text", "text": "warned"}},
	}); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent found=%v err=%v", found, err)
	}

	loaded, _, _ := store.Get(context.Background(), job.ID)
	if loaded.Status != StatusCompletedWithWarnings || loaded.Result == nil {
		t.Fatalf("completed_with_warnings did not persist result: %#v", loaded)
	}
}

func TestMemoryStoreDeduplicatesWorkerEvents(t *testing.T) {
	store := NewMemoryStore()
	job := createWorkerEventTestJob(t, store, "trace_dedupe")
	event := WorkerEvent{EventID: "worker_evt_same", JobID: job.ID, APIVersion: job.APIVersion, Type: "running", Sequence: 1, TraceID: job.TraceID, Data: map[string]any{"status": "running"}}

	if _, found, err := store.ApplyWorkerEvent(context.Background(), event); err != nil || !found {
		t.Fatalf("first ApplyWorkerEvent found=%v err=%v", found, err)
	}
	if _, found, err := store.ApplyWorkerEvent(context.Background(), event); err != nil || !found {
		t.Fatalf("second ApplyWorkerEvent found=%v err=%v", found, err)
	}
	events, found, err := store.ListEvents(context.Background(), job.ID, 0, 10)
	if err != nil || !found {
		t.Fatalf("ListEvents found=%v err=%v", found, err)
	}
	if len(events) != 2 {
		t.Fatalf("event count = %d, want queued plus one worker event", len(events))
	}
}

func TestMemoryStoreRejectsWorkerEventsWithoutProvenance(t *testing.T) {
	store := NewMemoryStore()
	job := createWorkerEventTestJob(t, store, "trace_provenance")

	tests := []WorkerEvent{
		{EventID: "missing_api", JobID: job.ID, Type: "running", Sequence: 1, TraceID: job.TraceID, Data: map[string]any{"status": "running"}},
		{EventID: "missing_trace", JobID: job.ID, APIVersion: job.APIVersion, Type: "running", Sequence: 1, Data: map[string]any{"status": "running"}},
		{JobID: job.ID, APIVersion: job.APIVersion, Type: "running", TraceID: job.TraceID, Data: map[string]any{"status": "running"}},
		{EventID: "bad_type", JobID: job.ID, APIVersion: job.APIVersion, Type: "unsafe.type", Sequence: 1, TraceID: job.TraceID, Data: map[string]any{"status": "running"}},
	}
	for _, event := range tests {
		if _, _, err := store.ApplyWorkerEvent(context.Background(), event); err == nil {
			t.Fatalf("ApplyWorkerEvent(%#v) returned nil error", event)
		}
	}
}

func TestMemoryStoreRejectsMismatchedWorkerEvents(t *testing.T) {
	store := NewMemoryStore()
	job := createWorkerEventTestJob(t, store, "trace_mismatch")

	if _, _, err := store.ApplyWorkerEvent(context.Background(), WorkerEvent{EventID: "wrong_api", JobID: job.ID, APIVersion: "wrong", Type: "running", Sequence: 1, TraceID: job.TraceID, Data: map[string]any{"status": "running"}}); err == nil || !strings.Contains(err.Error(), "api_version") {
		t.Fatalf("wrong api version error = %v", err)
	}
	if _, _, err := store.ApplyWorkerEvent(context.Background(), WorkerEvent{EventID: "wrong_trace", JobID: job.ID, APIVersion: job.APIVersion, Type: "running", Sequence: 2, TraceID: "wrong", Data: map[string]any{"status": "running"}}); err == nil || !strings.Contains(err.Error(), "trace_id") {
		t.Fatalf("wrong trace error = %v", err)
	}
}

func TestMemoryStoreDoesNotReviveCancelledJobsFromLateWorkerEvents(t *testing.T) {
	store := NewMemoryStore()
	job := createWorkerEventTestJob(t, store, "trace_cancel_guard")
	if _, found, err := store.UpdateStatus(context.Background(), job.ID, StatusCanceled); err != nil || !found {
		t.Fatalf("UpdateStatus found=%v err=%v", found, err)
	}
	if _, found, err := store.ApplyWorkerEvent(context.Background(), WorkerEvent{EventID: "late_completed", JobID: job.ID, APIVersion: job.APIVersion, Type: "completed", Sequence: 9, TraceID: job.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"text": "late"}}}); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent found=%v err=%v", found, err)
	}

	loaded, found, err := store.Get(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("Get found=%v err=%v", found, err)
	}
	if loaded.Status != StatusCanceled || loaded.Result != nil {
		t.Fatalf("late worker event revived job: %#v", loaded)
	}
}

func TestMemoryStoreAllowsSafeManualSessionRuntimeFields(t *testing.T) {
	store := NewMemoryStore()
	job := createWorkerEventTestJob(t, store, "trace_redact")

	if _, found, err := store.ApplyWorkerEvent(context.Background(), WorkerEvent{
		EventID:    "manual_session",
		JobID:      job.ID,
		APIVersion: job.APIVersion,
		Type:       "session.manual_action_required",
		Sequence:   2,
		TraceID:    job.TraceID,
		Data:       map[string]any{"status": "running", "novnc_url": "http://127.0.0.1:7900/session/sess_1", "session_id": "sess_1"},
	}); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent found=%v err=%v", found, err)
	}
	events, _, _ := store.ListEvents(context.Background(), job.ID, 0, 10)
	data := events[1].Data
	if data["novnc_url"] != "http://127.0.0.1:7900/session/sess_1" || data["session_id"] != "sess_1" {
		t.Fatalf("safe manual session fields were not preserved: %#v", data)
	}
}

func TestMemoryStoreRedactsUnsafeManualSessionRuntimeFields(t *testing.T) {
	store := NewMemoryStore()
	job := createWorkerEventTestJob(t, store, "trace_redact_unsafe")

	if _, found, err := store.ApplyWorkerEvent(context.Background(), WorkerEvent{
		EventID:    "manual_session_unsafe",
		JobID:      job.ID,
		APIVersion: job.APIVersion,
		Type:       "session.manual_action_required",
		Sequence:   2,
		TraceID:    job.TraceID,
		Data:       map[string]any{"status": "running", "novnc_url": "https://example.invalid/session/sess_1", "session_id": "sess 1"},
	}); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent found=%v err=%v", found, err)
	}
	events, _, _ := store.ListEvents(context.Background(), job.ID, 0, 10)
	data := events[1].Data
	if data["novnc_url"] != "[redacted]" || data["session_id"] != "[redacted]" {
		t.Fatalf("unsafe manual session fields were not redacted: %#v", data)
	}
}

func TestMemoryStoreRejectsUnknownStatus(t *testing.T) {
	store := NewMemoryStore()
	job := createWorkerEventTestJob(t, store, "trace_unknown_status")
	if _, _, err := store.UpdateStatus(context.Background(), job.ID, Status("unknown")); err == nil {
		t.Fatal("UpdateStatus returned nil error for unknown status")
	}
}

func TestMemoryStoreGetScopedHidesOtherTenantJobs(t *testing.T) {
	store := NewMemoryStore()
	job := createWorkerEventTestJob(t, store, "trace_scoped_get")
	if _, found, err := store.GetScoped(context.Background(), job.ID, job.TenantID, job.AppID); err != nil || !found {
		t.Fatalf("GetScoped own scope found=%v err=%v", found, err)
	}
	if _, found, err := store.GetScoped(context.Background(), job.ID, "tenant_other", job.AppID); err != nil || found {
		t.Fatalf("GetScoped other tenant found=%v err=%v, want hidden", found, err)
	}
}

func TestMemoryStoreListAllEventsUsesGlobalCreatedOrder(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	first := createWorkerEventTestJob(t, store, "trace_order_first")
	now = now.Add(time.Second)
	second := createWorkerEventTestJob(t, store, "trace_order_second")
	now = now.Add(time.Second)
	if _, found, err := store.ApplyWorkerEvent(context.Background(), WorkerEvent{
		EventID:    "first_running",
		JobID:      first.ID,
		APIVersion: first.APIVersion,
		Type:       "running",
		Sequence:   2,
		TraceID:    first.TraceID,
		Data:       map[string]any{"status": "running"},
	}); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent found=%v err=%v", found, err)
	}
	now = now.Add(time.Second)
	if _, found, err := store.ApplyWorkerEvent(context.Background(), WorkerEvent{
		EventID:    "second_running",
		JobID:      second.ID,
		APIVersion: second.APIVersion,
		Type:       "running",
		Sequence:   2,
		TraceID:    second.TraceID,
		Data:       map[string]any{"status": "running"},
	}); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent found=%v err=%v", found, err)
	}

	events, err := store.ListAllEvents(context.Background(), EventListFilter{TenantID: first.TenantID, AppID: first.AppID, Limit: 10})
	if err != nil {
		t.Fatalf("ListAllEvents returned error: %v", err)
	}
	if got, want := []string{events[0].JobID, events[1].JobID, events[2].JobID, events[3].JobID}, []string{first.ID, second.ID, first.ID, second.ID}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("global event order = %v, want %v", got, want)
	}
	seen := map[string]struct{}{}
	for _, event := range events {
		if _, ok := seen[event.ID]; ok {
			t.Fatalf("duplicate global event id %q in %#v", event.ID, events)
		}
		seen[event.ID] = struct{}{}
	}
}

func createWorkerEventTestJob(t *testing.T, store *MemoryStore, traceID string) Job {
	t.Helper()
	job, err := store.Create(context.Background(), CreateRequest{
		APIVersion:     "2026-05-22",
		TenantID:       "tenant_a",
		AppID:          "app_a",
		IdempotencyKey: "idem_" + traceID,
		Target:         "mock",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		TraceID:        traceID,
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	return job
}
