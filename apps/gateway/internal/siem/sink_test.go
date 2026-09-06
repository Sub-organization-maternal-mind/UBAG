package siem

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileSinkWritesValidJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "audit.log")
	sink := NewFileSink(path)

	events := []Event{
		Redact(Event{ID: "e1", TenantID: "t1", Action: "job.create", Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Attributes: map[string]any{"password": "x", "ok": "yes"}}),
		Redact(Event{ID: "e2", TenantID: "t1", Action: "job.cancel", Timestamp: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)}),
	}
	if err := sink.Export(context.Background(), events); err != nil {
		t.Fatalf("first export: %v", err)
	}
	// Append a second batch to confirm append semantics.
	if err := sink.Export(context.Background(), events[:1]); err != nil {
		t.Fatalf("second export: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	var decoded []wireEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		var ev wireEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("invalid json line %q: %v", line, err)
		}
		decoded = append(decoded, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(decoded))
	}
	if decoded[0].Timestamp != "2026-01-02T03:04:05Z" {
		t.Fatalf("timestamp not RFC3339 UTC: %q", decoded[0].Timestamp)
	}
	if decoded[0].Attributes["password"] != redactedPlaceholder {
		t.Fatalf("expected redacted password in file, got %v", decoded[0].Attributes["password"])
	}
}

func TestFileSinkEmptyPathErrors(t *testing.T) {
	sink := NewFileSink("   ")
	if err := sink.Export(context.Background(), []Event{{ID: "e1"}}); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestFileSinkEmptyBatchIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	sink := NewFileSink(path)
	if err := sink.Export(context.Background(), nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("empty batch must not create the file")
	}
}
