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

// flushingWriter is a ResponseWriter mock that records Flush calls so tests can
// assert a wrapping recorder forwards flushes to the real writer.
type flushingWriter struct {
	header  http.Header
	flushes int
}

func (w *flushingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *flushingWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *flushingWriter) WriteHeader(int) {}

func (w *flushingWriter) Flush() { w.flushes++ }

// plainWriter is a ResponseWriter that deliberately does NOT implement
// http.Flusher, so a wrapping recorder's Flush must be a safe no-op.
type plainWriter struct {
	header http.Header
}

func (w *plainWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *plainWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *plainWriter) WriteHeader(int) {}

// The SSE handler's w.(http.Flusher) assertion only succeeds when the recorder
// chain implements and forwards Flush; a streaming handler wrapped by
// RequestLog must be able to flush through it.
func TestStatusRecorderImplementsFlusher(t *testing.T) {
	if _, ok := any(&statusRecorder{}).(http.Flusher); !ok {
		t.Fatal("statusRecorder does not implement http.Flusher")
	}
}

func TestStatusRecorderFlushForwardsToWriter(t *testing.T) {
	mock := &flushingWriter{}
	recorder := &statusRecorder{ResponseWriter: mock, status: http.StatusOK}
	flusher, ok := any(recorder).(http.Flusher)
	if !ok {
		t.Fatal("statusRecorder does not implement http.Flusher")
	}
	flusher.Flush()
	if mock.flushes != 1 {
		t.Fatalf("flushes = %d, want 1", mock.flushes)
	}
}

func TestStatusRecorderFlushWithoutFlusherIsNoop(t *testing.T) {
	recorder := &statusRecorder{ResponseWriter: &plainWriter{}, status: http.StatusOK}
	flusher, ok := any(recorder).(http.Flusher)
	if !ok {
		t.Fatal("statusRecorder does not implement http.Flusher")
	}
	flusher.Flush() // must not panic when the wrapped writer cannot flush
}
