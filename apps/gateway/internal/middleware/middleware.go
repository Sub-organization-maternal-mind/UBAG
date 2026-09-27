// Package middleware provides the §7.2 gateway middleware chain pieces that
// are shared outside the HTTP handler chain (Trace is registered by
// httpapi; TraceID is read by obs and the gRPC interceptors).
package middleware

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// contextKey is the unexported key type for context values set by this package.
type contextKey int

const keyTraceID contextKey = iota

// TraceID retrieves the trace ID injected by Trace from the request context.
// Returns an empty string if no trace ID is present.
func TraceID(ctx context.Context) string {
	v, _ := ctx.Value(keyTraceID).(string)
	return v
}

// Trace injects a trace ID into every request context and sets the
// X-Trace-ID response header. If the incoming request carries a
// traceparent header, the trace portion is extracted from it; otherwise a
// new hex-random ID is generated.
func Trace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := extractOrGenTraceID(r)
		ctx := context.WithValue(r.Context(), keyTraceID, traceID)
		w.Header().Set("X-Trace-ID", traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// maxInboundTraceIDLen bounds a client-supplied correlation ID. The value ends
// up in log lines, job rows, webhook headers and the WebSocket handshake, so an
// unbounded header value is a log-forging and storage-amplification vector.
const maxInboundTraceIDLen = 64

// isLowerHex reports whether s is non-empty and made only of [0-9a-f].
// Trace and request IDs are emitted as lowercase hex; accepting anything else
// lets a caller inject separators, newlines or ANSI escapes into every log line
// that carries the correlation ID.
func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}

// extractOrGenTraceID resolves the correlation ID for a request.
//
// W3C traceparent is 00-<32 hex trace-id>-<16 hex parent-id>-<2 hex flags>.
// It was previously accepted if it merely had four dash-separated parts with a
// 32-character second field, so the value could be arbitrary text - including
// the all-zero ID the spec forbids, embedded newlines, or ANSI escapes - and
// that value was then stamped on the job row, returned to workers, echoed in
// webhook headers and written into every log line for the request. A
// malformed or hostile header is now ignored and a fresh ID generated, which is
// what the W3C spec requires.
func extractOrGenTraceID(r *http.Request) string {
	if tp := r.Header.Get("Traceparent"); tp != "" {
		if id, ok := traceIDFromTraceparent(tp); ok {
			return id
		}
	}
	// X-Request-Id is the caller-supplied alternative. It is NOT
	// spec-constrained, and operators legitimately send values like
	// "req-abc-123" or a UUID, so it must not be restricted to hex. What must
	// be blocked is anything that could forge a log line or break a header:
	// control characters (CR/LF/NUL/ESC/...), spaces, and non-ASCII bytes. The
	// value is written straight into slog fields, the job row, webhook headers
	// and the WebSocket handshake.
	if rid := r.Header.Get("X-Request-Id"); rid != "" {
		if len(rid) <= maxInboundTraceIDLen && isHeaderSafeToken(rid) {
			return rid
		}
	}
	return genHexID()
}

// isHeaderSafeToken reports whether s is non-empty and consists only of
// printable ASCII excluding space (0x21-0x7E). This is the set that is safe to
// place in an HTTP header value, a log field and a database column without
// escaping.
func isHeaderSafeToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7E {
			return false
		}
	}
	return true
}

// traceIDFromTraceparent extracts and validates the trace-id field of a W3C
// traceparent header.
func traceIDFromTraceparent(tp string) (string, bool) {
	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		return "", false
	}
	version, traceID, parentID, flags := parts[0], parts[1], parts[2], parts[3]
	// version is 2 lowercase hex; future versions may append fields, which
	// len(parts) != 4 already rejects.
	if len(version) != 2 || !isLowerHex(version) {
		return "", false
	}
	// trace-id: exactly 32 lowercase hex, and not all zeroes (spec-invalid).
	if len(traceID) != 32 || !isLowerHex(traceID) || isAllZero(traceID) {
		return "", false
	}
	// parent-id (span-id): exactly 16 lowercase hex, not all zeroes.
	if len(parentID) != 16 || !isLowerHex(parentID) || isAllZero(parentID) {
		return "", false
	}
	// flags: exactly 2 lowercase hex.
	if len(flags) != 2 || !isLowerHex(flags) {
		return "", false
	}
	return traceID, true
}

func isAllZero(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

func genHexID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RequestLog emits a single structured slog line for every HTTP request,
// after it completes. Conforms to the §18.1 log-line contract.
func RequestLog(serviceName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			dur := time.Since(start).Milliseconds()
			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			} else if rec.status >= 400 {
				level = slog.LevelWarn
			}
			slog.Log(r.Context(), level, "http request",
				"service", serviceName,
				"trace_id", TraceID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", dur,
				"remote_addr", remoteAddr(r),
				"user_agent", r.Header.Get("User-Agent"),
			)
		})
	}
}

func remoteAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i != -1 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}

// statusRecorder captures the HTTP status code written by the downstream handler.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.written {
		r.status = status
		r.written = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.written {
		r.status = http.StatusOK
		r.written = true
	}
	return r.ResponseWriter.Write(b)
}

// Hijack implements http.Hijacker so that WebSocket upgrade handlers can take
// control of the underlying connection even when wrapped by this middleware.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not implement http.Hijacker")
	}
	return hijacker.Hijack()
}

// Flush implements http.Flusher so streaming handlers (the SSE job stream, the
// OpenAI facade) can push bytes through this middleware to the client. Without
// it the handler's w.(http.Flusher) assertion fails and the stream never
// flushes until the handler returns.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap exposes the wrapped writer so http.ResponseController can reach the
// underlying connection (e.g. SetWriteDeadline for bounded SSE writes).
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// APIVersionHeader sets the "Ubag-Api-Version-Used" response header to
// version on every request regardless of handler outcome.
func APIVersionHeader(version string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Ubag-Api-Version-Used", version)
			next.ServeHTTP(w, r)
		})
	}
}
