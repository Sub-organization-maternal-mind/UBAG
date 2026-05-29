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

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
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
