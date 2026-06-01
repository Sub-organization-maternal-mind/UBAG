package obs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/compliance"
	"github.com/ubag/ubag/apps/gateway/internal/obs"
)

// captureLogger returns a logger that writes to a *bytes.Buffer and the buffer.
func captureLogger(ctx context.Context, t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := obs.ServiceLogger(ctx, &buf)
	return logger, &buf
}

// parseLastJSON parses the last non-empty line of buf as JSON.
func parseLastJSON(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("parseLastJSON: failed to parse %q: %v", line, err)
		}
		return m
	}
	t.Fatal("parseLastJSON: buffer contains no JSON lines")
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 1: Required fields present
// ─────────────────────────────────────────────────────────────────────────────

func TestRequiredFields(t *testing.T) {
	ctx := context.Background()
	logger, buf := captureLogger(ctx, t)
	logger.InfoContext(ctx, "test message")

	m := parseLastJSON(t, buf)

	required := []string{"timestamp", "level", "environment", "service", "message", "trace_id"}
	for _, key := range required {
		if _, ok := m[key]; !ok {
			t.Errorf("required field %q missing from log output; got: %v", key, m)
		}
	}

	// Validate specific values.
	if m["service"] != "ubag-gateway" {
		t.Errorf("service: want %q, got %q", "ubag-gateway", m["service"])
	}
	if m["message"] != "test message" {
		t.Errorf("message: want %q, got %q", "test message", m["message"])
	}
	if _, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", m["timestamp"].(string)); err != nil {
		t.Errorf("timestamp %q is not RFC3339Nano: %v", m["timestamp"], err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 2: password field is redacted
// ─────────────────────────────────────────────────────────────────────────────

func TestPasswordRedacted(t *testing.T) {
	ctx := context.Background()
	logger, buf := captureLogger(ctx, t)
	logger.InfoContext(ctx, "login attempt", "password", "secret123")

	m := parseLastJSON(t, buf)
	if _, ok := m["password"]; ok {
		t.Error("password field must be absent from log output but was present")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 3: raw_prompt field is redacted
// ─────────────────────────────────────────────────────────────────────────────

func TestRawPromptRedacted(t *testing.T) {
	ctx := context.Background()
	logger, buf := captureLogger(ctx, t)
	logger.InfoContext(ctx, "prompt logged", "raw_prompt", "tell me a secret")

	m := parseLastJSON(t, buf)
	if _, ok := m["raw_prompt"]; ok {
		t.Error("raw_prompt field must be absent from log output but was present")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 4: Level changes on SIGHUP
// ─────────────────────────────────────────────────────────────────────────────

func TestLevelChangeOnSIGHUP(t *testing.T) {
	// Temporarily set level to "info" so debug is disabled.
	t.Setenv("UBAG_LOG_LEVEL", "info")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	logger := obs.InitLogger(ctx, &buf)

	// Debug must be suppressed at info level.
	logger.DebugContext(ctx, "should be invisible before SIGHUP")
	if strings.Contains(buf.String(), "should be invisible before SIGHUP") {
		t.Fatal("debug message should not appear when level is info")
	}

	// Switch to debug and send SIGHUP.
	t.Setenv("UBAG_LOG_LEVEL", "debug")
	// Give the goroutine time to set up signal.Notify before we fire.
	time.Sleep(20 * time.Millisecond)
	if err := sendSIGHUP(t); err != nil {
		t.Skipf("cannot send SIGHUP in this environment: %v", err)
	}
	// Allow the goroutine to process.
	time.Sleep(40 * time.Millisecond)

	buf.Reset()
	logger.DebugContext(ctx, "should be visible after SIGHUP")
	if !strings.Contains(buf.String(), "should be visible after SIGHUP") {
		t.Error("debug message should appear after SIGHUP raised log level to debug")
	}
}

// sendSIGHUP delivers SIGHUP to the current process.
func sendSIGHUP(t *testing.T) error {
	t.Helper()
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGHUP)
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 5: PHI record is dropped
// ─────────────────────────────────────────────────────────────────────────────

func TestPHIRecordDropped(t *testing.T) {
	phiCtx := obs.WithClassification(context.Background(), compliance.ClassPHI)

	var buf bytes.Buffer
	logger := obs.ServiceLogger(phiCtx, &buf)

	logger.InfoContext(phiCtx, "patient record accessed", "patient_id", "pt-9876")

	if buf.Len() > 0 {
		t.Errorf("PHI log record must be dropped entirely; got output: %s", buf.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Additional: non-PHI classifications still emit
// ─────────────────────────────────────────────────────────────────────────────

func TestNonPHIClassificationEmits(t *testing.T) {
	piiCtx := obs.WithClassification(context.Background(), compliance.ClassPII)

	var buf bytes.Buffer
	logger := obs.ServiceLogger(piiCtx, &buf)
	logger.InfoContext(piiCtx, "pii event")

	if buf.Len() == 0 {
		t.Error("PII-classified log records should still emit (with redaction)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Additional: token/api_key variants
// ─────────────────────────────────────────────────────────────────────────────

func TestTokenAndAPIKeyRedacted(t *testing.T) {
	ctx := context.Background()
	logger, buf := captureLogger(ctx, t)
	logger.InfoContext(ctx, "auth",
		"token", "tok_abc",
		"api_key", "key_xyz",
		"user_token", "tok_inner",
	)

	m := parseLastJSON(t, buf)
	for _, key := range []string{"token", "api_key", "user_token"} {
		if _, ok := m[key]; ok {
			t.Errorf("field %q should be redacted but was present", key)
		}
	}
}
