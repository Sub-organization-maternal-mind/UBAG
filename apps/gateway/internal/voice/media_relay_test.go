package voice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// testClientPeerConnection builds the CLIENT side peer for tests with a
// deterministic network setup: UDP4 only and mDNS candidate obfuscation off.
// Default settings can stall ICE gathering for seconds on CI runners (mDNS
// registration, interface enumeration), which made the tests time-dependent.
func testClientPeerConnection() (*webrtc.PeerConnection, error) {
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetIncludeLoopbackCandidate(true)
	return webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
}

// testICE lets the hub and test clients connect over loopback on any host.
var testICE = &ICEConfig{IncludeLoopback: true}

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
	clientPC, err := testClientPeerConnection()
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
	hub := &MediaHub{Dialer: relayDialer(addr, testRelaySecret), ICE: testICE}
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
		ICE:      testICE,
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
	hub := &MediaHub{Dialer: relayDialer(addr, testRelaySecret), ICE: testICE}
	hubWithClient(t, hub, Session{ID: "voice_shutdown", TenantID: "t", Target: "chatgpt_web", InstanceRef: "browser-1"})
	hub.Close()
	if hub.ActiveSessions() != 0 {
		t.Fatalf("active sessions after Close = %d", hub.ActiveSessions())
	}
}

// The Python relay verifies the same HMAC; this vector was computed there
// (hmac sha256, "voice-relay|s1|1700000000").
func TestRelayTokenMatchesPythonRelay(t *testing.T) {
	const want = "43e2f2445ad07779cdf6135f4756cbe2ea5802718f6b6b9c930bb67d53a10591"
	if got := RelayToken([]byte("relay-test-secret"), "s1", 1700000000); got != want {
		t.Fatalf("RelayToken = %s, want %s", got, want)
	}
}

// The control data channel ignores every command until the client presents a
// credential the hub's authorizer accepts for exactly this session.
func TestMediaHubControlChannelRequiresScopedCredential(t *testing.T) {
	_, addr := startFakeRelay(t)
	hub := &MediaHub{
		ICE:    testICE,
		Dialer: relayDialer(addr, testRelaySecret),
		AuthorizeControl: func(s Session, credential string) bool {
			return s.ID == "voice_ctl" && credential == "good"
		},
	}
	clientPC, err := testClientPeerConnection()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientPC.Close() })
	mic, _ := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "m", "c")
	_, _ = clientPC.AddTrack(mic)
	dc, err := clientPC.CreateDataChannel("control", nil)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{})
	dc.OnOpen(func() { close(opened) })
	offer, _ := clientPC.CreateOffer(nil)
	_ = clientPC.SetLocalDescription(offer)
	waitGathered(t, clientPC)
	answer, err := hub.HandleOffer(context.Background(), Session{ID: "voice_ctl", TenantID: "t", Target: "chatgpt_web", InstanceRef: "browser-1"}, clientPC.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	_ = clientPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer})
	defer hub.Disconnect("voice_ctl")
	select {
	case <-opened:
	case <-time.After(8 * time.Second):
		t.Fatal("control data channel never opened")
	}
	muted := func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return hub.sessions["voice_ctl"].muted.Load()
	}

	_ = dc.SendText(`{"op":"mute","muted":true}`)
	time.Sleep(300 * time.Millisecond)
	if muted() {
		t.Fatal("unauthenticated mute was honored")
	}
	_ = dc.SendText(`{"op":"auth","credential":"wrong"}`)
	_ = dc.SendText(`{"op":"mute","muted":true}`)
	time.Sleep(300 * time.Millisecond)
	if muted() {
		t.Fatal("mute honored after a rejected credential")
	}
	_ = dc.SendText(`{"op":"auth","credential":"good"}`)
	_ = dc.SendText(`{"op":"mute","muted":true}`)
	waitFor(t, "authorized mute", muted)
}
