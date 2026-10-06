package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// ubag_event_wakeups_total{source} values. "notify" is reserved for the
// deferred Postgres LISTEN/NOTIFY hub and is never emitted today.
const (
	sseWakeLocal    = "local"
	sseWakeNotify   = "notify"
	sseWakeFallback = "fallback"
)

const (
	sseReplayPage = 100
	// A Last-Event-ID is resolved by scanning the job's history; bound it so a
	// pathological job cannot turn one reconnect into an unbounded read.
	sseResumeScanPage = 500
	sseResumeScanMax  = 20000
	sseLastEventIDMax = 256
)

// sseFallbackInterval is the safety-net re-read while the store's wake hub is
// on (a dropped wake or another gateway's write). A var only so tests can
// shrink it.
var sseFallbackInterval = jobstore.DefaultEventFallbackInterval

// sseTerminalEvent reports whether an event ends the job for an SSE consumer.
// failed/failed_retryable are terminal only when explicitly non-retryable: a
// retryable failure may be followed by another attempt on the same stream.
func sseTerminalEvent(event jobstore.Event) bool {
	switch event.Type {
	case "completed", "completed_with_warnings", "failed_terminal", "dead_letter",
		"cancelled", "canceled", "timed_out", "timeout":
		return true
	case "failed":
		retryable, ok := event.Data["retryable"].(bool)
		return ok && !retryable
	}
	return false
}

// resolveSSEResume finds the event named by Last-Event-ID in the job history.
// ok=false means the id is unknown (or beyond the scan bound) and the caller
// replays from the start, which is what a client without the header gets.
func (s *Server) resolveSSEResume(ctx context.Context, jobID, lastEventID string) (jobstore.Event, bool, error) {
	after := 0
	for scanned := 0; scanned < sseResumeScanMax; {
		page, _, err := s.jobs.ListEvents(ctx, jobID, after, sseResumeScanPage)
		if err != nil {
			return jobstore.Event{}, false, err
		}
		for _, event := range page {
			if event.ID == lastEventID {
				return event, true, nil
			}
			after = event.Sequence
		}
		if len(page) < sseResumeScanPage {
			break
		}
		scanned += len(page)
	}
	return jobstore.Event{}, false, nil
}

// sseTerminalAtOrBefore reports whether the job's log holds a terminal event
// with a sequence at or below cursor, i.e. a client resuming at cursor has seen
// the end. The caller already knows nothing follows the cursor, so this only
// runs on that rare path; the scan is bounded like resolveSSEResume.
func (s *Server) sseTerminalAtOrBefore(ctx context.Context, jobID string, cursor int) (bool, error) {
	after := 0
	for scanned := 0; scanned < sseResumeScanMax; {
		page, _, err := s.jobs.ListEvents(ctx, jobID, after, sseResumeScanPage)
		if err != nil {
			return false, err
		}
		for _, event := range page {
			if event.Sequence > cursor {
				return false, nil
			}
			if sseTerminalEvent(event) {
				return true, nil
			}
			after = event.Sequence
		}
		if len(page) < sseResumeScanPage {
			break
		}
		scanned += len(page)
	}
	return false, nil
}

func (s *Server) countSSEWakeup(source string) {
	s.metrics.mu.Lock()
	s.metrics.sseWakeups[source]++
	s.metrics.mu.Unlock()
}

func (s *Server) observeSSEFrameLag(event jobstore.Event) {
	if event.CreatedAt.IsZero() {
		return
	}
	lag := time.Since(event.CreatedAt)
	if lag < 0 {
		lag = 0
	}
	s.metrics.mu.Lock()
	s.metrics.sseFrameLag = addDuration(s.metrics.sseFrameLag, lag)
	s.metrics.mu.Unlock()
}

// waitSSEEventsHub is the hub-on wait: it re-reads only on a wake or at the
// fallback cadence and returns (nil, nil) at the heartbeat tick. The caller
// subscribed to wake BEFORE its first read, so a commit between that read and
// this wait is never lost.
func (s *Server) waitSSEEventsHub(ctx context.Context, jobID string, after int, wake <-chan struct{}) ([]jobstore.Event, error) {
	heartbeat := time.NewTimer(sseHeartbeatInterval)
	defer heartbeat.Stop()
	fallback := time.NewTimer(sseFallbackInterval)
	defer fallback.Stop()
	for {
		events, _, err := s.jobs.ListEvents(ctx, jobID, after, sseReplayPage)
		if err != nil || len(events) > 0 {
			return events, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-heartbeat.C:
			return nil, nil
		case <-wake:
			s.countSSEWakeup(sseWakeLocal)
		case <-fallback.C:
			s.countSSEWakeup(sseWakeFallback)
			fallback.Reset(sseFallbackInterval)
		}
	}
}

