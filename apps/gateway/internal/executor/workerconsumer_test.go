package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/alerts"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

func TestWorkerConsumerRunOnceIngestsWorkerEventsAndCompletesLease(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:     "2026-05-22",
		TenantID:       "tenant_a",
		AppID:          "app_a",
		IdempotencyKey: "idem_consumer_success",
		Target:         "mock",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		Options:        map[string]any{"mock_tokens": []any{"hello", " ", "world"}},
		TraceID:        "trace_consumer_success",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}

	consumer := WorkerConsumer{
		Spool: dispatcher,
		Jobs:  store,
		Runner: WorkerRunFunc(func(_ context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				{EventID: "worker_evt_running", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "running", Sequence: 1, TraceID: envelope.TraceID, Data: map[string]any{"status": "running"}},
				{EventID: "worker_evt_done", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "completed", Sequence: 2, TraceID: envelope.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "hello world"}}},
			}, nil
		}),
	}

	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	loaded, found, err := store.Get(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("Get found=%v err=%v", found, err)
	}
	if loaded.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %s, want %s", loaded.Status, jobstore.StatusCompleted)
	}
	if loaded.Result == nil {
		t.Fatal("result was not stored")
	}
	doneEntries, err := os.ReadDir(dispatcher.doneDir())
	if err != nil {
		t.Fatalf("read done dir: %v", err)
	}
	if len(doneEntries) != 1 {
		t.Fatalf("done entries = %d, want 1", len(doneEntries))
	}
}

func TestWorkerConsumerNotifiesTerminalJobBeforeCompletingLease(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:  "2026-05-22",
		TenantID:    "tenant_a",
		AppID:       "app_a",
		Target:      "mock",
		CommandType: "submit",
		Input:       map[string]any{"prompt": "hello"},
		Callbacks: map[string]any{
			"webhook_url":       "https://example.com/callback",
			"webhook_secret_id": "wh_sec_test",
		},
		TraceID: "trace_terminal_notify",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	lease := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_notify", envelope: EnvelopeFromJob(job)}
	notifier := &fakeTerminalNotifier{}
	consumer := WorkerConsumer{
		Queue:            fakeWorkerQueue{lease: lease},
		Jobs:             store,
		TerminalNotifier: notifier,
		Runner: WorkerRunFunc(func(_ context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				{EventID: "worker_evt_notify_done", Type: "completed", Sequence: 1, TraceID: envelope.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "ok"}}},
			}, nil
		}),
	}
	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	if !lease.completed {
		t.Fatal("lease was not completed")
	}
	if len(notifier.jobs) != 1 || notifier.jobs[0].Status != jobstore.StatusCompleted {
		t.Fatalf("terminal notifications = %#v", notifier.jobs)
	}
}

func TestWorkerConsumerRunOnceFailsLeaseOnRunnerError(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:  "2026-05-22",
		TenantID:    "tenant_a",
		AppID:       "app_a",
		Target:      "mock",
		CommandType: "submit",
		Input:       map[string]any{"prompt": "hello"},
		TraceID:     "trace_consumer_failure",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}
	consumer := WorkerConsumer{
		Spool: dispatcher,
		Jobs:  store,
		Runner: WorkerRunFunc(func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return nil, errors.New("boom with secret-looking text access_token=redacted")
		}),
	}

	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	loaded, found, err := store.Get(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("Get found=%v err=%v", found, err)
	}
	if loaded.Status != jobstore.StatusFailedRetryable {
		t.Fatalf("status = %s, want %s", loaded.Status, jobstore.StatusFailedRetryable)
	}
	failedEntries, err := os.ReadDir(dispatcher.failedDir())
	if err != nil {
		t.Fatalf("read failed dir: %v", err)
	}
	if len(failedEntries) != 1 {
		t.Fatalf("failed entries = %d, want 1", len(failedEntries))
	}
}

