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

// quotaError constructs a UBAG-QUOTA-* error (§6.3 quota namespace).
func quotaError(code, message string, retryAfterMS *int) apiError {
	return apiError{
		Code:         code,
		Category:     "quota",
		Message:      message,
		Retryable:    retryAfterMS != nil,
		RetryAfterMS: retryAfterMS,
	}
}

// rateError constructs a UBAG-RATE-* error (§6.3 rate namespace).
func rateError(code, message string, retryAfterMS *int) apiError {
	return apiError{
		Code:         code,
		Category:     "rate",
		Message:      message,
		Retryable:    true,
		RetryAfterMS: retryAfterMS,
	}
}

// workerError constructs a UBAG-WORKER-* error (§6.3 worker namespace).
func workerError(code, message string) apiError {
	return apiError{Code: code, Category: "worker", Message: message, Retryable: true}
}

// browserError constructs a UBAG-BROWSER-* error (§6.3 browser namespace).
func browserError(code, message string) apiError {
	return apiError{Code: code, Category: "browser", Message: message, Retryable: true}
}

// contextError constructs a UBAG-CONTEXT-* error (§6.3 v2.1 context namespace).
func contextError(code, message string) apiError {
	return apiError{Code: code, Category: "context", Message: message, Retryable: true}
}

// tabError constructs a UBAG-TAB-* error (§6.3 v2.1 tab namespace).
func tabError(code, message string) apiError {
	return apiError{Code: code, Category: "tab", Message: message, Retryable: true}
}

// concurrencyError constructs a UBAG-CONCURRENCY-* error (§6.3 v2.1 concurrency namespace).
func concurrencyError(code, message string, retryAfterMS *int) apiError {
	return apiError{
		Code:         code,
		Category:     "concurrency",
		Message:      message,
		Retryable:    true,
		RetryAfterMS: retryAfterMS,
	}
}

// adapterError constructs a UBAG-ADAPTER-* error (§6.3 adapter namespace).
func adapterError(code, message string, details map[string]any) apiError {
	return apiError{Code: code, Category: "adapter", Message: message, Retryable: true, Details: details}
}

// targetError constructs a UBAG-TARGET-* error (§6.3 target namespace).
func targetError(code, message string, retryable bool, retryAfterMS *int) apiError {
	return apiError{
		Code:         code,
		Category:     "target",
		Message:      message,
		Retryable:    retryable,
		RetryAfterMS: retryAfterMS,
	}
}

// cacheError constructs a UBAG-CACHE-* error (§6.3 cache namespace).
func cacheError(code, message string, retryable bool) apiError {
	return apiError{Code: code, Category: "cache", Message: message, Retryable: retryable}
}

// webhookError constructs a UBAG-WEBHOOK-* error (§6.3 webhook namespace).
func webhookError(code, message string, retryable bool) apiError {
	return apiError{Code: code, Category: "webhook", Message: message, Retryable: retryable}
}

// ptrInt returns a pointer to an int literal — helper for RetryAfterMS.
func ptrInt(v int) *int { return &v }
