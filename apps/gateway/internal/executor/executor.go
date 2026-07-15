package executor

import (
	"context"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/conversations"
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
	APIVersion     string `json:"api_version"`
	JobID          string `json:"job_id"`
	TenantID       string `json:"tenant_id"`
	AppID          string `json:"app_id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	TraceID        string `json:"trace_id"`
	RetryOf        string `json:"retry_of,omitempty"`
	// NotBefore, when set, instructs the consumer to nack+requeue the job
	// until time.Now() >= NotBefore (§14.6 scheduling).
	NotBefore *time.Time  `json:"not_before,omitempty"`
	Job       DispatchJob `json:"job"`
	// Conversation carries the conversation-affinity binding for the job. It is
	// populated only when the gateway has conversations enabled (a non-nil
	// manager) and the job carries a conversation_id; otherwise it is omitted so
	// the envelope is byte-identical to the pre-feature form.
	Conversation *DispatchConversation `json:"conversation,omitempty"`
	Client       map[string]any        `json:"client,omitempty"`
	CreatedAt    time.Time             `json:"created_at"`
}

// DispatchConversation is the worker-envelope conversation-affinity block. The
// worker uses it to resume the bound provider chat thread (ThreadRef, a chat URL
// only — never cookies or storage state) and to decide what to do when the bound
// thread has vanished (OnMissing mirrors job.options.conversation_missing).
type DispatchConversation struct {
	Key       string `json:"key"`
	ThreadRef string `json:"thread_ref,omitempty"`
	OnMissing string `json:"on_missing,omitempty"`
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

// EnvelopeFromJob builds the worker dispatch envelope for a job without any
// conversation affinity. It is preserved for the existing dispatch call sites
// and is byte-identical to the pre-feature form; it delegates to
// EnvelopeFromJobWithConversation with a nil manager.
func EnvelopeFromJob(job jobstore.Job) DispatchEnvelope {
	return EnvelopeFromJobWithConversation(context.Background(), job, nil)
}

// EnvelopeFromJobWithConversation builds the worker dispatch envelope and layers
// on the flag-gated conversation-affinity feature:
//
//   - When manager is non-nil (conversations enabled) and the job carries a
//     conversation_id, a conversation block is injected with the thread ref
//     resolved from the store (empty when the key is not yet bound) and
//     on_missing taken from job.options.conversation_missing (default "fail").
//
// options.provider_config is not handled here: it rides in job.Options (the
// gateway injects the validated model_settings into options.provider_config at
// create time) and flows through unchanged via cloneMap(job.Options). With
// manager nil the result is byte-identical to EnvelopeFromJob, so dispatch
// paths that have not opted in are unaffected.
func EnvelopeFromJobWithConversation(ctx context.Context, job jobstore.Job, manager *conversations.Manager) DispatchEnvelope {
	envelope := DispatchEnvelope{
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

	envelope.Conversation = conversationBlockForJob(ctx, job, manager)
	return envelope
}

// ProviderConfigFromModelSettings copies model_settings into the flat
// provider_config dict the worker reads, dropping any reserved control key
// (any key beginning with "_", e.g. _enabled / _new_chat). It returns an empty
// map when there is nothing to send so callers can omit provider_config and let
// the operator defaults apply.
func ProviderConfigFromModelSettings(settings map[string]any) map[string]any {
	if len(settings) == 0 {
		return nil
	}
	config := make(map[string]any, len(settings))
	for key, value := range settings {
		if strings.HasPrefix(key, "_") {
			continue
		}
		config[key] = value
	}
	return config
}

// conversationBlockForJob resolves the conversation-affinity block for a job.
// It returns nil (block omitted) when conversations are disabled (nil manager)
// or the job carries no conversation_id, keeping the envelope byte-identical to
// the pre-feature form. Otherwise the block is always present: thread_ref is the
// resolved provider chat URL, or empty when the key is not yet bound (or a
// resolve error occurs — the worker's on_missing policy then governs).
func conversationBlockForJob(ctx context.Context, job jobstore.Job, manager *conversations.Manager) *DispatchConversation {
	conversationID := strings.TrimSpace(job.ConversationID)
	if manager == nil || conversationID == "" {
		return nil
	}
	block := &DispatchConversation{
		Key:       conversationID,
		OnMissing: conversationOnMissing(job.Options),
	}
	if conv, found, err := manager.Resolve(ctx, conversations.Key{
		TenantID:        job.TenantID,
		AppID:           job.AppID,
		Target:          job.Target,
		ConversationKey: conversationID,
	}); err == nil && found {
		block.ThreadRef = conv.ProviderThreadRef
	}
	return block
}

// conversationOnMissing reads job.options.conversation_missing, defaulting to
// "fail" when absent or blank (matching the job-request schema default).
func conversationOnMissing(options map[string]any) string {
	if v, ok := options["conversation_missing"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return "fail"
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