func TestWorkerConsumerRaisesAlertOnManualActionRequired(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:     "2026-05-22",
		TenantID:       "tenant_a",
		AppID:          "app_a",
		IdempotencyKey: "idem_manual_action",
		Target:         "mock",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		TraceID:        "trace_manual_action",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}

	alertStore := alerts.NewMemoryStore()
	manager := alerts.NewManager(alertStore, nil, nil, alerts.ConfigSummary{})
	consumer := WorkerConsumer{
		Spool:  dispatcher,
		Jobs:   store,
		Alerts: manager,
		Runner: WorkerRunFunc(func(_ context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				{EventID: "worker_evt_manual", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "session.manual_action_required", Sequence: 1, TraceID: envelope.TraceID, Data: map[string]any{
					"status":     "manual_action_required",
					"target":     "mock",
					"session_id": "sess-1",
					"reason":     "manual_login_required",
					"message":    "open the live browser session",
					"adapter":    "mock",
				}},
				{EventID: "worker_evt_done", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "completed", Sequence: 2, TraceID: envelope.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "done"}}},
			}, nil
		}),
	}

	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}

	raised, err := manager.List(context.Background(), alerts.Filter{TenantID: "tenant_a"})
	if err != nil {
		t.Fatalf("list alerts: %v", err)
	}
	if len(raised) != 1 {
		t.Fatalf("expected exactly 1 alert, got %d", len(raised))
	}
	alert := raised[0]
	if alert.JobID != job.ID {
		t.Fatalf("alert job id = %q, want %q", alert.JobID, job.ID)
	}
	if alert.Kind != alerts.KindManualLogin {
		t.Fatalf("alert kind = %q, want %q", alert.Kind, alerts.KindManualLogin)
	}
	if alert.SessionID != "sess-1" || alert.TargetID != "mock" {
		t.Fatalf("alert context not captured: %+v", alert)
	}
}

func TestWorkerConsumerRecordsConcurrencyCapChange(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:     "2026-05-22",
		TenantID:       "tenant_a",
		AppID:          "app_a",
		IdempotencyKey: "idem_concurrency",
		Target:         "mock",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		TraceID:        "trace_concurrency",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}

	registry := topology.NewConcurrencyRegistry()
	consumer := WorkerConsumer{
		Spool:       dispatcher,
		Jobs:        store,
		Concurrency: registry,
		Runner: WorkerRunFunc(func(_ context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				{EventID: "worker_evt_cap", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "concurrency.cap_changed", Sequence: 1, TraceID: envelope.TraceID, Data: map[string]any{
					"target":       "mock",
					"identity_ref": "acct-1",
					"current_cap":  float64(3),
					"min":          float64(1),
					"max":          float64(8),
					"in_flight":    float64(2),
					"reason":       "additive_increase",
				}},
				{EventID: "worker_evt_done", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "completed", Sequence: 2, TraceID: envelope.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "done"}}},
			}, nil
		}),
	}

	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}

	views := registry.List("tenant_a")
	if len(views) != 1 {
		t.Fatalf("expected exactly 1 concurrency view, got %d", len(views))
	}
	view := views[0]
	if view.Target != "mock" || view.IdentityRef != "acct-1" {
		t.Fatalf("view identity not captured: %+v", view)
	}
	if view.CurrentCap != 3 || view.Min != 1 || view.Max != 8 || view.InFlight != 2 {
		t.Fatalf("view counters not captured: %+v", view)
	}
	if view.LastChangeReason != "additive_increase" {
		t.Fatalf("view reason = %q, want additive_increase", view.LastChangeReason)
	}
	// A different tenant must not see another tenant's reported ceiling.
	if other := registry.List("tenant_b"); len(other) != 0 {
		t.Fatalf("tenant isolation breached: %+v", other)
	}
}

