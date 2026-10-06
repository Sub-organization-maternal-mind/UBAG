package nodes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// Attempt reconciliation (UBAG_HELPER_DISPATCH, perf-fleet slice P4.18).
//
// A leased job whose attempt ledger is not empty may have a helper still running
// it, or may have submitted its prompt to a provider before the primary was lost
// (a gateway crash or restart: nothing renews its attempt lease any more). Before
// the job is placed or run again, the consumer asks the Reconciler what the ledger
// and the helper say. The Reconciler never changes anything: it answers one of
//
//   - run: nothing live and nothing submitted; start now. A new attempt takes the
//     next lease generation (BeginAttempt supersedes the lapsed one); a job that
//     stays on the gateway just runs.
//   - wait: hold the lease and ask again after RetryAfter (the old lease has not
//     lapsed yet, or the helper cannot be reached yet and the window is still open).
//   - resume: the prompt was submitted and the helper still holds the attempt (or
//     its finished outcome): renew it and collect that outcome. Never RunAttempt, so
//     a helper that forgot the attempt can never be made to start it again.
//   - fail_closed: the prompt was submitted and nothing can settle it (the helper
//     has no record, answered for another generation, was revoked, or stayed
//     unreachable for the whole window). The job ends failed with submitted and
//     reconcile_required and is never run again (decision D4).
//
// The manager is not consulted: the node store's last accepted state is all this
// reads, so "manager down" changes nothing here (a lapsed grant stops new
// placements, not the inspection of an attempt that was already placed).

// ReconcileAction is what the consumer does with the job.
type ReconcileAction string

const (
	ReconcileRun        ReconcileAction = "run"
	ReconcileWait       ReconcileAction = "wait"
	ReconcileResume     ReconcileAction = "resume"
	ReconcileFailClosed ReconcileAction = "fail_closed"
)

// Reconcile reasons. Fixed tokens: they reach logs, the failure event's reason and
// a metric label, so nothing helper-supplied is ever one of them.
const (
	ReconcileNoAttempt         = "no_attempt"           // run: the job never had an attempt
	ReconcileAttemptEnded      = "attempt_ended"        // run: the last attempt is over and never submitted
	ReconcileAttemptLapsed     = "attempt_lapsed"       // run: it lapsed unsubmitted and the helper confirms or cannot say otherwise
	ReconcileLeaseHeld         = "attempt_lease_held"   // wait: the last attempt may still be live
	ReconcileHelperUnreachable = "helper_unreachable"   // wait inside the window, fail_closed after it
	ReconcileHelperRunning     = "helper_still_running" // wait: unsubmitted, lapsed, but the helper has not stopped it yet
	ReconcileHelperHolds       = "helper_holds_attempt" // resume: the helper is still running the submitted attempt
	ReconcileHelperFinished    = "helper_finished"      // resume: the helper holds the finished outcome
	ReconcileHelperSubmitted   = "helper_saw_submit"    // resume: only the helper saw the prompt leave (the ledger is behind)
	ReconcileNoRecord          = "helper_no_record"     // fail_closed: the helper restarted and forgot the attempt
	ReconcileStale             = "stale_helper_result"  // fail_closed: the helper answered for another generation or input
	ReconcileNodeRevoked       = "node_revoked"         // fail_closed: the node is revoked, it is not asked
	ReconcileFenced            = "attempt_fenced"       // fail_closed: the fence is closed, nothing can be committed any more
	ReconcileNotStopping       = "helper_not_stopping"  // fail_closed: unsubmitted, but the helper still runs it long after its lease
)

// ReconcileOutcome is one (action, reason) pair the policy can produce.
type ReconcileOutcome struct {
	Action ReconcileAction
	Reason string
}

