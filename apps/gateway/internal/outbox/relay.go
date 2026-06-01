package outbox

import (
	"context"
	"time"
)

type PublishFunc func(ctx context.Context, topic string, payload []byte) error

// ErrFunc receives non-fatal relay errors for logging or metrics. A nil
// ErrFunc silently discards errors (acceptable for edge deployments).
type ErrFunc func(op string, err error)

type Relay struct {
	store   Store
	publish PublishFunc
	onErr   ErrFunc
	poll    time.Duration
}

func NewRelay(store Store, publish PublishFunc) *Relay {
	return &Relay{
		store:   store,
		publish: publish,
		poll:    500 * time.Millisecond,
	}
}

// WithErrFunc registers an error sink for relay failures. Call before Run.
func (r *Relay) WithErrFunc(fn ErrFunc) *Relay {
	r.onErr = fn
	return r
}

func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(r.poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.flush(ctx)
		}
	}
}

func (r *Relay) flush(ctx context.Context) {
	events, err := r.store.Pending(ctx, 50)
	if err != nil {
		r.errorf("pending", err)
		return
	}
	for _, e := range events {
		if ctx.Err() != nil {
			return
		}
		if err := r.publish(ctx, e.Topic, e.Payload); err != nil {
			r.errorf("publish", err)
			continue
		}
		if err := r.store.MarkPublished(ctx, e.ID); err != nil {
			// At-least-once: event was already published; log but do not
			// treat as fatal — it will re-appear in the next Pending scan.
			r.errorf("mark_published", err)
		}
	}
}

func (r *Relay) errorf(op string, err error) {
	if r.onErr != nil {
		r.onErr(op, err)
	}
}
