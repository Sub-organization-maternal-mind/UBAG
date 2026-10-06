package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// In-process relay latency harness (P5.1). Real MediaHub + pion client over
// loopback against fakeRelay (which echoes every frame), client mic paced at
// the 20 ms Opus frame duration. It records the two gateway-side measurement
// points defined in docs/load-testing.md ("Voice relay latency"):
//
//	mic     RTP read -> relay.Send returned   (DirectionMic)
//	speaker relay.Recv returned -> WriteSample returned (DirectionSpeaker)
//
// Loopback numbers are NON-AUTHORITATIVE: they bound gateway-side queueing
// and scheduling only, not network, TURN, or the browser audio stack.

// ageRecorder keeps every observed age so percentiles are exact (the
// Prometheus histogram only has bucket resolution).
type ageRecorder struct {
	countingMetrics
	agesMu sync.Mutex
	ages   map[string][]time.Duration
}

func newAgeRecorder() *ageRecorder {
	return &ageRecorder{countingMetrics: countingMetrics{dropped: map[string]int64{}}, ages: map[string][]time.Duration{}}
}

func (r *ageRecorder) ObserveFrameAge(direction string, age time.Duration) {
	r.agesMu.Lock()
	r.ages[direction] = append(r.ages[direction], age)
	r.agesMu.Unlock()
}

func (r *ageRecorder) reset() {
	r.agesMu.Lock()
	r.ages = map[string][]time.Duration{}
	r.agesMu.Unlock()
}

func (r *ageRecorder) snapshot(direction string) []time.Duration {
	r.agesMu.Lock()
	defer r.agesMu.Unlock()
	return append([]time.Duration(nil), r.ages[direction]...)
}

// latencyPercentile is the nearest-rank percentile of an unsorted sample.
func latencyPercentile(sample []time.Duration, p float64) time.Duration {
	if len(sample) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), sample...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(p*float64(len(sorted))+0.999999999) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// latencyRow is one (sessions, direction) result, also the JSON report row
