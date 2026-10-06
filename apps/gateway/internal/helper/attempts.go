package helper

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

const (
	// Output bounds per attempt. They mirror executor.DefaultHelperMax* (the
	// primary's ingest limits, P4.9: a test pins them equal) so an attempt that
	// would be refused at the primary fails here, at the source, instead of
	// being buffered. Over-limit output fails the attempt; it is never truncated
	// into a success.
	maxEventDataBytes = 60 * 1024
	maxAttemptBytes   = 4 << 20
	maxAttemptEvents  = 4096

	maxErrorMessageBytes = 512
)

// Why an attempt was stopped from outside. The first cause wins.
var (
	errCancelled    = errors.New("attempt cancelled")
	errDrainGrace   = errors.New("drain grace elapsed")
	errDeadline     = errors.New("attempt deadline reached")
	errLeaseExpired = errors.New("attempt lease expired")
	errSuperseded   = errors.New("attempt superseded by a newer lease generation")
	errShutdown     = errors.New("helper shutting down")

	// errAttemptEnded is what emit returns once the attempt takes no more events.
	errAttemptEnded = errors.New("attempt has ended")
)

// Fixed, sanitized error messages for failures the service itself writes. The
// runner's own error text is never forwarded (it can carry page content).
var failureMessages = map[string]string{
	"helper_lease_expired":      "the attempt lease expired without renewal",
	"helper_attempt_superseded": "a newer lease generation replaced this attempt",
	"helper_shutdown":           "the helper node is shutting down",
	"helper_runner_error":       "the runner stopped with an error",
	"helper_runner_crash":       "the runner crashed",
	"helper_runner_no_terminal": "the runner returned without a terminal event",
	"helper_output_limit":       "attempt output exceeded the helper limits",
	"helper_event_invalid":      "the runner produced an invalid event",
}

// attempt is one leased execution. Its life is tied to its lease, not to any
// RunAttempt stream: events go into a bounded log that streams read from, so a
// broken stream loses nothing and a re-attach replays the log from sequence 1.
type attempt struct {
	s           *Server
	id, jobID   string
	gen         uint64
	fingerprint string
	workload    string
	key         identityKey
	spec        AttemptSpec

	ctx    context.Context
	cancel context.CancelCauseFunc

	mu            sync.Mutex
	state         helperv1.AttemptState
	leaseExpiry   time.Time
	leaseTimer    Timer
	deadlineTimer Timer
	killTimer     Timer
	cause         error // first external stop reason
	superseded    bool
	submitted     bool
	manualAction  bool
	tokens        int
	events        []*helperv1.AttemptEvent
	bytes         int
	outcome       *helperv1.AttemptOutcome
	finishedAt    time.Time
	released      bool          // Run returned and the identity slot was freed
	changed       chan struct{} // closed and replaced on every append
	done          chan struct{} // closed when released
}

func (a *attempt) now() time.Time { return a.s.clock.Now() }

// armLease (re)starts the self-expiry timer. Callers hold a.mu.
func (a *attempt) armLease() {
	d := max(a.leaseExpiry.Sub(a.now()), 0)
	if a.leaseTimer == nil {
		a.leaseTimer = a.s.clock.AfterFunc(d, a.onLeaseTimer)
		return
	}
	a.leaseTimer.Reset(d)
}

