package executor

import (
	"context"
	"fmt"
	"os"
	"strings"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// EventSink receives worker events as a runner produces them. Events handed to
// a sink are PROVISIONAL: the consumer treats them as final only after the run
// ends without error and with exactly one valid terminal event (the same rule
// the batch path enforces on the whole list).
type EventSink interface {
	Emit(ctx context.Context, event jobstore.WorkerEvent) error
}

// StreamingWorkerRunner is the optional incremental sibling of WorkerRunner. A
// nil error means the run reached a clean end (e.g. the daemon's terminal
// marker); a stream that just stops must return an error, never nil.
type StreamingWorkerRunner interface {
	StreamWorker(ctx context.Context, envelope DispatchEnvelope, sink EventSink) error
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

// collectingSink buffers streamed events under the same event-count bound as
// the batch parser. ponytail: the byte budget stays with the runner's stdout
// bound; per-attempt event/byte budgets arrive with incremental apply (P2.2).
type collectingSink struct{ events []jobstore.WorkerEvent }

func (s *collectingSink) Emit(_ context.Context, event jobstore.WorkerEvent) error {
	if event.Type == "" {
		return fmt.Errorf("worker event %d is missing type", len(s.events)+1)
	}
	if len(s.events) >= maxWorkerEvents {
		return fmt.Errorf("worker emitted more than %d events", maxWorkerEvents)
	}
	s.events = append(s.events, event)
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

// runWorker is the consumer's single call site into the runner. Flag off (or a
// batch-only runner) is the untouched RunWorker path.
func (c *WorkerConsumer) runWorker(ctx context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
	if sr, ok := c.Runner.(StreamingWorkerRunner); ok && streamIngestEnabled() {
		sink := &collectingSink{events: []jobstore.WorkerEvent{}}
		if err := sr.StreamWorker(ctx, envelope, sink); err != nil {
			return nil, err
		}
		return sink.events, nil
	}
	return c.Runner.RunWorker(ctx, envelope)
}
