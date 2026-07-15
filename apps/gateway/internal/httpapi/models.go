package httpapi

import "time"

const (
	DefaultAPIVersion = "2026-05-22"
	serviceName       = "ubag-gateway"
)

// ─────────────────────────────────────────────────────────────────────────────
// Generic response wrappers
// ─────────────────────────────────────────────────────────────────────────────

type healthResponse struct {
	Service   string         `json:"service"`
	Status    string         `json:"status"`
	Version   string         `json:"version,omitempty"`
	CheckedAt time.Time      `json:"checked_at"`
	Checks    map[string]any `json:"checks,omitempty"`
	Ready     bool           `json:"ready,omitempty"`
	TraceID   string         `json:"trace_id"`
}

type versionResponse struct {
	Service           string    `json:"service"`
	Version           string    `json:"version"`
	APIVersions       []string  `json:"api_versions"`
	DefaultAPIVersion string    `json:"default_api_version"`
	Commit            string    `json:"commit"`
	BuiltAt           time.Time `json:"built_at"`
	TraceID           string    `json:"trace_id"`
}

// ─────────────────────────────────────────────────────────────────────────────
// §6.1 Job request envelope
// ─────────────────────────────────────────────────────────────────────────────

type createJobRequest struct {
	APIVersion     string        `json:"api_version"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
	Client         clientRequest `json:"client"`
	Job            jobRequest    `json:"job"`
}

type clientRequest struct {
	AppID      string     `json:"app_id"`
	AppVersion string     `json:"app_version"`
	DeviceID   string     `json:"device_id,omitempty"`
	UserRef    string     `json:"user_ref,omitempty"`
	SDK        sdkRequest `json:"sdk"`
}

type sdkRequest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// jobRequest carries the job-specific fields. Options/Callbacks/Context are
// kept as map[string]any for job-store compatibility; use parseJobOptions() to
// get typed access in validation and orchestration.
type jobRequest struct {
	Target         string         `json:"target"`
	CommandType    string         `json:"command_type"`
	ConversationID string         `json:"conversation_id,omitempty"`
	TemplateID     string         `json:"template_id,omitempty"`
	Input          map[string]any `json:"input"`
	ModelSettings  map[string]any `json:"model_settings,omitempty"`
	Options        map[string]any `json:"options,omitempty"`
	Callbacks      map[string]any `json:"callbacks,omitempty"`
	Context        map[string]any `json:"context,omitempty"`
	// NotBefore defers execution until the given UTC time (RFC3339). When
	// omitted or in the past the job executes immediately (§14.6 scheduling).
	NotBefore *time.Time `json:"not_before,omitempty"`
}

// JobOptions is the typed view of jobRequest.Options (blueprint §6.1).
// Parsed from the raw map via parseJobOptions; never serialised directly.
type JobOptions struct {
	// Priority maps to the 5 orchestration lanes: critical|high|normal|low|bulk (§14.4).
	Priority string `json:"priority,omitempty"`
	// TimeoutSeconds is the per-job maximum wall-clock time in the worker (§13.9).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// ReturnMode controls whether the caller receives "streaming" or "final" (§9.2).
	ReturnMode string `json:"return_mode,omitempty"`
	// ResponseFormats lists the desired output renderings (§16.3).
	ResponseFormats []string `json:"response_formats,omitempty"`
	// RetryPolicy overrides the default retry behaviour (§14.2).
	RetryPolicy *JobRetryPolicy `json:"retry_policy,omitempty"`
	// CachePolicy overrides the default cache behaviour (§17).
	CachePolicy string `json:"cache_policy,omitempty"`
	// TraceContext is the W3C traceparent carried into the browser worker (§18.3).
	TraceContext string `json:"trace_context,omitempty"`
}

// JobRetryPolicy overrides the default retry behaviour (§14.2).
type JobRetryPolicy struct {
	MaxRetries  int    `json:"max_retries,omitempty"`     // 1–10; default 3
	BackoffBase int    `json:"backoff_base_ms,omitempty"` // base delay ms; default 1000
	BackoffMax  int    `json:"backoff_max_ms,omitempty"`  // cap ms; default 60000
	Category    string `json:"category,omitempty"`        // "transient" | "permanent" | ""
}

// JobCallbacks carries the §6.1 callback block.
type JobCallbacks struct {
	WebhookURL      string `json:"webhook_url,omitempty"`
	WebhookSecretID string `json:"webhook_secret_id,omitempty"`
}

// JobContext carries the §6.1 context block (not to be confused with Go context.Context).
type JobContext struct {
	Locale     string   `json:"locale,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	CostCenter string   `json:"cost_center,omitempty"`
}

