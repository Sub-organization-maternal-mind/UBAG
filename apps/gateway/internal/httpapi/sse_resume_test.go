package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// Streaming tests for the P3.13 SSE behaviour: hub wake, Last-Event-ID resume,
// close-on-terminal, 204 past a terminal, stream cap, stalled-reader drop.

// wakeStore adds a test wake hub (the JobWaker contract) to a MemoryStore.
type wakeStore struct {
	*jobstore.MemoryStore
	mu    sync.Mutex
	chans map[chan struct{}]struct{}
}

func newWakeStore(m *jobstore.MemoryStore) *wakeStore {
	return &wakeStore{MemoryStore: m, chans: map[chan struct{}]struct{}{}}
}

func (w *wakeStore) SubscribeJobWake(string) (<-chan struct{}, func(), bool) {
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	w.chans[ch] = struct{}{}
	w.mu.Unlock()
	return ch, func() { w.mu.Lock(); delete(w.chans, ch); w.mu.Unlock() }, true
}

// fire wakes every waiter, as the real hub does after a committed write.
func (w *wakeStore) fire() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.chans {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func frameID(frame string) string {
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, "id: ") {
			return strings.TrimPrefix(line, "id: ")
		}
	}
	return ""
}

// rawSSE opens the stream without asserting the status.
func (f *sseFixture) rawSSE(t *testing.T, headers map[string]string) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.server.URL+"/v1/sse/jobs/"+f.job.ID, nil)
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
	return resp, cancel
}

