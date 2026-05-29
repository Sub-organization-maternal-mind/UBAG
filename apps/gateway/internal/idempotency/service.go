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
	Complete(ctx context.Context, scope Scope, resourceID string, httpStatus int) error
	Release(ctx context.Context, scope Scope) error
	Ready(ctx context.Context) error
}