// parseJobOptions extracts typed options from the raw map, applying defaults.
// Missing fields are left at their zero values; unknown fields are ignored.
func parseJobOptions(raw map[string]any) JobOptions {
	if raw == nil {
		return JobOptions{Priority: "normal"}
	}
	opts := JobOptions{Priority: "normal"}
	if v, ok := raw["priority"].(string); ok && v != "" {
		opts.Priority = v
	}
	if v, ok := raw["timeout_seconds"].(float64); ok {
		opts.TimeoutSeconds = int(v)
	}
	if v, ok := raw["return_mode"].(string); ok {
		opts.ReturnMode = v
	}
	if v, ok := raw["response_formats"].([]any); ok {
		for _, f := range v {
			if s, ok := f.(string); ok {
				opts.ResponseFormats = append(opts.ResponseFormats, s)
			}
		}
	}
	if v, ok := raw["cache_policy"].(string); ok {
		opts.CachePolicy = v
	}
	if v, ok := raw["trace_context"].(string); ok {
		opts.TraceContext = v
	}
	return opts
}

// ─────────────────────────────────────────────────────────────────────────────
// §6.2 Job response envelope — structured result and metadata
// ─────────────────────────────────────────────────────────────────────────────

// JobOutput carries the multi-format output (blueprint §6.2 result.output).
type JobOutput struct {
	Text      string         `json:"text,omitempty"`
	Markdown  string         `json:"markdown,omitempty"`
	PlainText string         `json:"plain_text,omitempty"`
	Sections  map[string]any `json:"sections,omitempty"`
	HTML      string         `json:"html,omitempty"`
}

// JobOutputValidation carries the post-extraction schema validation result (§16).
type JobOutputValidation struct {
	SchemaID string `json:"schema_id,omitempty"`
	Passed   bool   `json:"passed"`
	Errors   []any  `json:"errors,omitempty"`
}

// JobResultEnvelope is the §6.2 result block in a job response.
type JobResultEnvelope struct {
	Output      *JobOutput           `json:"output,omitempty"`
	Validation  *JobOutputValidation `json:"validation,omitempty"`
	Cached      bool                 `json:"cached"`
	CacheSource *string              `json:"cache_source"`
}

// JobCost carries the cost attribution block (§6.2 metadata.cost).
type JobCost struct {
	BrowserSeconds float64 `json:"browser_seconds,omitempty"`
	Credits        float64 `json:"credits,omitempty"`
}