// ReconcileOutcomes is every pair PlanReconcile can answer with, in output order
// (the bounded label set of the reconcile metric; a test pins that the policy
// never leaves it).
var ReconcileOutcomes = []ReconcileOutcome{
	{ReconcileRun, ReconcileNoAttempt}, {ReconcileRun, ReconcileAttemptEnded}, {ReconcileRun, ReconcileAttemptLapsed},
	{ReconcileWait, ReconcileLeaseHeld}, {ReconcileWait, ReconcileHelperUnreachable}, {ReconcileWait, ReconcileHelperRunning},
	{ReconcileResume, ReconcileHelperHolds}, {ReconcileResume, ReconcileHelperFinished}, {ReconcileResume, ReconcileHelperSubmitted},
	{ReconcileFailClosed, ReconcileHelperUnreachable}, {ReconcileFailClosed, ReconcileNoRecord},
	{ReconcileFailClosed, ReconcileStale}, {ReconcileFailClosed, ReconcileNodeRevoked},
	{ReconcileFailClosed, ReconcileFenced}, {ReconcileFailClosed, ReconcileNotStopping},
}

// Reconcile timing defaults.
const (
	// DefaultReconcileWindow is how long a submitted attempt may stay unresolved
	// with its helper unreachable, counted from the moment its ledger lease lapsed
	// (nothing has renewed it since the primary was lost). The helper stops the
	// attempt itself when its lease lapses, so waiting longer only helps to fetch an
	// outcome it finished earlier; it bounds how long such a job sits non-terminal.
	DefaultReconcileWindow = 10 * time.Minute
	MaxReconcileWindow     = 24 * time.Hour
	// DefaultReconcileMargin is how long past a lapsed lease the holder is still
	// treated as possibly running: the helper's kill grace (10 s) plus slack.
	DefaultReconcileMargin = 15 * time.Second
	// DefaultReconcilePoll caps one wait: the consumer asks again at least this often.
	DefaultReconcilePoll = 10 * time.Second
)

// ReconcileConfig tunes the policy; zero values take the defaults.
type ReconcileConfig struct {
	Window time.Duration
	Margin time.Duration
	Poll   time.Duration
}

func (c ReconcileConfig) normalized() ReconcileConfig {
	if c.Window <= 0 {
		c.Window = DefaultReconcileWindow
	}
	c.Window = min(c.Window, MaxReconcileWindow)
	if c.Margin <= 0 {
		c.Margin = DefaultReconcileMargin
	}
	if c.Poll <= 0 {
		c.Poll = DefaultReconcilePoll
	}
	return c
}

// HelperAttemptState is what a helper says about an attempt.
type HelperAttemptState int

const (
	// HelperAttemptUnknown: the helper has no record (restarted, or never had it).
	HelperAttemptUnknown HelperAttemptState = iota
	// HelperAttemptRunning: accepted, running or submitted; not finished.
	HelperAttemptRunning
	// HelperAttemptFinished: ended; the outcome is held.
	HelperAttemptFinished
)

// HelperView is an InspectAttempt answer trimmed to what the policy judges. The
// outcome itself never passes through here: whoever collects it re-inspects and
// commits it through the fenced ingest.
type HelperView struct {
	State        HelperAttemptState
	Generation   uint64
	Fingerprint  string
	Submitted    bool
	LastSequence uint64
	HasOutcome   bool
}

// Consistent reports whether the answer is about this ledger attempt: same lease
// generation and same input fingerprint, and a finished answer that actually
// carries an outcome and its last event. An answer that is not is a stale (or
// confused) helper result and is never acted on. Only meaningful for a helper
// that has a record (not HelperAttemptUnknown).
func (v HelperView) Consistent(att jobstore.Attempt) bool {
	if v.Generation != att.Generation || v.Fingerprint == "" || v.Fingerprint != att.InputFingerprint {
		return false
	}
	return v.State != HelperAttemptFinished || (v.HasOutcome && v.LastSequence > 0)
}

// ReconcileInput is everything one decision reads.
type ReconcileInput struct {
	Now time.Time
	// Attempts is the job's ledger, ascending generation.
	Attempts []jobstore.Attempt
	// Inspected says the helper was asked. Then View is its answer, or InspectErr
	// says why it could not answer (ErrNodeRevoked: it was not asked at all).
	Inspected  bool
	View       *HelperView
	InspectErr error
}

