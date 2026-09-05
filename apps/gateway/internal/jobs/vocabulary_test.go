package jobs

import (
	"context"
	"testing"
	"time"
)

// TestUpdateStatusRejectsBackwardsTransition pins the seam fix: the API
// mutation path must honor the same transition validation as the worker-event
// path. A queued job cannot be moved back to "created"; a running job cannot
// be demoted to "queued".
func TestUpdateStatusRejectsBackwardsTransition(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	job, err := store.Create(context.Background(), CreateRequest{
		APIVersion:  "2026-05-22",
		TenantID:    "tenant_test",
		AppID:       "app_test",
		Target:      "mock.chat",
		CommandType: "chat",
		Input:       map[string]any{"prompt": "hi"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// queued -> created is backwards; the job must stay queued.
	updated, found, err := store.UpdateStatus(context.Background(), job.ID, StatusCreated)
	if err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}
	if !found {
		t.Fatal("job not found")
	}
	if updated.Status != StatusQueued {
		t.Fatalf("backwards transition applied: status = %q, want %q (queued)", updated.Status, StatusQueued)
	}

	// Advance to running, then try to demote to assigned.
	if _, _, err := store.UpdateStatus(context.Background(), job.ID, StatusRunning); err != nil {
		t.Fatalf("UpdateStatus forward: %v", err)
	}
	updated, _, _ = store.UpdateStatus(context.Background(), job.ID, StatusAssigned)
	if updated.Status != StatusRunning {
		t.Fatalf("demotion applied: status = %q, want %q (running)", updated.Status, StatusRunning)
	}
}

// TestStatusFromWorkerEventDataStatusBounded pins the trust-hole fix: a
// worker event's data.status may only steer the state machine to a status the
// transition rules allow from the current status — never backwards, never a
// terminal jump from an early state.
func TestStatusFromWorkerEventDataStatusBounded(t *testing.T) {
	// A queued job receives a "session.opening" event whose data claims
	// "completed": the data.status shortcut must not apply.
	event := WorkerEvent{
		Type: "session.opening",
		Data: map[string]any{"status": "completed"},
	}
	got := statusFromWorkerEvent(event, StatusQueued)
	if got != StatusQueued {
		t.Fatalf("unbounded data.status applied: %q, want %q (fallback)", got, StatusQueued)
	}

	// A legitimately-forward data.status still applies.
	event = WorkerEvent{
		Type: "session.authenticated",
		Data: map[string]any{"status": "running"},
	}
	got = statusFromWorkerEvent(event, StatusQueued)
	if got != StatusRunning {
		t.Fatalf("legitimate data.status rejected: %q, want %q (running)", got, StatusRunning)
	}
}

// TestAllStatusPredicatesOneDeclaration pins the vocabulary table: every
// declared status is classified exactly once, terminal flags match the
// contract set, and the worker-alias map covers every alias the worker emits.
func TestAllStatusPredicatesOneDeclaration(t *testing.T) {
	if len(statusTable) != len(LifecycleStatuses()) {
		t.Fatalf("statusTable has %d entries, LifecycleStatuses has %d", len(statusTable), len(LifecycleStatuses()))
	}
	for status := range statusTable {
		if !KnownStatus(status) {
			t.Fatalf("statusTable entry %q not known", status)
		}
		if TerminalStatus(status) != statusTable[status].terminal {
			t.Fatalf("terminal flag mismatch for %q", status)
		}
	}
	// Worker aliases resolve to their canonical statuses.
	aliases := map[string]Status{
		"canceled": StatusCanceled,
		"timeout":  StatusTimedOut,
		"failed":   StatusFailedRetryable, // retryable=true default
		"blocked":  StatusFailedRetryable,
	}
	for alias, want := range aliases {
		event := WorkerEvent{Type: alias, Data: map[string]any{"retryable": true}}
		if got := statusFromWorkerEvent(event, StatusQueued); got != want {
			t.Fatalf("alias %q mapped to %q, want %q", alias, got, want)
		}
	}
}