func TestWorkerConsumerConcurrencyRecordingIsNilSafe(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:     "2026-05-22",
		TenantID:       "tenant_a",
		AppID:          "app_a",
		IdempotencyKey: "idem_concurrency_nilsafe",
		Target:         "mock",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		TraceID:        "trace_concurrency_nilsafe",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}

	// No Concurrency registry configured: cap-change events must be ignored
	// without interrupting ingestion.
	consumer := WorkerConsumer{
		Spool: dispatcher,
		Jobs:  store,
		Runner: WorkerRunFunc(func(_ context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				{EventID: "worker_evt_cap", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "concurrency.cap_changed", Sequence: 1, TraceID: envelope.TraceID, Data: map[string]any{"target": "mock", "current_cap": float64(2)}},
				{EventID: "worker_evt_done", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "completed", Sequence: 2, TraceID: envelope.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "done"}}},
			}, nil
		}),
	}

	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
}

func TestWorkerConsumerProjectsTopologyReport(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:     "2026-05-22",
		TenantID:       "tenant_a",
		AppID:          "app_a",
		IdempotencyKey: "idem_topology",
		Target:         "chatgpt_web",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		TraceID:        "trace_topology",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}

	topoStore := topology.NewMemoryStore()
	consumer := WorkerConsumer{
		Spool:    dispatcher,
		Jobs:     store,
		Topology: topoStore,
		Runner: WorkerRunFunc(func(_ context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				{EventID: "worker_evt_topo", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "browser.topology_reported", Sequence: 1, TraceID: envelope.TraceID, Data: map[string]any{
					"tenant_id": "spoofed_tenant",
					"instances": []any{map[string]any{
						"instance_id":   "br_0001",
						"worker_id":     "worker-1",
						"engine":        "chromium",
						"state":         "active",
						"context_count": float64(1),
						"tab_count":     float64(1),
						// Worker-supplied tenant must be overridden by the job tenant.
						"tenant_id": "spoofed_tenant",
					}},
					"contexts": []any{map[string]any{
						"context_id":         "ctx_0001",
						"instance_id":        "br_0001",
						"target_id":          "chatgpt_web",
						"identity_ref":       "acct-1",
						"login_state":        "authenticated",
						"conversation_model": "url",
						"max_tabs":           float64(4),
						// Even if a worker lied, storage-state must be forced false.
						"has_storage_state": true,
					}},
					"tabs": []any{map[string]any{
						"tab_id":         "tab_0001",
						"context_id":     "ctx_0001",
						"state":          "busy",
						"conversation_id": "conv_1",
						"current_job_id":  envelope.JobID,
						"jobs_completed":  float64(0),
					}},
				}},
				{EventID: "worker_evt_done", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "completed", Sequence: 2, TraceID: envelope.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "done"}}},
			}, nil
		}),
	}

	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}

	instances, err := topoStore.ListInstances(context.Background(), topology.InstanceFilter{TenantID: "tenant_a"})
	if err != nil {
		t.Fatalf("ListInstances returned error: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(instances))
	}
	if instances[0].InstanceID != "br_0001" || instances[0].Engine != "chromium" {
		t.Fatalf("instance not projected: %+v", instances[0])
	}
	if instances[0].TenantID != "tenant_a" {
		t.Fatalf("instance tenant = %q, want tenant_a (job tenant wins)", instances[0].TenantID)
	}

	contexts, err := topoStore.ListContexts(context.Background(), topology.ContextFilter{TenantID: "tenant_a"})
	if err != nil {
		t.Fatalf("ListContexts returned error: %v", err)
	}
	if len(contexts) != 1 {
		t.Fatalf("expected 1 context, got %d", len(contexts))
	}
	if contexts[0].HasStorageState {
		t.Fatalf("storage-state material must never be projected: %+v", contexts[0])
	}
	if contexts[0].IdentityRef != "acct-1" || contexts[0].TargetID != "chatgpt_web" {
		t.Fatalf("context not projected: %+v", contexts[0])
	}

	tabs, err := topoStore.ListTabs(context.Background(), topology.TabFilter{TenantID: "tenant_a"})
	if err != nil {
		t.Fatalf("ListTabs returned error: %v", err)
	}
	if len(tabs) != 1 {
		t.Fatalf("expected 1 tab, got %d", len(tabs))
	}
	if tabs[0].TabID != "tab_0001" || tabs[0].State != "busy" {
		t.Fatalf("tab not projected: %+v", tabs[0])
	}

	// Tenant isolation: a different tenant sees nothing.
	other, _ := topoStore.ListInstances(context.Background(), topology.InstanceFilter{TenantID: "spoofed_tenant"})
	if len(other) != 0 {
		t.Fatalf("tenant isolation breached: %+v", other)
	}

	// The terminal job lease must still complete (topology event is intercepted,
	// never forwarded to ApplyWorkerEvent).
	finished, found, err := store.Get(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("Get found=%v err=%v", found, err)
	}
	if finished.Status != jobstore.StatusCompleted {
		t.Fatalf("job status = %q, want %q", finished.Status, jobstore.StatusCompleted)
	}
}