// ReconcilePlan is the decision.
type ReconcilePlan struct {
	Action ReconcileAction
	Reason string
	// RetryAfter is how long a wait lasts.
	RetryAfter time.Duration
	// NeedInspect: ask the helper about Attempt and decide again with its answer.
	// No other field is meaningful then.
	NeedInspect bool
	// Attempt is the ledger attempt the plan is about (zero when the job has none).
	Attempt jobstore.Attempt
	// NextGeneration is the generation a run takes (the attempt after Attempt).
	NextGeneration uint64
	// Deadline is when the reconcile window for Attempt ends (its lease lapse plus
	// Window). A resume that still cannot reach its helper by then fails closed.
	Deadline time.Time
}

// PlanReconcile is the whole policy, pure. The ledger decides first; the helper's
// answer (a second call, after NeedInspect) only ever refines it, and nothing the
// helper says can make a submitted attempt runnable again.
func PlanReconcile(in ReconcileInput, cfg ReconcileConfig) ReconcilePlan {
	cfg = cfg.normalized()
	n := len(in.Attempts)
	if n == 0 {
		return ReconcilePlan{Action: ReconcileRun, Reason: ReconcileNoAttempt, NextGeneration: 1}
	}
	last := in.Attempts[n-1]
	p := ReconcilePlan{Attempt: last, NextGeneration: last.Generation + 1, Deadline: last.LeaseExpiresAt.Add(cfg.Window)}
	do := func(a ReconcileAction, reason string) ReconcilePlan { p.Action, p.Reason = a, reason; return p }

	if last.State != jobstore.AttemptActive {
		// Finished or expired: the fence is closed. Unsubmitted, the next attempt
		// starts; submitted, nothing the helper holds can be committed and nothing may
		// be run (BeginAttempt refuses a successor too).
		if last.Submitted() {
			return do(ReconcileFailClosed, ReconcileFenced)
		}
		return do(ReconcileRun, ReconcileAttemptEnded)
	}

	lapsedAt := last.LeaseExpiresAt
	// settled: the holder cannot still be running (its lease lapsed and its kill
	// grace passed). Until then the attempt may be live: wait for it, never race it.
	if settled := lapsedAt.Add(cfg.Margin); in.Now.Before(settled) && !last.Submitted() {
		p.RetryAfter = clampRetry(settled.Sub(in.Now)+time.Second, cfg.Poll)
		return do(ReconcileWait, ReconcileLeaseHeld)
	}
	unreachable := func() ReconcilePlan { // the helper could not be asked or did not answer
		if in.Now.Sub(lapsedAt) >= cfg.Window {
			return do(ReconcileFailClosed, ReconcileHelperUnreachable)
		}
		p.RetryAfter = cfg.Poll
		return do(ReconcileWait, ReconcileHelperUnreachable)
	}

	revoked := errors.Is(in.InspectErr, ErrNodeRevoked)
	switch {
	case !in.Inspected:
		p.NeedInspect = true
		return p
	case revoked && last.Submitted():
		return do(ReconcileFailClosed, ReconcileNodeRevoked)
	case in.InspectErr != nil || in.View == nil:
		if last.Submitted() {
			return unreachable()
		}
		// Unsubmitted and the helper cannot say otherwise: the attempt is reassigned.
		// Residual risk (ADR-0015): a helper that submitted and was lost before any
		// event reached the primary is invisible from here.
		return do(ReconcileRun, ReconcileAttemptLapsed)
	}

	v := in.View
	if v.State == HelperAttemptUnknown {
		if last.Submitted() {
			return do(ReconcileFailClosed, ReconcileNoRecord)
		}
		return do(ReconcileRun, ReconcileAttemptLapsed)
	}
	if !v.Consistent(last) {
		if last.Submitted() {
			return do(ReconcileFailClosed, ReconcileStale)
		}
		// An unsubmitted attempt is not failed on the word of an answer about another
		// generation: it is ignored, as if the helper had not answered.
		return do(ReconcileRun, ReconcileAttemptLapsed)
	}
	switch {
	case last.Submitted() && v.State == HelperAttemptFinished:
		return do(ReconcileResume, ReconcileHelperFinished)
	case last.Submitted():
		return do(ReconcileResume, ReconcileHelperHolds)
	case v.Submitted:
		// The helper saw the prompt leave; the primary died before it read the event.
		// Whoever resumes records the boundary in the ledger before committing.
		return do(ReconcileResume, ReconcileHelperSubmitted)
	case v.State == HelperAttemptRunning:
		if in.Now.Sub(lapsedAt) >= cfg.Window {
			return do(ReconcileFailClosed, ReconcileNotStopping)
		}
		p.RetryAfter = cfg.Poll
		return do(ReconcileWait, ReconcileHelperRunning)
	}
	return do(ReconcileRun, ReconcileAttemptLapsed) // finished without ever submitting
}

