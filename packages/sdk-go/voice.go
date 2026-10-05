package ubag

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Typed surfaces for capability discovery and voice sessions (multimodal /
// voice release). Wire shapes mirror apps/gateway/internal/httpapi
// (voice_handlers.go, capabilities.go) and packages/openapi.

// Voice session statuses. "connected" is reported only once the provider voice
// UI is verified ready AND the client media is up; failures terminate the
// session with LastError such as "activation_failed: <state>".
const (
	VoiceStatusQueued     = "queued"
	VoiceStatusConnecting = "connecting"
	VoiceStatusConnected  = "connected"
	VoiceStatusTerminated = "terminated"
)

// Voice session modes.
const (
	VoiceModeLive      = "live"
	VoiceModeUtterance = "utterance"
)

// VoiceCreateRequest is the POST /v1/voice/sessions body.
type VoiceCreateRequest struct {
	Target string `json:"target"`
	// IdentityRef is a preference among the tenant's own provider accounts.
	IdentityRef string `json:"identity_ref,omitempty"`
	// TTLSeconds is the lease window between renewals (30..3600).
	TTLSeconds *int `json:"ttl_seconds,omitempty"`
	// Mode is "live" (default) or "utterance".
	Mode string `json:"mode,omitempty"`
}

// VoiceSession is one voice session record.
type VoiceSession struct {
	SessionID    string    `json:"session_id"`
	TenantID     string    `json:"tenant_id"`
	AppID        string    `json:"app_id"`
	Target       string    `json:"target"`
	Mode         string    `json:"mode"`
	JobID        string    `json:"job_id,omitempty"`
	Status       string    `json:"status"`
	Muted        bool      `json:"muted"`
	IdentityRef  string    `json:"identity_ref,omitempty"`
	InstanceRef  string    `json:"instance_ref,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LeaseExpires time.Time `json:"lease_expires_at"`
	TerminatedAt time.Time `json:"terminated_at"`
}

// VoiceSessionResponse answers create (201 connecting / 202 queued), get and
// terminate. The top-level convenience fields are populated on create only;
// JobID and Next appear for utterance-mode sessions.
type VoiceSessionResponse struct {
	Kind      string       `json:"kind"`
	SessionID string       `json:"session_id,omitempty"`
	Target    string       `json:"target,omitempty"`
	Status    string       `json:"status,omitempty"`
	Mode      string       `json:"mode,omitempty"`
	JobID     string       `json:"job_id,omitempty"`
	Next      string       `json:"next,omitempty"`
	Session   VoiceSession `json:"session"`
}

// VoiceSessionList is the GET /v1/voice/sessions collection.
type VoiceSessionList struct {
	APIVersion string         `json:"api_version"`
	Kind       string         `json:"kind"`
	Data       []VoiceSession `json:"data"`
	NextCursor *string        `json:"next_cursor"`
	TraceID    string         `json:"trace_id"`
}

// VoiceListParams filters ListVoiceSessions.
type VoiceListParams struct {
	Cursor string
	Limit  int
	Target string
}

// VoiceConnectRequest is the POST .../connect body.
type VoiceConnectRequest struct {
	SDPOffer string `json:"sdp_offer"`
}

// ICEServer is one STUN/TURN entry for RTCPeerConnection.iceServers.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// VoiceConnectResponse carries the SDP answer, the short-lived media
// credential that authenticates the "control" data channel, and the ICE
// servers (TURN credentials are time-limited).
type VoiceConnectResponse struct {
	Kind                     string      `json:"kind"`
	SessionID                string      `json:"session_id"`
	Status                   string      `json:"status"`
	SDPAnswer                string      `json:"sdp_answer,omitempty"`
	MediaCredential          string      `json:"media_credential,omitempty"`
	MediaCredentialExpiresMS int64       `json:"media_credential_expires_ms,omitempty"`
	ICEServers               []ICEServer `json:"ice_servers,omitempty"`
}

// VoiceControlResponse answers mute and renew.
type VoiceControlResponse struct {
	Kind           string     `json:"kind"`
	SessionID      string     `json:"session_id"`
	Muted          *bool      `json:"muted,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
}

// AttachmentPolicyView is a target's native attachment policy.
type AttachmentPolicyView struct {
	MaxFiles     int `json:"max_files"`
	MaxFileBytes int `json:"max_file_bytes"`
	Accepted     []struct {
		Kind         string   `json:"kind"`
		ContentTypes []string `json:"content_types"`
	} `json:"accepted"`
}

// InlineMessageParts lists the facade inline part formats the target accepts.
type InlineMessageParts struct {
	ImageURL   []string `json:"image_url"`
	InputAudio []string `json:"input_audio"`
	RemoteURLs bool     `json:"remote_urls"`
}

// VoiceCapability is a target's voice support. The four booleans are
// deliberately separate: Supported (adapter declares a live voice entry),
// Configured (this gateway has voice store, media plane and activation wired),
// Verified (a live two-way acceptance run is recorded; VerifiedNote carries
// the text) and Available (configured AND a free authenticated account +
// environment exists right now). Live is the legacy alias of Supported and
// AvailableAccounts the legacy alias of FreeResources.
type VoiceCapability struct {
	Supported         bool   `json:"supported"`
	Configured        bool   `json:"configured"`
	Verified          bool   `json:"verified"`
	VerifiedNote      string `json:"verified_note,omitempty"`
	Available         bool   `json:"available"`
	FreeResources     int    `json:"free_resources"`
	Live              bool   `json:"live"`
	UtteranceJobs     bool   `json:"utterance_jobs"`
	LiveEntryControl  string `json:"live_entry_control,omitempty"`
	AvailableAccounts int    `json:"available_accounts"`
}

// Capability is one target's media + voice record.
type Capability struct {
	Target              string               `json:"target"`
	DisplayName         string               `json:"display_name"`
	Kind                string               `json:"kind"`
	SafeMode            bool                 `json:"safe_mode"`
	ManualLoginRequired bool                 `json:"manual_login_required"`
	Attachments         AttachmentPolicyView `json:"attachments"`
	InlineMessageParts  InlineMessageParts   `json:"inline_message_parts"`
	Voice               VoiceCapability      `json:"voice"`
}

// CapabilitiesResponse is the GET /v1/capabilities collection.
type CapabilitiesResponse struct {
	APIVersion string       `json:"api_version"`
	Kind       string       `json:"kind"`
	Data       []Capability `json:"data"`
	NextCursor *string      `json:"next_cursor"`
	TraceID    string       `json:"trace_id"`
}

// Find returns the capability record for target.
func (response *CapabilitiesResponse) Find(target string) (Capability, bool) {
	for _, capability := range response.Data {
		if capability.Target == target {
			return capability, true
		}
	}
	return Capability{}, false
}

// ListCapabilities returns per-target media and voice support
// (GET /v1/capabilities).
func (client *Client) ListCapabilities(ctx context.Context, options ...RequestOption) (*CapabilitiesResponse, error) {
	var out CapabilitiesResponse
	if err := client.requestInto(ctx, http.MethodGet, "/v1/capabilities", nil, client.resolveOptions(options...), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateVoiceSession reserves a voice session (POST /v1/voice/sessions): a
// queued session answers 202 (Status "queued"); a full queue is a 429
// *APIError whose RetryAfterMS reports the Retry-After guidance.
func (client *Client) CreateVoiceSession(ctx context.Context, request VoiceCreateRequest, options ...RequestOption) (*VoiceSessionResponse, error) {
	var out VoiceSessionResponse
	if err := client.requestInto(ctx, http.MethodPost, "/v1/voice/sessions", request, client.resolveOptions(options...), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListVoiceSessions returns the tenant's voice sessions (GET /v1/voice/sessions).
func (client *Client) ListVoiceSessions(ctx context.Context, params VoiceListParams, options ...RequestOption) (*VoiceSessionList, error) {
	pairs := make([][2]string, 0, 3)
	if params.Cursor != "" {
		pairs = append(pairs, [2]string{"cursor", params.Cursor})
	}
	if params.Limit > 0 {
		pairs = append(pairs, [2]string{"limit", strconv.Itoa(params.Limit)})
	}
	if params.Target != "" {
		pairs = append(pairs, [2]string{"target", params.Target})
	}
	var out VoiceSessionList
	if err := client.requestInto(ctx, http.MethodGet, "/v1/voice/sessions"+encodeQueryPairs(pairs), nil, client.resolveOptions(options...), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetVoiceSession reads one session's status (GET /v1/voice/sessions/{id}).
func (client *Client) GetVoiceSession(ctx context.Context, sessionID string, options ...RequestOption) (*VoiceSessionResponse, error) {
	var out VoiceSessionResponse
	if err := client.requestInto(ctx, http.MethodGet, voicePath(sessionID, ""), nil, client.resolveOptions(options...), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ConnectVoiceSession negotiates the WebRTC media connection
// (POST /v1/voice/sessions/{id}/connect).
func (client *Client) ConnectVoiceSession(ctx context.Context, sessionID string, request VoiceConnectRequest, options ...RequestOption) (*VoiceConnectResponse, error) {
	var out VoiceConnectResponse
	if err := client.requestInto(ctx, http.MethodPost, voicePath(sessionID, "/connect"), request, client.resolveOptions(options...), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MuteVoiceSession sets the session's mic direction (POST .../mute).
func (client *Client) MuteVoiceSession(ctx context.Context, sessionID string, muted bool, options ...RequestOption) (*VoiceControlResponse, error) {
	var out VoiceControlResponse
	if err := client.requestInto(ctx, http.MethodPost, voicePath(sessionID, "/mute"), map[string]bool{"muted": muted}, client.resolveOptions(options...), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RenewVoiceSessionLease extends the lease window (POST .../renew).
func (client *Client) RenewVoiceSessionLease(ctx context.Context, sessionID string, options ...RequestOption) (*VoiceControlResponse, error) {
	var out VoiceControlResponse
	if err := client.requestInto(ctx, http.MethodPost, voicePath(sessionID, "/renew"), nil, client.resolveOptions(options...), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TerminateVoiceSession terminates the session and releases its leases
// (POST .../terminate). Idempotent.
func (client *Client) TerminateVoiceSession(ctx context.Context, sessionID string, options ...RequestOption) (*VoiceSessionResponse, error) {
	var out VoiceSessionResponse
	if err := client.requestInto(ctx, http.MethodPost, voicePath(sessionID, "/terminate"), nil, client.resolveOptions(options...), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func voicePath(sessionID, action string) string {
	return "/v1/voice/sessions/" + url.PathEscape(sessionID) + action
}
