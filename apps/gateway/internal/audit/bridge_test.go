package audit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/siem"
)

// fakeEnqueuer collects forwarded siem.Events for inspection in tests.
type fakeEnqueuer struct {
	mu     sync.Mutex
	events []siem.Event
}

func (f *fakeEnqueuer) enqueue(e siem.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeEnqueuer) snapshot() []siem.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]siem.Event, len(f.events))
	copy(out, f.events)
	return out
}

// TestBridgeStoreForwardsToSIEM verifies that a single Append results in
// exactly one forwarded siem.Event with the expected fields.
func TestBridgeStoreForwardsToSIEM(t *testing.T) {
	fe := &fakeEnqueuer{}
	bridge := NewBridgeStore(NewMemoryStore(), fe.enqueue)
	ctx := context.Background()

	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	rec, err := bridge.Append(ctx, Record{
		TenantID:   "t1",
		AppID:      "app-1",
		Actor:      "user@example.com",
		Action:     "authorize",
		Resource:   "/v1/runs",
		Outcome:    "allow",
		OccurredAt: at,
		Attributes: map[string]any{"role": "operator"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	events := fe.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 forwarded event, got %d", len(events))
	}
	e := events[0]

	if e.ID != rec.ID {
		t.Errorf("event.ID = %q, want %q", e.ID, rec.ID)
	}
	if e.TenantID != "t1" {
		t.Errorf("event.TenantID = %q, want %q", e.TenantID, "t1")
	}
	if e.AppID != "app-1" {
		t.Errorf("event.AppID = %q, want %q", e.AppID, "app-1")
	}
	if e.Type != "audit" {
		t.Errorf("event.Type = %q, want \"audit\"", e.Type)
	}
	if e.Actor != "user@example.com" {
		t.Errorf("event.Actor = %q, want \"user@example.com\"", e.Actor)
	}
	if e.Action != "authorize" {
		t.Errorf("event.Action = %q, want \"authorize\"", e.Action)
	}
	if e.Resource != "/v1/runs" {
		t.Errorf("event.Resource = %q, want \"/v1/runs\"", e.Resource)
	}
	if e.Outcome != "allow" {
		t.Errorf("event.Outcome = %q, want \"allow\"", e.Outcome)
	}
	if !e.Timestamp.Equal(rec.OccurredAt) {
		t.Errorf("event.Timestamp = %v, want %v", e.Timestamp, rec.OccurredAt)
	}
	if e.Attributes["role"] != "operator" {
		t.Errorf("event.Attributes[\"role\"] = %v, want \"operator\"", e.Attributes["role"])
	}
}

// TestBridgeStoreMultipleAppends verifies forwarding across multiple Append calls.
func TestBridgeStoreMultipleAppends(t *testing.T) {
	fe := &fakeEnqueuer{}
	bridge := NewBridgeStore(NewMemoryStore(), fe.enqueue)
	ctx := context.Background()

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := bridge.Append(ctx, sampleRecord("t2", "login", "allow", base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	if got := len(fe.snapshot()); got != 5 {
		t.Errorf("expected 5 forwarded events, got %d", got)
	}
}

// TestBridgeStoreSIEMEventRedacted confirms that when events pass through a
// real siem.Exporter (which applies Redact before delivery), sensitive
// attributes are scrubbed at the sink. This validates the end-to-end redaction
// path rather than testing bridge.go's own logic.
func TestBridgeStoreSIEMEventRedacted(t *testing.T) {
	received := &fakeSink{}
	exporter, err := siem.NewExporter(siem.ExporterConfig{
		Sinks:         []siem.Sink{received},
		BufferSize:    16,
		BatchSize:     10,
		FlushInterval: 10 * time.Millisecond,
		MaxAttempts:   1,
	})
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	bridge := NewBridgeStore(NewMemoryStore(), exporter.Enqueue)
	ctx := context.Background()

	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	_, err = bridge.Append(ctx, Record{
		TenantID:   "t3",
		AppID:      "app-1",
		Actor:      "user@example.com",
		Action:     "login",
		Resource:   "/v1/sessions",
		Outcome:    "allow",
		OccurredAt: at,
		Attributes: map[string]any{
			"password": "super-secret-123",
			"role":     "operator",
		},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Flush via Close with a short timeout.
	closeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err := exporter.Close(closeCtx); err != nil && closeCtx.Err() == nil {
		t.Fatalf("Close: %v", err)
	}

	events := received.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 delivered event, got %d", len(events))
	}
	attrs := events[0].Attributes
	if attrs["password"] != "[REDACTED]" {
		t.Errorf("password attribute = %q, want \"[REDACTED]\"", attrs["password"])
	}
	if attrs["role"] != "operator" {
		t.Errorf("role attribute = %v, want \"operator\"", attrs["role"])
	}
}

// TestBridgeStoreNilExporterPanics verifies that NewBridgeStore panics when
// notify is nil, catching misconfiguration at construction time.
func TestBridgeStoreNilExporterPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil notify func, but did not panic")
		}
	}()
	NewBridgeStore(NewMemoryStore(), nil)
}

// TestBridgeStoreChainRemainsValid verifies that the underlying chain is still
// intact after multiple appends through the bridge.
func TestBridgeStoreChainRemainsValid(t *testing.T) {
	fe := &fakeEnqueuer{}
	bridge := NewBridgeStore(NewMemoryStore(), fe.enqueue)
	ctx := context.Background()

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i, action := range []string{"authorize", "authorize", "login"} {
		if _, err := bridge.Append(ctx, sampleRecord("t4", action, "allow", base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	listed, err := bridge.List(ctx, Filter{TenantID: "t4"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("expected 3 records, got %d", len(listed))
	}
	if !VerifyChain(listed) {
		t.Error("chain must verify after bridge appends")
	}
}

// fakeSink is an in-memory siem.Sink for testing the redaction path.
type fakeSink struct {
	mu     sync.Mutex
	events []siem.Event
}

func (f *fakeSink) Name() string { return "fake" }

func (f *fakeSink) Export(_ context.Context, events []siem.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, events...)
	return nil
}

func (f *fakeSink) snapshot() []siem.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]siem.Event, len(f.events))
	copy(out, f.events)
	return out
}
