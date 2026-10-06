package executor

import (
	"context"
	"log/slog"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/helpermetrics"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// AttemptReconciler is the ledger-first gate (UBAG_HELPER_DISPATCH, perf-fleet
// slice P4.18). The consumer asks it about every leased job after taking the
// execution lease and before placing or running the job; *nodes.Reconciler is the
// implementation. A job without attempts costs one ledger read.
//
// Every queue backend passes through this gate: a file-spool lease recovered after
// a restart, a lease reclaimed after its TTL and a NATS redelivery all end as a
// lease here, so none of them can run a job whose prompt may already have left. The
// spool's own startup recovery stays what it was (it returns the job to the queue;
// it executes nothing).
type AttemptReconciler interface {
	Reconcile(ctx context.Context, jobID string) (nodes.ReconcilePlan, error)
}

var _ AttemptReconciler = (*nodes.Reconciler)(nil)

// reconcileFailure is a submitted attempt nothing can settle. It is ErrAmbiguous:
// the job ends failed_terminal with submitted and reconcile_required and is never
// run again; reason is the reconciler's fixed token.
type reconcileFailure struct{ reason string }

func (e *reconcileFailure) Error() string {
	return "attempt reconcile: unresolved after prompt submission (" + e.reason + ")"
}
func (e *reconcileFailure) Unwrap() error { return ErrAmbiguous }

// reconcileVerdict is what the gate tells RunOnce. At most one field is set; all
// zero means run the job as usual (place it, or run it locally).
type reconcileVerdict struct {
	hold     error             // *HelperRetryError: retry the lease after a delay
	resume   *jobstore.Attempt // a submitted attempt the helper holds: Remote.Resume it
	resumeBy time.Time         // the end of the reconcile window: a resume that still cannot reach its helper then fails closed
	fail     error             // *reconcileFailure: fail the job closed, run nothing
}

// reconcileGate asks the reconciler about the job. A ledger that cannot be read
// holds the job: whether a prompt left is unknown, so nothing runs.
func (c *WorkerConsumer) reconcileGate(ctx context.Context, jobID string) reconcileVerdict {
	plan, err := c.Reconcile.Reconcile(ctx, jobID)
	if err != nil {
		slog.Warn("attempt reconcile: the ledger could not be read; holding the job", "job_id", jobID, "error", err)
		return reconcileVerdict{hold: &HelperRetryError{Reason: "ledger_unavailable", RetryAfter: c.Remote.cfg.RetryDelay, Err: err}}
	}
	helpermetrics.RecordReconcile(string(plan.Action), plan.Reason)
	log := slog.With("job_id", jobID, "attempt_id", plan.Attempt.AttemptID, "node_id", plan.Attempt.NodeID,
		"generation", plan.Attempt.Generation, "reason", plan.Reason)
	switch plan.Action {
	case nodes.ReconcileWait:
		log.Info("attempt reconcile: holding the job", "retry_after", plan.RetryAfter)
		return reconcileVerdict{hold: &HelperRetryError{Reason: plan.Reason, RetryAfter: plan.RetryAfter}}
	case nodes.ReconcileResume:
		log.Info("attempt reconcile: resuming a submitted attempt (no RunAttempt)")
		attempt := plan.Attempt
		return reconcileVerdict{resume: &attempt, resumeBy: plan.Deadline}
	case nodes.ReconcileFailClosed:
		log.Error("attempt reconcile: the attempt cannot be settled; the job is failed for reconciling and never run again")
		if plan.Reason == nodes.ReconcileNodeRevoked || plan.Reason == nodes.ReconcileHelperUnreachable {
			// A lost node takes its conversation threads with it (nil-safe).
			if n, err := c.Conversations.MarkNodeBroken(ctx, plan.Attempt.NodeID); err != nil {
				log.Warn("attempt reconcile: marking the node's conversations broken failed", "error", err)
			} else if n > 0 {
				log.Warn("attempt reconcile: conversations bound to the lost node are broken", "count", n)
			}
		}
		return reconcileVerdict{fail: &reconcileFailure{reason: plan.Reason}}
	}
	return reconcileVerdict{}
}