func TestWorkerConsumerTopologyRecordingIsNilSafe(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:     "2026-05-22",
		TenantID:       "tenant_a",
		AppID:          "app_a",
		IdempotencyKey: "idem_topology_nilsafe",
		Target:         "chatgpt_web",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		TraceID:        "trace_topology_nilsafe",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}

	// No Topology ingestor configured (SQLite/Postgres deployment): the event is
	// silently dropped without interrupting ingestion or the job lease.
	consumer := WorkerConsumer{
		Spool: dispatcher,
		Jobs:  store,
		Runner: WorkerRunFunc(func(_ context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				{EventID: "worker_evt_topo", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "browser.topology_reported", Sequence: 1, TraceID: envelope.TraceID, Data: map[string]any{
					"instances": []any{map[string]any{"instance_id": "br_0001", "engine": "chromium", "state": "active"}},
				}},
				{EventID: "worker_evt_done", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "completed", Sequence: 2, TraceID: envelope.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "done"}}},
			}, nil
		}),
	}

	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
}

func TestWorkerConsumerManualActionAlertIsNilSafe(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:     "2026-05-22",
		TenantID:       "tenant_a",
		AppID:          "app_a",
		IdempotencyKey: "idem_manual_nilsafe",
		Target:         "mock",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		TraceID:        "trace_manual_nilsafe",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}

	// No Alerts manager configured: ingestion must still succeed.
	consumer := WorkerConsumer{
		Spool: dispatcher,
		Jobs:  store,
		Runner: WorkerRunFunc(func(_ context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				{EventID: "worker_evt_manual", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "session.manual_action_required", Sequence: 1, TraceID: envelope.TraceID, Data: map[string]any{"status": "manual_action_required", "reason": "manual_login_required"}},
				{EventID: "worker_evt_done", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "completed", Sequence: 2, TraceID: envelope.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "done"}}},
			}, nil
		}),
	}

	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	loaded, found, err := store.Get(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("Get found=%v err=%v", found, err)
	}
	if loaded.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %s, want %s", loaded.Status, jobstore.StatusCompleted)
	}
}

func TestWorkerConsumerDoesNotRunCancelledJob(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:  "2026-05-22",
		TenantID:    "tenant_a",
		AppID:       "app_a",
		Target:      "mock",
		CommandType: "submit",
		Input:       map[string]any{"prompt": "hello"},
		TraceID:     "trace_consumer_cancelled",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}
	if _, found, err := store.UpdateStatus(context.Background(), job.ID, jobstore.StatusCanceled); err != nil || !found {
		t.Fatalf("UpdateStatus found=%v err=%v", found, err)
	}
	consumer := WorkerConsumer{
		Spool: dispatcher,
		Jobs:  store,
		Runner: WorkerRunFunc(func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			t.Fatal("runner must not be called for cancelled job")
			return nil, nil
		}),
	}

	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	cancelledEntries, err := os.ReadDir(dispatcher.cancelledDir())
	if err != nil {
		t.Fatalf("read cancelled dir: %v", err)
	}
	if len(cancelledEntries) != 1 {
		t.Fatalf("cancelled entries = %d, want 1", len(cancelledEntries))
	}
	if filepath.Ext(cancelledEntries[0].Name()) != ".json" {
		t.Fatalf("unexpected cancelled entry name: %s", cancelledEntries[0].Name())
	}
}

