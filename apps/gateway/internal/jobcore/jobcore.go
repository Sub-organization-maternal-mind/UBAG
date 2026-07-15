// Package jobcore holds the protocol-agnostic business rules shared by the
// gateway's HTTP and gRPC front-ends: client/payload normalization, the
// canonical idempotency hash for job creation, and executable-payload safety
// validation. Keeping these here ensures a single source of truth so the HTTP
// and gRPC APIs accept and reject the exact same job payloads.
package jobcore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ubag/ubag/apps/gateway/internal/payloadpolicy"
)

// Client is the normalized client descriptor for a job-creation request.
type Client struct {
	AppID      string
	AppVersion string
	DeviceID   string
	UserRef    string
	SDKName    string
	SDKVersion string
}

// Spec is the normalized job specification for a job-creation request.
type Spec struct {
	Target         string
	CommandType    string
	ConversationID string
	TemplateID     string
	Input          map[string]any
	Options        map[string]any
	Callbacks      map[string]any
	Context        map[string]any
	// ModelSettings is the caller-supplied, catalog-validated per-job provider
	// UI settings map (job.model_settings), keyed by the target adapter's own
	// setting keys. Included in the payload-safety scan so credentials can never
	// ride in on it.
	ModelSettings map[string]any
}

// ClientToMap renders a client descriptor as the canonical map form used for
// hashing, payload validation, and persistence.
func ClientToMap(client Client) map[string]any {
	output := map[string]any{
		"app_id":      strings.TrimSpace(client.AppID),
		"app_version": strings.TrimSpace(client.AppVersion),
		"sdk": map[string]any{
			"name":    strings.TrimSpace(client.SDKName),
			"version": strings.TrimSpace(client.SDKVersion),
		},
	}
	if strings.TrimSpace(client.DeviceID) != "" {
		output["device_id"] = strings.TrimSpace(client.DeviceID)
	}
	if strings.TrimSpace(client.UserRef) != "" {
		output["user_ref"] = strings.TrimSpace(client.UserRef)
	}
	return output
}

