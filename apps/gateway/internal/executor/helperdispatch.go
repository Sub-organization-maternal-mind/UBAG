package executor

import (
	"context"
	"fmt"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// finishRemoteRun closes the lease of a job whose remote attempt returned
// cleanly (RemoteWorkerRunner.Run returned nil). The helper's events were
// committed through the fenced ingest while the attempt ran, so unlike the local
// path there is nothing to apply here: the job's own state is the outcome.
//
// The terminal job then gets the same closing sequence a locally ingested job
// gets (finishTerminalIngestedJob): observe, release the concurrency token, the
// post-job hook, the terminal notification, then the lease finisher. A job that
// is NOT terminal after a clean return is a bug or a store anomaly; it is failed
// closed, as ambiguous when the ledger says the prompt left (never replayed).
func (c *WorkerConsumer) finishRemoteRun(ctx context.Context, lease WorkerLease, job jobstore.Job, workerDuration time.Duration) (bool, error) {
	finalJob, found, err := c.Jobs.Get(ctx, lease.JobID())
	if err != nil {
		c.observeWorkerRun(job.Target, "failure", workerDuration)
		_ = lease.Retry(ctx)
		return true, err
	}
	if !found {
		c.observeWorkerRun(job.Target, "failure", workerDuration)
		_ = lease.Poison(ctx, "job disappeared during helper ingestion")
		return true, fmt.Errorf("job %s disappeared during helper ingestion", lease.JobID())
	}
	c.observeWorkerRun(job.Target, workerMetricOutcome(finalJob.Status), workerDuration)
	switch {
	case finalJob.Status == jobstore.StatusCanceled:
		return c.finishTerminalIngestedJob(ctx, lease, finalJob, lease.Cancel)
	case finalJob.Status == jobstore.StatusCompleted || finalJob.Status == jobstore.StatusCompletedWithWarnings:
		return c.finishTerminalIngestedJob(ctx, lease, finalJob, lease.Complete)
	case jobstore.TerminalStatus(finalJob.Status):
		return c.finishTerminalIngestedJob(ctx, lease, finalJob, lease.Fail)
	}

	cause := fmt.Errorf("helper attempt ended without a terminal job state")
	opCtx := ctx
	if c.Remote.submittedInLedger(ctx, lease.JobID()) {
		cause = fmt.Errorf("%w: %w", ErrAmbiguous, cause)
		var opCancel context.CancelFunc
		opCtx, opCancel = detachedOpContext(ctx)
		defer opCancel()
	}
	if applyErr := c.applyFailure(opCtx, lease, EnvelopeFromJob(job), cause); applyErr != nil {
		_ = lease.Fail(opCtx) // never Retry: a replay could resubmit
		return true, applyErr
	}
	if notifyErr := c.notifyCurrentTerminalJob(opCtx, lease); notifyErr != nil {
		return true, notifyErr
	}
	return true, lease.Fail(opCtx)
}

// submittedInLedger reports whether any attempt of the job crossed the
// submission boundary. A ledger that cannot be read answers true: when it is
// unclear whether the prompt left, the job is failed for reconciling, not run
// again.
func (r *RemoteWorkerRunner) submittedInLedger(ctx context.Context, jobID string) bool {
	if r == nil {
		return false
	}
	attempts, err := r.cfg.Store.ListAttempts(ctx, jobID)
	if err != nil {
		return true
	}
	for _, a := range attempts {
		if a.Submitted() {
			return true
		}
	}
	return false
}
