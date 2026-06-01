package executor

import (
	"context"
	"strings"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// parseJobOptions extracts the typed options view from a raw options map.
// Only the fields used by the executor (priority) are read here.
func parseJobOptions(raw map[string]any) jobOptions {
	opts := jobOptions{Priority: "normal"}
	if raw == nil {
		return opts
	}
	if v, ok := raw["priority"].(string); ok && strings.TrimSpace(v) != "" {
		opts.Priority = strings.TrimSpace(v)
	}
	return opts
}

type jobOptions struct {
	Priority string
}

type DispatchEnvelope struct {
	APIVersion     string         `json:"api_version"`
	JobID          string         `json:"job_id"`
	TenantID       string         `json:"tenant_id"`
	AppID          string         `json:"app_id"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	TraceID        string         `json:"trace_id"`
	RetryOf        string         `json:"retry_of,omitempty"`
	// NotBefore, when set, instructs the consumer to nack+requeue the job
	// until time.Now() >= NotBefore (§14.6 scheduling).
	NotBefore      *time.Time     `json:"not_before,omitempty"`
	Job            DispatchJob    `json:"job"`
	Client         map[string]any `json:"client,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

type DispatchJob struct {
	Target         string         `json:"target"`
	CommandType    string         `json:"command_type"`
	ConversationID string         `json:"conversation_id,omitempty"`
	TemplateID     string         `json:"template_id,omitempty"`
	Input          map[string]any `json:"input,omitempty"`
	Options        map[string]any `json:"options,omitempty"`
	Callbacks      map[string]any `json:"callbacks,omitempty"`
	Context        map[string]any `json:"context,omitempty"`
}

type Receipt struct {
	Backend    string    `json:"backend"`
	QueueName  string    `json:"queue_name"`
	MessageID  string    `json:"message_id"`
	EnqueuedAt time.Time `json:"enqueued_at"`
}

type Stats struct {
	QueueName        string
	DepthByState     map[string]int
	OldestAgeByState map[string]time.Duration
}

type Dispatcher interface {
	Ready(ctx context.Context) error
	EnqueueJob(ctx context.Context, job jobstore.Job) (Receipt, error)
	CancelJob(ctx context.Context, job jobstore.Job, reason string) error
	Stats(ctx context.Context) (Stats, error)
}

func EnvelopeFromJob(job jobstore.Job) DispatchEnvelope {
	return DispatchEnvelope{
		APIVersion:     job.APIVersion,
		JobID:          job.ID,
		TenantID:       job.TenantID,
		AppID:          job.AppID,
		IdempotencyKey: job.IdempotencyKey,
		TraceID:        job.TraceID,
		RetryOf:        job.RetryOf,
		NotBefore:      job.NotBefore,
		Client:         cloneMap(job.Client),
		CreatedAt:      job.CreatedAt,
		Job: DispatchJob{
			Target:         job.Target,
			CommandType:    job.CommandType,
			ConversationID: job.ConversationID,
			TemplateID:     job.TemplateID,
			Input:          cloneMap(job.Input),
			Options:        cloneMap(job.Options),
			Callbacks:      cloneMap(job.Callbacks),
			Context:        cloneMap(job.Context),
		},
	}
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
