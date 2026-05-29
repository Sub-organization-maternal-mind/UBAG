package jobs

import (
	"context"
	"time"
)

type Status string

const (
	StatusCreated               Status = "created"
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

func KnownStatus(status Status) bool {
	switch status {
	case StatusCreated,
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
