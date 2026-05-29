package executor

import (
	"context"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

type NoopDispatcher struct{}

func NewNoopDispatcher() NoopDispatcher {
	return NoopDispatcher{}
}

func (NoopDispatcher) Ready(context.Context) error {
	return nil
}

func (NoopDispatcher) EnqueueJob(_ context.Context, job jobstore.Job) (Receipt, error) {
	return Receipt{
		Backend:    "noop",
		QueueName:  "jobs",
		MessageID:  job.ID,
		EnqueuedAt: time.Now().UTC(),
	}, nil
}

func (NoopDispatcher) CancelJob(context.Context, jobstore.Job, string) error {
	return nil
}

func (NoopDispatcher) Stats(context.Context) (Stats, error) {
	return Stats{
		QueueName:        "jobs",
		DepthByState:     map[string]int{"queued": 0},
		OldestAgeByState: map[string]time.Duration{"queued": 0},
	}, nil
}
