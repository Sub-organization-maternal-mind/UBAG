package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// Characterization tests for GET /v1/sse/jobs/{id} (zero behaviour change):
// replay from sequence 0, no close on a terminal event, idle ping, and no
// Last-Event-ID resume. They are the safety net for later SSE work.

const (
	sseTestTenant = "tenant_sse"
	sseTestApp    = "app_sse"
)

type sseFixture struct {
	store  *jobstore.MemoryStore
	server *httptest.Server
	job    jobstore.Job
}

func newSSEFixture(t *testing.T, heartbeat time.Duration) *sseFixture {
	t.Helper()
	return newSSEFixtureCfg(t, heartbeat, nil, nil)
}

// newSSEFixtureCfg lets a test wrap the store (e.g. with a wake hub) and set
// server Config fields.
func newSSEFixtureCfg(t *testing.T, heartbeat time.Duration, wrap func(*jobstore.MemoryStore) jobstore.Store, mutate func(*Config)) *sseFixture {
	t.Helper()
	if heartbeat > 0 {
		previous := sseHeartbeatInterval
		sseHeartbeatInterval = heartbeat
		t.Cleanup(func() { sseHeartbeatInterval = previous })
	}
	store := jobstore.NewMemoryStore()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: DefaultAPIVersion, TenantID: sseTestTenant, AppID: sseTestApp,
		IdempotencyKey: "idem_sse_characterization", Target: "mock", CommandType: "submit",
		Input: map[string]any{"prompt": "hello"}, TraceID: "trace_sse_characterization",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	var jobs jobstore.Store = store
	if wrap != nil {
		jobs = wrap(store)
	}
	cfg := Config{AppSecret: "dev-secret", TenantID: sseTestTenant, AppID: sseTestApp, Jobs: jobs}
	if mutate != nil {
		mutate(&cfg)
	}
	handler := NewServer(cfg).Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &sseFixture{store: store, server: server, job: job}
}

func (f *sseFixture) apply(t *testing.T, eventID, eventType string, sequence int, data map[string]any) {
	t.Helper()
	if _, found, err := f.store.ApplyWorkerEvent(context.Background(), jobstore.WorkerEvent{
		EventID: eventID, JobID: f.job.ID, APIVersion: f.job.APIVersion, Type: eventType,
		Sequence: sequence, TraceID: f.job.TraceID, Data: data,
	}); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent %s found=%v err=%v", eventID, found, err)
	}
}

// openSSE connects and returns a channel of frames (blocks separated by a
// blank line). The channel closes when the response body ends.
func (f *sseFixture) openSSE(t *testing.T, query string, headers map[string]string) (<-chan string, context.CancelFunc, *http.Response) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.server.URL+"/v1/sse/jobs/"+f.job.ID+query, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer dev-secret")
	req.Header.Set("Ubag-Api-Version", DefaultAPIVersion)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open sse: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse status = %d, want 200", resp.StatusCode)
	}
	frames := make(chan string, 512)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var frame []string
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if len(frame) > 0 {
					frames <- strings.Join(frame, "\n")
					frame = nil
				}
				continue
			}
			frame = append(frame, line)
		}
	}()
	return frames, cancel, resp
}

func nextFrame(t *testing.T, frames <-chan string) string {
	t.Helper()
	select {
	case frame, ok := <-frames:
		if !ok {
			t.Fatal("SSE stream closed, want another frame")
		}
		return frame
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an SSE frame")
	}
	return ""
}

func frameEventName(frame string) string {
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, "event: ") {
			return strings.TrimPrefix(line, "event: ")
		}
	}
	return ""
}