// handleJobSSE implements GET /v1/sse/jobs/{id}. It replays history, then
// waits for new events (hub wake when the store has one, else WaitEvents).
// Last-Event-ID and the after_sequence query both resume the stream; the larger
// resolved cursor wins and a malformed or negative after_sequence is a 400. A
// cursor at or past the job's terminal event gets 204 whatever the flags, which
// stops EventSource from reconnecting. With UBAG_SSE_CLOSE_ON_TERMINAL the
// stream additionally ends right after the first terminal event it sends.
func (s *Server) handleJobSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}

	segments := splitRouteTail(r.URL.Path, "/v1/sse/jobs/")
	if len(segments) != 1 || segments[0] == "" {
		s.writeNotFound(w, r)
		return
	}

	job, ok := s.loadAuthorizedJob(w, r, segments[0], "job:read")
	if !ok {
		return
	}

	if !s.tryIncrementSSEConnections() {
		w.Header().Set("Retry-After", "5")
		e := queueError("UBAG-OVERLOAD-SSE-STREAMS-001", "too many open event streams; retry later", true)
		e.RetryAfterMS = ptrInt(5 * 1000)
		s.writeError(w, r, http.StatusServiceUnavailable, e)
		return
	}
	defer s.decrementSSEConnections()

	// Subscribe before the first read (lost-wake rule); auth already passed.
	var wake <-chan struct{}
	hubOn := false
	if waker, ok := s.jobs.(jobstore.JobWaker); ok {
		if ch, cancel, on := waker.SubscribeJobWake(job.ID); on {
			wake, hubOn = ch, true
			defer cancel()
		}
	}

	afterSequence := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("after_sequence")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-EVENT-SEQUENCE-001", "after_sequence must be a non-negative integer"))
			return
		}
		afterSequence = parsed
	}
	if lastID := strings.TrimSpace(r.Header.Get("Last-Event-ID")); lastID != "" && len(lastID) <= sseLastEventIDMax {
		seen, found, err := s.resolveSSEResume(r.Context(), job.ID, lastID)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load job events"))
			return
		}
		if found && seen.Sequence > afterSequence {
			afterSequence = seen.Sequence
		}
	}

	events, found, err := s.jobs.ListEvents(r.Context(), job.ID, afterSequence, sseReplayPage)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load job events"))
		return
	}
	if !found {
		s.writeJobNotFound(w, r)
		return
	}
	if len(events) == 0 && afterSequence > 0 {
		done, err := s.sseTerminalAtOrBefore(r.Context(), job.ID, afterSequence)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load job events"))
			return
		}
		if done {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	controller := http.NewResponseController(w)
	// Bounded write: a stalled client (full TCP buffer, dead peer) must be
	// dropped, not stall the event loop.
	writeFrame := func(frame string) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
		if _, err := fmt.Fprint(w, frame); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	traceID := traceIDFromContext(r.Context())
	// emit writes a batch; false means stop the stream (write failure or a
	// terminal event with close-on-terminal on).
	emit := func(batch []jobstore.Event, live bool) bool {
		for _, event := range batch {
			payload, _ := json.Marshal(jobEventToResponse(event, traceID))
			if !writeFrame(fmt.Sprintf("id: %s\nevent: job.%s\ndata: %s\n\n", event.ID, event.Type, payload)) {
				return false
			}
			if live {
				s.observeSSEFrameLag(event)
			}
			afterSequence = event.Sequence
			if s.sseCloseOnTerminal && sseTerminalEvent(event) {
				return false
			}
		}
		return true
	}

	if !emit(events, false) {
		return
	}
	if strings.EqualFold(r.URL.Query().Get("snapshot"), "true") {
		return
	}

	// Per-connection deadline: close after the TTL; SSE clients reconnect and
	// resume from the last event they saw.
	expiresAt := time.Now().Add(sseConnectionTTL)
	for time.Now().Before(expiresAt) {
		var next []jobstore.Event
		if hubOn {
			next, err = s.waitSSEEventsHub(r.Context(), job.ID, afterSequence, wake)
		} else {
			// Bounded wait: WaitEvents returns as soon as events land, and the
			// window expiry doubles as the heartbeat tick, so an idle stream
			// emits a ": ping" comment every interval instead of staying silent
			// (silent connections get killed by proxies and look dead to clients).
			waitCtx, cancel := context.WithTimeout(r.Context(), sseHeartbeatInterval)
			next, _, err = s.jobs.WaitEvents(waitCtx, job.ID, afterSequence, sseReplayPage)
			cancel()
			if len(next) > 0 {
				// No hub: delivery came from the store's own wait/poll.
				s.countSSEWakeup(sseWakeFallback)
			}
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			// Client went away (Canceled) or the store failed: close the
			// stream; the client reconnects and resumes.
			return
		}
		if !emit(next, true) {
			return
		}
		if len(next) == 0 && !writeFrame(": ping\n\n") {
			return
		}
	}
}