func clampRetry(d, poll time.Duration) time.Duration {
	return min(max(d, time.Second), poll)
}

// Ledger is the read side of the attempt ledger the Reconciler needs.
type Ledger interface {
	ListAttempts(ctx context.Context, jobID string) ([]jobstore.Attempt, error)
}

// Inspector asks the node that holds an attempt what it knows. It is read-only
// and must not start, renew or cancel anything. An error means the node could not
// answer (unreachable, refused, timed out).
type Inspector interface {
	Inspect(ctx context.Context, nodeID, jobID, attemptID string) (HelperView, error)
}

// RegistrySource is the part of Store the Reconciler reads: a revoked node is
// never dialed.
type RegistrySource interface {
	GetRegistry(ctx context.Context, nodeID string) (RegistryEntry, error)
}

// Reconciler decides what happens to a leased job that has attempts.
type Reconciler struct {
	Ledger    Ledger
	Inspector Inspector
	// Registry is optional; with it a revoked node is not dialed.
	Registry RegistrySource
	Config   ReconcileConfig
	// Now defaults to time.Now.
	Now func() time.Time
}

// Reconcile reads the job's ledger and, only when the answer depends on it, asks
// the helper. A ledger error is returned (the caller holds the job: it cannot tell
// whether a prompt left); a helper that cannot answer is part of the plan.
func (r *Reconciler) Reconcile(ctx context.Context, jobID string) (ReconcilePlan, error) {
	if r == nil || r.Ledger == nil {
		return ReconcilePlan{}, errors.New("reconcile: a ledger is required")
	}
	attempts, err := r.Ledger.ListAttempts(ctx, jobID)
	if err != nil {
		return ReconcilePlan{}, fmt.Errorf("reconcile: list attempts: %w", err)
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	in := ReconcileInput{Now: now(), Attempts: attempts}
	plan := PlanReconcile(in, r.Config)
	if !plan.NeedInspect {
		return plan, nil
	}

	in.Inspected = true
	switch {
	case r.revoked(ctx, plan.Attempt.NodeID):
		in.InspectErr = ErrNodeRevoked
	case r.Inspector == nil:
		in.InspectErr = errors.New("no inspector")
	default:
		view, err := r.Inspector.Inspect(ctx, plan.Attempt.NodeID, jobID, plan.Attempt.AttemptID)
		if err != nil {
			in.InspectErr = err
			slog.Info("reconcile: helper did not answer", "job_id", jobID, "attempt_id", plan.Attempt.AttemptID,
				"node_id", plan.Attempt.NodeID)
		} else {
			in.View = &view
		}
	}
	in.Now = now()
	return PlanReconcile(in, r.Config), nil
}

// revoked reports a node whose registry entry says revoked; a store error or a
// missing entry says nothing (the dial refuses such a node anyway).
func (r *Reconciler) revoked(ctx context.Context, nodeID string) bool {
	if r.Registry == nil || nodeID == "" {
		return false
	}
	entry, err := r.Registry.GetRegistry(ctx, nodeID)
	return err == nil && entry.Revoked()
}
