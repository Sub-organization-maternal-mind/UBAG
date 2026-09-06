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
