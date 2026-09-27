package idempotency

import (
	"context"
	"strings"
	"time"
)

type DecisionKind string

const (
	DecisionReserved DecisionKind = "reserved"
	DecisionReplay   DecisionKind = "replay"
	DecisionConflict DecisionKind = "conflict"
)

// RecordStatus distinguishes an in-flight reservation (Begin/Reserve returned,
// the operation has not finished) from a completed one. The in-flight lock
// deadline bounds how long a crashed gateway can hold the key before another
// caller may take it over.
type RecordStatus string

const (
	// RecordInFlight marks a reservation whose operation has not completed.
	// It carries a LockedUntil deadline; once the deadline passes a new
	// Reserve may take the key over (crashed-gateway recovery).
	RecordInFlight RecordStatus = "in_flight"
	// RecordCompleted marks a reservation whose operation finished
	// successfully; replays are served from it until the record expires.
	RecordCompleted RecordStatus = "completed"
)

// defaultInFlightLock bounds how long an in-flight reservation blocks the key
// after the caller disappears (process crash, hard kill) before another
// caller may re-reserve it. Completed records are unaffected: they replay
// until the record TTL expires.
const defaultInFlightLock = 5 * time.Minute

type Scope struct {
	TenantID  string
	AppID     string
	Operation string
	Key       string
}

func (s Scope) CacheKey() string {
	parts := []string{s.TenantID, s.AppID, s.Operation, s.Key}
	return strings.Join(parts, "\x00")
}

type Record struct {
	Scope       Scope
	RequestHash string
	ResourceID  string
	HTTPStatus  int
	Status      RecordStatus
	// LockedUntil bounds the in-flight lock. Zero means "no deadline", which
	// is only ever true for records persisted before the lock existed; those
	// keep the legacy replay-while-uncompleted behavior.
	LockedUntil time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ExpiresAt   time.Time
}

type Decision struct {
	Kind   DecisionKind
	Record Record
}

type Service interface {
	Reserve(ctx context.Context, scope Scope, requestHash string) (Decision, error)
	// Complete marks the reserved record completed. It is a compare-and-set on
	// requestHash: if a different payload has taken the key over since this
	// request reserved it, the stale completion is discarded (no error, no
	// write) instead of clobbering the new owner's record.
	Complete(ctx context.Context, scope Scope, requestHash string, resourceID string, httpStatus int) error
	// Release drops an in-flight reservation. Like Complete it is a
	// compare-and-set on requestHash, so a stale release after another payload
	// took the key over cannot delete the new owner's record.
	Release(ctx context.Context, scope Scope, requestHash string) error
	// Sweep deletes every record whose TTL has expired and returns how many
	// records were removed. It is intended to be called from a background
	// loop; the expires_at index exists precisely to serve it.
	Sweep(ctx context.Context) (int64, error)
	Ready(ctx context.Context) error
}