func TestWorkerConsumerRunsPersistedEnvelopeInsteadOfLeaseEnvelope(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:  "2026-05-22",
		TenantID:    "tenant_a",
		AppID:       "app_a",
		Target:      "mock",
		CommandType: "submit",
		Input:       map[string]any{"prompt": "hello"},
		TraceID:     "trace_trusted_envelope",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	leaseEnvelope := EnvelopeFromJob(job)
	leaseEnvelope.CreatedAt = time.Time{}
	lease := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_trusted", envelope: leaseEnvelope}
	consumer := WorkerConsumer{
		Queue: fakeWorkerQueue{lease: lease},
		Jobs:  store,
		Runner: WorkerRunFunc(func(_ context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			if !envelope.CreatedAt.Equal(job.CreatedAt) {
				t.Fatalf("runner received lease created_at %s, want persisted %s", envelope.CreatedAt, job.CreatedAt)
			}
			return []jobstore.WorkerEvent{
				{EventID: "worker_evt_trusted_done", Type: "completed", Sequence: 1, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "ok"}}},
			}, nil
		}),
	}

	processed, err := consumer.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	if !lease.completed {
		t.Fatal("lease was not completed")
	}
}

func TestNormalizeWorkerEventRequiresDedupeIdentity(t *testing.T) {
	envelope := EnvelopeFromJob(sampleJob())
	_, err := normalizeWorkerEvent(envelope, jobstore.WorkerEvent{
		Type: "running",
		Data: map[string]any{"status": "running"},
	})
	if err == nil {
		t.Fatal("normalizeWorkerEvent returned nil, want missing identity error")
	}
	if !strings.Contains(err.Error(), "event_id or positive sequence") {
		t.Fatalf("error = %v", err)
	}
}

func TestWorkerConsumerPoisonsLeaseWhenEnvelopeDoesNotMatchJob(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:  "2026-05-22",
		TenantID:    "tenant_a",
		AppID:       "app_a",
		Target:      "mock",
		CommandType: "submit",
		Input:       map[string]any{"prompt": "trusted"},
		TraceID:     "trace_poison_envelope",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	leaseEnvelope := EnvelopeFromJob(job)
	leaseEnvelope.Job.Input = map[string]any{"prompt": "tampered"}
	lease := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_poison", envelope: leaseEnvelope}
	ran := false
	consumer := WorkerConsumer{
		Queue: fakeWorkerQueue{lease: lease},
		Jobs:  store,
		Runner: WorkerRunFunc(func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			ran = true
			return nil, nil
		}),
	}

	processed, err := consumer.RunOnce(context.Background())
	if !processed || err == nil {
		t.Fatalf("RunOnce processed=%v err=%v, want poison error", processed, err)
	}
	if ran {
		t.Fatal("runner was called for a tampered lease envelope")
	}
	if !lease.poisoned {
		t.Fatal("lease was not poisoned")
	}
}

func TestParseWorkerJSONLRejectsMalformedLines(t *testing.T) {
	if _, err := parseWorkerJSONL([]byte("{not-json}\n")); err == nil {
		t.Fatal("parseWorkerJSONL returned nil error for malformed JSON")
	}
}