// JobMetadataEnvelope is the §6.2 metadata block in a job response.
type JobMetadataEnvelope struct {
	QueuedAt         *time.Time `json:"queued_at,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	DurationMS       *int64     `json:"duration_ms,omitempty"`
	BrowserSessionID string     `json:"browser_session_id,omitempty"`
	Adapter          string     `json:"adapter,omitempty"`
	Worker           string     `json:"worker,omitempty"`
	Retries          int        `json:"retries"`
	Cost             *JobCost   `json:"cost,omitempty"`
	// Carry-over fields from the v0 metadata map for backward compatibility.
	CommandType    string         `json:"command_type,omitempty"`
	AppID          string         `json:"app_id,omitempty"`
	TenantID       string         `json:"tenant_id,omitempty"`
	ConversationID string         `json:"conversation_id,omitempty"`
	TemplateID     string         `json:"template_id,omitempty"`
	Client         any            `json:"client,omitempty"`
	Input          map[string]any `json:"input,omitempty"`
	Options        map[string]any `json:"options,omitempty"`
	Callbacks      map[string]any `json:"callbacks,omitempty"`
	Context        map[string]any `json:"context,omitempty"`
	RetryOf        string         `json:"retry_of,omitempty"`
}

type jobResponse struct {
	APIVersion       string `json:"api_version"`
	JobID            string `json:"job_id"`
	IdempotentReplay bool   `json:"idempotent_replay"`
	Status           string `json:"status"`
	// Error/ErrorClass carry the last terminal-failure detail and ManualAction the
	// pending human-intervention prompt, reconstructed on read from the recorded
	// worker events (a failed job leaves Result nil). Emitted as top-level strings
	// so consumers that read a flat error/manual_action can surface the real cause
	// instead of a generic empty result. Omitted when absent.
	Error        string              `json:"error,omitempty"`
	ErrorClass   string              `json:"error_class,omitempty"`
	ManualAction string              `json:"manual_action,omitempty"`
	Target       string              `json:"target"`
	Result       *JobResultEnvelope  `json:"result"`
	Metadata     JobMetadataEnvelope `json:"metadata"`
	TraceID      string              `json:"trace_id"`
	EventsURL    string              `json:"events_url"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// §10 API surface — job event, list, batch
// ─────────────────────────────────────────────────────────────────────────────

type jobMutationRequest struct {
	APIVersion     string         `json:"api_version,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	JobID          string         `json:"job_id,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type jobEventResponse struct {
	EventID    string         `json:"event_id"`
	JobID      string         `json:"job_id"`
	APIVersion string         `json:"api_version"`
	Type       string         `json:"type"`
	CreatedAt  time.Time      `json:"created_at"`
	Sequence   int            `json:"sequence"`
	Data       map[string]any `json:"data"`
	TraceID    string         `json:"trace_id"`
}

type listJobsResponse struct {
	APIVersion string        `json:"api_version"`
	Jobs       []jobResponse `json:"jobs"`
	NextCursor *string       `json:"next_cursor"`
	TraceID    string        `json:"trace_id"`
}

type jobEventsResponse struct {
	APIVersion string             `json:"api_version"`
	JobID      string             `json:"job_id"`
	Events     []jobEventResponse `json:"events"`
	NextCursor *string            `json:"next_cursor"`
	TraceID    string             `json:"trace_id"`
}

// batchCreateJobRequest carries up to 100 job submissions in one request (§10, §19.2).
type batchCreateJobRequest struct {
	APIVersion string             `json:"api_version"`
	Jobs       []createJobRequest `json:"jobs"`
}

// batchCreateJobResponse lists the accepted/rejected outcomes per submission.
type batchJobOutcome struct {
	Index            int       `json:"index"`
	Status           string    `json:"status"` // "accepted" | "rejected"
	JobID            string    `json:"job_id,omitempty"`
	IdempotentReplay bool      `json:"idempotent_replay,omitempty"`
	Error            *apiError `json:"error,omitempty"`
}

type batchCreateJobResponse struct {
	APIVersion string            `json:"api_version"`
	Results    []batchJobOutcome `json:"results"`
	Accepted   int               `json:"accepted"`
	Rejected   int               `json:"rejected"`
	TraceID    string            `json:"trace_id"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Collections, webhooks, cache
// ─────────────────────────────────────────────────────────────────────────────

type collectionResponse struct {
	APIVersion string           `json:"api_version"`
	Kind       string           `json:"kind"`
	Data       []map[string]any `json:"data"`
	NextCursor *string          `json:"next_cursor"`
	TraceID    string           `json:"trace_id"`
}

type cacheStatusResponse struct {
	APIVersion string `json:"api_version"`
	Profile    string `json:"profile"`
	Enabled    bool   `json:"enabled"`
	Entries    []any  `json:"entries"`
	TraceID    string `json:"trace_id"`
}

type webhookReplayRequest struct {
	APIVersion     string         `json:"api_version,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	WebhookID      string         `json:"webhook_id,omitempty"`
	DeliveryID     string         `json:"delivery_id,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type webhookReplayResponse struct {
	APIVersion       string         `json:"api_version"`
	Status           string         `json:"status"`
	IdempotentReplay bool           `json:"idempotent_replay"`
	WebhookID        string         `json:"webhook_id,omitempty"`
	DeliveryID       string         `json:"delivery_id,omitempty"`
	AuditEvent       string         `json:"audit_event"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	TraceID          string         `json:"trace_id"`
}
