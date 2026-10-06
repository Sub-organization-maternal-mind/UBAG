package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
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
// batch path.
//
// Applies are BATCHED and tokens COALESCED. A flush writes everything pending
// to the store in one ApplyWorkerEvents call (one lock, one commit, one waiter
// wake). Token deltas that arrive between flushes merge into one token event
// (text concatenated, id and index of the first, data.coalesced_deltas counting
// the deltas), so a fast stream costs one write per flush interval rather than
// one per delta. The first token after a quiet period is written immediately, so
// time-to-first-token is unchanged; any non-token event flushes at once; a timer
// flushes a trailing token. The run's end, clean or not, flushes what is pending
// before the terminal is applied or the failure recorded, so history order is
// the worker's order.
//
// The stream is bounded by a per-attempt BYTE BUDGET (UBAG_WORKER_STREAM_MAX_BYTES)
// instead of the batch path's 512-event / 1 MiB caps. Every delivered event
// costs its data size plus a fixed overhead, stale and telemetry events
// included, so the budget also bounds the event count. An overrun fails the
// attempt (class over_budget); it never truncates.
type streamIngest struct {
	c        *WorkerConsumer
	job      jobstore.Job
	envelope DispatchEnvelope

	mu        sync.Mutex            // Emit is serial, the flush timer is not
	events    int                   // events delivered by the runner, stale ones included
	terminal  *jobstore.WorkerEvent // the held terminal, nil until one arrives
	submitted bool                  // a prompt_submitted event went by
	spent     time.Duration         // time inside Emit and the flush timer: store writes and projections

	configured bool
	budget     int                    // per-attempt byte budget
	interval   time.Duration          // coalescing window; 0 writes every event as it arrives
	usedBytes  int                    // bytes charged against the budget
	pending    []jobstore.WorkerEvent // normalized events not yet in the store
	lastFlush  time.Time
	timer      *time.Timer
	ctx        context.Context    // the run's context, for the timer's flush
	closed     bool               // the run is over: the timer does nothing more
	flushErr   *streamIngestError // a timer flush failed; the next Emit or close reports it
}

const (
	defaultStreamByteBudget    = 8 << 20
	defaultStreamFlushInterval = 50 * time.Millisecond
	streamEventOverheadBytes   = 256      // fixed budget cost per delivered event
	maxCoalescedTokenBytes     = 16 << 10 // a merged token stays far under the store's 64 KiB data cap
	maxPendingStreamEvents     = 64       // flush before a batch grows past this
	finalFlushTimeout          = 10 * time.Second
)

func streamByteBudget() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("UBAG_WORKER_STREAM_MAX_BYTES"))); err == nil && n > 0 {
		return n
	}
	return defaultStreamByteBudget
}

// streamFlushInterval reads UBAG_WORKER_STREAM_FLUSH_MS, the coalescing window
// in milliseconds. 0 turns coalescing off.
func streamFlushInterval() time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("UBAG_WORKER_STREAM_FLUSH_MS"))); err == nil && n >= 0 {
		return time.Duration(n) * time.Millisecond
	}
	return defaultStreamFlushInterval
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
	s.mu.Lock()
	defer s.mu.Unlock()
	started := time.Now()
	defer func() { s.spent += time.Since(started) }()

	if !s.configured {
		s.configured, s.budget, s.interval = true, streamByteBudget(), streamFlushInterval()
	}
	s.ctx = ctx
	if s.flushErr != nil {
		return s.flushErr
	}

	s.events++
	s.usedBytes += streamEventOverheadBytes + dataSize(event.Data)
	if s.usedBytes > s.budget {
		return s.fail("over_budget", fmt.Errorf("worker stream exceeded the %d byte budget", s.budget))
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

	s.buffer(normalized)
	// A non-token event, a full batch, no coalescing, or the first token after a
	// quiet period goes to the store now; other tokens wait for the timer.
	wait := s.interval - time.Since(s.lastFlush)
	if normalized.Type != "token" || len(s.pending) >= maxPendingStreamEvents || wait <= 0 {
		if failure := s.flush(ctx); failure != nil {
			return failure
		}
		return nil
	}
	if s.timer == nil {
		s.timer = time.AfterFunc(wait, s.flushOnTimer)
	}
	return nil
}

