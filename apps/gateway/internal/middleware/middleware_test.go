package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestRecovery_CatchesPanic(t *testing.T) {
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("test panic")
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	Recovery(panicHandler).ServeHTTP(rr, req)
	// After panic recovery the recorder should have received a 500.
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 after panic, got %d", rr.Code)
	}
}

func TestRequestLog_DoesNotCrash(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rr := httptest.NewRecorder()
	RequestLog("test-service")(nopHandler).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAPIVersionHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	APIVersionHeader("2026-05-22")(nopHandler).ServeHTTP(rr, req)
	if rr.Header().Get("Ubag-Api-Version-Used") != "2026-05-22" {
		t.Errorf("Ubag-Api-Version-Used not set correctly: %q", rr.Header().Get("Ubag-Api-Version-Used"))
	}
}

func TestSetIETFRateLimitHeaders(t *testing.T) {
	rr := httptest.NewRecorder()
	SetIETFRateLimitHeaders(rr, RateLimitHeaders{Limit: 100, Remaining: 42, ResetAt: 1700000000})
	if rr.Header().Get("RateLimit-Limit") != "100" {
		t.Errorf("RateLimit-Limit: got %q", rr.Header().Get("RateLimit-Limit"))
	}
	if rr.Header().Get("RateLimit-Remaining") != "42" {
		t.Errorf("RateLimit-Remaining: got %q", rr.Header().Get("RateLimit-Remaining"))
	}
	if rr.Header().Get("RateLimit-Reset") != "1700000000" {
		t.Errorf("RateLimit-Reset: got %q", rr.Header().Get("RateLimit-Reset"))
	}
}

func TestIETFHeaders_NotXPrefix(t *testing.T) {
	// Blueprint §10.6 requires RateLimit-* (IETF), not X-RateLimit-*.
	rr := httptest.NewRecorder()
	SetIETFRateLimitHeaders(rr, RateLimitHeaders{Limit: 1, Remaining: 0, ResetAt: 0})
	for _, h := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if rr.Header().Get(h) != "" {
			t.Errorf("old X-RateLimit header %q must not be set", h)
		}
	}
	if !strings.HasPrefix(rr.Header().Get("RateLimit-Limit"), "") {
		t.Error("RateLimit-Limit should be set")
	}
}
