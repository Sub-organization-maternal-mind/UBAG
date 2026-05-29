package siem

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// recordingSink captures exported events and can simulate a fixed number of
// transient failures before succeeding.
type recordingSink struct {
	mu          sync.Mutex
	name        string
	calls       int
	failFirst   int
	failForever bool
	events      []Event
}

func (s *recordingSink) Name() string {
	if s.name == "" {
		return "recording"
	}
	return s.name
}

func (s *recordingSink) Export(_ context.Context, events []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failForever || s.calls <= s.failFirst {
		return fmt.Errorf("transient failure on call %d", s.calls)
	}
	s.events = append(s.events, events...)
	return nil
}

func (s *recordingSink) snapshot() (int, []Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.events))
	copy(out, s.events)
	return s.calls, out
}

func zeroBackoff(int) time.Duration { return 0 }

func TestExporterEnqueueExportsRedacted(t *testing.T) {
	sink := &recordingSink{}
	exp, err := NewExporter(ExporterConfig{Sinks: []Sink{sink}, BatchSize: 10, FlushInterval: 5 * time.Millisecond, Backoff: zeroBackoff})
	if err != nil {
		t.Fatalf("new exporter: %v", err)
	}
	exp.Start()
	exp.Enqueue(Event{ID: "e1", TenantID: "t1", Action: "job.create", Attributes: map[string]any{"password": "x", "ok": "y"}})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := exp.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, events := sink.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 exported event, got %d", len(events))
	}
	if events[0].Attributes["password"] != redactedPlaceholder {
		t.Fatalf("event not redacted before export: %v", events[0].Attributes["password"])
	}
	stats := exp.Stats()
	if stats.Enqueued != 1 || stats.Exported != 1 || stats.Dropped != 0 || stats.Failed != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestExporterRetriesTransientThenSucceeds(t *testing.T) {
	sink := &recordingSink{failFirst: 2}
	exp, err := NewExporter(ExporterConfig{Sinks: []Sink{sink}, MaxAttempts: 5, BatchSize: 1, FlushInterval: 5 * time.Millisecond, Backoff: zeroBackoff})
	if err != nil {
		t.Fatalf("new exporter: %v", err)
	}
	exp.Start()
	exp.Enqueue(Event{ID: "e1", TenantID: "t1", Action: "webhook.replay"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := exp.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	calls, events := sink.snapshot()
	if calls != 3 {
		t.Fatalf("expected 3 attempts (2 fail + 1 ok), got %d", calls)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 exported event, got %d", len(events))
	}
	stats := exp.Stats()
	if stats.Exported != 1 || stats.Failed != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestExporterDeadLettersAfterMaxAttempts(t *testing.T) {
	sink := &recordingSink{failForever: true}
	exp, err := NewExporter(ExporterConfig{Sinks: []Sink{sink}, MaxAttempts: 3, BatchSize: 1, FlushInterval: 5 * time.Millisecond, Backoff: zeroBackoff})
	if err != nil {
		t.Fatalf("new exporter: %v", err)
	}
	exp.Start()
	exp.Enqueue(Event{ID: "e1", TenantID: "t1", Action: "ratelimit.update"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := exp.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	calls, _ := sink.snapshot()
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
	if stats := exp.Stats(); stats.Failed != 1 {
		t.Fatalf("expected dead-letter Failed=1, got %+v", stats)
	}
}

func TestExporterDropsWhenBufferFull(t *testing.T) {
	sink := &recordingSink{}
	exp, err := NewExporter(ExporterConfig{Sinks: []Sink{sink}, BufferSize: 2, Backoff: zeroBackoff})
	if err != nil {
		t.Fatalf("new exporter: %v", err)
	}
	// Intentionally do NOT Start the worker so the buffer fills and overflows.
	for i := 0; i < 5; i++ {
		exp.Enqueue(Event{ID: fmt.Sprintf("e%d", i), TenantID: "t1"})
	}
	stats := exp.Stats()
	if stats.Enqueued != 2 {
		t.Fatalf("expected 2 enqueued (buffer size), got %d", stats.Enqueued)
	}
	if stats.Dropped != 3 {
		t.Fatalf("expected 3 dropped, got %d", stats.Dropped)
	}
}

func TestExporterCloseDrainsBuffer(t *testing.T) {
	sink := &recordingSink{}
	exp, err := NewExporter(ExporterConfig{Sinks: []Sink{sink}, BufferSize: 16, BatchSize: 4, FlushInterval: time.Hour, Backoff: zeroBackoff})
	if err != nil {
		t.Fatalf("new exporter: %v", err)
	}
	exp.Start()
	const total = 10
	for i := 0; i < total; i++ {
		exp.Enqueue(Event{ID: fmt.Sprintf("e%d", i), TenantID: "t1"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := exp.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, events := sink.snapshot()
	if len(events) != total {
		t.Fatalf("expected all %d events drained, got %d", total, len(events))
	}
	if stats := exp.Stats(); stats.Exported != total || stats.Dropped != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestNewExporterRequiresSink(t *testing.T) {
	if _, err := NewExporter(ExporterConfig{}); err == nil {
		t.Fatal("expected error when no sinks configured")
	}
}