// onLeaseTimer is the local self-expiry: an attempt whose lease was not
// renewed is killed by the helper itself, with no help from the primary (the
// primary may be unreachable, which is exactly when this matters).
func (a *attempt) onLeaseTimer() {
	a.mu.Lock()
	if a.outcome != nil || a.cause != nil {
		a.mu.Unlock()
		return
	}
	if a.now().Before(a.leaseExpiry) { // renewed after this timer was armed
		a.armLease()
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	a.kill(errLeaseExpired)
}

// extendLease moves the lease forward (never back), clamped to now+LeaseMaxTTL.
// It reports false when the attempt can no longer be renewed: its lease already
// lapsed (a late renew must not resurrect it), or it is being stopped.
func (a *attempt) extendLease(want time.Time) (expiry time.Time, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.outcome != nil {
		return a.leaseExpiry, true
	}
	now := a.now()
	if a.cause != nil || !now.Before(a.leaseExpiry) {
		return a.leaseExpiry, false
	}
	if limit := now.Add(a.s.cfg.LeaseMaxTTL); want.After(limit) {
		want = limit
	}
	if want.After(a.leaseExpiry) {
		a.leaseExpiry = want
		a.armLease()
	}
	return a.leaseExpiry, true
}

// kill stops the attempt from outside: it cancels the runner and, if the
// runner does not end by itself within KillGrace, writes the terminal anyway.
// The identity slot stays held until Run really returns, so a runner that
// ignores its context can never overlap a successor on the same identity.
func (a *attempt) kill(cause error) {
	a.mu.Lock()
	if a.outcome != nil || a.cause != nil {
		a.mu.Unlock()
		return
	}
	a.cause = cause
	if a.leaseTimer != nil {
		a.leaseTimer.Stop()
	}
	if a.deadlineTimer != nil {
		a.deadlineTimer.Stop()
	}
	a.killTimer = a.s.clock.AfterFunc(a.s.cfg.KillGrace, func() { a.finalize(nil, false) })
	a.mu.Unlock()
	a.cancel(cause)
}

// emit is the EmitFunc handed to the runner.
func (a *attempt) emit(ev Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.outcome != nil {
		return errAttemptEnded
	}
	terminal := ev.Type == helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL
	switch {
	case ev.Type <= helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_UNSPECIFIED || ev.Type > helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL,
		terminal && (ev.Outcome == nil || ev.Outcome.GetStatus() == helperv1.AttemptStatus_ATTEMPT_STATUS_UNSPECIFIED),
		!terminal && ev.Outcome != nil:
		return a.failLocked("helper_event_invalid", "invalid_event")
	case len(ev.DataJSON) > maxEventDataBytes, len(ev.Outcome.GetResultJson()) > maxEventDataBytes,
		!terminal && len(a.events) >= maxAttemptEvents-1: // keep one slot for the terminal
		return a.failLocked("helper_output_limit", "output_limit")
	}
	switch ev.Type {
	case helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_PROMPT_SUBMITTED:
		// The submission boundary is recorded before the event is visible, so
		// Inspect can never say "not submitted" about an attempt whose stream
		// already carried the boundary (decision D4).
		a.submitted = true
		a.state = helperv1.AttemptState_ATTEMPT_STATE_SUBMITTED
	case helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TOKEN:
		a.tokens++
	case helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_MANUAL_ACTION_REQUIRED:
		a.manualAction = true
	}
	if terminal {
		a.appendTerminalLocked(ev.Outcome, ev.DataJSON)
		return nil
	}
	if a.bytes+len(ev.DataJSON) > maxAttemptBytes {
		return a.failLocked("helper_output_limit", "output_limit")
	}
	a.appendLocked(ev.Type, ev.DataJSON, nil)
	return nil
}

// failLocked ends the attempt `failed` with a sanitized, service-written
// message and tells the runner to stop.
func (a *attempt) failLocked(code, reason string) error {
	a.appendTerminalLocked(&helperv1.AttemptOutcome{
		Status: helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED, ErrorCode: code, StreamEndReason: reason,
		ErrorMessage: failureMessages[code],
	}, "")
	return fmt.Errorf("%w: %s", errAttemptEnded, code)
}

// appendTerminalLocked normalises the outcome against what the helper itself
// saw, then writes it. The helper's own knowledge of the submission boundary
// wins over the runner's claim (decision D4): a failure after the prompt was
// submitted is `submitted` and needs reconciliation, never a blind resubmit.
func (a *attempt) appendTerminalLocked(o *helperv1.AttemptOutcome, dataJSON string) {
	out := proto.Clone(o).(*helperv1.AttemptOutcome)
	out.Submitted = out.Submitted || a.submitted
	if out.Status == helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED && out.Submitted {
		out.ReconcileRequired = true
	}
	if len(out.ErrorMessage) > maxErrorMessageBytes {
		out.ErrorMessage = strings.ToValidUTF8(out.ErrorMessage[:maxErrorMessageBytes], "")
	}
	a.outcome = out
	a.state = helperv1.AttemptState_ATTEMPT_STATE_FINISHED
	a.finishedAt = a.now()
	for _, t := range []Timer{a.leaseTimer, a.deadlineTimer, a.killTimer} {
		if t != nil {
			t.Stop()
		}
	}
	a.appendLocked(helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL, dataJSON, out)
}

func (a *attempt) appendLocked(t helperv1.AttemptEventType, dataJSON string, o *helperv1.AttemptOutcome) {
	ev := &helperv1.AttemptEvent{
		AttemptId:       a.id,
		Sequence:        uint64(len(a.events)) + 1,
		LeaseGeneration: a.gen,
		Type:            t,
		CreatedAt:       timestamppb.New(a.now()),
		DataJson:        dataJSON,
		Outcome:         o,
	}
	a.bytes += proto.Size(ev)
	a.events = append(a.events, ev)
	close(a.changed)
	a.changed = make(chan struct{})
}

// finalize makes sure the attempt has a terminal event: it is called when Run
// returns and, for an attempt that was killed, when KillGrace elapses.
func (a *attempt) finalize(runErr error, panicked bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.outcome != nil {
		return
	}
	failed := func(code string) *helperv1.AttemptOutcome {
		return &helperv1.AttemptOutcome{
			Status: helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED, ErrorCode: code, ErrorMessage: failureMessages[code],
		}
	}
	var o *helperv1.AttemptOutcome
	switch {
	case errors.Is(a.cause, errCancelled):
		o = &helperv1.AttemptOutcome{Status: helperv1.AttemptStatus_ATTEMPT_STATUS_CANCELLED, StreamEndReason: "cancelled"}
	case errors.Is(a.cause, errDrainGrace):
		o = &helperv1.AttemptOutcome{Status: helperv1.AttemptStatus_ATTEMPT_STATUS_CANCELLED, StreamEndReason: "drain_grace_elapsed"}
	case errors.Is(a.cause, errDeadline):
		// A deadline cut is timed_out with partial output flagged and never a
		// result (decision D4).
		o = &helperv1.AttemptOutcome{
			Status: helperv1.AttemptStatus_ATTEMPT_STATUS_TIMED_OUT, StreamEndReason: "deadline", Partial: a.tokens > 0,
		}
	case errors.Is(a.cause, errLeaseExpired):
		o = failed("helper_lease_expired")
		o.StreamEndReason = "lease_expired"
	case errors.Is(a.cause, errSuperseded):
		o = failed("helper_attempt_superseded")
		o.StreamEndReason = "superseded"
	case errors.Is(a.cause, errShutdown):
		o = failed("helper_shutdown")
		o.StreamEndReason = "shutdown"
	case panicked:
		o = failed("helper_runner_crash")
		o.StreamEndReason = "runner_crash"
	case runErr != nil:
		o = failed("helper_runner_error")
		o.StreamEndReason = "runner_error"
	default:
		o = failed("helper_runner_no_terminal")
		o.StreamEndReason = "runner_no_terminal"
	}
	a.appendTerminalLocked(o, "")
}

// run drives the runner for one attempt, in its own goroutine.
func (s *Server) run(a *attempt) {
	defer s.wg.Done()
	var runErr error
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				s.log.Error("helper runner panicked", "job_id", a.jobID, "attempt_id", a.id, "panic", truncate(fmt.Sprint(r), 200))
			}
		}()
		a.mu.Lock()
		if a.outcome == nil && a.state == helperv1.AttemptState_ATTEMPT_STATE_ACCEPTED {
			a.state = helperv1.AttemptState_ATTEMPT_STATE_RUNNING
		}
		a.mu.Unlock()
		runErr = s.cfg.Runner.Run(a.ctx, a.spec, a.emit)
	}()
	a.finalize(runErr, panicked)

	a.mu.Lock()
	manual := a.manualAction && a.outcome.GetStatus() != helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED
	status, code := a.outcome.GetStatus(), a.outcome.GetErrorCode()
	a.mu.Unlock()
	s.gate.release(a.key, a.id, manual, s.clock.Now())
	a.cancel(nil)
	a.mu.Lock()
	a.released = true
	a.mu.Unlock()
	close(a.done)
	s.log.Info("helper attempt ended", "job_id", a.jobID, "attempt_id", a.id, "generation", a.gen, "status", status.String(), "error_code", code)
}

// view is a consistent snapshot of an attempt for Inspect/Renew/Cancel.
type view struct {
	state       helperv1.AttemptState
	gen         uint64
	expiry      time.Time
	fingerprint string
	lastSeq     uint64
	submitted   bool
	outcome     *helperv1.AttemptOutcome
}

func (a *attempt) view() view {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := view{
		state: a.state, gen: a.gen, expiry: a.leaseExpiry, fingerprint: a.fingerprint,
		lastSeq: uint64(len(a.events)), submitted: a.submitted,
	}
	if a.outcome != nil {
		v.outcome = proto.Clone(a.outcome).(*helperv1.AttemptOutcome)
	}
	return v
}

// next returns the events from index `from`, the channel that closes when more
// arrive, and whether the terminal is already in the log.
func (a *attempt) next(from int) (evs []*helperv1.AttemptEvent, more <-chan struct{}, finished bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.events[from:], a.changed, a.outcome != nil
}

func (a *attempt) finished() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.outcome != nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}
