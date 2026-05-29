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
		},
	}
	return payloadpolicy.Validate(payload)
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