func TestSSEReplaysFromZeroAndDoesNotCloseOnTerminalEvent(t *testing.T) {
	f := newSSEFixture(t, 50*time.Millisecond)
	f.apply(t, "sse_evt_running", "running", 2, map[string]any{"status": "running"})
	f.apply(t, "sse_evt_done", "completed", 3, map[string]any{
		"status": "completed", "result": map[string]any{"type": "text", "text": "done"},
	})

	// An unknown Last-Event-ID falls back to a replay from sequence 0.
	frames, _, resp := f.openSSE(t, "", map[string]string{"Last-Event-ID": "does-not-matter"})
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	for _, want := range []string{"job.queued", "job.running", "job.completed"} {
		frame := nextFrame(t, frames)
		if got := frameEventName(frame); got != want {
			t.Fatalf("frame event = %q, want %q (frame=%q)", got, want, frame)
		}
		if !strings.Contains(frame, "id: ") || !strings.Contains(frame, "data: ") {
			t.Fatalf("frame missing id/data lines: %q", frame)
		}
	}
	// The stream stays open after the terminal event: the next frame is an idle
	// heartbeat comment, not EOF.
	if frame := nextFrame(t, frames); frame != ": ping" {
		t.Fatalf("frame after terminal event = %q, want \": ping\" (stream must not close on terminal)", frame)
	}
}

func TestSSEEmitsPingWhenIdleAndDeliversLiveEvents(t *testing.T) {
	f := newSSEFixture(t, 50*time.Millisecond)
	frames, _, _ := f.openSSE(t, "", nil)
	if got := frameEventName(nextFrame(t, frames)); got != "job.queued" {
		t.Fatalf("first frame event = %q, want job.queued", got)
	}
	if frame := nextFrame(t, frames); frame != ": ping" {
		t.Fatalf("idle frame = %q, want \": ping\"", frame)
	}
	f.apply(t, "sse_live_running", "running", 2, map[string]any{"status": "running"})
	// Heartbeats may interleave until the wait wakes; skip them.
	for range 20 {
		frame := nextFrame(t, frames)
		if frame == ": ping" {
			continue
		}
		if got := frameEventName(frame); got != "job.running" {
			t.Fatalf("live frame event = %q, want job.running", got)
		}
		return
	}
	t.Fatal("never received the live running event")
}

func TestSSEReplayContinuesPastFirstPageOfHundredEvents(t *testing.T) {
	f := newSSEFixture(t, 0)
	const extra = 104
	for i := 0; i < extra; i++ {
		f.apply(t, fmt.Sprintf("sse_bulk_%03d", i), "token", i+2, map[string]any{
			"status": "token_streaming", "delta": map[string]any{"text": "x"},
		})
	}
	frames, _, _ := f.openSSE(t, "", nil)
	var ids []string
	for len(ids) < extra+1 {
		frame := nextFrame(t, frames)
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "id: ") {
				ids = append(ids, strings.TrimPrefix(line, "id: "))
			}
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("event id %q delivered twice across the replay page boundary", id)
		}
		seen[id] = true
	}
}

func TestSSESnapshotClosesAfterReplayEvenWithoutTerminal(t *testing.T) {
	f := newSSEFixture(t, 50*time.Millisecond)
	frames, _, _ := f.openSSE(t, "?snapshot=true", nil)
	if got := frameEventName(nextFrame(t, frames)); got != "job.queued" {
		t.Fatalf("snapshot frame event = %q, want job.queued", got)
	}
	select {
	case frame, ok := <-frames:
		if ok {
			t.Fatalf("unexpected extra frame on snapshot stream: %q", frame)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot stream did not close after replay")
	}
}

func TestSSERejectsMissingJobAndMissingAuth(t *testing.T) {
	f := newSSEFixture(t, 0)
	handler := NewServer(Config{AppSecret: "dev-secret", TenantID: sseTestTenant, AppID: sseTestApp, Jobs: f.store}).Handler()

	missing := doJSON(handler, http.MethodGet, "/v1/sse/jobs/job_does_not_exist", "", authHeaders(""))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing job status = %d, want 404; body=%s", missing.Code, missing.Body.String())
	}
	noAuth := doJSON(handler, http.MethodGet, "/v1/sse/jobs/"+f.job.ID, "", nil)
	if noAuth.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401; body=%s", noAuth.Code, noAuth.Body.String())
	}
}
