package voice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func relayDialer(addr string, secret []byte) *TCPRelayDialer {
	return &TCPRelayDialer{Address: func(Session) (string, error) { return addr, nil }, Secret: secret, ReadyTimeout: 2 * time.Second}
}

func TestRelayDialerAuthenticatesAndFailsClosed(t *testing.T) {
	_, addr := startFakeRelay(t)
	if conn, err := relayDialer(addr, testRelaySecret).Dial(t.Context(), Session{ID: "s1", InstanceRef: "browser-1"}); err != nil {
		t.Fatalf("authorized dial: %v", err)
	} else {
		_ = conn.Close()
	}
	if _, err := relayDialer(addr, []byte("wrong")).Dial(t.Context(), Session{ID: "s1", InstanceRef: "browser-1"}); !errors.Is(err, ErrRelayUnavailable) {
		t.Fatalf("wrong secret = %v, want ErrRelayUnavailable", err)
	}
	if _, err := relayDialer(addr, nil).Dial(t.Context(), Session{ID: "s1", InstanceRef: "browser-1"}); !errors.Is(err, ErrRelayUnavailable) {
		t.Fatalf("empty secret must fail closed, got %v", err)
	}
}

// One audio environment hosts one session: a second dial is refused as busy
// until the first connection closes.
func TestRelayDialerOneSessionPerEnvironment(t *testing.T) {
	_, addr := startFakeRelay(t)
	d := relayDialer(addr, testRelaySecret)
	first, err := d.Dial(t.Context(), Session{ID: "s1", InstanceRef: "browser-1"})
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	if _, err := d.Dial(t.Context(), Session{ID: "s2", InstanceRef: "browser-1"}); !errors.Is(err, ErrRelayBusy) {
		t.Fatalf("second dial = %v, want ErrRelayBusy", err)
	}
	_ = first.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := d.Dial(t.Context(), Session{ID: "s3", InstanceRef: "browser-1"})
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("relay slot never released: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// hubWithClient negotiates a hub session against the fake relay and returns
// the client's mic track for sending samples.
func hubWithClient(t *testing.T, hub *MediaHub, session Session) *webrtc.TrackLocalStaticSample {
	t.Helper()
	clientPC, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("client pc: %v", err)
	}
	t.Cleanup(func() { _ = clientPC.Close() })
	mic, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "client-mic", "ubag-client")
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, err := clientPC.AddTrack(mic); err != nil {
		t.Fatalf("add track: %v", err)
	}
	offer, err := clientPC.CreateOffer(nil)
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	if err := clientPC.SetLocalDescription(offer); err != nil {
		t.Fatalf("local: %v", err)
	}
	waitGathered(t, clientPC)
	answer, err := hub.HandleOffer(context.Background(), session, clientPC.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("HandleOffer: %v", err)
	}
	if err := clientPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatalf("remote: %v", err)
	}
	return mic
}

func sendSamples(mic *webrtc.TrackLocalStaticSample, n int) {
	for i := 0; i < n; i++ {
		_ = mic.WriteSample(media.Sample{Data: []byte{byte(i), 0xAA, 0xBB}, Duration: opusFrameDuration})
		time.Sleep(10 * time.Millisecond)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Muted microphone frames never reach the relay; unmuting resumes them, and
// the relay is told about each change.
func TestMediaHubMuteSuppressesMicFrames(t *testing.T) {
	relay, addr := startFakeRelay(t)
	hub := &MediaHub{Dialer: relayDialer(addr, testRelaySecret)}
	mic := hubWithClient(t, hub, Session{ID: "voice_mute", TenantID: "t", Target: "chatgpt_web", InstanceRef: "browser-1"})
	defer hub.Disconnect("voice_mute")

	go sendSamples(mic, 200) // steady flow for the whole test
	waitFor(t, "first frames", func() bool { return relay.frameCount() >= 3 })

	if !hub.SetMuted("voice_mute", true) {
		t.Fatal("SetMuted found no live session")
	}
	time.Sleep(150 * time.Millisecond) // let in-flight frames drain
	before := relay.frameCount()
	time.Sleep(400 * time.Millisecond)
	if after := relay.frameCount(); after != before {
		t.Fatalf("muted session leaked %d frames to the provider", after-before)
	}
	hub.SetMuted("voice_mute", false)
	waitFor(t, "frames after unmute", func() bool { return relay.frameCount() > before })

	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.controls) < 2 {
		t.Fatalf("relay saw %d control messages, want mute+unmute", len(relay.controls))
	}
}

// A second offer replaces the first connection; the replaced connection's
// teardown must not close the replacement.
func TestMediaHubReconnectReplacesOldConnection(t *testing.T) {
	relay, addr := startFakeRelay(t)
	metrics := &countingMetrics{dropped: map[string]int64{}}
	var closed []string
	hub := &MediaHub{
		Dialer:   relayDialer(addr, testRelaySecret),
		Metrics:  metrics,
		OnClosed: func(_ Session, reason string) { closed = append(closed, reason) },
	}
	session := Session{ID: "voice_reconnect", TenantID: "t", Target: "chatgpt_web", InstanceRef: "browser-1"}
	hubWithClient(t, hub, session)
	waitFor(t, "first hello", func() bool { relay.mu.Lock(); defer relay.mu.Unlock(); return relay.hellos == 1 })

	mic := hubWithClient(t, hub, session) // reconnect
	defer hub.Disconnect("voice_reconnect")
	if hub.ActiveSessions() != 1 {
		t.Fatalf("active sessions = %d, want exactly the replacement", hub.ActiveSessions())
	}
	go sendSamples(mic, 50)
	waitFor(t, "frames over the replacement", func() bool { return relay.frameCount() >= 3 })
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if len(metrics.ended) != 1 || metrics.ended[0] != "reconnect_replaced" {
		t.Fatalf("ended = %v, want only the replaced generation", metrics.ended)
	}
	if len(closed) != 0 {
		t.Fatalf("replacement fired OnClosed: %v (the caller owns the session)", closed)
	}
}

// Hub shutdown closes every live media session.
func TestMediaHubCloseEndsAllSessions(t *testing.T) {
	_, addr := startFakeRelay(t)
	hub := &MediaHub{Dialer: relayDialer(addr, testRelaySecret)}
	hubWithClient(t, hub, Session{ID: "voice_shutdown", TenantID: "t", Target: "chatgpt_web", InstanceRef: "browser-1"})
	hub.Close()
	if hub.ActiveSessions() != 0 {
		t.Fatalf("active sessions after Close = %d", hub.ActiveSessions())
	}
}
