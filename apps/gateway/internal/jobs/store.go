package jobs

import (
	"context"
	"time"
)

type Status string

const (
	StatusCreated               Status = "created"
	StatusScheduled             Status = "scheduled"
	StatusQueued                Status = "queued"
	StatusAssigned              Status = "assigned"
	StatusRunning               Status = "running"
	StatusTokenStreaming        Status = "token_streaming"
	StatusCompleting            Status = "completing"
	StatusCompleted             Status = "completed"
	StatusCompletedWithWarnings Status = "completed_with_warnings"
	StatusFailedRetryable       Status = "failed_retryable"
	StatusFailedTerminal        Status = "failed_terminal"
	StatusDeadLetter            Status = "dead_letter"
	StatusCanceled              Status = "cancelled"
	StatusTimedOut              Status = "timed_out"
)

type Event struct {
	ID         string
	JobID      string
	APIVersion string
	Type       string
	Sequence   int
	Data       map[string]any
	TraceID    string
	CreatedAt  time.Time
}

type WorkerEvent struct {
	EventID    string         `json:"event_id"`
	JobID      string         `json:"job_id"`
	APIVersion string         `json:"api_version"`
	Type       string         `json:"type"`
	Sequence   int            `json:"sequence"`
	Data       map[string]any `json:"data"`
	TraceID    string         `json:"trace_id"`
	CreatedAt  time.Time      `json:"created_at"`
}

type CreateRequest struct {
	APIVersion     string
	TenantID       string
	AppID          string
	IdempotencyKey string
	Target         string
	CommandType    string
	Client         map[string]any
	ConversationID string
	TemplateID     string
	Input          map[string]any
	Options        map[string]any
	Callbacks      map[string]any
	Context        map[string]any
	TraceID        string
	RetryOf        string
	// NotBefore, when set, delays execution until this time. The job is created
	// with StatusScheduled and the worker consumer nacks+requeues until the
	// time is reached.
	NotBefore *time.Time
}

type Job struct {
	ID             string
	APIVersion     string
	TenantID       string
	AppID          string
	IdempotencyKey string
	Target         string
	CommandType    string
	Client         map[string]any
	ConversationID string
	TemplateID     string
	Input          map[string]any
	Options        map[string]any
	Callbacks      map[string]any
	Context        map[string]any
	Status         Status
	Result         any
	TraceID        string
	RetryOf        string
	NotBefore      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type ListFilter struct {
	TenantID string
	AppID    string
	Status   string
	Target   string
}

type EventListFilter struct {
	TenantID     string
	AppID        string
	AfterEventID string
	Limit        int
}

type Store interface {
	Create(ctx context.Context, request CreateRequest) (Job, error)
	Get(ctx context.Context, id string) (Job, bool, error)
	List(ctx context.Context, filter ListFilter) ([]Job, error)
	ListEvents(ctx context.Context, jobID string, afterSequence int, limit int) ([]Event, bool, error)
	WaitEvents(ctx context.Context, jobID string, afterSequence int, limit int) ([]Event, bool, error)
	UpdateStatus(ctx context.Context, id string, status Status) (Job, bool, error)
	ApplyWorkerEvent(ctx context.Context, event WorkerEvent) (Job, bool, error)
	Ready(ctx context.Context) error
}

type MetricsStore interface {
	CountsByStatus(ctx context.Context, filter ListFilter) (map[Status]int, int, error)
}

type ScopedStore interface {
	GetScoped(ctx context.Context, id string, tenantID string, appID string) (Job, bool, error)
}

type EventLister interface {
	ListAllEvents(ctx context.Context, filter EventListFilter) ([]Event, error)
}

// RecentEventLister is an optional Store capability returning the most recent
// `limit` events for a job in ascending sequence order. It bounds the signal
// reconstruction scan on hot status polls: a job's terminal-failure event and
// any pending manual-action prompt are always among the latest events (a job
// waiting on sign-in is not streaming tokens), so the tail is sufficient — and
// a token-streaming job with thousands of recorded events is no longer re-read
// from sequence 0 on every GET /jobs/{id} poll.
type RecentEventLister interface {
	RecentEvents(ctx context.Context, jobID string, limit int) ([]Event, bool, error)
}

// reverseEvents reverses an event slice in place. Store implementations fetch
// the newest events with ORDER BY sequence DESC LIMIT N, then reverse so callers
// receive them in the ascending order the signal reconstruction expects.
func reverseEvents(events []Event) {
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
}

func KnownStatus(status Status) bool {
	switch status {
	case StatusCreated,
		StatusScheduled,
		StatusQueued,
		StatusAssigned,
		StatusRunning,
		StatusTokenStreaming,
		StatusCompleting,
		StatusCompleted,
		StatusCompletedWithWarnings,
		StatusFailedRetryable,
		StatusFailedTerminal,
		StatusDeadLetter,
		StatusCanceled,
		StatusTimedOut:
		return true
	default:
		return false
	}
}

func TerminalStatus(status Status) bool {
	switch status {
	case StatusCompleted,
		StatusCompletedWithWarnings,
		StatusFailedRetryable,
		StatusFailedTerminal,
		StatusDeadLetter,
		StatusCanceled,
		StatusTimedOut:
		return true
	default:
		return false
	}
}

func LifecycleStatuses() []Status {
	return []Status{
		StatusCreated,
		StatusScheduled,
		StatusQueued,
		StatusAssigned,
		StatusRunning,
		StatusTokenStreaming,
		StatusCompleting,
		StatusCompleted,
		StatusCompletedWithWarnings,
		StatusFailedRetryable,
		StatusFailedTerminal,
		StatusDeadLetter,
		StatusCanceled,
		StatusTimedOut,
	}
}
