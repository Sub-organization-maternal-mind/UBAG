package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// nopHandler is a no-op downstream handler used in middleware tests.
var nopHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestTrace_GeneratesID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	Trace(nopHandler).ServeHTTP(rr, req)

	id := rr.Header().Get("X-Trace-ID")
	if id == "" {
		t.Fatal("X-Trace-ID header not set")
	}
	if len(id) != 32 { // 16 random bytes hex-encoded
		t.Errorf("unexpected trace ID length %d: %q", len(id), id)
	}
}

func TestTrace_PropagatesTraceparent(t *testing.T) {
	traceID := "4bf92f3577b34da6a3ce29d0f3b49d23"
	tp := "00-" + traceID + "-00f067aa0ba902b7-01"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Traceparent", tp)
	rr := httptest.NewRecorder()

	var captured string
	Trace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = TraceID(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if captured != traceID {
		t.Errorf("expected trace ID %q, got %q", traceID, captured)
	}
	if rr.Header().Get("X-Trace-ID") != traceID {
		t.Errorf("X-Trace-ID header mismatch: %q", rr.Header().Get("X-Trace-ID"))
	}
}

func TestTrace_PropagatesXRequestID(t *testing.T) {
	rid := "req-abc-123"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", rid)
	rr := httptest.NewRecorder()
	Trace(nopHandler).ServeHTTP(rr, req)
	if rr.Header().Get("X-Trace-ID") != rid {
		t.Errorf("expected X-Trace-ID = %q, got %q", rid, rr.Header().Get("X-Trace-ID"))
	}
}
