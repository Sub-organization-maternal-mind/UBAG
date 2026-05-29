package httpapi

import "time"

const (
	DefaultAPIVersion = "2026-05-22"
	serviceName       = "ubag-gateway"
)

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

type jobRequest struct {
	Target         string         `json:"target"`
	CommandType    string         `json:"command_type"`
	ConversationID string         `json:"conversation_id,omitempty"`
	TemplateID     string         `json:"template_id,omitempty"`
	Input          map[string]any `json:"input"`
	Options        map[string]any `json:"options,omitempty"`
	Callbacks      map[string]any `json:"callbacks,omitempty"`
	Context        map[string]any `json:"context,omitempty"`
}

type jobMutationRequest struct {
	APIVersion     string         `json:"api_version,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	JobID          string         `json:"job_id,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type jobResponse struct {
	APIVersion       string         `json:"api_version"`
	JobID            string         `json:"job_id"`
	IdempotentReplay bool           `json:"idempotent_replay"`
	Status           string         `json:"status"`
	Target           string         `json:"target"`
	Result           any            `json:"result"`
	Metadata         map[string]any `json:"metadata"`
	TraceID          string         `json:"trace_id"`
	EventsURL        string         `json:"events_url"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
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
