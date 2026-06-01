// Package obs provides contract-conformant structured logging for the UBAG
// gateway. It emits JSON with required fields (timestamp, level, environment,
// service, message, trace_id), redacts PII/secret keys, and drops PHI records.
package obs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/ubag/ubag/apps/gateway/internal/compliance"
	"github.com/ubag/ubag/apps/gateway/internal/middleware"
)

// ─────────────────────────────────────────────────────────────────────────────
// Forbidden key patterns (from packages/observability/src/safety.mjs)
// ─────────────────────────────────────────────────────────────────────────────

var forbiddenKeyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(^|_)(authorization|cookie|set_cookie|password|passwd|secret|token|api_key|apikey|private_key|credential|session_cookie)($|_)`),
	regexp.MustCompile(`(?i)(^|_)(raw_prompt|raw_response|html|screenshot_base64|card_number|cvv)($|_)`),
}

// isForbiddenKey returns true when key matches any forbidden pattern.
func isForbiddenKey(key string) bool {
	for _, p := range forbiddenKeyPatterns {
		if p.MatchString(key) {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Classification context key
// ─────────────────────────────────────────────────────────────────────────────

type classCtxKey struct{}

// WithClassification stores a DataClassification in the context so the
// redacting handler can decide whether to drop the entire log record.
func WithClassification(ctx context.Context, c compliance.DataClassification) context.Context {
	return context.WithValue(ctx, classCtxKey{}, c)
}

func classificationFromContext(ctx context.Context) (compliance.DataClassification, bool) {
	c, ok := ctx.Value(classCtxKey{}).(compliance.DataClassification)
	return c, ok
}

// ─────────────────────────────────────────────────────────────────────────────
// RedactingHandler
// ─────────────────────────────────────────────────────────────────────────────

// redactingHandler wraps an inner slog.Handler and:
//  1. Drops records whose context carries a PHI classification.
//  2. Strips attributes whose keys match forbiddenKeyPatterns.
//  3. Injects trace_id from the request context.
type redactingHandler struct {
	inner slog.Handler
}

// NewRedactingHandler wraps inner with PII-redaction and PHI-drop logic.
func NewRedactingHandler(inner slog.Handler) slog.Handler {
	return &redactingHandler{inner: inner}
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	// Drop PHI records entirely.
	if c, ok := classificationFromContext(ctx); ok && compliance.ShouldSkipLog(c) {
		return nil
	}

	// Build a cleaned record, re-using the same time/level/message.
	clean := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)

	// Inject trace_id first so it always appears near the start.
	clean.AddAttrs(slog.String("trace_id", middleware.TraceID(ctx)))

	// Walk the original attrs and drop forbidden keys.
	r.Attrs(func(a slog.Attr) bool {
		if !isForbiddenKey(a.Key) {
			clean.AddAttrs(a)
		}
		return true
	})

	return h.inner.Handle(ctx, clean)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Filter forbidden attrs from the pre-seeded set too.
	safe := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if !isForbiddenKey(a.Key) {
			safe = append(safe, a)
		}
	}
	return &redactingHandler{inner: h.inner.WithAttrs(safe)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

// ─────────────────────────────────────────────────────────────────────────────
// ReplaceAttr helper — maps slog built-in keys to contract field names
// ─────────────────────────────────────────────────────────────────────────────

// replaceContractAttrs is used as slog.HandlerOptions.ReplaceAttr to rename and
// reformat the built-in timestamp, level, and message keys to match the §18.1
// log-line contract:
//   - "time"  → "timestamp"  (RFC3339Nano UTC)
//   - "level" → "level"      (lowercase: debug/info/warn/error)
//   - "msg"   → "message"
func replaceContractAttrs(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case slog.TimeKey:
		return slog.String("timestamp", a.Value.Time().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))
	case slog.LevelKey:
		level := a.Value.Any().(slog.Level)
		var ls string
		switch {
		case level < slog.LevelInfo:
			ls = "debug"
		case level < slog.LevelWarn:
			ls = "info"
		case level < slog.LevelError:
			ls = "warn"
		default:
			ls = "error"
		}
		return slog.String("level", ls)
	case slog.MessageKey:
		return slog.String("message", a.Value.String())
	}
	return a
}

// ─────────────────────────────────────────────────────────────────────────────
// Level hot-reload via SIGHUP
// ─────────────────────────────────────────────────────────────────────────────

// parseLevel maps a string to slog.Level, defaulting to Info.
func parseLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return l
}

// levelVar wraps slog.LevelVar behind an atomic so startLevelReloader can swap
// the level without races on the underlying handler options.
func startLevelReloader(ctx context.Context, lv *slog.LevelVar) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				raw := os.Getenv("UBAG_LOG_LEVEL")
				lv.Set(parseLevel(raw))
			}
		}
	}()
}

// ─────────────────────────────────────────────────────────────────────────────
// Public API
// ─────────────────────────────────────────────────────────────────────────────

// ServiceLogger returns a *slog.Logger pre-seeded with service and environment.
// It does not set the global default; callers may use it directly or pass it to
// InitLogger.
func ServiceLogger(ctx context.Context, w io.Writer) *slog.Logger {
	env := os.Getenv("UBAG_ENVIRONMENT")
	if env == "" {
		env = "local"
	}
	raw := os.Getenv("UBAG_LOG_LEVEL")
	lv := &slog.LevelVar{}
	lv.Set(parseLevel(raw))

	jsonHandler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: lv,
		ReplaceAttr: replaceContractAttrs,
	})

	// Pre-seed service and environment as WithAttrs so they appear in every record.
	seededHandler := jsonHandler.WithAttrs([]slog.Attr{
		slog.String("service", "ubag-gateway"),
		slog.String("environment", env),
	})

	return slog.New(NewRedactingHandler(seededHandler))
}

// InitLogger configures the default slog logger with the JSON+redaction handler
// and starts a background goroutine that re-reads UBAG_LOG_LEVEL on SIGHUP.
// Call once from serve.Run.
func InitLogger(ctx context.Context, w io.Writer) *slog.Logger {
	env := os.Getenv("UBAG_ENVIRONMENT")
	if env == "" {
		env = "local"
	}
	raw := os.Getenv("UBAG_LOG_LEVEL")
	lv := &slog.LevelVar{}
	lv.Set(parseLevel(raw))

	jsonHandler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       lv,
		ReplaceAttr: replaceContractAttrs,
	})

	seededHandler := jsonHandler.WithAttrs([]slog.Attr{
		slog.String("service", "ubag-gateway"),
		slog.String("environment", env),
	})

	logger := slog.New(NewRedactingHandler(seededHandler))
	startLevelReloader(ctx, lv)
	return logger
}
