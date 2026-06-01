package webhooks

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"net/url"
	"time"

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

func (w *DeliveryWorker) Run(ctx context.Context) error {
	pollInterval := w.PollInterval
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	for {
		processed, err := w.RunOnce(ctx)
		if err != nil {
			return err
		}
		if processed {
			continue
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (w *DeliveryWorker) RunOnce(ctx context.Context) (bool, error) {
	if err := w.Ready(ctx); err != nil {
		return false, err
	}
	workerID := firstNonEmpty(w.WorkerID, "gateway-webhook-worker")
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
		case result.StatusCode >= 200 && result.StatusCode < 300:
			if err := w.Store.MarkDelivered(ctx, delivery.ID, delivery.LeaseID, result); err != nil {
				return true, err
			}
		case result.ErrorClass == "circuit_open" && w.Breakers != nil:
			// Use the breaker's cooldown as the retry delay to avoid DLQ churn.
			var next time.Time
			if u, parseErr := url.Parse(delivery.URL); parseErr == nil {
				host := u.Hostname()
				b := w.Breakers.Get(resilience.KindWebhook, host)
				cooldown := b.CooldownRemaining()
				if cooldown <= 0 {
					cooldown = time.Second // fallback minimum
				}
				next = now().UTC().Add(cooldown)
			} else {
				next = NextRetryAt(now().UTC(), delivery.ID, delivery.AttemptCount+1, w.RetryPolicy)
			}
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
