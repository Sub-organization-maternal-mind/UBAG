package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func requestWith(headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// A well-formed traceparent must still be honoured, or cross-service traces
// break.
func TestExtractOrGenTraceID_AcceptsValidTraceparent(t *testing.T) {
	const want = "4bf92f3577b34da6a3ce929d0e0e4736"
	tp := "00-" + want + "-00f067aa0ba902b7-01"
	if got := extractOrGenTraceID(requestWith(map[string]string{"Traceparent": tp})); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The previous parser accepted any 32-character second field. A trace ID ends
// up in every log line, on the job row, and in webhook headers, so arbitrary
// text there is a log-forging vector. Each of these must now be rejected and
// replaced with a freshly generated ID.
func TestExtractOrGenTraceID_RejectsHostileTraceparent(t *testing.T) {
	cases := []struct {
		name string
		tp   string
	}{
		{"all-zero trace id", "00-00000000000000000000000000000000-00f067aa0ba902b7-01"},
		{"non-hex trace id", "00-zzzz92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01"},
		{"uppercase hex", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01"},
		{"embedded newline", "00-4bf92f3577b34da6a3ce929d0e0e47\n-00f067aa0ba902b7-01"},
		{"short trace id", "00-4bf92f35-00f067aa0ba902b7-01"},
		{"long trace id", "00-4bf92f3577b34da6a3ce929d0e0e47366-00f067aa0ba902b7-01"},
		{"all-zero parent id", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"},
		{"bad flags", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0z"},
		{"short flags", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1"},
		{"too few parts", "00-4bf92f3577b34da6a3ce929d0e0e4736-01"},
		{"too many parts", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra"},
		{"ansi escape", "00-\x1b[31m4bf92f3577b34da6a3ce929d0e0e4-00f067aa0ba902b7-01"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractOrGenTraceID(requestWith(map[string]string{"Traceparent": tc.tp}))
			if got == tc.tp {
				t.Fatalf("hostile traceparent accepted verbatim: %q", got)
			}
			if len(got) != 32 || !isLowerHex(got) {
				t.Errorf("expected a generated 32-char lowercase hex id, got %q", got)
			}
		})
	}
}

// X-Request-Id was previously trusted verbatim. It is not spec-constrained and
// operators legitimately send "req-abc-123" or a UUID, so it is NOT restricted
// to hex - but it must never be able to forge a log line, so control
// characters, spaces, non-ASCII and over-long values are discarded.
func TestExtractOrGenTraceID_RejectsHostileRequestID(t *testing.T) {
	cases := []struct {
		name string
		rid  string
	}{
		{"log injection", "abc\nERROR forged log line"},
		{"carriage return", "abc\rdef"},
		{"tab", "abc\tdef"},
		{"null byte", "abc\x00def"},
		{"ansi escape", "\x1b[2J\x1b[H"},
		{"space", "abc def"},
		{"non ascii", "café-trace"},
		{"too long", strings.Repeat("a", maxInboundTraceIDLen+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractOrGenTraceID(requestWith(map[string]string{"X-Request-Id": tc.rid}))
			if got == tc.rid {
				t.Fatalf("hostile request id accepted verbatim: %q", got)
			}
			if len(got) != 32 || !isLowerHex(got) {
				t.Errorf("expected a generated 32-char lowercase hex id, got %q", got)
			}
		})
	}
}

// Operator correlation IDs that are not hex must still pass through - that is
// the point of honouring the header.
func TestExtractOrGenTraceID_AcceptsNonHexRequestID(t *testing.T) {
	for _, rid := range []string{
		"req-abc-123",
		"550e8400-e29b-41d4-a716-446655440000",
		"trace/2026-09-27#42",
	} {
		if got := extractOrGenTraceID(requestWith(map[string]string{"X-Request-Id": rid})); got != rid {
			t.Errorf("request id %q was discarded, got %q", rid, got)
		}
	}
}

// A valid caller-supplied request ID must still pass through - it is how
// operators correlate their own requests.
func TestExtractOrGenTraceID_AcceptsValidRequestID(t *testing.T) {
	const want = "0123456789abcdef0123456789abcdef"
	if got := extractOrGenTraceID(requestWith(map[string]string{"X-Request-Id": want})); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A malformed traceparent must not stop a valid X-Request-Id from being used.
func TestExtractOrGenTraceID_FallsBackToRequestID(t *testing.T) {
	const want = "0123456789abcdef0123456789abcdef"
	got := extractOrGenTraceID(requestWith(map[string]string{
		"Traceparent":  "garbage",
		"X-Request-Id": want,
	}))
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The emitted id must never contain a character that could break a log line or
// a header value.
func TestExtractOrGenTraceID_OutputIsAlwaysHeaderSafe(t *testing.T) {
	headers := []map[string]string{
		{},
		{"Traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{"Traceparent": "bad"},
		{"X-Request-Id": "req abc"},
		{"Traceparent": "bad", "X-Request-Id": "also bad!"},
	}
	for i, h := range headers {
		got := extractOrGenTraceID(requestWith(h))
		if !isHeaderSafeToken(got) {
			t.Errorf("case %d: emitted id is not header-safe: %q", i, got)
		}
		if len(got) > maxInboundTraceIDLen {
			t.Errorf("case %d: emitted id exceeds the length bound: %q", i, got)
		}
	}
}
