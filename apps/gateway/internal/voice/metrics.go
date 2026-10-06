package voice

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MediaCounters is the shared, concurrency-safe metrics sink for the media
// plane. serve.go creates ONE instance and hands it to both the MediaHub
// (which writes) and the gateway's metrics registry (which reads), so
// dropped audio and session lifecycle are observable without coupling the
// voice package to the Prometheus text renderer.
type MediaCounters struct {
	mu                sync.Mutex
	framesDropped     map[string]*atomic.Int64
	sessionsEnded     map[string]*atomic.Int64
	sessionsConnected atomic.Int64
	frameAge          map[string]*frameAgeHist // guarded by mu
	jitter            jitterHist               // guarded by mu
	packetsReceived   atomic.Int64
	packetsLost       atomic.Int64
	queueDepth        atomic.Pointer[func() int]
}

// JitterBuckets are the ubag_voice_inbound_jitter_seconds upper bounds in
// seconds: 1ms doubling to 512ms (RTP jitter on a healthy link is a few ms;
// beyond ~80ms it is audible).
var JitterBuckets = [...]float64{0.001, 0.002, 0.004, 0.008, 0.016, 0.032, 0.064, 0.128, 0.256, 0.512}

type jitterHist struct {
	buckets [len(JitterBuckets)]uint64 // non-cumulative; the +Inf remainder is count-sum(buckets)
	count   uint64
	sum     float64
}

// JitterSnapshot is the inbound-jitter histogram (non-cumulative buckets,
// index-aligned with JitterBuckets).
type JitterSnapshot struct {
	Buckets [len(JitterBuckets)]uint64
	Count   uint64
	Sum     float64
}

// FrameAgeBuckets are the ubag_voice_relay_frame_age_seconds upper bounds in
// seconds: 5ms doubling to 1.28s, tuned around the 20ms Opus frame and the
// 100ms relay p95 goal.
var FrameAgeBuckets = [...]float64{0.005, 0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28}

type frameAgeHist struct {
	buckets [len(FrameAgeBuckets)]uint64 // non-cumulative; the +Inf remainder is count-sum(buckets)
	count   uint64
	sum     float64
}

// FrameAgeSnapshot is one direction's histogram. Buckets are NON-cumulative
// and index-aligned with FrameAgeBuckets.
type FrameAgeSnapshot struct {
	Direction string
	Buckets   [len(FrameAgeBuckets)]uint64
	Count     uint64
	Sum       float64
}

// NewMediaCounters builds an empty counter set.
func NewMediaCounters() *MediaCounters {
	return &MediaCounters{
		framesDropped: map[string]*atomic.Int64{},
		sessionsEnded: map[string]*atomic.Int64{},
		frameAge:      map[string]*frameAgeHist{},
	}
}

func (m *MediaCounters) counterFor(bucket map[string]*atomic.Int64, key string) *atomic.Int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := bucket[key]; ok {
		return c
	}
	c := &atomic.Int64{}
	bucket[key] = c
	return c
}

// AddFramesDropped implements MediaMetrics.
func (m *MediaCounters) AddFramesDropped(direction string, n int64) {
	m.counterFor(m.framesDropped, strings.ToLower(strings.TrimSpace(direction))).Add(n)
}

// ObserveFrameAge implements FrameAgeObserver. The direction label stays a
// closed set (mic|speaker) so the series cannot grow.
func (m *MediaCounters) ObserveFrameAge(direction string, age time.Duration) {
	if direction != DirectionMic && direction != DirectionSpeaker {
		return
	}
	seconds := age.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	m.mu.Lock()
	h := m.frameAge[direction]
	if h == nil {
		h = &frameAgeHist{}
		m.frameAge[direction] = h
	}
	h.count++
	h.sum += seconds
	for i, bound := range FrameAgeBuckets {
		if seconds <= bound {
			h.buckets[i]++
			break
		}
	}
	m.mu.Unlock()
}

// ObserveInboundJitter implements LinkObserver. No per-session label.
func (m *MediaCounters) ObserveInboundJitter(seconds float64) {
	if seconds < 0 || seconds != seconds { // negative or NaN
		return
	}
	m.mu.Lock()
	m.jitter.count++
	m.jitter.sum += seconds
	for i, bound := range JitterBuckets {
		if seconds <= bound {
			m.jitter.buckets[i]++
			break
		}
	}
	m.mu.Unlock()
}

