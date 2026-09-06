package siem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Sink is a destination capable of receiving redacted audit events. Export
// must be safe to call concurrently with itself only insofar as the Exporter
// guarantees: the Exporter serializes Export calls per sink, so a Sink need
// not be internally concurrency-safe, but it must respect ctx cancellation.
type Sink interface {
	// Name returns a stable identifier used in metrics and logs.
	Name() string
	// Export delivers a batch of already-redacted events. A non-nil error
	// signals the Exporter to retry the batch (subject to bounded attempts).
	Export(ctx context.Context, events []Event) error
}

// wireEvent mirrors Event but pins the timestamp to UTC RFC3339 for export.
type wireEvent struct {
	ID         string         `json:"id"`
	TenantID   string         `json:"tenant_id"`
	AppID      string         `json:"app_id"`
	Type       string         `json:"type"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	Resource   string         `json:"resource"`
	Outcome    string         `json:"outcome"`
	Timestamp  string         `json:"timestamp"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

func toWire(e Event) wireEvent {
	ts := e.Timestamp.UTC()
	if e.Timestamp.IsZero() {
		ts = time.Time{}
	}
	return wireEvent{
		ID:         e.ID,
		TenantID:   e.TenantID,
		AppID:      e.AppID,
		Type:       e.Type,
		Actor:      e.Actor,
		Action:     e.Action,
		Resource:   e.Resource,
		Outcome:    e.Outcome,
		Timestamp:  ts.Format(time.RFC3339),
		Attributes: e.Attributes,
	}
}

// FileSink appends newline-delimited JSON events to a file. The parent
// directory is created on demand. Each batch is buffered and written with a
// single Write call under a mutex so concurrent processes appending to the
// same file do not interleave partial lines (atomic-ish; full atomicity
// across processes is not guaranteed by POSIX append for arbitrary sizes, but
// per-batch single-write keeps individual batches coherent).
type FileSink struct {
	Path string

	mu sync.Mutex
}

// NewFileSink constructs a FileSink writing to path.
func NewFileSink(path string) *FileSink {
	return &FileSink{Path: strings.TrimSpace(path)}
}

// Name implements Sink.
func (s *FileSink) Name() string {
	return "file"
}

// Export implements Sink.
func (s *FileSink) Export(_ context.Context, events []Event) error {
	if s == nil || strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("siem: file sink path is required")
	}
	if len(events) == 0 {
		return nil
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, event := range events {
		if err := encoder.Encode(toWire(event)); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir := filepath.Dir(s.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(buf.Bytes()); err != nil {
		return err
	}
	return file.Sync()
}
