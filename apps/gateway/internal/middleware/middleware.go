// Package middleware provides the §7.2 gateway middleware chain as independent,
// composable net/http middleware functions. The intended stacking order is:
//
//	Metrics → Recovery → Trace → Log → Auth → RateLimit → APIVersion → (handler)
//
// Each piece is a standalone function, not a method on Server, so it can be
// unit-tested and reused outside the main server (e.g. gRPC interceptors).
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
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// contextKey is the unexported key type for context values set by this package.
type contextKey int

const (
	keyTraceID    contextKey = iota
	keyPrincipal             // set by Auth middleware; retrieved via Principal()
	keyAPIVersion            // resolved api_version after the version middleware
)

// ————————————————————————————————————————————————————————————————————
// 1. Trace — inject a per-request trace ID (W3C traceparent or generated ULID-
//    style hex). Blueprint §18.3: W3C tracecontext propagated through the chain.
// ————————————————————————————————————————————————————————————————————

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

func extractOrGenTraceID(r *http.Request) string {
	// W3C traceparent: 00-<trace-id>-<parent-id>-<flags>
	if tp := r.Header.Get("Traceparent"); tp != "" {
		parts := strings.Split(tp, "-")
		if len(parts) == 4 && len(parts[1]) == 32 {
			return parts[1]
		}
	}
	if rid := r.Header.Get("X-Request-Id"); rid != "" {
		return rid
	}
	return genHexID()
}

func genHexID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ————————————————————————————————————————————————————————————————————
// 2. Recovery — catch panics and return 500 instead of crashing the process.
//    Blueprint §7.2 middleware chain, §20.4 graceful shutdown.
// ————————————————————————————————————————————————————————————————————

// Recovery catches panics in downstream handlers, logs them at Error level
// (including stack trace), and writes an HTTP 500 response.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				traceID := TraceID(r.Context())
				slog.Error("handler panic recovered",
					"trace_id", traceID,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				if !headersWritten(w) {
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// headersWritten is a best-effort check; it relies on the response writer
// having already called WriteHeader when a panic fires mid-body.
func headersWritten(_ http.ResponseWriter) bool { return false } // conservative: always attempt 500

// ————————————————————————————————————————————————————————————————————
// 3. RequestLog — emit one structured JSON log line per request.
//    Blueprint §18.1: ts, level, service, trace_id, method, path, status,
//    duration_ms, remote_addr.
// ————————————————————————————————————————————————————————————————————

// RequestLog emits a single structured slog line for every HTTP request,
// after it completes. Conforms to the §18.1 log-line contract.
// The service name is embedded in the log record via slog.With at startup;
// pass it in via ServiceName so RequestLog stays dependency-free.
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

// ————————————————————————————————————————————————————————————————————
// 4. APIVersionHeader — set Ubag-Api-Version-Used on every response.
//    Blueprint §6.5: server returns this header for debugging.
// ————————————————————————————————————————————————————————————————————

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

// ————————————————————————————————————————————————————————————————————
// 5. IETFRateLimit — communicate token-bucket state via RFC-standard headers.
//    Blueprint §10.6: RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset
//    (IETF draft-ietf-httpapi-ratelimit-headers), plus Retry-After on 429.
// ————————————————————————————————————————————————————————————————————

// RateLimitHeaders carries the state to be communicated via IETF headers.
type RateLimitHeaders struct {
	Limit     int   // token-bucket capacity (per window)
	Remaining int   // tokens remaining after this request
	ResetAt   int64 // Unix timestamp (seconds) when the window resets
}

// SetIETFRateLimitHeaders writes the §10.6 IETF draft headers to w.
// Call this from the rate-limit middleware/handler when limit state is known.
func SetIETFRateLimitHeaders(w http.ResponseWriter, h RateLimitHeaders) {
	w.Header().Set("RateLimit-Limit", strconv.Itoa(h.Limit))
	w.Header().Set("RateLimit-Remaining", strconv.Itoa(h.Remaining))
	w.Header().Set("RateLimit-Reset", strconv.FormatInt(h.ResetAt, 10))
}

// SetRetryAfter writes the Retry-After header (seconds) on 429 responses.
func SetRetryAfter(w http.ResponseWriter, seconds int) {
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}
