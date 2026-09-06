// Package middleware provides the §7.2 gateway middleware chain pieces that
// are shared outside the HTTP handler chain (Trace is registered by
// httpapi; TraceID is read by obs and the gRPC interceptors).
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
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
