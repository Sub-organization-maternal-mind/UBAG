package webhooks_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/webhooks"
)

// A transient store error must not end webhook delivery for the lifetime of the
// process. Run used to `return err` on the first failure and serve.Run only
// logged it, so a single blip on LeaseDue silently stopped every job_callback
// in the outbox from ever being delivered.
func TestDeliveryWorker_RunSurvivesTransientStoreErrors(t *testing.T) {
	store := &flakyStore{
		// Fail the first three iterations, then succeed.
		failFirst: 3,
	}

	w := &webhooks.DeliveryWorker{
		Store:        store,
		Sender:       &noopSender{},
		PollInterval: time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Run must not return while ctx is live.
	select {
	case err := <-done:
		t.Fatalf("Run returned early on a transient store error: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	// It must have retried past the failures.
	if got := store.calls.Load(); got < 3 {
		t.Errorf("expected the worker to retry past 3 failures, saw %d iterations", got)
	}
	if got := w.RunErrors(); got < 3 {
		t.Errorf("expected RunErrors >= 3, got %d", got)
	}

	// Still running (has not given up) when the context ends.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// Run must still return promptly, and only once, when the context is cancelled
// while a store error is in flight.
func TestDeliveryWorker_RunReturnsOnContextCancel(t *testing.T) {
	store := &flakyStore{failFirst: 1 << 30, calls: atomic.Int64{}} // always fails
	w := &webhooks.DeliveryWorker{
		Store:        store,
		Sender:       &noopSender{},
		PollInterval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

type noopSender struct{}

func (noopSender) Send(context.Context, webhooks.Delivery) (webhooks.AttemptResult, error) {
	return webhooks.AttemptResult{ErrorClass: "none"}, nil
}

type flakyStore struct {
	webhooks.OutboxStore
	failFirst int32
	failures  atomic.Int32
	calls     atomic.Int64
}

func (s *flakyStore) Ready(context.Context) error { return nil }

func (s *flakyStore) LeaseDue(context.Context, string, int, time.Duration) ([]webhooks.Delivery, error) {
	s.calls.Add(1)
	if s.failures.Add(1) <= s.failFirst {
		return nil, errors.New("transient store failure")
	}
	return nil, nil
}
