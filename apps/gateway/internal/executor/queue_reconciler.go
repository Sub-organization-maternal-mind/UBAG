package executor

import (
	"context"
	"log/slog"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

const (
	defaultQueueReconcilerInterval = 60 * time.Second
	// defaultQueueReconcilerMinAge keeps the reconciler off jobs whose request
	// handler may still be between Create and EnqueueJob.
	defaultQueueReconcilerMinAge = 2 * time.Minute
)

// JobPresenceChecker is implemented by dispatchers that can say whether a job
// already has a queue entry in any state. The reconciler only runs against such
// dispatchers: re-publishing a job a broker already holds (e.g. NATS past its
// dedupe window) would execute it twice, so without a presence check it does
// nothing.
type JobPresenceChecker interface {
	HasJob(ctx context.Context, jobID string) (bool, error)
}

// QueuedJobReconciler closes the create-then-enqueue crash window: the gateway
// writes the job row (status queued) and only then enqueues it, so a crash in
// between leaves a queued job no worker will ever see. Each sweep re-enqueues
// queued/scheduled jobs older than MinAge that have no queue entry. Enqueue is
// idempotent per job ID, so a race with a live handler or a concurrent sweep is
// harmless.
//
// ponytail: the dispatch outbox is not wired in serve today, so this covers the
// direct EnqueueJob path only; revisit if the outbox becomes the default path.
type QueuedJobReconciler struct {
	Jobs       jobstore.Store
	Dispatcher Dispatcher
	// MinAge is the grace period after job creation; <= 0 uses the default.
	MinAge   time.Duration
	Interval time.Duration
	Now      func() time.Time // injectable clock; defaults to time.Now().UTC()
}

func (r *QueuedJobReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func (r *QueuedJobReconciler) minAge() time.Duration {
	if r.MinAge > 0 {
		return r.MinAge
	}
	return defaultQueueReconcilerMinAge
}

func (r *QueuedJobReconciler) interval() time.Duration {
	if r.Interval > 0 {
		return r.Interval
	}
	return defaultQueueReconcilerInterval
}

// Run sweeps once immediately (the startup pass that repairs a previous crash)
// and then on Interval until ctx is cancelled. Sweep errors never stop the loop.
func (r *QueuedJobReconciler) Run(ctx context.Context) error {
	r.sweepLogged(ctx)
	ticker := time.NewTicker(r.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			r.sweepLogged(ctx)
		}
	}
}

func (r *QueuedJobReconciler) sweepLogged(ctx context.Context) {
	n, err := r.SweepOnce(ctx)
	if err != nil {
		slog.Warn("queued-job reconciler sweep failed", "error", err)
	}
	if n > 0 {
		slog.Info("re-enqueued queued jobs missing from the queue", "count", n)
	}
}

// SweepOnce re-enqueues every old queued/scheduled job that has no queue entry
// and returns how many it re-enqueued. A per-job failure is skipped (retried on
// the next sweep) so one bad job cannot block the rest.
func (r *QueuedJobReconciler) SweepOnce(ctx context.Context) (int, error) {
	if r == nil || r.Jobs == nil || r.Dispatcher == nil {
		return 0, nil
	}
	checker, ok := r.Dispatcher.(JobPresenceChecker)
	if !ok {
		return 0, nil
	}
	cutoff := r.now().Add(-r.minAge())
	reenqueued := 0
	for _, status := range []jobstore.Status{jobstore.StatusQueued, jobstore.StatusScheduled} {
		jobs, err := r.Jobs.List(ctx, jobstore.ListFilter{Status: string(status), Limit: jobstore.UnboundedScanLimit})
		if err != nil {
			return reenqueued, err
		}
		for _, job := range jobs {
			if ctx.Err() != nil {
				return reenqueued, ctx.Err()
			}
			if job.CreatedAt.After(cutoff) {
				continue
			}
			present, err := checker.HasJob(ctx, job.ID)
			if err != nil || present {
				continue
			}
			if _, err := r.Dispatcher.EnqueueJob(ctx, job); err != nil {
				slog.Warn("queued-job reconciler enqueue failed", "job_id", job.ID, "error", err)
				continue
			}
			reenqueued++
		}
	}
	return reenqueued, nil
}
