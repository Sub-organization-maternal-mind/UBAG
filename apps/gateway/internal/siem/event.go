// Package siem provides SIEM / audit event export for the UBAG gateway.
//
// It models its store structure on internal/webhooks and depends only on the
// Go standard library plus database/sql. Every event is passed through a
// redaction pass (see Redact) before it is handed to any Sink, so raw
// secrets, tokens, cookies, credentials, and authorization material are never
// exported.
package siem

import (
	"regexp"
	"strings"
	"time"
)

// redactedPlaceholder is substituted for any value that is recognized as
// sensitive, either by attribute key or by value heuristic.
const redactedPlaceholder = "[REDACTED]"

// Event is a single audit / SIEM record. Timestamps are always serialized as
// UTC RFC3339 (see MarshalJSON-equivalent normalization performed by the
// sinks). Attributes carry structured, action-specific context and are the
// primary surface scanned for secrets.
type Event struct {
	ID         string         `json:"id"`
	TenantID   string         `json:"tenant_id"`
	AppID      string         `json:"app_id"`
	Type       string         `json:"type"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	Resource   string         `json:"resource"`
	Outcome    string         `json:"outcome"`
	Timestamp  time.Time      `json:"timestamp"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// sensitiveKeyTokens is the denylist of substrings that mark an attribute key
// (case-insensitive) as sensitive. Any match causes the value to be replaced
// with redactedPlaceholder regardless of its content.
var sensitiveKeyTokens = []string{
	"password",
	"secret",
	"token",
	"cookie",
	"authorization",
	"credential",
	"private_key",
	"mfa",
	"totp",
	"captcha",
	"bearer",
	"api_key",
}

// bearerPattern matches an HTTP "Bearer <token>" authorization value.
var bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9\-._~+/]+=*`)

// jwtPattern matches a compact JWS / JWT: three base64url segments separated
// by dots, with realistic minimum segment lengths to avoid matching ordinary
// dotted identifiers.
var jwtPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`)

// privateKeyPattern matches PEM private-key blocks.
var privateKeyPattern = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)

// Redact returns a deep copy of e with sensitive attribute keys and values
// obscured. The input event is never mutated. Redaction applies two rules:
//
//  1. Key denylist: any attribute whose key contains a denylisted substring
//     (case-insensitive) has its value replaced with "[REDACTED]".
//  2. Value heuristics: string values (at any nesting depth) that look like a
//     bearer header, a JWT, or a PEM private key are replaced with
//     "[REDACTED]".
func Redact(e Event) Event {
	out := e
	if e.Attributes != nil {
		out.Attributes = redactMap(e.Attributes)
	}
	out.Timestamp = e.Timestamp.UTC()
	return out
}

func redactMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		if isSensitiveKey(key) {
			out[key] = redactedPlaceholder
			continue
		}
		out[key] = redactValue(value)
	}
	return out
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case string:
		return redactString(typed)
	case map[string]any:
		return redactMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactValue(item)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			out[i] = redactString(item)
		}
		return out
	default:
		return value
	}
}

func redactString(value string) string {
	if value == "" {
		return value
	}
	if bearerPattern.MatchString(value) ||
		jwtPattern.MatchString(value) ||
		privateKeyPattern.MatchString(value) {
		return redactedPlaceholder
	}
	return value
}

func isSensitiveKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return false
	}
	for _, token := range sensitiveKeyTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}
