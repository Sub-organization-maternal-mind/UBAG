package executor

import (
	"context"
	"encoding/json"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

const defaultReaperInterval = 60 * time.Second

// StaleJobReaper times out jobs that stay non-terminal past their deadline and
// releases their in-flight concurrency token. It closes the gap the per-job
// release alone cannot: a job whose worker died mid-lease (or that the queue
// never redelivers) otherwise stays non-terminal forever — holding its token —
// until the gateway restarts. It is also the first thing to actually ENFORCE a
// job's options.timeout_seconds.
//
// It is conservative by construction, because a false timeout on a production
// job is worse than a slow one: it never reaps a job still waiting on a future
// NotBefore, and its fallback MaxLifetime is measured from the job's LAST update
// (idle time), so a job that is still emitting events is never killed. Only the
// explicit per-job timeout_seconds is measured from creation (a hard wall-clock
// budget the caller opted into). Release goes through ReleaseForJob, so a reap
// that races a real terminal transition can never double-count the lane.
type StaleJobReaper struct {
	Jobs        jobstore.Store
	Concurrency *topology.ConcurrencyRegistry
	Notifier    TerminalJobNotifier // optional; nil disables terminal notification
	// MaxLifetime is the fallback deadline (idle time since last update) for jobs
	// without an explicit timeout_seconds. When <= 0 the fallback is disabled and
	// only jobs with an explicit timeout_seconds are reaped.
	MaxLifetime time.Duration
	Interval    time.Duration
	Now         func() time.Time // injectable clock; defaults to time.Now().UTC()
}

func (rp *StaleJobReaper) now() time.Time {
	if rp.Now != nil {
		return rp.Now()
	}
	return time.Now().UTC()
}

func (rp *StaleJobReaper) interval() time.Duration {
	if rp.Interval > 0 {
		return rp.Interval
	}
	return defaultReaperInterval
}

// Run sweeps on Interval until ctx is cancelled. Sweep errors are swallowed so a
// transient store hiccup never tears down the loop.
func (rp *StaleJobReaper) Run(ctx context.Context) error {
	ticker := time.NewTicker(rp.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_, _ = rp.SweepOnce(ctx)
		}
	}
}

// SweepOnce times out every stale non-terminal job it finds and returns how many
// it reaped.
func (rp *StaleJobReaper) SweepOnce(ctx context.Context) (int, error) {
	if rp == nil || rp.Jobs == nil {
		return 0, nil
	}
	now := rp.now()
	reaped := 0
	for _, status := range jobstore.LifecycleStatuses() {
		if jobstore.TerminalStatus(status) {
			continue
		}
		jobs, err := rp.Jobs.List(ctx, jobstore.ListFilter{Status: string(status)})
		if err != nil {
			return reaped, err
		}
		for _, job := range jobs {
			if !rp.isStale(job, now) {
				continue
			}
			// UpdateStatus is a no-op (and returns the existing status) if the job
			// already reached a terminal state between List and here, so a racing
			// worker/cancel wins and we don't clobber it.
			updated, found, err := rp.Jobs.UpdateStatus(ctx, job.ID, jobstore.StatusTimedOut)
			if err != nil || !found {
				continue
			}
			if !jobstore.TerminalStatus(updated.Status) {
				continue
			}
			if rp.Concurrency != nil {
				rp.Concurrency.ReleaseForJob(updated.ID)
			}
			if rp.Notifier != nil {
				_ = rp.Notifier.EnqueueTerminalJob(ctx, updated)
			}
			if updated.Status == jobstore.StatusTimedOut {
				reaped++
			}
		}
	}
	return reaped, nil
}

// isStale reports whether a non-terminal job has exceeded its deadline.
func (rp *StaleJobReaper) isStale(job jobstore.Job, now time.Time) bool {
	// A job still waiting on its schedule is not stuck.
	if job.NotBefore != nil && now.Before(*job.NotBefore) {
		return false
	}
	// Explicit per-job budget: total wall-clock since creation.
	if secs := jobTimeoutSeconds(job); secs > 0 {
		if !job.CreatedAt.IsZero() && now.Sub(job.CreatedAt) >= time.Duration(secs)*time.Second {
			return true
		}
	}
	// Fallback safety net: idle time since the last update. A job that is still
	// making progress keeps UpdatedAt fresh and is never reaped by this path.
	if rp.MaxLifetime > 0 {
		last := job.UpdatedAt
		if last.IsZero() || job.CreatedAt.After(last) {
			last = job.CreatedAt
		}
		if !last.IsZero() && now.Sub(last) >= rp.MaxLifetime {
			return true
		}
	}
	return false
}

// jobTimeoutSeconds extracts options.timeout_seconds from a job. JSON numbers
// decode as float64 (or json.Number when UseNumber is set), so both are handled.
func jobTimeoutSeconds(job jobstore.Job) int {
	if job.Options == nil {
		return 0
	}
	switch v := job.Options["timeout_seconds"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}
