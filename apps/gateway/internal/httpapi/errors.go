package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code         string         `json:"code"`
	Category     string         `json:"category"`
	Message      string         `json:"message"`
	Retryable    bool           `json:"retryable"`
	RetryAfterMS *int           `json:"retry_after_ms,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
	DocURL       string         `json:"doc_url"`
	TraceID      string         `json:"trace_id"`
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, err apiError) {
	err.TraceID = traceIDFromContext(r.Context())
	if err.DocURL == "" {
		err.DocURL = fmt.Sprintf("https://docs.ubag.dev/errors/%s", err.Code)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: err})
}

func validationError(code, message string) apiError {
	return apiError{
		Code:      code,
		Category:  "validation",
		Message:   message,
		Retryable: false,
	}
}

func authError(code, message string) apiError {
	return apiError{
		Code:      code,
		Category:  "auth",
		Message:   message,
		Retryable: false,
	}
}

func authzError(code, message string) apiError {
	return apiError{
		Code:      code,
		Category:  "authz",
		Message:   message,
		Retryable: false,
	}
}

func queueError(code, message string, retryable bool) apiError {
	return apiError{
		Code:      code,
		Category:  "queue",
		Message:   message,
		Retryable: retryable,
	}
}

func internalError(message string) apiError {
	return apiError{
		Code:      "UBAG-INTERNAL-GATEWAY-001",
		Category:  "internal",
		Message:   message,
		Retryable: true,
	}
}
