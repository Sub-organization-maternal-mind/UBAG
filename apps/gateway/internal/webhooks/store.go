package webhooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

type DeliveryStatus string

const (
	StatusPending        DeliveryStatus = "pending"
	StatusLeased         DeliveryStatus = "leased"
	StatusRetryScheduled DeliveryStatus = "retry_scheduled"
	StatusDelivered      DeliveryStatus = "delivered"
	StatusDeadLettered   DeliveryStatus = "dead_lettered"
	StatusCancelled      DeliveryStatus = "cancelled"
)

type Delivery struct {
	ID               string
	TenantID         string
	AppID            string
	JobID            string
	EventName        string
	EndpointID       string
	EndpointKind     string
	URL              string
	SecretID         string
	DedupeKey        string
	Payload          []byte
	TraceID          string
	Status           DeliveryStatus
	AttemptCount     int
	MaxAttempts      int
	NextAttemptAt    time.Time
	LeaseID          string
	LeasedUntil      time.Time
	LastHTTPStatus   int
	LastErrorClass   string
	LastErrorMessage string
	ReplayOf         string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeliveredAt      time.Time
}

type EnqueueRequest struct {
	TenantID      string
	AppID         string
	JobID         string
	EventName     string
	EndpointID    string
	EndpointKind  string
	URL           string
	SecretID      string
	DedupeKey     string
	Payload       []byte
	TraceID       string
	MaxAttempts   int
	NextAttemptAt time.Time
	ReplayOf      string
}

type AttemptResult struct {
	StatusCode   int
	ErrorClass   string
	ErrorMessage string
	Retryable    bool
	Duration     time.Duration
	OccurredAt   time.Time
}

type Stats struct {
	DepthByState     map[string]int
	OldestAgeByState map[string]time.Duration
}

type OutboxStore interface {
	Ready(ctx context.Context) error
	Enqueue(ctx context.Context, request EnqueueRequest) (Delivery, bool, error)
	Get(ctx context.Context, tenantID string, appID string, deliveryID string) (Delivery, bool, error)
	Replay(ctx context.Context, tenantID string, appID string, deliveryID string, idempotencyKey string, now time.Time) (Delivery, bool, error)
	LeaseDue(ctx context.Context, workerID string, limit int, leaseFor time.Duration) ([]Delivery, error)
	MarkDelivered(ctx context.Context, deliveryID string, leaseID string, result AttemptResult) error
	MarkRetry(ctx context.Context, deliveryID string, leaseID string, nextAttemptAt time.Time, result AttemptResult) error
	MarkDeadLetter(ctx context.Context, deliveryID string, leaseID string, result AttemptResult) error
	Stats(ctx context.Context) (Stats, error)
}

func StableID(prefix string, parts ...string) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(parts)))
	return prefix + "_" + hex.EncodeToString(sum[:])[:24]
}

func TerminalDeliveryStatus(status DeliveryStatus) bool {
	switch status {
	case StatusDelivered, StatusDeadLettered, StatusCancelled:
		return true
	default:
		return false
	}
}

func cloneBytes(input []byte) []byte {
	if input == nil {
		return nil
	}
	output := make([]byte, len(input))
	copy(output, input)
	return output
}

func normalizeMaxAttempts(value int) int {
	if value <= 0 {
		return 8
	}
	return value
}
