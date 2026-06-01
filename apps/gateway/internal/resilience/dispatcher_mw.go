package resilience

import (
	"context"
	"fmt"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// BreakerOpenError is returned by DispatcherMiddleware when a per-target
// circuit breaker is open. Callers can use errors.As to check for this type
// and read the Retry-After duration.
type BreakerOpenError struct {
	Target     string
	RetryAfter time.Duration
}

func (e *BreakerOpenError) Error() string {
	return fmt.Sprintf("UBAG-QUEUE-BREAKER-OPEN-001: circuit breaker open for target %q, retry after %s", e.Target, e.RetryAfter)
}

// DispatcherMiddleware wraps next with per-target circuit breakers from reg.
// If reg is nil, next is returned unchanged (passthrough — existing tests unaffected).
func DispatcherMiddleware(next executor.Dispatcher, reg *Registry) executor.Dispatcher {
	if reg == nil {
		return next
	}
	return &breakerDispatcher{next: next, reg: reg}
}

type breakerDispatcher struct {
	next executor.Dispatcher
	reg  *Registry
}

func (d *breakerDispatcher) Ready(ctx context.Context) error {
	return d.next.Ready(ctx)
}

func (d *breakerDispatcher) EnqueueJob(ctx context.Context, job jobstore.Job) (executor.Receipt, error) {
	b := d.reg.Get(KindUpstream, job.TenantID+"/"+job.Target)
	if !b.Allow() {
		return executor.Receipt{}, &BreakerOpenError{
			Target:     job.TenantID + "/" + job.Target,
			RetryAfter: b.CooldownRemaining(),
		}
	}
	receipt, err := d.next.EnqueueJob(ctx, job)
	if err != nil {
		b.RecordFailure()
		return receipt, err
	}
	b.RecordSuccess()
	return receipt, nil
}

func (d *breakerDispatcher) CancelJob(ctx context.Context, job jobstore.Job, reason string) error {
	return d.next.CancelJob(ctx, job, reason)
}

func (d *breakerDispatcher) Stats(ctx context.Context) (executor.Stats, error) {
	return d.next.Stats(ctx)
}
