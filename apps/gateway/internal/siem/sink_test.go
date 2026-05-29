package siem

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestHTTPSinkPostsRedactedBatchAndBypassesProxy(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody map[string]any
		gotAuth string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	// Set a bogus proxy: if the sink honored it, the request would fail.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")

	sink := NewHTTPSink(server.URL, "audit-ref", StaticSecretResolver{"audit-ref": "s3cr3t-token"})
	event := Redact(Event{
		ID: "e1", TenantID: "t1", Action: "secret.rotate",
		Timestamp:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Attributes: map[string]any{"token": "leak-me", "ok": "fine"},
	})
	if err := sink.Export(context.Background(), []Event{event}); err != nil {
		t.Fatalf("export: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotAuth != "Bearer s3cr3t-token" {
		t.Fatalf("expected resolved bearer header, got %q", gotAuth)
	}
	rawEvents, ok := gotBody["events"].([]any)
	if !ok || len(rawEvents) != 1 {
		t.Fatalf("expected one event in payload, got %v", gotBody["events"])
	}
	first := rawEvents[0].(map[string]any)
	attrs := first["attributes"].(map[string]any)
	if attrs["token"] != redactedPlaceholder {
		t.Fatalf("expected redacted token in payload, got %v", attrs["token"])
	}
	if attrs["ok"] != "fine" {
		t.Fatalf("benign attribute altered: %v", attrs["ok"])
	}
}

func TestHTTPSinkNon2xxReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	sink := NewHTTPSink(server.URL, "", nil)
	err := sink.Export(context.Background(), []Event{{ID: "e1"}})
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("expected non-2xx error, got %v", err)
	}
}

func TestSyslogSinkWritesRFC5424Lines(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	lineCh := make(chan string, 2)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				lineCh <- line
			}
			if err != nil {
				return
			}
		}
	}()

	sink := NewSyslogSink("tcp", listener.Addr().String())
	sink.Hostname = "gw-host"
	event := Redact(Event{ID: "e1", TenantID: "t1", Type: "audit.job", Action: "job.create", Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})
	if err := sink.Export(context.Background(), []Event{event}); err != nil {
		t.Fatalf("export: %v", err)
	}

	select {
	case line := <-lineCh:
		if !strings.HasPrefix(line, "<86>1 ") {
			t.Fatalf("expected RFC5424 PRI/VERSION prefix, got %q", line)
		}
		if !strings.Contains(line, "gw-host") {
			t.Fatalf("expected hostname in line, got %q", line)
		}
		if !strings.Contains(line, `"id":"e1"`) {
			t.Fatalf("expected structured event payload, got %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for syslog line")
	}
}
