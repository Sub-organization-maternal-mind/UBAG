package webhooks

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/jobcore"
	"github.com/ubag/ubag/apps/gateway/internal/resilience"
)

type Sender interface {
	Send(ctx context.Context, delivery Delivery) (AttemptResult, error)
}

type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	JitterRatio float64
}

type DeliveryWorker struct {
	Store        OutboxStore
	Sender       Sender
	WorkerID     string
	PollInterval time.Duration
	LeaseFor     time.Duration
	BatchSize    int
	RetryPolicy  RetryPolicy
	Now          func() time.Time
	Breakers     *resilience.Registry // optional; nil disables circuit-breaker retry delay

	// runErrors counts RunOnce failures observed by Run. Previously these ended
	// the worker outright with no counter, so a delivery loop that had stopped
	// was indistinguishable from an idle one.
	runErrors atomic.Uint64
}

func (w *DeliveryWorker) recordRunError() {
	if w == nil {
		return
	}
	w.runErrors.Add(1)
}

// RunErrors reports how many delivery iterations have failed. Exported to
// /v1/metrics as ubag_webhook_worker_run_errors_total: a non-zero value means
// delivery is retrying, and a value that stops growing while callbacks are
// still queued means the loop is not making progress.
func (w *DeliveryWorker) RunErrors() uint64 {
	if w == nil {
		return 0
	}
	return w.runErrors.Load()
}

func (w *DeliveryWorker) Ready(ctx context.Context) error {
	if w == nil {
		return fmt.Errorf("webhook delivery worker is not configured")
	}
	if w.Store == nil {
		return fmt.Errorf("webhook outbox store is not configured")
	}
	if w.Sender == nil {
		return fmt.Errorf("webhook sender is not configured")
	}
	return w.Store.Ready(ctx)
}

// Run drives the delivery loop until ctx is cancelled.
//
// A transient store error (a blip on LeaseDue, a failed MarkDelivered) must not
// end delivery for the lifetime of the process. The loop previously returned on
// the first error, and serve.Run only logged it, so one hiccup silently
// stopped every job_callback in the outbox from ever being delivered - with no
// metric and no health signal. Errors are now counted, logged, and retried with
// capped exponential backoff, matching WorkerConsumer.runSerial. Only ctx
// cancellation returns.
func (w *DeliveryWorker) Run(ctx context.Context) error {
	pollInterval := w.PollInterval
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	maxBackoff := pollInterval * 16
	if maxBackoff < 5*time.Second {
		maxBackoff = 5 * time.Second
	}
	consecutiveErrors := 0
	for {
		processed, err := w.RunOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			consecutiveErrors++
			w.recordRunError()
			delay := pollInterval * time.Duration(1<<min(consecutiveErrors, 5))
			if delay > maxBackoff {
				delay = maxBackoff
			}
			slog.Error("webhook delivery iteration failed; retrying",
				"error", err,
				"consecutive_errors", consecutiveErrors,
				"retry_in", delay)
			if err := sleepCtx(ctx, delay); err != nil {
				return err
			}
			continue
		}
		consecutiveErrors = 0
		if processed {
			continue
		}
		if err := sleepCtx(ctx, pollInterval); err != nil {
			return err
		}
	}
}

// sleepCtx waits for d, or returns early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (w *DeliveryWorker) RunOnce(ctx context.Context) (bool, error) {
	if err := w.Ready(ctx); err != nil {
		return false, err
	}
	workerID := jobcore.FirstNonEmpty(w.WorkerID, "gateway-webhook-worker")
	leaseFor := w.LeaseFor
	if leaseFor <= 0 {
		leaseFor = 30 * time.Second
	}
	batchSize := w.BatchSize
	if batchSize <= 0 {
		batchSize = 10
	}
	deliveries, err := w.Store.LeaseDue(ctx, workerID, batchSize, leaseFor)
	if err != nil || len(deliveries) == 0 {
		return len(deliveries) > 0, err
	}
	now := w.Now
	if now == nil {
		now = time.Now
	}
	for _, delivery := range deliveries {
		result, err := w.Sender.Send(ctx, delivery)
		if err != nil {
			return true, err
		}
		switch {
		case result.ErrorClass == "none":
			if err := w.Store.MarkDelivered(ctx, delivery.ID, delivery.LeaseID, result); err != nil {
				return true, err
			}
		case result.ErrorClass == "circuit_open":
			// Use the breaker's cooldown as the retry delay to avoid DLQ churn.
			cooldown := time.Second // default floor
			if w.Breakers != nil {
				// url.Parse failure here is unreachable in practice Ã¢â‚¬â€ ValidateCallbackURL
				// filters malformed URLs before they reach the delivery queue.
				if u, parseErr := url.Parse(delivery.URL); parseErr == nil {
					if remaining := w.Breakers.Get(resilience.KindWebhook, u.Hostname()).CooldownRemaining(); remaining > cooldown {
						cooldown = remaining
					}
				}
			}
			next := now().UTC().Add(cooldown)
			if err := w.Store.MarkRetry(ctx, delivery.ID, delivery.LeaseID, next, result); err != nil {
				return true, err
			}
		case result.Retryable && delivery.AttemptCount+1 < normalizeMaxAttempts(delivery.MaxAttempts):
			next := NextRetryAt(now().UTC(), delivery.ID, delivery.AttemptCount+1, w.RetryPolicy)
			if err := w.Store.MarkRetry(ctx, delivery.ID, delivery.LeaseID, next, result); err != nil {
				return true, err
			}
		default:
			if err := w.Store.MarkDeadLetter(ctx, delivery.ID, delivery.LeaseID, result); err != nil {
				return true, err
			}
		}
	}
	return true, nil
}

func NextRetryAt(now time.Time, deliveryID string, attempts int, policy RetryPolicy) time.Time {
	base := policy.BaseDelay
	if base <= 0 {
		base = time.Second
	}
	maxDelay := policy.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 5 * time.Minute
	}
	if attempts < 1 {
		attempts = 1
	}
	delay := base
	for i := 1; i < attempts; i++ {
		if delay >= maxDelay/2 {
			delay = maxDelay
			break
		}
		delay *= 2
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	if policy.JitterRatio > 0 {
		ratio := math.Min(policy.JitterRatio, 0.9)
		factor := 1 - ratio + (stableUnitInterval(deliveryID) * 2 * ratio)
		delay = time.Duration(float64(delay) * factor)
		if delay < time.Millisecond {
			delay = time.Millisecond
		}
	}
	return now.Add(delay)
}

func stableUnitInterval(value string) float64 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(value))
	return float64(hash.Sum32()) / float64(math.MaxUint32)
}
