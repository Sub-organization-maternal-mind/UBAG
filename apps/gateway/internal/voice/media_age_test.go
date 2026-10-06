package voice

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4/pkg/media"
)

// stalledRelay is a RelayConn whose Send blocks on gate and whose Recv is
// fed from recvCh.
type stalledRelay struct {
	gate   chan struct{}
	sent   atomic.Int64
	recvCh chan []byte
}

func (r *stalledRelay) Send([]byte) error {
	<-r.gate
	r.sent.Add(1)
	return nil
}
func (r *stalledRelay) Control(map[string]any) error { return nil }
func (r *stalledRelay) Close() error                 { return nil }
func (r *stalledRelay) Recv(ctx context.Context) ([]byte, error) {
	select {
	case f := <-r.recvCh:
		return f, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *countingMetrics) droppedCount(direction string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropped[direction]
}

// runStalledMic stalls the relay on the first frame, queues 3 frames stamped
// 1 s old plus one fresh frame, then releases the relay.
func runStalledMic(t *testing.T, maxAge time.Duration) (*stalledRelay, *countingMetrics) {
	t.Helper()
	metrics := &countingMetrics{dropped: map[string]int64{}}
	hub := &MediaHub{QueueMaxAge: maxAge, Metrics: metrics}
	relay := &stalledRelay{gate: make(chan struct{})}
	ms := &mediaSession{relay: relay, mic: make(chan micFrame, micBufferDepth), done: make(chan struct{})}
	defer close(ms.done)
	go ms.pumpMicToRelay(hub)

	ms.mic <- micFrame{payload: []byte{1}, at: time.Now()}
	waitFor(t, "pump to take the first frame", func() bool { return len(ms.mic) == 0 })
	for i := 0; i < 3; i++ {
		ms.mic <- micFrame{payload: []byte{2}, at: time.Now().Add(-time.Second)}
	}
	ms.mic <- micFrame{payload: []byte{3}, at: time.Now()}
	close(relay.gate)
	waitFor(t, "queue to drain", func() bool { return len(ms.mic) == 0 })
	time.Sleep(50 * time.Millisecond)
	return relay, metrics
}

func TestStalledRelayDropsStaleMicFrames(t *testing.T) {
	relay, metrics := runStalledMic(t, 500*time.Millisecond)
	if got := metrics.droppedCount("mic_age"); got != 3 {
		t.Fatalf("mic_age drops = %d, want 3", got)
	}
	if got := relay.sent.Load(); got != 2 {
		t.Fatalf("sent = %d, want 2 (in-flight + fresh)", got)
	}
}

func TestMicAgeBoundOffPreservesCountOnlyBehaviour(t *testing.T) {
	relay, metrics := runStalledMic(t, 0)
	if got := metrics.droppedCount("mic_age"); got != 0 {
		t.Fatalf("mic_age drops = %d, want 0 with the bound off", got)
	}
	if got := relay.sent.Load(); got != 5 {
		t.Fatalf("sent = %d, want 5 (stale frames still forwarded)", got)
	}
}

// 120 ms packets (the relay's maximum) stay bounded by count, not age.
func TestMicCountBoundHoldsForLongPackets(t *testing.T) {
	metrics := &countingMetrics{dropped: map[string]int64{}}
	hub := &MediaHub{QueueMaxAge: time.Minute, Metrics: metrics}
	ms := &mediaSession{mic: make(chan micFrame, micBufferDepth)}
	const n = 200
	for i := 0; i < n; i++ {
		pushDropOldest(ms.mic, micFrame{payload: make([]byte, 120), at: time.Now()}, func() {
			hub.metrics().AddFramesDropped("mic", 1)
		})
	}
	if len(ms.mic) != micBufferDepth {
		t.Fatalf("queue len = %d, want %d", len(ms.mic), micBufferDepth)
	}
	if got := metrics.droppedCount("mic"); got != n-micBufferDepth {
		t.Fatalf("mic drops = %d, want %d", got, n-micBufferDepth)
	}
}

type speakerHarness struct {
	relay   *stalledRelay
	metrics *countingMetrics
	gate    chan struct{}
	written atomic.Int64
	ms      *mediaSession
	wg      sync.WaitGroup
}

func startSpeaker(t *testing.T, maxAge time.Duration) *speakerHarness {
	t.Helper()
	h := &speakerHarness{
		relay:   &stalledRelay{recvCh: make(chan []byte, 512)},
		metrics: &countingMetrics{dropped: map[string]int64{}},
		gate:    make(chan struct{}),
	}
	hub := &MediaHub{QueueMaxAge: maxAge, Metrics: h.metrics}
	h.ms = &mediaSession{
		relay: h.relay,
		done:  make(chan struct{}),
		writeSample: func(media.Sample) error {
			<-h.gate
			h.written.Add(1)
			return nil
		},
	}
	h.wg.Add(1)
	go func() { defer h.wg.Done(); h.ms.pumpRelayToSpeaker(hub) }()
	t.Cleanup(func() {
		close(h.ms.done)
		select {
		case <-h.gate:
		default:
			close(h.gate)
		}
		h.wg.Wait()
	})
	return h
}

func TestStalledSpeakerDropsStaleFrames(t *testing.T) {
	h := startSpeaker(t, 40*time.Millisecond)
	for i := 0; i < 4; i++ {
		h.relay.recvCh <- []byte{byte(i)}
	}
	waitFor(t, "recv to drain", func() bool { return len(h.relay.recvCh) == 0 })
	time.Sleep(150 * time.Millisecond) // frames 2-4 age past the bound while write is stalled
	close(h.gate)
	waitFor(t, "stale speaker drops", func() bool { return h.metrics.droppedCount("speaker_age") == 3 })
	if got := h.written.Load(); got != 1 {
		t.Fatalf("written = %d, want 1 (only the in-flight frame)", got)
	}
}

func TestSpeakerCountBoundHolds(t *testing.T) {
	h := startSpeaker(t, time.Minute)
	const n = 200
	for i := 0; i < n; i++ {
		h.relay.recvCh <- []byte{1}
	}
	waitFor(t, "recv to drain", func() bool { return len(h.relay.recvCh) == 0 })
	time.Sleep(100 * time.Millisecond)
	// At most 1 frame in flight in the stalled writer + micBufferDepth queued;
	// the rest dropped (the writer may or may not have taken its frame yet).
	got := h.metrics.droppedCount("speaker")
	if got < n-1-micBufferDepth || got > n-micBufferDepth {
		t.Fatalf("speaker drops = %d, want %d or %d", got, n-1-micBufferDepth, n-micBufferDepth)
	}
}

func TestSpeakerAgeBoundOffWritesInline(t *testing.T) {
	h := startSpeaker(t, 0)
	close(h.gate)
	for i := 0; i < 3; i++ {
		h.relay.recvCh <- []byte{1}
	}
	waitFor(t, "inline writes", func() bool { return h.written.Load() == 3 })
	if got := h.metrics.droppedCount("speaker_age"); got != 0 {
		t.Fatalf("speaker_age = %d, want 0", got)
	}
}