func (f *sseFixture) metricsText(t *testing.T) string {
	t.Helper()
	resp, err := http.Get(f.server.URL + "/v1/metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func expectClosed(t *testing.T, frames <-chan string) {
	t.Helper()
	select {
	case frame, ok := <-frames:
		if ok {
			t.Fatalf("unexpected frame after terminal: %q", frame)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not close")
	}
}

func setSSEFallback(t *testing.T, d time.Duration) {
	t.Helper()
	previous := sseFallbackInterval
	sseFallbackInterval = d
	t.Cleanup(func() { sseFallbackInterval = previous })
}

func TestSSEHubWakeDeliversWithoutPollDelay(t *testing.T) {
	setSSEFallback(t, time.Minute)
	var ws *wakeStore
	f := newSSEFixtureCfg(t, time.Minute, func(m *jobstore.MemoryStore) jobstore.Store {
		ws = newWakeStore(m)
		return ws
	}, nil)
	frames, _, _ := f.openSSE(t, "", nil)
	if got := frameEventName(nextFrame(t, frames)); got != "job.queued" {
		t.Fatalf("first frame = %q, want job.queued", got)
	}
	// Fallback and heartbeat are a minute away: only the wake can deliver this.
	start := time.Now()
	f.apply(t, "hub_evt_running", "running", 2, map[string]any{"status": "running"})
	ws.fire()
	if got := frameEventName(nextFrame(t, frames)); got != "job.running" {
		t.Fatalf("live frame = %q, want job.running", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("wake delivery took %s, want well under the fallback interval", elapsed)
	}
	metrics := f.metricsText(t)
	if !strings.Contains(metrics, `ubag_event_wakeups_total{source="local"} 1`) {
		t.Fatalf("local wakeup not counted:\n%s", metrics)
	}
	if !strings.Contains(metrics, "ubag_sse_event_frame_lag_seconds_count{service=\"ubag-gateway\"} 1") {
		t.Fatalf("frame lag not observed:\n%s", metrics)
	}
}

func TestSSEHubFallbackRecoversDroppedWake(t *testing.T) {
	setSSEFallback(t, 50*time.Millisecond)
	f := newSSEFixtureCfg(t, time.Minute, func(m *jobstore.MemoryStore) jobstore.Store { return newWakeStore(m) }, nil)
	frames, _, _ := f.openSSE(t, "", nil)
	_ = nextFrame(t, frames)
	f.apply(t, "hub_evt_dropped", "running", 2, map[string]any{"status": "running"}) // no fire
	if got := frameEventName(nextFrame(t, frames)); got != "job.running" {
		t.Fatalf("frame = %q, want job.running via fallback", got)
	}
	if !strings.Contains(f.metricsText(t), `ubag_event_wakeups_total{source="fallback"}`) {
		t.Fatal("fallback wakeup series missing")
	}
}

func TestSSELiveEventsArriveWithoutHub(t *testing.T) {
	f := newSSEFixture(t, time.Minute)
	frames, _, _ := f.openSSE(t, "", nil)
	_ = nextFrame(t, frames)
	start := time.Now()
	f.apply(t, "nohub_evt_running", "running", 2, map[string]any{"status": "running"})
	if got := frameEventName(nextFrame(t, frames)); got != "job.running" {
		t.Fatalf("live frame = %q, want job.running", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("WaitEvents path should wake on the write, not at the heartbeat")
	}
}

// snapshotIDs returns the stored event ids in order (the store assigns them).
func (f *sseFixture) snapshotIDs(t *testing.T) []string {
	t.Helper()
	frames, _, _ := f.openSSE(t, "?snapshot=true", nil)
	var ids []string
	for frame := range frames {
		if id := frameID(frame); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func seedLifecycle(t *testing.T, f *sseFixture) {
	t.Helper()
	f.apply(t, "res_evt_running", "running", 2, map[string]any{"status": "running"})
	f.apply(t, "res_evt_done", "completed", 3, map[string]any{
		"status": "completed", "result": map[string]any{"type": "text", "text": "done"},
	})
}

func TestSSEResumeSkipsSeenEvents(t *testing.T) {
	f := newSSEFixture(t, 50*time.Millisecond)
	seedLifecycle(t, f)
	frames, _, _ := f.openSSE(t, "", map[string]string{"Last-Event-ID": f.snapshotIDs(t)[1]})
	if got := frameEventName(nextFrame(t, frames)); got != "job.completed" {
		t.Fatalf("first resumed frame = %q, want job.completed (queued/running already seen)", got)
	}
}

func TestSSEUnknownLastEventIDReplaysFromStart(t *testing.T) {
	f := newSSEFixture(t, 50*time.Millisecond)
	seedLifecycle(t, f)
	frames, _, _ := f.openSSE(t, "", map[string]string{"Last-Event-ID": "evt_unknown"})
	if got := frameEventName(nextFrame(t, frames)); got != "job.queued" {
		t.Fatalf("first frame = %q, want job.queued", got)
	}
}

func TestSSECloseOnTerminalEndsStream(t *testing.T) {
	f := newSSEFixtureCfg(t, 50*time.Millisecond, nil, func(c *Config) { c.SSECloseOnTerminal = true })
	seedLifecycle(t, f)
	frames, _, _ := f.openSSE(t, "", nil)
	for _, want := range []string{"job.queued", "job.running", "job.completed"} {
		if got := frameEventName(nextFrame(t, frames)); got != want {
			t.Fatalf("frame = %q, want %q", got, want)
		}
	}
	expectClosed(t, frames)
}

func TestSSECloseOnTerminalClosesLiveStream(t *testing.T) {
	f := newSSEFixtureCfg(t, time.Minute, nil, func(c *Config) { c.SSECloseOnTerminal = true })
	frames, _, _ := f.openSSE(t, "", nil)
	_ = nextFrame(t, frames)
	f.apply(t, "live_evt_done", "completed", 2, map[string]any{
		"status": "completed", "result": map[string]any{"type": "text", "text": "done"},
	})
	if got := frameEventName(nextFrame(t, frames)); got != "job.completed" {
		t.Fatalf("frame = %q, want job.completed", got)
	}
	expectClosed(t, frames)
}

func TestSSEResumePastTerminalReturns204(t *testing.T) {
	f := newSSEFixtureCfg(t, 50*time.Millisecond, nil, func(c *Config) { c.SSECloseOnTerminal = true })
	seedLifecycle(t, f)
	resp, _ := f.rawSSE(t, map[string]string{"Last-Event-ID": f.snapshotIDs(t)[2]})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 once resumed past the terminal event", resp.StatusCode)
	}
	if n := f.countOpen(); n != 0 {
		t.Fatalf("204 must not hold a stream slot, open=%d", n)
	}
}

// The 204 past a terminal event holds with the close-on-terminal flag off too
// (the contract says "either way"); a client must never be left pinging.
func TestSSEResumePastTerminalReturns204WhenFlagOff(t *testing.T) {
	f := newSSEFixture(t, 50*time.Millisecond)
	seedLifecycle(t, f)
	resp, _ := f.rawSSE(t, map[string]string{"Last-Event-ID": f.snapshotIDs(t)[2]})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 with the flag off", resp.StatusCode)
	}
}

// failed(retryable) and blocked are terminal in the store (a retry is a new
// job), so close-on-terminal must end the stream and a reconnect gets 204.
func TestSSECloseOnTerminalForRetryableAndBlocked(t *testing.T) {
	for _, tc := range []struct {
		eventType string
		data      map[string]any
	}{
		{"failed", map[string]any{"retryable": true}},
		{"failed_retryable", map[string]any{"retryable": true}},
		{"blocked", map[string]any{"status": "blocked"}},
	} {
		t.Run(tc.eventType, func(t *testing.T) {
			f := newSSEFixtureCfg(t, 50*time.Millisecond, nil, func(c *Config) { c.SSECloseOnTerminal = true })
			f.apply(t, "end_evt", tc.eventType, 2, tc.data)
			frames, _, _ := f.openSSE(t, "", nil)
			_ = nextFrame(t, frames) // queued
			if got := frameEventName(nextFrame(t, frames)); got != "job."+tc.eventType {
				t.Fatalf("frame = %q, want job.%s", got, tc.eventType)
			}
			expectClosed(t, frames)
			if status, _ := f.sseCase(t, "?after_sequence=2&snapshot=true", nil); status != http.StatusNoContent {
				t.Fatalf("resume status = %d, want 204", status)
			}
		})
	}
}

// sseCase issues the GET with a raw query string and returns the
// status and the frame ids of a snapshot read (snapshot=true ends the stream).
func (f *sseFixture) sseCase(t *testing.T, query string, headers map[string]string) (int, []string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.server.URL+"/v1/sse/jobs/"+f.job.ID+query, nil)
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
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var ids []string
	for _, frame := range strings.Split(string(body), "\n\n") {
		if id := frameID(frame); id != "" {
			ids = append(ids, id)
		}
	}
	return resp.StatusCode, ids
}

// Replays packages/conformance/fixtures/streaming/sse-resume.json. The store
// assigns its own ids and sequences, so the fixture's events map to the seeded
// events by position and its cursors by "events at or below".
func TestSSEResumeConformanceFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/conformance/fixtures/streaming/sse-resume.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx struct {
		Events []struct {
			EventID  string `json:"event_id"`
			Sequence int    `json:"sequence"`
		} `json:"events"`
		Cases []struct {
			ID      string `json:"id"`
			Request struct {
				AfterSequence *int   `json:"after_sequence"`
				LastEventID   string `json:"last_event_id"`
			} `json:"request"`
			Expect struct {
				Status int      `json:"status"`
				Frames []string `json:"frames"`
			} `json:"expect"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	f := newSSEFixture(t, 50*time.Millisecond)
	f.apply(t, "fx_running", "running", 2, map[string]any{"status": "running"})
	f.apply(t, "fx_tok1", "token", 3, map[string]any{"delta": "Par"})
	f.apply(t, "fx_tok2", "token", 4, map[string]any{"delta": "is"})
	f.apply(t, "fx_done", "completed", 5, map[string]any{
		"status": "completed", "result": map[string]any{"type": "text", "text": "Paris"},
	})
	stored := f.snapshotIDs(t)
	if len(stored) != len(fx.Events) {
		t.Fatalf("seeded %d events, fixture has %d", len(stored), len(fx.Events))
	}
	byFixtureID := map[string]string{}
	fromStore := map[string]string{}
	for i, e := range fx.Events {
		byFixtureID[e.EventID] = stored[i]
		fromStore[stored[i]] = e.EventID
	}
	for _, c := range fx.Cases {
		query := "?snapshot=true"
		headers := map[string]string{}
		if c.Request.AfterSequence != nil {
			cursor := *c.Request.AfterSequence
			if cursor >= 0 { // map the fixture cursor onto the store's own sequences
				seen := 0
				for _, e := range fx.Events {
					if e.Sequence <= cursor {
						seen++
					}
				}
				cursor = seen
			}
			query += fmt.Sprintf("&after_sequence=%d", cursor)
		}
		if c.Request.LastEventID != "" {
			id, ok := byFixtureID[c.Request.LastEventID]
			if !ok {
				id = c.Request.LastEventID
			}
			headers["Last-Event-ID"] = id
		}
		status, ids := f.sseCase(t, query, headers)
		if status != c.Expect.Status {
			t.Fatalf("%s: status = %d, want %d", c.ID, status, c.Expect.Status)
		}
		var got []string
		for _, id := range ids {
			got = append(got, fromStore[id])
		}
		if len(got) != len(c.Expect.Frames) {
			t.Fatalf("%s: frames = %v, want %v", c.ID, got, c.Expect.Frames)
		}
		for i := range got {
			if got[i] != c.Expect.Frames[i] {
				t.Fatalf("%s: frames = %v, want %v", c.ID, got, c.Expect.Frames)
			}
		}
	}
}

func TestSSEStreamCapReturns503ThenRecovers(t *testing.T) {
	f := newSSEFixtureCfg(t, time.Minute, nil, func(c *Config) { c.SSEMaxStreams = 1 })
	frames, cancelFirst, _ := f.openSSE(t, "", nil)
	_ = nextFrame(t, frames)

	resp, _ := f.rawSSE(t, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second stream status = %d, want 503", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("503 must carry Retry-After")
	}
	if n := f.countOpen(); n != 1 {
		t.Fatalf("open streams = %d, want 1", n)
	}

	cancelFirst()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, cancel := f.rawSSE(t, nil)
		status := resp.StatusCode
		cancel()
		if status == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot never released, last status = %d", status)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestSSEStalledReaderIsDropped(t *testing.T) {
	previous := sseWriteTimeout
	sseWriteTimeout = 200 * time.Millisecond
	t.Cleanup(func() { sseWriteTimeout = previous })
	f := newSSEFixture(t, time.Minute)

	// ~7 MB of replay overflows the loopback socket buffers while the client
	// never reads, so the server write must hit the shortened deadline.
	chunk := strings.Repeat("x", 60*1024)
	for i := 0; i < 120; i++ {
		f.apply(t, fmt.Sprintf("stall_evt_%03d", i), "token", i+2, map[string]any{
			"status": "token_streaming", "delta": map[string]any{"text": chunk},
		})
	}
	resp, _ := f.rawSSE(t, nil) // headers only; the body is never read
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	deadline := time.Now().Add(8 * time.Second)
	for f.countOpen() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("stalled reader was not dropped after the write deadline")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// countOpen reads the live gauge through the metrics endpoint.
func (f *sseFixture) countOpen() int {
	resp, err := http.Get(f.server.URL + "/v1/metrics")
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "ubag_sse_connections_current{") {
			var n int
			for _, c := range line[strings.LastIndex(line, " ")+1:] {
				n = n*10 + int(c-'0')
			}
			return n
		}
	}
	return -1
}