// SnapshotJitter returns the inbound-jitter histogram.
func (m *MediaCounters) SnapshotJitter() JitterSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return JitterSnapshot{Buckets: m.jitter.buckets, Count: m.jitter.count, Sum: m.jitter.sum}
}

// AddInboundPackets implements LinkObserver (deltas; non-positive ignored).
func (m *MediaCounters) AddInboundPackets(received, lost int64) {
	if received > 0 {
		m.packetsReceived.Add(received)
	}
	if lost > 0 {
		m.packetsLost.Add(lost)
	}
}

// InboundPackets returns the cumulative received and lost packet totals.
func (m *MediaCounters) InboundPackets() (received, lost int64) {
	return m.packetsReceived.Load(), m.packetsLost.Load()
}

// SetMicQueueDepthFunc registers the live mic-queue-depth reader (the hub's
// MicQueueDepth); the gauge is evaluated at scrape time.
func (m *MediaCounters) SetMicQueueDepthFunc(fn func() int) { m.queueDepth.Store(&fn) }

// MicQueueDepth returns the frames currently queued toward relays (0 until a
// reader is registered).
func (m *MediaCounters) MicQueueDepth() int {
	if fn := m.queueDepth.Load(); fn != nil && *fn != nil {
		return (*fn)()
	}
	return 0
}

// SnapshotFrameAge returns both directions (zero-valued until observed so the
// Prometheus series always exists), in mic, speaker order.
func (m *MediaCounters) SnapshotFrameAge() []FrameAgeSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]FrameAgeSnapshot, 0, 2)
	for _, direction := range []string{DirectionMic, DirectionSpeaker} {
		snap := FrameAgeSnapshot{Direction: direction}
		if h := m.frameAge[direction]; h != nil {
			snap.Buckets, snap.Count, snap.Sum = h.buckets, h.count, h.sum
		}
		out = append(out, snap)
	}
	return out
}

// AddSessionsConnected implements MediaMetrics.
func (m *MediaCounters) AddSessionsConnected() { m.sessionsConnected.Add(1) }

// AddSessionsEnded implements MediaMetrics.
func (m *MediaCounters) AddSessionsEnded(reason string) {
	m.counterFor(m.sessionsEnded, strings.TrimSpace(reason)).Add(1)
}

// SnapshotFramesDropped returns dropped-frame totals per direction, sorted
// by direction name, with zero entries for the standard directions so the
// Prometheus series always exists.
func (m *MediaCounters) SnapshotFramesDropped() []struct {
	Direction string
	Count     int64
} {
	defaults := []string{"mic", "speaker"}
	m.mu.Lock()
	out := make([]struct {
		Direction string
		Count     int64
	}, 0, len(m.framesDropped)+len(defaults))
	seen := map[string]bool{}
	for _, direction := range defaults {
		seen[direction] = true
		var count int64
		if c, ok := m.framesDropped[direction]; ok {
			count = c.Load()
		}
		out = append(out, struct {
			Direction string
			Count     int64
		}{direction, count})
	}
	for direction, c := range m.framesDropped {
		if seen[direction] {
			continue
		}
		seen[direction] = true
		out = append(out, struct {
			Direction string
			Count     int64
		}{direction, c.Load()})
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Direction < out[j].Direction })
	return out
}

// SnapshotSessionsEnded returns ended-session totals per reason, sorted,
// with the standard reasons always present.
func (m *MediaCounters) SnapshotSessionsEnded() []struct {
	Reason string
	Count  int64
} {
	defaults := []string{"session_terminated", "lease_expired", "peer_connection_failed"}
	m.mu.Lock()
	out := make([]struct {
		Reason string
		Count  int64
	}, 0, len(m.sessionsEnded)+len(defaults))
	seen := map[string]bool{}
	for _, reason := range defaults {
		seen[reason] = true
		var count int64
		if c, ok := m.sessionsEnded[reason]; ok {
			count = c.Load()
		}
		out = append(out, struct {
			Reason string
			Count  int64
		}{reason, count})
	}
	for reason, c := range m.sessionsEnded {
		if seen[reason] {
			continue
		}
		seen[reason] = true
		out = append(out, struct {
			Reason string
			Count  int64
		}{reason, c.Load()})
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Reason < out[j].Reason })
	return out
}

// SessionsConnected returns the connected-session total.
func (m *MediaCounters) SessionsConnected() int64 { return m.sessionsConnected.Load() }
