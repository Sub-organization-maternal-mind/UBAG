package voice

import (
	"context"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func TestJitterInboundAudioStatsFold(t *testing.T) {
	report := webrtc.StatsReport{
		"a": webrtc.InboundRTPStreamStats{Kind: "audio", PacketsReceived: 100, PacketsLost: 3, Jitter: 0.004},
		"b": webrtc.InboundRTPStreamStats{Kind: "audio", PacketsReceived: 50, PacketsLost: -2, Jitter: 0.009}, // duplicates: lost clamps to 0
		"c": webrtc.InboundRTPStreamStats{Kind: "video", PacketsReceived: 999, PacketsLost: 99, Jitter: 5},
		"d": webrtc.InboundRTPStreamStats{Kind: "audio", PacketsReceived: 0, Jitter: 7}, // nothing received yet
	}
	jitter, received, lost, ok := inboundAudioStats(report)
	if !ok || jitter != 0.009 || received != 150 || lost != 3 {
		t.Fatalf("fold = jitter %v received %d lost %d ok %v", jitter, received, lost, ok)
	}
	if _, _, _, ok := inboundAudioStats(webrtc.StatsReport{}); ok {
		t.Fatal("an empty report must not produce a sample")
	}
}

func TestJitterCountersHistogramPacketsAndDepth(t *testing.T) {
	m := NewMediaCounters()
	m.ObserveInboundJitter(0.0005) // <= 1ms
	m.ObserveInboundJitter(0.02)   // <= 32ms
	m.ObserveInboundJitter(3)      // +Inf only
	m.ObserveInboundJitter(-1)     // ignored
	snap := m.SnapshotJitter()
	if snap.Count != 3 || snap.Buckets[0] != 1 || snap.Buckets[5] != 1 {
		t.Fatalf("jitter snapshot = %+v", snap)
	}
	m.AddInboundPackets(10, 2)
	m.AddInboundPackets(-5, -1) // never negative
	if r, l := m.InboundPackets(); r != 10 || l != 2 {
		t.Fatalf("packets = %d/%d", r, l)
	}
	if m.MicQueueDepth() != 0 {
		t.Fatal("depth must be 0 until a reader is registered")
	}
	m.SetMicQueueDepthFunc(func() int { return 7 })
	if m.MicQueueDepth() != 7 {
		t.Fatalf("depth = %d", m.MicQueueDepth())
	}
}

// TestJitterLoopbackSamplesPeerConnectionStats drives a real loopback
// session and asserts the hub folds pc.GetStats inbound audio into the
// label-free jitter histogram and packet counters.
func TestJitterLoopbackSamplesPeerConnectionStats(t *testing.T) {
	if testing.Short() {
		t.Skip("loopback WebRTC session; skipped in -short")
	}
	_, addr := startFakeRelay(t)
	counters := NewMediaCounters()
	hub := &MediaHub{
		ICE:           testICE,
		Dialer:        &TCPRelayDialer{Address: func(Session) (string, error) { return addr, nil }, Secret: testRelaySecret},
		Metrics:       counters,
		StatsInterval: 100 * time.Millisecond,
	}
	counters.SetMicQueueDepthFunc(hub.MicQueueDepth)
	defer hub.Close()

	clientPC, err := testClientPeerConnection()
	if err != nil {
		t.Fatalf("client pc: %v", err)
	}
	defer clientPC.Close()
	micTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "client-mic", "ubag-client",
	)
	if err != nil {
		t.Fatalf("client track: %v", err)
	}
	if _, err := clientPC.AddTrack(micTrack); err != nil {
		t.Fatalf("client add track: %v", err)
	}
	clientPC.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			if _, _, err := track.ReadRTP(); err != nil {
				return
			}
		}
	})
	offer, err := clientPC.CreateOffer(nil)
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	if err := clientPC.SetLocalDescription(offer); err != nil {
		t.Fatalf("client local desc: %v", err)
	}
	waitGathered(t, clientPC)
	answer, err := hub.HandleOffer(context.Background(),
		Session{ID: "voice_jitter", TenantID: "t", Target: "chatgpt_web", InstanceRef: "browser-1"},
		clientPC.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("HandleOffer: %v", err)
	}
	if err := clientPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatalf("client remote desc: %v", err)
	}

	// Pace at the Opus frame duration until stats were sampled (samples
	// written before DTLS completes are dropped by pion).
	ticker := time.NewTicker(opusFrameDuration)
	defer ticker.Stop()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		<-ticker.C
		_ = micTrack.WriteSample(media.Sample{Data: make([]byte, 60), Duration: opusFrameDuration})
		if counters.SnapshotJitter().Count >= 2 {
			break
		}
	}
	snap := counters.SnapshotJitter()
	if snap.Count < 2 {
		t.Fatalf("jitter histogram never sampled: %+v", snap)
	}
	received, lost := counters.InboundPackets()
	if received <= 0 || lost < 0 {
		t.Fatalf("packets received/lost = %d/%d", received, lost)
	}
	if got := counters.MicQueueDepth(); got < 0 || got > micBufferDepth {
		t.Fatalf("mic queue depth %d outside 0..%d", got, micBufferDepth)
	}
}