// that tests/load/acceptance.mjs ingests via --voice-latency.
type latencyRow struct {
	Sessions  int     `json:"sessions"`
	Direction string  `json:"direction"`
	Samples   int     `json:"samples"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// runRelayLatency drives n concurrent loopback sessions (each with its own
// fakeRelay: the relay serves one session at a time) paced at 20 ms, waits
// for steady flow, then measures for `measure`.
func runRelayLatency(tb testing.TB, n int, measure time.Duration) []latencyRow {
	tb.Helper()
	recorder := newAgeRecorder()
	relayAddrs := map[string]string{}
	relays := make([]*fakeRelay, n)
	for i := 0; i < n; i++ {
		relay, addr := startFakeRelay(tb)
		relays[i] = relay
		relayAddrs[fmt.Sprintf("voice_lat_%d", i)] = addr
	}
	hub := &MediaHub{
		ICE: testICE,
		Dialer: &TCPRelayDialer{
			Address: func(s Session) (string, error) { return relayAddrs[s.ID], nil },
			Secret:  testRelaySecret,
		},
		Metrics: recorder,
	}
	// UBAG_VOICE_QUEUE_MAX_AGE_MS (P5.3) lets the bench compare the age bound on/off.
	if v, err := strconv.Atoi(os.Getenv("UBAG_VOICE_QUEUE_MAX_AGE_MS")); err == nil && v > 0 {
		hub.QueueMaxAge = time.Duration(v) * time.Millisecond
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	defer func() {
		close(stop)
		wg.Wait()
		hub.Close()
	}()

	for i := 0; i < n; i++ {
		clientPC, err := testClientPeerConnection()
		if err != nil {
			tb.Fatalf("client pc: %v", err)
		}
		defer clientPC.Close()
		micTrack, err := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "client-mic", "ubag-client",
		)
		if err != nil {
			tb.Fatalf("client track: %v", err)
		}
		if _, err := clientPC.AddTrack(micTrack); err != nil {
			tb.Fatalf("client add track: %v", err)
		}
		// Drain the echoed speaker track so the client never backs up.
		clientPC.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			for {
				if _, _, err := track.ReadRTP(); err != nil {
					return
				}
			}
		})
		offer, err := clientPC.CreateOffer(nil)
		if err != nil {
			tb.Fatalf("offer: %v", err)
		}
		if err := clientPC.SetLocalDescription(offer); err != nil {
			tb.Fatalf("client local desc: %v", err)
		}
		waitGathered(tb, clientPC)
		answer, err := hub.HandleOffer(context.Background(),
			Session{ID: fmt.Sprintf("voice_lat_%d", i), TenantID: "t", Target: "chatgpt_web", InstanceRef: fmt.Sprintf("browser-%d", i)},
			clientPC.LocalDescription().SDP)
		if err != nil {
			tb.Fatalf("HandleOffer %d: %v", i, err)
		}
		if err := clientPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
			tb.Fatalf("client remote desc: %v", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := make([]byte, 60) // typical Opus voice packet size
			ticker := time.NewTicker(opusFrameDuration)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					_ = micTrack.WriteSample(media.Sample{Data: payload, Duration: opusFrameDuration})
				}
			}
		}()
	}

	// Wait for steady flow on every relay (samples before DTLS completes are
	// dropped by pion, so a fixed delay would depend on handshake timing).
	deadline := time.Now().Add(20 * time.Second)
	for _, relay := range relays {
		for relay.frameCount() < 10 {
			if time.Now().After(deadline) {
				tb.Fatalf("relay never reached steady flow")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	recorder.reset()
	time.Sleep(measure)

	rows := make([]latencyRow, 0, 2)
	for _, direction := range []string{DirectionMic, DirectionSpeaker} {
		sample := recorder.snapshot(direction)
		rows = append(rows, latencyRow{
			Sessions: n, Direction: direction, Samples: len(sample),
			P50Ms: ms(latencyPercentile(sample, 0.50)), P95Ms: ms(latencyPercentile(sample, 0.95)), P99Ms: ms(latencyPercentile(sample, 0.99)),
		})
	}
	return rows
}

func TestLatencyPercentileNearestRank(t *testing.T) {
	sample := make([]time.Duration, 100)
	for i := range sample {
		sample[i] = time.Duration(i+1) * time.Millisecond
	}
	for p, want := range map[float64]time.Duration{0.50: 50 * time.Millisecond, 0.95: 95 * time.Millisecond, 0.99: 99 * time.Millisecond, 1: 100 * time.Millisecond} {
		if got := latencyPercentile(sample, p); got != want {
			t.Fatalf("p%.0f = %v, want %v", p*100, got, want)
		}
	}
	if latencyPercentile(nil, 0.95) != 0 {
		t.Fatal("empty sample must be 0")
	}
}

// TestLatencyRelayLoopbackRecordsBothDirections is the always-on guard: one
// paced loopback session must produce frame ages in BOTH directions. The
// bound is a loose sanity limit; the 100 ms goal is enforced on bench output
// by tests/load (max_voice_relay_p95_ms).
func TestLatencyRelayLoopbackRecordsBothDirections(t *testing.T) {
	if testing.Short() {
		t.Skip("loopback WebRTC session; skipped in -short")
	}
	for _, row := range runRelayLatency(t, 1, 1500*time.Millisecond) {
		if row.Samples < 20 {
			t.Fatalf("%s: %d samples, want >= 20", row.Direction, row.Samples)
		}
		if row.P95Ms >= 1000 {
			t.Fatalf("%s: p95 %.2f ms is not a sane in-process age", row.Direction, row.P95Ms)
		}
	}
}

// BenchmarkRelayLatency ramps 1/5/10/20 sessions at 20 ms pacing and reports
// p50/p95/p99 per direction (milliseconds). Set UBAG_VOICE_LATENCY_REPORT to
// a path to also write the rows as JSON for `acceptance.mjs --voice-latency`;
// UBAG_VOICE_LATENCY_SECONDS (default 3) sets the measurement window.
//
//	go test ./internal/voice -run '^$' -bench RelayLatency -benchtime 1x
func BenchmarkRelayLatency(b *testing.B) {
	window := 3 * time.Second
	if v, err := strconv.Atoi(os.Getenv("UBAG_VOICE_LATENCY_SECONDS")); err == nil && v > 0 {
		window = time.Duration(v) * time.Second
	}
	// A sub-benchmark may rerun with a larger b.N; keep the last rows per size.
	bySessions := map[int][]latencyRow{}
	sizes := []int{1, 5, 10, 20}
	for _, n := range sizes {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			var rows []latencyRow
			for i := 0; i < b.N; i++ {
				rows = runRelayLatency(b, n, window)
			}
			for _, row := range rows {
				b.ReportMetric(row.P50Ms, row.Direction+"_p50_ms")
				b.ReportMetric(row.P95Ms, row.Direction+"_p95_ms")
				b.ReportMetric(row.P99Ms, row.Direction+"_p99_ms")
				b.ReportMetric(float64(row.Samples), row.Direction+"_samples")
			}
			bySessions[n] = rows
		})
	}
	var reported []latencyRow
	for _, n := range sizes {
		reported = append(reported, bySessions[n]...)
	}
	if path := os.Getenv("UBAG_VOICE_LATENCY_REPORT"); path != "" && len(reported) > 0 {
		report := map[string]any{
			"schema":            "ubag-voice-latency/v1",
			"non_authoritative": true,
			"note":              "in-process loopback (fakeRelay + pion client); gateway-side queueing only",
			"frame_ms":          opusFrameDuration.Milliseconds(),
			"window_seconds":    window.Seconds(),
			"rows":              reported,
		}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			b.Fatalf("marshal report: %v", err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			b.Fatalf("write report: %v", err)
		}
	}
}
