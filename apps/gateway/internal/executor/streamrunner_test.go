package executor

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

var _ StreamingWorkerRunner = (*DaemonWorkerRunner)(nil)

// dualRunner implements both interfaces and records which one the consumer
// called, so the flag gate is observable.
type dualRunner struct {
	batch                  WorkerRunFunc
	batchCalls, streamCall atomic.Int32
}

func (d *dualRunner) RunWorker(ctx context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
	d.batchCalls.Add(1)
	return d.batch(ctx, e)
}

func (d *dualRunner) StreamWorker(ctx context.Context, e DispatchEnvelope, sink EventSink) error {
	d.streamCall.Add(1)
	return BatchStreamAdapter{Runner: d.batch}.StreamWorker(ctx, e, sink)
}

func streamCases() map[string]WorkerRunFunc {
	return map[string]WorkerRunFunc{
		"completed": func(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				ingestTestEvent(e, "st_run", "running", 1, map[string]any{"status": "running"}),
				ingestTestEvent(e, "st_done", "completed", 2, completedIngestData()),
			}, nil
		},
		"no terminal": func(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{ingestTestEvent(e, "st_run", "running", 1, map[string]any{"status": "running"})}, nil
		},
		"two terminals": func(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				ingestTestEvent(e, "st_a", "completed", 1, completedIngestData()),
				ingestTestEvent(e, "st_b", "completed", 2, completedIngestData()),
			}, nil
		},
		"no events": func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) { return nil, nil },
		"run error": func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return nil, errors.New("boom")
		},
	}
}

// With the flag on, the batch adapter must leave the consumer's observable end
// state identical to the flag-off batch path for every outcome.
func TestStreamIngestOnMatchesBatchOutcome(t *testing.T) {
	for name, fn := range streamCases() {
		t.Run(name, func(t *testing.T) {
			t.Setenv("UBAG_WORKER_STREAM_INGEST", "")
			offFinal, offLease, offEvents := runIngestConsumer(t, fn)

			t.Setenv("UBAG_WORKER_STREAM_INGEST", "true")
			runner := &dualRunner{batch: fn}
			onFinal, onLease, onEvents := runIngestWith(t, runner)

			if runner.streamCall.Load() != 1 || runner.batchCalls.Load() != 0 {
				t.Fatalf("stream calls=%d batch calls=%d, want 1/0", runner.streamCall.Load(), runner.batchCalls.Load())
			}
			if onFinal.Status != offFinal.Status {
				t.Fatalf("status on=%s off=%s", onFinal.Status, offFinal.Status)
			}
			if eventTypes(onEvents) != eventTypes(offEvents) {
				t.Fatalf("events on=%s off=%s", eventTypes(onEvents), eventTypes(offEvents))
			}
			if onLease.completed != offLease.completed || onLease.failed != offLease.failed || onLease.retried != offLease.retried {
				t.Fatalf("lease outcome differs: on=%+v off=%+v", onLease, offLease)
			}
		})
	}
}

func TestStreamIngestOffNeverCallsStreamWorker(t *testing.T) {
	t.Setenv("UBAG_WORKER_STREAM_INGEST", "")
	runner := &dualRunner{batch: streamCases()["completed"]}
	final, _, _ := runIngestWith(t, runner)
	if final.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %s", final.Status)
	}
	if runner.streamCall.Load() != 0 || runner.batchCalls.Load() != 1 {
		t.Fatalf("stream=%d batch=%d, want 0/1", runner.streamCall.Load(), runner.batchCalls.Load())
	}
}

// A stream that stops without a terminal event must fail the job, never
// complete it.
func TestStreamIngestStreamWithoutTerminalFails(t *testing.T) {
	t.Setenv("UBAG_WORKER_STREAM_INGEST", "1")
	runner := &dualRunner{batch: streamCases()["no terminal"]}
	final, _, events := runIngestWith(t, runner)
	if final.Status != jobstore.StatusFailedRetryable {
		t.Fatalf("status = %s, want %s (events %s)", final.Status, jobstore.StatusFailedRetryable, eventTypes(events))
	}
}

func TestCollectingSinkBoundsEventsAndRequiresType(t *testing.T) {
	sink := &collectingSink{}
	if err := sink.Emit(context.Background(), jobstore.WorkerEvent{}); err == nil {
		t.Fatal("expected missing-type error")
	}
	for i := 0; i < maxWorkerEvents; i++ {
		if err := sink.Emit(context.Background(), jobstore.WorkerEvent{Type: "token"}); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	if err := sink.Emit(context.Background(), jobstore.WorkerEvent{Type: "token"}); err == nil {
		t.Fatal("expected event-count bound error")
	}
}

// --- daemon streaming protocol ----------------------------------------------

func TestStreamDaemonJobEmitsEventsInOrder(t *testing.T) {
	stdout := bufio.NewReader(strings.NewReader(
		daemonEventLine(t, 1, "queued") + "\n" + daemonEventLine(t, 2, "completed") + "\n" + daemonEndLine(t, "completed", "") + "\n"))
	var stdin strings.Builder
	sink := &collectingSink{}
	if err := streamDaemonJob(context.Background(), &stdin, stdout, daemonTestEnvelope(), time.Minute, sink); err != nil {
		t.Fatalf("streamDaemonJob: %v", err)
	}
	if len(sink.events) != 2 || sink.events[0].Type != "queued" || sink.events[1].Type != "completed" {
		t.Fatalf("unexpected events: %+v", sink.events)
	}
}

// A stream that ends without __ubag_job_end__ must fail even though valid
// events (including a terminal one) were already emitted to the sink.
func TestStreamDaemonJobWithoutMarkerFails(t *testing.T) {
	stdout := bufio.NewReader(strings.NewReader(daemonEventLine(t, 1, "completed") + "\n"))
	var stdin strings.Builder
	sink := &collectingSink{}
	err := streamDaemonJob(context.Background(), &stdin, stdout, daemonTestEnvelope(), time.Minute, sink)
	if err == nil || !strings.Contains(err.Error(), "without a terminal marker") {
		t.Fatalf("expected missing-marker error, got %v", err)
	}
}

func TestStreamDaemonJobRejectsMalformedLineAndFailedMarker(t *testing.T) {
	var stdin strings.Builder
	bad := bufio.NewReader(strings.NewReader("{not-json}\n"))
	if err := streamDaemonJob(context.Background(), &stdin, bad, daemonTestEnvelope(), time.Minute, &collectingSink{}); err == nil ||
		!strings.Contains(err.Error(), "malformed JSONL") {
		t.Fatalf("expected malformed error, got %v", err)
	}
	failed := bufio.NewReader(strings.NewReader(daemonEventLine(t, 1, "queued") + "\n" + daemonEndLine(t, "failed", "provider blew up") + "\n"))
	if err := streamDaemonJob(context.Background(), &stdin, failed, daemonTestEnvelope(), time.Minute, &collectingSink{}); err == nil ||
		!strings.Contains(err.Error(), "provider blew up") {
		t.Fatalf("expected failed-marker error, got %v", err)
	}
}

func TestDaemonRunnerStreamsThroughRealProcess(t *testing.T) {
	var spawns int32
	runner := helperDaemonRunner(t, &spawns)
	sink := &collectingSink{}
	if err := runner.StreamWorker(context.Background(), daemonTestEnvelope(), sink); err != nil {
		t.Fatalf("StreamWorker: %v", err)
	}
	if len(sink.events) != 1 || sink.events[0].Type != "completed" {
		t.Fatalf("unexpected events: %+v", sink.events)
	}
}
