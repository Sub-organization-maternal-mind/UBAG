package executor

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/payloadpolicy"
)

// HelperAttemptSpec is what the primary sends to a helper node for one attempt
// (UBAG_HELPER_PLANE). It is a projection of DispatchEnvelope, deliberately a
// separate type so new envelope fields never leak to a helper by default:
// callbacks, client, context, local attachment paths and browser profile
// options are dropped, and the helper-side browser profile is addressed only
// by the opaque ProfileRef. Inert: nothing sends it yet.
type HelperAttemptSpec struct {
	APIVersion     string                `json:"api_version"`
	JobID          string                `json:"job_id"`
	TenantID       string                `json:"tenant_id"`
	AppID          string                `json:"app_id"`
	TraceID        string                `json:"trace_id"`
	AttemptID      string                `json:"attempt_id"`
	ProfileRef     string                `json:"profile_ref"`
	Target         string                `json:"target"`
	CommandType    string                `json:"command_type"`
	ConversationID string                `json:"conversation_id,omitempty"`
	TemplateID     string                `json:"template_id,omitempty"`
	Input          map[string]any        `json:"input,omitempty"`
	Options        map[string]any        `json:"options,omitempty"`
	Conversation   *DispatchConversation `json:"conversation,omitempty"`
	CreatedAt      time.Time             `json:"created_at"`
}

var (
	// ErrHelperTargetRefused: antigravity_* targets hold gateway-side
	// credentials/accounts and never run on a helper.
	ErrHelperTargetRefused = errors.New("helper: target is not eligible for helper execution")
	// ErrHelperSpecInvalid: a required field is missing or the projected spec
	// failed the payload policy (fail closed).
	ErrHelperSpecInvalid = errors.New("helper: invalid attempt spec")

	helperProfileRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

	// Local-only job options: the worker resolves these to a profile directory
	// on the primary's filesystem, meaningless (and path-leaking) on a helper.
	// account_binding_id is the caller-supplied identity label; on a helper the
	// identity is the primary-issued profile_ref only.
	helperDroppedOptions = []string{"user_data_dir", "profile_dir", "profile_path", "account_binding_id"}
	// Primary-local input fields injected by attachment materialisation.
	helperDroppedInputs = []string{"attachment_local_paths", "audio_local_path"}

	// The helper must not learn where the primary's browser or viewer lives.
	helperDeniedEnv = map[string]struct{}{
		"UBAG_REMOTE_BROWSER_ENDPOINT": {},
		"UBAG_NOVNC_BASE_URL":          {},
	}
)

// NewHelperAttemptSpec projects an envelope for helper dispatch. profileRef is
// the opaque identity/profile handle the helper maps to its own profile.
func NewHelperAttemptSpec(env DispatchEnvelope, attemptID, profileRef string) (HelperAttemptSpec, error) {
	if strings.HasPrefix(env.Job.Target, "antigravity_") {
		return HelperAttemptSpec{}, ErrHelperTargetRefused
	}
	if env.JobID == "" || env.TenantID == "" || env.Job.Target == "" || attemptID == "" {
		return HelperAttemptSpec{}, fmt.Errorf("%w: job_id, tenant_id, target and attempt_id are required", ErrHelperSpecInvalid)
	}
	if !helperProfileRefPattern.MatchString(profileRef) {
		return HelperAttemptSpec{}, fmt.Errorf("%w: profile_ref must be an opaque token", ErrHelperSpecInvalid)
	}
	spec := HelperAttemptSpec{
		APIVersion:     env.APIVersion,
		JobID:          env.JobID,
		TenantID:       env.TenantID,
		AppID:          env.AppID,
		TraceID:        env.TraceID,
		AttemptID:      attemptID,
		ProfileRef:     profileRef,
		Target:         env.Job.Target,
		CommandType:    env.Job.CommandType,
		ConversationID: env.Job.ConversationID,
		TemplateID:     env.Job.TemplateID,
		Input:          cloneMap(env.Job.Input),
		Options:        cloneMap(env.Job.Options),
		CreatedAt:      env.CreatedAt,
	}
	if env.Conversation != nil {
		c := *env.Conversation
		spec.Conversation = &c
	}
	for _, k := range helperDroppedInputs {
		delete(spec.Input, k)
	}
	for _, k := range helperDroppedOptions {
		delete(spec.Options, k)
	}
	// Defence in depth: the job payload was validated at create time, but the
	// spec leaves the trust boundary, so re-check it with the same policy.
	if err := payloadpolicy.Validate(spec.policyView()); err != nil {
		return HelperAttemptSpec{}, fmt.Errorf("%w: payload policy", ErrHelperSpecInvalid)
	}
	return spec, nil
}

// policyView exposes the spec as a generic map for payloadpolicy.Validate.
func (s HelperAttemptSpec) policyView() map[string]any {
	view := map[string]any{
		"api_version": s.APIVersion, "job_id": s.JobID, "tenant_id": s.TenantID,
		"app_id": s.AppID, "trace_id": s.TraceID, "attempt_id": s.AttemptID,
		"profile_ref": s.ProfileRef, "target": s.Target, "command_type": s.CommandType,
		"conversation_id": s.ConversationID, "template_id": s.TemplateID,
		"input": s.Input, "options": s.Options,
	}
	if s.Conversation != nil {
		view["conversation"] = map[string]any{
			"key": s.Conversation.Key, "thread_ref": s.Conversation.ThreadRef, "on_missing": s.Conversation.OnMissing,
		}
	}
	return view
}

// helperWorkerEnv is the environment for a helper-side worker: the shared
// worker allowlist minus the primary's remote-browser and noVNC endpoints.
func helperWorkerEnv() []string { return filteredWorkerEnv(helperDeniedEnv) }
