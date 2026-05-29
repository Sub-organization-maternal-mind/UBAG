package executor_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// TestNATSDispatcher runs when UBAG_TEST_NATS_URL is set to a live NATS server.
// Without the env var the test is skipped, matching the Postgres store pattern.
func TestNATSDispatcher(t *testing.T) {
	url := os.Getenv("UBAG_TEST_NATS_URL")
	if url == "" {
		t.Skip("UBAG_TEST_NATS_URL not set; skipping NATS integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stream := "UBAG_JOBS_TEST"
	subject := "ubag.test.jobs"
	d := executor.NewNATSDispatcher(url, stream, subject)
	defer d.Close()

	t.Run("Ready", func(t *testing.T) {
		if err := d.Ready(ctx); err != nil {
			t.Fatalf("Ready failed: %v", err)
		}
	})

	t.Run("EnqueueJob", func(t *testing.T) {
		job := testNATSJob("nats-job-001")
		receipt, err := d.EnqueueJob(ctx, job)
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		if receipt.Backend != "nats" {
			t.Errorf("receipt.Backend = %q; want nats", receipt.Backend)
		}
		if receipt.QueueName != stream {
			t.Errorf("receipt.QueueName = %q; want %q", receipt.QueueName, stream)
		}
		if receipt.MessageID == "" {
			t.Error("receipt.MessageID is empty")
		}
	})

	t.Run("EnqueueTerminalJobIsNoop", func(t *testing.T) {
		job := testNATSJob("nats-job-terminal")
		job.Status = jobstore.StatusCompleted
		receipt, err := d.EnqueueJob(ctx, job)
		if err != nil {
			t.Fatalf("EnqueueJob terminal failed: %v", err)
		}
		if receipt.Backend != "nats" {
			t.Errorf("receipt.Backend = %q; want nats", receipt.Backend)
		}
	})

	t.Run("CancelJob", func(t *testing.T) {
		job := testNATSJob("nats-job-cancel-001")
		if _, err := d.EnqueueJob(ctx, job); err != nil {
			t.Fatalf("EnqueueJob before cancel failed: %v", err)
		}
		if err := d.CancelJob(ctx, job, "test cancellation"); err != nil {
			t.Fatalf("CancelJob failed: %v", err)
		}
	})

	t.Run("CancelJobOnFreshDispatcher", func(t *testing.T) {
		fresh := executor.NewNATSDispatcher(url, stream, subject)
		defer fresh.Close()
		job := testNATSJob("nats-job-cancel-fresh")
		if err := fresh.CancelJob(ctx, job, "fresh dispatcher cancellation"); err != nil {
			t.Fatalf("CancelJob on fresh dispatcher failed: %v", err)
		}
	})

	t.Run("Stats", func(t *testing.T) {
		stats, err := d.Stats(ctx)
		if err != nil {
			t.Fatalf("Stats failed: %v", err)
		}
		if stats.QueueName == "" {
			t.Error("stats.QueueName is empty")
		}
		if _, ok := stats.DepthByState["queued"]; !ok {
			t.Error("stats.DepthByState missing 'queued'")
		}
	})
}

func TestNATSWorkerQueueIngestsPublishedJob(t *testing.T) {
	url := os.Getenv("UBAG_TEST_NATS_URL")
	if url == "" {
		t.Skip("UBAG_TEST_NATS_URL not set; skipping NATS worker integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	suffix := time.Now().UnixNano()
	stream := fmt.Sprintf("UBAG_WORKER_TEST_%d", suffix)
	subject := fmt.Sprintf("ubag.test.worker.%d", suffix)
	cleanupNATSStream(t, ctx, url, stream)

	store := jobstore.NewMemoryStore()
	job, err := store.Create(ctx, jobstore.CreateRequest{
		APIVersion:  "2026-05-22",
		TenantID:    "tenant_test",
		AppID:       "app_test",
		Target:      "mock.chat",
		CommandType: "chat",
		Input:       map[string]any{"prompt": "hello"},
		TraceID:     "trace_nats_worker",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	dispatcher := executor.NewNATSDispatcher(url, stream, subject)
	defer dispatcher.Close()
	if _, err := dispatcher.EnqueueJob(ctx, job); err != nil {
		t.Fatalf("EnqueueJob returned error: %v", err)
	}

	queue, err := executor.NewNATSWorkerQueue(executor.NATSWorkerQueueConfig{
		URL:        url,
		StreamName: stream,
		Subject:    subject,
		Durable:    "worker_consumer",
		FetchWait:  time.Second,
	})
	if err != nil {
		t.Fatalf("NewNATSWorkerQueue returned error: %v", err)
	}
	defer queue.Close()

	consumer := executor.WorkerConsumer{
		Queue: queue,
		Jobs:  store,
		Runner: executor.WorkerRunFunc(func(_ context.Context, envelope executor.DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				{EventID: "worker_evt_nats_running", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "running", Sequence: 1, TraceID: envelope.TraceID, Data: map[string]any{"status": "running"}},
				{EventID: "worker_evt_nats_done", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "completed", Sequence: 2, TraceID: envelope.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "nats ok"}}},
			}, nil
		}),
	}

	processed, err := consumer.RunOnce(ctx)
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	loaded, found, err := store.Get(ctx, job.ID)
	if err != nil || !found {
		t.Fatalf("Get found=%v err=%v", found, err)
	}
	if loaded.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %s, want %s", loaded.Status, jobstore.StatusCompleted)
	}
}

func cleanupNATSStream(t *testing.T, ctx context.Context, url string, stream string) {
	t.Helper()
	conn, err := nats.Connect(url)
	if err != nil {
		t.Logf("cleanup connect skipped: %v", err)
		return
	}
	t.Cleanup(func() {
		conn.Close()
	})
	js, err := jetstream.New(conn)
	if err != nil {
		t.Logf("cleanup jetstream skipped: %v", err)
		return
	}
	_ = js.DeleteStream(ctx, stream)
	t.Cleanup(func() {
		_ = js.DeleteStream(context.Background(), stream)
	})
}

func testNATSJob(id string) jobstore.Job {
	return jobstore.Job{
		ID:          id,
		APIVersion:  "2026-05-22",
		TenantID:    "tenant_test",
		AppID:       "app_test",
		Target:      "mock.chat",
		CommandType: "chat",
		Status:      jobstore.StatusQueued,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
}