func TestProcessWorkerRunnerRunsPythonWorkerFromGatewayEnvelope(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skipf("python not available on PATH: %v", err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "worker", "run_mock_worker.py"))
	if err != nil {
		t.Fatalf("resolve worker script: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("worker script missing: %v", err)
	}

	events, err := (ProcessWorkerRunner{Python: python, Script: script}).RunWorker(context.Background(), DispatchEnvelope{
		APIVersion:     "2026-05-22",
		JobID:          "job_process_runner",
		TenantID:       "tenant_a",
		AppID:          "app_a",
		IdempotencyKey: "idem_process_runner",
		TraceID:        "trace_process_runner",
		Job: DispatchJob{
			Target:      "mock",
			CommandType: "submit",
			Input:       map[string]any{"prompt": "hello"},
			Options:     map[string]any{"mock_tokens": []any{"process", " ", "ok"}},
		},
	})
	if err != nil {
		t.Fatalf("RunWorker returned error: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Type != "completed" {
		t.Fatalf("unexpected events: %#v", events)
	}
	if events[len(events)-1].Data["result"].(map[string]any)["text"] != "process ok" {
		t.Fatalf("unexpected result event: %#v", events[len(events)-1])
	}
}

func TestMinimalWorkerEnvIncludesBrowserRuntimeConfigOnly(t *testing.T) {
	t.Setenv("UBAG_REMOTE_BROWSER_ENDPOINT", "http://browser-viewer:9223")
	t.Setenv("UBAG_BROWSER_ENGINE", "chromium")
	t.Setenv("UBAG_BROWSER_PROTOCOL", "cdp")
	t.Setenv("UBAG_BROWSER_HEADED", "false")
	t.Setenv("UBAG_NOVNC_BASE_URL", "http://127.0.0.1:7900")
	t.Setenv("UBAG_BROWSER_VNC_PASSWORD", "must-not-pass")
	t.Setenv("UBAG_POSTGRES_DSN", "must-not-pass")

	env := minimalWorkerEnv()
	values := map[string]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}

	if values["UBAG_REMOTE_BROWSER_ENDPOINT"] != "http://browser-viewer:9223" {
		t.Fatalf("remote browser endpoint was not propagated: %#v", values)
	}
	if values["UBAG_BROWSER_ENGINE"] != "chromium" || values["UBAG_BROWSER_PROTOCOL"] != "cdp" {
		t.Fatalf("browser engine/protocol were not propagated: %#v", values)
	}
	if _, ok := values["UBAG_BROWSER_VNC_PASSWORD"]; ok {
		t.Fatal("VNC password must not be propagated to worker subprocess")
	}
	if _, ok := values["UBAG_POSTGRES_DSN"]; ok {
		t.Fatal("database DSN must not be propagated to worker subprocess")
	}
}

type fakeWorkerQueue struct {
	lease *fakeWorkerLease
}

type fakeTerminalNotifier struct {
	jobs []jobstore.Job
}

func (n *fakeTerminalNotifier) EnqueueTerminalJob(_ context.Context, job jobstore.Job) error {
	n.jobs = append(n.jobs, job)
	return nil
}

func (q fakeWorkerQueue) Ready(context.Context) error {
	return nil
}

func (q fakeWorkerQueue) LeaseNext(context.Context) (WorkerLease, bool, error) {
	if q.lease == nil {
		return nil, false, nil
	}
	return q.lease, true, nil
}

type fakeWorkerLease struct {
	jobID        string
	leaseID      string
	envelope     DispatchEnvelope
	completed    bool
	failed       bool
	cancelled    bool
	retried      bool
	poisoned     bool
	poisonReason string
}

func (l *fakeWorkerLease) JobID() string {
	return l.jobID
}

func (l *fakeWorkerLease) LeaseID() string {
	return l.leaseID
}

func (l *fakeWorkerLease) QueueName() string {
	return "fake"
}

func (l *fakeWorkerLease) Envelope() DispatchEnvelope {
	return l.envelope
}

func (l *fakeWorkerLease) Complete(context.Context) error {
	l.completed = true
	return nil
}

func (l *fakeWorkerLease) Fail(context.Context) error {
	l.failed = true
	return nil
}

func (l *fakeWorkerLease) Cancel(context.Context) error {
	l.cancelled = true
	return nil
}

func (l *fakeWorkerLease) Retry(context.Context) error {
	l.retried = true
	return nil
}

func (l *fakeWorkerLease) Poison(_ context.Context, reason string) error {
	l.poisoned = true
	l.poisonReason = reason
	return nil
}
