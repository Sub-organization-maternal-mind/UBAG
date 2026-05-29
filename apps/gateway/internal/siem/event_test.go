package siem

import (
	"testing"
	"time"
)

func sampleAttributes() map[string]any {
	return map[string]any{
		"password":      "hunter2",
		"api_key":       "AKIA1234567890",
		"Authorization": "Bearer abc123",
		"cookie":        "session=xyz",
		"private_key":   "-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----",
		"mfa_code":      "123456",
		"totp":          "987654",
		"safe_field":    "ordinary value",
		"nested": map[string]any{
			"secret_token": "nested-secret",
			"note":         "Bearer eyJabc.payloadpart.signaturepart",
			"jwt":          "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcDEFghiJKL",
			"keep":         "fine",
		},
		"list": []any{"Bearer tok-value-1234", "harmless"},
	}
}

func TestRedactStripsDenylistedKeys(t *testing.T) {
	in := Event{
		ID:         "evt_1",
		TenantID:   "tenant-a",
		Timestamp:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Attributes: sampleAttributes(),
	}
	out := Redact(in)

	denylisted := []string{"password", "api_key", "Authorization", "cookie", "private_key", "mfa_code", "totp"}
	for _, key := range denylisted {
		if got := out.Attributes[key]; got != redactedPlaceholder {
			t.Fatalf("expected key %q redacted, got %v", key, got)
		}
	}
	if out.Attributes["safe_field"] != "ordinary value" {
		t.Fatalf("safe_field should be untouched, got %v", out.Attributes["safe_field"])
	}
}

func TestRedactScansNestedAndValueHeuristics(t *testing.T) {
	in := Event{Attributes: sampleAttributes()}
	out := Redact(in)

	nested, ok := out.Attributes["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested attribute missing or wrong type: %T", out.Attributes["nested"])
	}
	if nested["secret_token"] != redactedPlaceholder {
		t.Fatalf("nested denylisted key not redacted: %v", nested["secret_token"])
	}
	if nested["note"] != redactedPlaceholder {
		t.Fatalf("bearer-looking value not redacted: %v", nested["note"])
	}
	if nested["jwt"] != redactedPlaceholder {
		t.Fatalf("jwt-looking value not redacted: %v", nested["jwt"])
	}
	if nested["keep"] != "fine" {
		t.Fatalf("benign nested value altered: %v", nested["keep"])
	}

	list, ok := out.Attributes["list"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("list attribute malformed: %v", out.Attributes["list"])
	}
	if list[0] != redactedPlaceholder {
		t.Fatalf("bearer value in slice not redacted: %v", list[0])
	}
	if list[1] != "harmless" {
		t.Fatalf("benign slice value altered: %v", list[1])
	}
}

func TestRedactDoesNotMutateInput(t *testing.T) {
	in := Event{Attributes: map[string]any{"password": "hunter2"}}
	_ = Redact(in)
	if in.Attributes["password"] != "hunter2" {
		t.Fatalf("input event was mutated: %v", in.Attributes["password"])
	}
}

func TestRedactNormalizesTimestampUTC(t *testing.T) {
	loc := time.FixedZone("UTC+5", 5*3600)
	in := Event{Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, loc)}
	out := Redact(in)
	if out.Timestamp.Location() != time.UTC {
		t.Fatalf("expected UTC timestamp, got %v", out.Timestamp.Location())
	}
}
