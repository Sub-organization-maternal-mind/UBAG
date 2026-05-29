package ubag

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ErrorEnvelope struct {
	Error ErrorDetails `json:"error"`
}

type ErrorDetails struct {
	Code         string         `json:"code"`
	Category     string         `json:"category"`
	Message      string         `json:"message"`
	Retryable    *bool          `json:"retryable"`
	RetryAfterMS *int64         `json:"retry_after_ms,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
	DocURL       string         `json:"doc_url,omitempty"`
	TraceID      string         `json:"trace_id"`
}

type APIError struct {
	StatusCode int
	Status     string
	URL        string
	Method     string
	Headers    http.Header
	Envelope   *ErrorEnvelope
	Body       any
	RawBody    []byte
}

func (err *APIError) Error() string {
	if err.Envelope != nil && err.Envelope.Error.Message != "" {
		return err.Envelope.Error.Message
	}
	return fmt.Sprintf("UBAG API request failed with HTTP %s", err.Status)
}

func (err *APIError) Code() string {
	if err.Envelope == nil {
		return ""
	}
	return err.Envelope.Error.Code
}

func (err *APIError) Category() string {
	if err.Envelope == nil {
		return ""
	}
	return err.Envelope.Error.Category
}

func (err *APIError) Retryable() bool {
	return err.Envelope != nil && err.Envelope.Error.Retryable != nil && *err.Envelope.Error.Retryable
}

func (err *APIError) RetryAfterMS() (int64, bool) {
	if err.Envelope != nil && err.Envelope.Error.RetryAfterMS != nil {
		return *err.Envelope.Error.RetryAfterMS, true
	}

	retryAfter := err.Headers.Get("Retry-After")
	if retryAfter == "" {
		return 0, false
	}
	if seconds, parseErr := strconv.ParseFloat(retryAfter, 64); parseErr == nil {
		if seconds < 0 {
			seconds = 0
		}
		return int64(seconds * 1000), true
	}
	if retryAt, parseErr := http.ParseTime(retryAfter); parseErr == nil {
		delta := time.Until(retryAt)
		if delta < 0 {
			delta = 0
		}
		return delta.Milliseconds(), true
	}
	return 0, false
}

func (err *APIError) TraceID() string {
	if err.Envelope != nil && err.Envelope.Error.TraceID != "" {
		return err.Envelope.Error.TraceID
	}
	if traceID := err.Headers.Get("Ubag-Trace-Id"); traceID != "" {
		return traceID
	}
	return err.Headers.Get("X-Request-Id")
}

type TransportError struct {
	URL    string
	Method string
	Cause  error
}

func (err *TransportError) Error() string {
	if err.Cause == nil {
		return "UBAG API request could not be sent"
	}
	return "UBAG API request could not be sent: " + err.Cause.Error()
}

func (err *TransportError) Unwrap() error {
	return err.Cause
}

func newAPIError(response *http.Response, responseBody []byte, target, method string) *APIError {
	apiError := &APIError{
		StatusCode: response.StatusCode,
		Status:     response.Status,
		URL:        target,
		Method:     method,
		Headers:    response.Header.Clone(),
		RawBody:    responseBody,
		Body:       string(responseBody),
	}

	var envelope ErrorEnvelope
	if json.Unmarshal(responseBody, &envelope) == nil && IsErrorEnvelope(envelope) {
		apiError.Envelope = &envelope
		apiError.Body = envelope
		return apiError
	}

	var body any
	if json.Unmarshal(responseBody, &body) == nil {
		apiError.Body = body
	}

	return apiError
}

func IsErrorEnvelope(envelope ErrorEnvelope) bool {
	errorDetails := envelope.Error
	return strings.HasPrefix(errorDetails.Code, "UBAG-") &&
		errorDetails.Category != "" &&
		errorDetails.Message != "" &&
		errorDetails.Retryable != nil &&
		errorDetails.TraceID != ""
}