// buffer queues a normalized event, merging a token into a token that is
// already the newest pending event.
func (s *streamIngest) buffer(event jobstore.WorkerEvent) {
	if n := len(s.pending); n > 0 && event.Type == "token" {
		if merged, ok := mergeTokens(s.pending[n-1], event); ok {
			s.pending[n-1] = merged
			return
		}
	}
	s.pending = append(s.pending, event)
}

// mergeTokens folds next's delta text into prev. It reports false when either
// is not a plain text delta or the result would pass maxCoalescedTokenBytes.
func mergeTokens(prev, next jobstore.WorkerEvent) (jobstore.WorkerEvent, bool) {
	if prev.Type != "token" || next.Type != "token" {
		return prev, false
	}
	a, aok := tokenText(prev)
	b, bok := tokenText(next)
	if !aok || !bok || len(a)+len(b) > maxCoalescedTokenBytes {
		return prev, false
	}
	data := make(map[string]any, len(prev.Data)+1)
	for k, v := range prev.Data {
		data[k] = v
	}
	data["delta"] = map[string]any{"text": a + b}
	count, _ := data["coalesced_deltas"].(int)
	if count == 0 {
		count = 1
	}
	data["coalesced_deltas"] = count + 1
	prev.Data = data
	return prev, true
}

// tokenText returns a token event's delta text.
func tokenText(event jobstore.WorkerEvent) (string, bool) {
	delta, ok := event.Data["delta"].(map[string]any)
	if !ok {
		return "", false
	}
	text, ok := delta["text"].(string)
	return text, ok
}

// dataSize is the JSON size of an event's data, the unit of the byte budget.
func dataSize(data map[string]any) int {
	encoded, err := json.Marshal(data)
	if err != nil {
		return 0
	}
	return len(encoded)
}

// flush writes every pending event in one batch (s.mu held).
func (s *streamIngest) flush(ctx context.Context) *streamIngestError {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if len(s.pending) == 0 {
		return nil
	}
	batch := s.pending
	s.pending = nil
	s.lastFlush = time.Now()
	_, found, err := s.c.Jobs.ApplyWorkerEvents(ctx, batch)
	if err != nil {
		slog.Error("ApplyWorkerEvents failed", "job_id", batch[0].JobID, "events", len(batch), "error", err)
		return s.fail("store", err)
	}
	if !found {
		failure := s.fail("missing_job", fmt.Errorf("worker event referenced missing job %s", batch[0].JobID))
		failure.missingJob = true
		return failure
	}
	for _, event := range batch {
		s.c.afterEventApplied(ctx, s.job, event)
	}
	return nil
}

// flushOnTimer is the trailing-token flush. A failure is parked for the next
// Emit or close, which is where the run can act on it.
func (s *streamIngest) flushOnTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer = nil
	if s.closed || s.flushErr != nil {
		return
	}
	started := time.Now()
	s.flushErr = s.flush(s.ctx)
	s.spent += time.Since(started)
}

// close ends the run's ingestion: it flushes what is pending, so the history
// holds every non-terminal event the worker produced before the terminal is
// applied or the failure recorded, and stops the timer. The context outlives a
// cancelled run so the partial still lands. A flush failure is returned.
func (s *streamIngest) close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.flushErr != nil {
		return s.flushErr
	}
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalFlushTimeout)
	defer cancel()
	started := time.Now()
	failure := s.flush(flushCtx)
	s.spent += time.Since(started)
	if failure != nil {
		return failure
	}
	return nil
}

// finish is called once the runner reported a clean end and close succeeded. It
// returns the held terminal as the run's only remaining event, or fails the
// attempt when the stream did not carry exactly one terminal.
func (s *streamIngest) finish() ([]jobstore.WorkerEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		if s.events == 0 {
			return nil, s.fail("empty_result", fmt.Errorf("worker emitted no events"))
		}
		return nil, s.fail("missing_terminal", fmt.Errorf("worker emitted 0 terminal events; expected exactly one"))
	}
	return []jobstore.WorkerEvent{*s.terminal}, nil
}
