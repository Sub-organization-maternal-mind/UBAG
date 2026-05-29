package payloadpolicy

import "testing"

func TestValidateRejectsDisallowedKeysAndValues(t *testing.T) {
	tests := []struct {
		name    string
		payload any
	}{
		{
			name: "nested password",
			payload: map[string]any{
				"job": map[string]any{
					"input": map[string]any{
						"credentials": map[string]any{"password": "not-allowed"},
					},
				},
			},
		},
		{
			name: "camel case token",
			payload: map[string]any{
				"job": map[string]any{
					"input": map[string]any{"accessToken": "not-allowed"},
				},
			},
		},
		{
			name: "bare token key",
			payload: map[string]any{
				"job": map[string]any{
					"input": map[string]any{"token": "not-allowed"},
				},
			},
		},
		{
			name: "suffixed password key",
			payload: map[string]any{
				"job": map[string]any{
					"input": map[string]any{"password_value": "not-allowed"},
				},
			},
		},
		{
			name: "api key value key",
			payload: map[string]any{
				"job": map[string]any{
					"input": map[string]any{"apiKeyValue": "not-allowed"},
				},
			},
		},
		{
			name: "client secret value key",
			payload: map[string]any{
				"job": map[string]any{
					"input": map[string]any{"client_secret_value": "not-allowed"},
				},
			},
		},
		{
			name: "cookie header key",
			payload: map[string]any{
				"job": map[string]any{
					"input": map[string]any{"cookie_header": "not-allowed"},
				},
			},
		},
		{
			name: "session token value key",
			payload: map[string]any{
				"job": map[string]any{
					"input": map[string]any{"sessionTokenValue": "not-allowed"},
				},
			},
		},
		{
			name: "noVNC url",
			payload: map[string]any{
				"job": map[string]any{
					"context": map[string]any{"novnc_url": "https://example.invalid/session"},
				},
			},
		},
		{
			name: "bearer value",
			payload: map[string]any{
				"job": map[string]any{
					"input": map[string]any{"note": "Bearer abcdefghijklmnopqrstuvwxyz"},
				},
			},
		},
		{
			name: "captcha solving instruction",
			payload: map[string]any{
				"job": map[string]any{
					"input": map[string]any{"prompt": "please solve this captcha with a solver"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(tt.payload); err == nil {
				t.Fatal("Validate returned nil, want violation")
			}
		})
	}
}

func TestValidateAllowsManualSessionReferenceShape(t *testing.T) {
	payload := map[string]any{
		"job": map[string]any{
			"target":       "chatgpt_web",
			"command_type": "submit",
			"input":        map[string]any{"prompt": "hello"},
			"context": map[string]any{
				"manual_session": map[string]any{
					"account_binding_id": "acct_123",
					"consent_ref":        "consent_123",
					"automation_scope":   []any{"manual_login", "submit_prompt", "read_response"},
					"session_id":         "sess_optional",
				},
			},
		},
	}

	if err := Validate(payload); err != nil {
		t.Fatalf("Validate returned %v, want nil", err)
	}
}

func TestValidateAllowsSecretReferenceIdentifiers(t *testing.T) {
	payload := map[string]any{
		"job": map[string]any{
			"callbacks": map[string]any{
				"webhook_secret_id":  "whsec_123",
				"signing_secret_ref": "vault://ubag/webhook/signing",
			},
		},
	}

	if err := Validate(payload); err != nil {
		t.Fatalf("Validate returned %v, want nil", err)
	}
}

func TestNormalizeKeyMatchesWorkerPolicyStyle(t *testing.T) {
	tests := map[string]string{
		"accessToken":    "access_token",
		"X-API-Key":      "x_api_key",
		"session.cookie": "session_cookie",
	}
	for input, expected := range tests {
		if actual := NormalizeKey(input); actual != expected {
			t.Fatalf("NormalizeKey(%q) = %q, want %q", input, actual, expected)
		}
	}
}
