package webhooks

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type MemoryStore struct {
	mu         sync.Mutex
	now        func() time.Time
	deliveries map[string]Delivery
	dedupe     map[string]string
	order      []string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		now:        time.Now,
		deliveries: map[string]Delivery{},
		dedupe:     map[string]string{},
	}
}

func (m *MemoryStore) Ready(context.Context) error {
	return nil
}

func (m *MemoryStore) Enqueue(_ context.Context, request EnqueueRequest) (Delivery, bool, error) {
	if err := validateEnqueue(request); err != nil {
		return Delivery{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := dedupeScope(request.TenantID, request.AppID, request.DedupeKey)
	if id := m.dedupe[key]; id != "" {
		return m.deliveries[id], false, nil
	}
	now := m.now().UTC()
	delivery := Delivery{
		ID:            StableID("whd", request.TenantID, request.AppID, request.DedupeKey),
		TenantID:      request.TenantID,
		AppID:         request.AppID,
		JobID:         request.JobID,
		EventName:     request.EventName,
		EndpointID:    firstNonEmpty(request.EndpointID, StableID("whe", request.URL, request.SecretID)),
		EndpointKind:  firstNonEmpty(request.EndpointKind, "job_callback"),
		URL:           request.URL,
		SecretID:      request.SecretID,
		DedupeKey:     request.DedupeKey,
		Payload:       cloneBytes(request.Payload),
		TraceID:       request.TraceID,
		Status:        StatusPending,
		AttemptCount:  0,
		MaxAttempts:   normalizeMaxAttempts(request.MaxAttempts),
		NextAttemptAt: request.NextAttemptAt.UTC(),
		ReplayOf:      request.ReplayOf,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if delivery.NextAttemptAt.IsZero() {
		delivery.NextAttemptAt = now
	}
	m.deliveries[delivery.ID] = delivery
	m.dedupe[key] = delivery.ID
	m.order = append(m.order, delivery.ID)
	return delivery, true, nil
}

func (m *MemoryStore) Get(_ context.Context, tenantID string, appID string, deliveryID string) (Delivery, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delivery, ok := m.deliveries[deliveryID]
	if !ok || delivery.TenantID != tenantID || delivery.AppID != appID {
		return Delivery{}, false, nil
	}
	delivery.Payload = cloneBytes(delivery.Payload)
	return delivery, true, nil
}

func (m *MemoryStore) Replay(ctx context.Context, tenantID string, appID string, deliveryID string, idempotencyKey string, now time.Time) (Delivery, bool, error) {
	original, found, err := m.Get(ctx, tenantID, appID, deliveryID)
	if err != nil || !found {
		return Delivery{}, found, err
	}
	return m.Enqueue(ctx, EnqueueRequest{
		TenantID:      original.TenantID,
		AppID:         original.AppID,
		JobID:         original.JobID,
		EventName:     original.EventName,
		EndpointID:    original.EndpointID,
		EndpointKind:  original.EndpointKind,
		URL:           original.URL,
		SecretID:      original.SecretID,
		DedupeKey:     "replay:" + original.ID + ":" + idempotencyKey,
		Payload:       original.Payload,
		TraceID:       original.TraceID,
		MaxAttempts:   original.MaxAttempts,
		NextAttemptAt: now.UTC(),
		ReplayOf:      original.ID,
	})
}

func (m *MemoryStore) LeaseDue(_ context.Context, workerID string, limit int, leaseFor time.Duration) ([]Delivery, error) {
	if stringsTrim(workerID) == "" {
		return nil, fmt.Errorf("webhook worker id is required")
	}
	if limit <= 0 {
		limit = 10
	}
	if leaseFor <= 0 {
		leaseFor = 30 * time.Second
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	output := []Delivery{}
	for _, id := range m.order {
		if len(output) >= limit {
			break
		}
		delivery := m.deliveries[id]
		if TerminalDeliveryStatus(delivery.Status) {
			continue
		}
		due := (delivery.Status == StatusPending || delivery.Status == StatusRetryScheduled) && !delivery.NextAttemptAt.After(now)
		expiredLease := delivery.Status == StatusLeased && !delivery.LeasedUntil.IsZero() && !delivery.LeasedUntil.After(now)
		if !due && !expiredLease {
			continue
		}
		delivery.Status = StatusLeased
		delivery.LeaseID = StableID("whlease", workerID, id, now.Format(time.RFC3339Nano))
		delivery.LeasedUntil = now.Add(leaseFor)
		delivery.UpdatedAt = now
		m.deliveries[id] = delivery
		delivery.Payload = cloneBytes(delivery.Payload)
		output = append(output, delivery)
	}
	return output, nil
}

func (m *MemoryStore) MarkDelivered(_ context.Context, deliveryID string, leaseID string, result AttemptResult) error {
	return m.mark(deliveryID, leaseID, StatusDelivered, time.Time{}, result)
}

func (m *MemoryStore) MarkRetry(_ context.Context, deliveryID string, leaseID string, nextAttemptAt time.Time, result AttemptResult) error {
	return m.mark(deliveryID, leaseID, StatusRetryScheduled, nextAttemptAt.UTC(), result)
}

func (m *MemoryStore) MarkDeadLetter(_ context.Context, deliveryID string, leaseID string, result AttemptResult) error {
	return m.mark(deliveryID, leaseID, StatusDeadLettered, time.Time{}, result)
}

func (m *MemoryStore) Stats(_ context.Context) (Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	stats := Stats{DepthByState: map[string]int{}, OldestAgeByState: map[string]time.Duration{}}
	for _, delivery := range m.deliveries {
		state := string(delivery.Status)
		stats.DepthByState[state]++
		age := now.Sub(delivery.UpdatedAt)
		if delivery.Status == StatusPending || delivery.Status == StatusRetryScheduled {
			age = now.Sub(delivery.NextAttemptAt)
		}
		if age < 0 {
			age = 0
		}
		if existing, ok := stats.OldestAgeByState[state]; !ok || age > existing {
			stats.OldestAgeByState[state] = age
		}
	}
	return stats, nil
}

func (m *MemoryStore) mark(deliveryID string, leaseID string, status DeliveryStatus, nextAttemptAt time.Time, result AttemptResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delivery, ok := m.deliveries[deliveryID]
	if !ok {
		return fmt.Errorf("webhook delivery %s not found", deliveryID)
	}
	if delivery.LeaseID != leaseID {
		return fmt.Errorf("webhook delivery %s lease mismatch", deliveryID)
	}
	now := m.now().UTC()
	delivery.Status = status
	delivery.AttemptCount++
	delivery.LeaseID = ""
	delivery.LeasedUntil = time.Time{}
	delivery.LastHTTPStatus = result.StatusCode
	delivery.LastErrorClass = result.ErrorClass
	delivery.LastErrorMessage = sanitizeErrorMessage(result.ErrorMessage)
	delivery.NextAttemptAt = nextAttemptAt
	delivery.UpdatedAt = now
	if status == StatusDelivered {
		delivery.DeliveredAt = now
	}
	m.deliveries[deliveryID] = delivery
	return nil
}

func validateEnqueue(request EnqueueRequest) error {
	if stringsTrim(request.TenantID) == "" || stringsTrim(request.AppID) == "" {
		return fmt.Errorf("webhook delivery tenant_id and app_id are required")
	}
	if stringsTrim(request.URL) == "" || stringsTrim(request.SecretID) == "" {
		return fmt.Errorf("webhook delivery URL and secret_id are required")
	}
	if stringsTrim(request.DedupeKey) == "" {
		return fmt.Errorf("webhook delivery dedupe_key is required")
	}
	if len(request.Payload) == 0 {
		return fmt.Errorf("webhook delivery payload is required")
	}
	return nil
}

func dedupeScope(tenantID string, appID string, dedupeKey string) string {
	return stringsTrim(tenantID) + "\x00" + stringsTrim(appID) + "\x00" + stringsTrim(dedupeKey)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if stringsTrim(value) != "" {
			return stringsTrim(value)
		}
	}
	return ""
}

func stringsTrim(value string) string {
	return strings.TrimSpace(value)
}
