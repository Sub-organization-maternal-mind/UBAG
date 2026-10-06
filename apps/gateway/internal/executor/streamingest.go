package executor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// streamIngest is the WorkerConsumer's EventSink behind UBAG_WORKER_STREAM_INGEST
// (default off). It makes a streamed run PROVISIONAL UNTIL ONE VALID TERMINAL:
//
//   - non-terminal events are applied to the job store as they arrive, so a
//     reader sees tokens while the worker is still running;
//   - telemetry (concurrency, topology, conversation, session) is intercepted
//     exactly as on the batch path and never applied as a job event;
//   - the single terminal event is HELD, never applied while the run is in
//     flight. Only after the runner reports a clean end (the daemon's JOB_END
//     marker) with exactly one valid terminal does the consumer apply it. A
//     stream that dies, stops without the marker, ends failed, or carries zero
//     or two terminals is ABANDONED: the held terminal is dropped, the tokens
//     already applied stay in the history as that attempt's discarded partial,
//     and the consumer appends the usual failure event. The job can therefore
//     never complete on a truncated or ambiguous stream.
//
// Events stamped (data.attempt_id) by another attempt are dropped, as on the
// batch path. Emit is called serially by the runner and the runner returns
// before the consumer reads the sink's state, so it needs no lock.
//
// ponytail: the 512-event and 1 MiB bounds stay as the stream's caps (an
// overrun fails the attempt, it never truncates). A per-attempt byte budget,
// batched applies and token coalescing belong to P3.11.
type streamIngest struct {
	c        *WorkerConsumer
	job      jobstore.Job
	envelope DispatchEnvelope

	events    int                   // events delivered by the runner, stale ones included
	terminal  *jobstore.WorkerEvent // the held terminal, nil until one arrives
	submitted bool                  // a prompt_submitted event went by
	spent     time.Duration         // time inside Emit: store writes and projections
}

// streamIngestError is a failure the ingestion itself raised while the stream
// was running. It carries the batch path's ingestion-metric class, and keeps
// the cause in the chain so errors.Is(err, context.Canceled) still works.
type streamIngestError struct {
	class      string
	events     int
	missingJob bool
	err        error
}

func (e *streamIngestError) Error() string { return e.err.Error() }
func (e *streamIngestError) Unwrap() error { return e.err }

// fail types the error from the in-process submission state, like the runner
// does for its own failures (UBAG_WORKER_STRICT_SUBMIT; flag off: unchanged).
func (s *streamIngest) fail(class string, err error) *streamIngestError {
	return &streamIngestError{class: class, events: s.events, err: classifySubmission(err, s.submitted)}
}

// Emit implements EventSink.
func (s *streamIngest) Emit(ctx context.Context, event jobstore.WorkerEvent) error {
	started := time.Now()
	defer func() { s.spent += time.Since(started) }()

	s.events++
	if s.events > maxWorkerEvents {
		return s.fail("invalid_event", fmt.Errorf("worker emitted more than %d events", maxWorkerEvents))
	}
	if event.Type == "" {
		return s.fail("invalid_event", fmt.Errorf("worker event %d is missing type", s.events))
	}
	if staleAttemptEvent(s.envelope, event) {
		return nil
	}
	normalized, err := normalizeWorkerEvent(s.envelope, event)
	if err != nil {
		return s.fail("invalid_event", err)
	}
	if normalized.Type == promptSubmittedEventType {
		s.submitted = true
	}
	if s.c.handleTelemetryEvent(ctx, s.job, normalized) {
		return nil
	}
	if terminalWorkerEventType(normalized.Type) {
		if s.terminal != nil {
			return s.fail("invalid_event", fmt.Errorf("worker emitted a second terminal event; expected exactly one"))
		}
		s.terminal = &normalized
		return nil
	}
	if s.terminal != nil {
		// Past the terminal only telemetry means anything; the store drops the
		// rest of a finished job's events too.
		return nil
	}
	_, found, err := s.c.Jobs.ApplyWorkerEvent(ctx, normalized)
	if err != nil {
		slog.Error("ApplyWorkerEvent failed", "job_id", normalized.JobID, "event_type", normalized.Type, "error", err)
		return s.fail("store", err)
	}
	if !found {
		failure := s.fail("missing_job", fmt.Errorf("worker event referenced missing job %s", normalized.JobID))
		failure.missingJob = true
		return failure
	}
	s.c.afterEventApplied(ctx, s.job, normalized)
	return nil
}

// finish is called once the runner reported a clean end. It returns the held
// terminal as the run's only remaining event, or fails the attempt when the
// stream did not carry exactly one terminal.
func (s *streamIngest) finish() ([]jobstore.WorkerEvent, error) {
	if s.terminal == nil {
		if s.events == 0 {
			return nil, s.fail("empty_result", fmt.Errorf("worker emitted no events"))
		}
		return nil, s.fail("missing_terminal", fmt.Errorf("worker emitted 0 terminal events; expected exactly one"))
	}
	return []jobstore.WorkerEvent{*s.terminal}, nil
}
