package executor

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// EventSink receives worker events as a runner produces them. Events handed to
// a sink are PROVISIONAL: the consumer treats them as final only after the run
// ends without error and with exactly one valid terminal event (the same rule
// the batch path enforces on the whole list). The WorkerConsumer's sink
// (streamIngest) applies the non-terminal ones to the job store immediately and
// holds the terminal back until the run ends cleanly.
type EventSink interface {
	Emit(ctx context.Context, event jobstore.WorkerEvent) error
}

// StreamingWorkerRunner is the optional incremental sibling of WorkerRunner. A
// nil error means the run reached a clean end (e.g. the daemon's terminal
// marker); a stream that just stops must return an error, never nil. Emit is
// called serially, and never after StreamWorker has returned.
type StreamingWorkerRunner interface {
	StreamWorker(ctx context.Context, envelope DispatchEnvelope, sink EventSink) error
}

// streamSelector is the optional capability of a runner that streams only some
// jobs: the serve router streams warm-daemon targets and keeps per-job worker
// targets on the batch path. A runner without it streams every job.
type streamSelector interface {
	StreamsJob(envelope DispatchEnvelope) bool
}

// BatchStreamAdapter lifts a batch WorkerRunner into a StreamingWorkerRunner:
// it runs the batch call and then emits the resulting events in order.
type BatchStreamAdapter struct{ Runner WorkerRunner }

func (a BatchStreamAdapter) StreamWorker(ctx context.Context, envelope DispatchEnvelope, sink EventSink) error {
	events, err := a.Runner.RunWorker(ctx, envelope)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := sink.Emit(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

// streamIngestEnabled gates the streaming runner path (default off).
func streamIngestEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("UBAG_WORKER_STREAM_INGEST"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// warnStreamWithoutStrictSubmit logs once per process.
var warnStreamWithoutStrictSubmit sync.Once

// streamsLive reports whether this job takes the streaming path: the flag is on,
// the runner streams, and (when the runner can say) it streams this job.
func (c *WorkerConsumer) streamsLive(envelope DispatchEnvelope) bool {
	if !streamIngestEnabled() {
		return false
	}
	if _, ok := c.Runner.(StreamingWorkerRunner); !ok {
		return false
	}
	if sel, ok := c.Runner.(streamSelector); ok && !sel.StreamsJob(envelope) {
		return false
	}
	warnStreamWithoutStrictSubmit.Do(func() {
		if !strictSubmitEnabled() {
			slog.Warn("UBAG_WORKER_STREAM_INGEST is on without UBAG_WORKER_STRICT_SUBMIT: " +
				"a job interrupted after tokens were streamed can be replayed from the queue; set both")
		}
	})
	return true
}

// runWorker is the consumer's single call site into the runner. Without a sink
// it is the untouched RunWorker path. With one (streamsLive) the runner streams
// into it and the result is the single held terminal event, which the batch
// ingestion then applies like the last event of any other run.
func (c *WorkerConsumer) runWorker(ctx context.Context, envelope DispatchEnvelope, ingest *streamIngest) ([]jobstore.WorkerEvent, error) {
	if ingest == nil {
		return c.Runner.RunWorker(ctx, envelope)
	}
	if err := c.Runner.(StreamingWorkerRunner).StreamWorker(ctx, envelope, ingest); err != nil {
		return nil, err
	}
	return ingest.finish()
}