// CanonicalCreateHash computes the deterministic idempotency request hash for a
// job-creation request. The hash is stable across protocols for identical
// logical payloads.
func CanonicalCreateHash(apiVersion string, client Client, spec Spec) (string, error) {
	payload := map[string]any{
		"api_version": strings.TrimSpace(apiVersion),
		"client":      ClientToMap(client),
		"job": map[string]any{
			"target":          strings.TrimSpace(spec.Target),
			"command_type":    strings.TrimSpace(spec.CommandType),
			"conversation_id": strings.TrimSpace(spec.ConversationID),
			"template_id":     strings.TrimSpace(spec.TemplateID),
			"input":           spec.Input,
			"options":         spec.Options,
			"callbacks":       spec.Callbacks,
			"context":         spec.Context,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return hashBytes(encoded), nil
}

// ValidatePayload enforces the executable-payload safety policy on a job
// request, ignoring the webhook secret reference which is resolved server-side.
func ValidatePayload(client Client, spec Spec) error {
	callbacks := cloneMap(spec.Callbacks)
	delete(callbacks, "webhook_secret_id")
	payload := map[string]any{
		"client": ClientToMap(client),
		"job": map[string]any{
			"target":          strings.TrimSpace(spec.Target),
			"command_type":    strings.TrimSpace(spec.CommandType),
			"conversation_id": strings.TrimSpace(spec.ConversationID),
			"template_id":     strings.TrimSpace(spec.TemplateID),
			"input":           spec.Input,
			"options":         spec.Options,
			"callbacks":       callbacks,
			"context":         spec.Context,
			"model_settings":  spec.ModelSettings,
		},
	}
	return payloadpolicy.Validate(payload)
}

// Model-catalog error codes (existing `validation` category; see
// packages/shared-schemas/errors.json). MODEL is used for a bad value on the
// provider's `model` selector; MODE covers every other rejected setting.
const (
	codeModelUnavailable = "UBAG-VALIDATION-MODEL-UNAVAILABLE-001"
	codeModeUnavailable  = "UBAG-VALIDATION-MODE-UNAVAILABLE-001"
)

// CatalogSetting declares one caller-selectable provider UI setting.
type CatalogSetting struct {
	Kind   string   `json:"kind"`             // "choice" | "toggle"
	Values []string `json:"values,omitempty"` // choice only
}

// ModelCatalog mirrors the adapter manifest model_catalog block. An empty
// Settings map means nothing is caller-selectable, so any supplied
// model_settings must be rejected (the operator default always applies).
type ModelCatalog struct {
	Settings map[string]CatalogSetting `json:"settings"`
}

// ModelSettingsError is returned by ValidateModelSettings when a requested
// provider UI setting is not permitted by the target adapter's model catalog.
// Code is a catalog error code suitable for the API error envelope; front-ends
// extract it with errors.As.
type ModelSettingsError struct {
	Code    string
	Message string
}

func (e *ModelSettingsError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// ValidateModelSettings enforces that every caller-supplied job.model_settings
// entry is offered by the target adapter's model catalog. It is a security
// control, not only UX: each value is interpolated into a Playwright selector
// via .format(value=desired) in the worker's page_driver, so an out-of-catalog
// value is a drift/injection surface that must never reach a browser.
//
// Rules (see the orchestration-semantics plan, Task B3 / Step 5):
//   - nil/empty settings → nil (operator defaults apply).
//   - any "_"-prefixed key → error (reserved worker control keys, e.g.
//     _enabled / _new_chat).
//   - every key must exist in catalog.Settings, else MODE-UNAVAILABLE.
//   - kind "choice" → value must be a string present in Values; a bad value on
//     the "model" key returns MODEL-UNAVAILABLE, any other choice key returns
//     MODE-UNAVAILABLE.
//   - kind "toggle" → value must be a bool, else MODE-UNAVAILABLE.
//   - an empty catalog rejects any supplied setting.
func ValidateModelSettings(target string, settings map[string]any, catalog ModelCatalog) error {
	if len(settings) == 0 {
		return nil
	}
	for key, value := range settings {
		if strings.HasPrefix(key, "_") {
			return &ModelSettingsError{
				Code:    codeModeUnavailable,
				Message: fmt.Sprintf("model_settings key %q is reserved and cannot be set by callers", key),
			}
		}
		setting, ok := catalog.Settings[key]
		if !ok {
			return &ModelSettingsError{
				Code:    codeModeUnavailable,
				Message: fmt.Sprintf("target %q does not offer a %q setting", target, key),
			}
		}
		switch setting.Kind {
		case "toggle":
			if _, ok := value.(bool); !ok {
				return &ModelSettingsError{
					Code:    codeModeUnavailable,
					Message: fmt.Sprintf("target %q setting %q expects a boolean", target, key),
				}
			}
		case "choice":
			str, ok := value.(string)
			if !ok || !slices.Contains(setting.Values, str) {
				return &ModelSettingsError{
					Code:    catalogChoiceCode(key),
					Message: fmt.Sprintf("value %v is not available for target %q setting %q", value, target, key),
				}
			}
		default:
			return &ModelSettingsError{
				Code:    codeModeUnavailable,
				Message: fmt.Sprintf("target %q setting %q has unsupported kind %q", target, key, setting.Kind),
			}
		}
	}
	return nil
}

// catalogChoiceCode selects the error code for a rejected choice value: the
// provider's model selector reports MODEL-UNAVAILABLE, every other choice
// (thinking mode, provider mode, …) reports MODE-UNAVAILABLE.
func catalogChoiceCode(key string) string {
	if key == "model" {
		return codeModelUnavailable
	}
	return codeModeUnavailable
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = normalizeJSONValue(value)
	}
	return output
}

func normalizeJSONValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed.String()
	case map[string]any:
		return cloneMap(typed)
	case []any:
		output := make([]any, 0, len(typed))
		for _, item := range typed {
			output = append(output, normalizeJSONValue(item))
		}
		return output
	default:
		return value
	}
}
