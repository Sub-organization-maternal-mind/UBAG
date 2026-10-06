package voice

import (
	"context"
	"crypto/hmac"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// testRelaySecret authenticates the hub's hello to the fake relay.
var testRelaySecret = []byte("relay-test-secret")

// fakeRelay speaks relay protocol v2: it verifies the hello token, answers
// ready, echoes every audio frame back (standing in for the browser
// container's mic→speaker loopback), records control messages, and serves ONE
// session at a time (a second concurrent connection gets error "busy").
type fakeRelay struct {
	listener net.Listener
	secret   []byte
	mu       sync.Mutex
	frames   [][]byte
	controls []map[string]any
	hellos   int
	active   bool
}

func startFakeRelay(t testing.TB) (*fakeRelay, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	relay := &fakeRelay{listener: listener, secret: testRelaySecret}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go relay.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return relay, listener.Addr().String()
}

func writeRelayFrame(conn net.Conn, kind byte, payload []byte) error {
	out := make([]byte, relayFrameHeaderBytes+1+len(payload))
	binary.LittleEndian.PutUint32(out, uint32(1+len(payload)))
	out[relayFrameHeaderBytes] = kind
	copy(out[relayFrameHeaderBytes+1:], payload)
	_, err := conn.Write(out)
	return err
}

func readRelayFrame(conn net.Conn) (byte, []byte, error) {
	header := make([]byte, relayFrameHeaderBytes)
	if _, err := readFull(conn, header); err != nil {
		return 0, nil, err
	}
	length := binary.LittleEndian.Uint32(header)
	if length < 1 || length > maxRelayFrameBytes {
		return 0, nil, errors.New("bad frame length")
	}
	frame := make([]byte, length)
	if _, err := readFull(conn, frame); err != nil {
		return 0, nil, err
	}
	return frame[0], frame[1:], nil
}

func (r *fakeRelay) serve(conn net.Conn) {
	defer conn.Close()
	kind, payload, err := readRelayFrame(conn)
	if err != nil || kind != relayTypeControl {
		return
	}
	var hello struct {
		Op        string `json:"op"`
		SessionID string `json:"session_id"`
		Exp       int64  `json:"exp"`
		Token     string `json:"token"`
	}
	if json.Unmarshal(payload, &hello) != nil || hello.Op != "hello" ||
		!hmac.Equal([]byte(hello.Token), []byte(RelayToken(r.secret, hello.SessionID, hello.Exp))) {
		_ = writeRelayFrame(conn, relayTypeControl, []byte(`{"op":"error","reason":"unauthorized"}`))
		return
	}
	r.mu.Lock()
	if r.active {
		r.mu.Unlock()
		_ = writeRelayFrame(conn, relayTypeControl, []byte(`{"op":"error","reason":"busy"}`))
		return
	}
	r.active = true
	r.hellos++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.active = false
		r.mu.Unlock()
	}()
	if err := writeRelayFrame(conn, relayTypeControl, []byte(`{"op":"ready"}`)); err != nil {
		return
	}
	for {
		kind, payload, err := readRelayFrame(conn)
		if err != nil {
			return
		}
		switch kind {
		case relayTypeAudio:
			r.mu.Lock()
			r.frames = append(r.frames, payload)
			r.mu.Unlock()
			if err := writeRelayFrame(conn, relayTypeAudio, payload); err != nil {
				return
			}
		case relayTypeControl:
			var msg map[string]any
			if json.Unmarshal(payload, &msg) == nil {
				r.mu.Lock()
				r.controls = append(r.controls, msg)
				r.mu.Unlock()
			}
		}
	}
}

func (r *fakeRelay) frameCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

type countingMetrics struct {
	mu        sync.Mutex
	dropped   map[string]int64
	ended     []string
	connected int
}

func (m *countingMetrics) AddFramesDropped(direction string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropped[direction] += n
}
func (m *countingMetrics) AddSessionsConnected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected++
}
func (m *countingMetrics) AddSessionsEnded(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ended = append(m.ended, reason)
}

// TestMediaHubLoopbackNegotiatesAndRelays drives the full media path in
// process: a client PeerConnection offers, the hub answers, the client's
// Opus RTP payloads flow to the relay, and relay frames flow back as RTP.
func TestMediaHubLoopbackNegotiatesAndRelays(t *testing.T) {
	relay, addr := startFakeRelay(t)
	metrics := &countingMetrics{dropped: map[string]int64{}}
	hub := &MediaHub{
		ICE:     testICE,
		Dialer:  &TCPRelayDialer{Address: func(Session) (string, error) { return addr, nil }, Secret: testRelaySecret},
		Metrics: metrics,
	}

	// Client-side peer connection with one Opus track.
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

	var received [][]byte
	var recvMu sync.Mutex
	clientPC.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			recvMu.Lock()
			received = append(received, pkt.Payload)
			recvMu.Unlock()
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
		Session{ID: "voice_loopback", TenantID: "t", Target: "chatgpt_web", InstanceRef: "browser-1"},
		clientPC.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("HandleOffer: %v", err)
	}
	if err := clientPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatalf("client remote desc: %v", err)
	}

	// Keep sending until the relay has seen a steady flow: samples written
	// before the DTLS handshake completes are silently dropped, so a fixed
	// burst would depend on handshake timing.
	deadline := time.Now().Add(8 * time.Second)
	sent := 0
	for time.Now().Before(deadline) && relay.frameCount() < 5 {
		if err := micTrack.WriteSample(media.Sample{Data: []byte{byte(sent), 0xAA, 0xBB}, Duration: opusFrameDuration}); err == nil {
			sent++
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := relay.frameCount(); got < 3 {
		t.Fatalf("relay received %d frames, want a steady flow (sent %d)", got, sent)
	}

	// And the client must receive the echoed frames back on its track.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		recvMu.Lock()
		n := len(received)
		recvMu.Unlock()
		if n >= sent {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	recvMu.Lock()
	n := len(received)
	recvMu.Unlock()
	if n == 0 {
		t.Fatal("client received no provider audio back")
	}

	// Disconnect tears the media path down deterministically.
	hub.Disconnect("voice_loopback")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		metrics.mu.Lock()
		ended := len(metrics.ended)
		metrics.mu.Unlock()
		if ended > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	metrics.mu.Lock()
	ended := len(metrics.ended)
	metrics.mu.Unlock()
	if ended == 0 {
		t.Fatal("Disconnect must end the media session")
	}
}

func TestTCPRelayDialerUnreachable(t *testing.T) {
	dialer := &TCPRelayDialer{Address: func(Session) (string, error) { return "127.0.0.1:1", nil }, Secret: testRelaySecret, DialTimeout: 200 * time.Millisecond}
	if _, err := dialer.Dial(context.Background(), Session{ID: "s", InstanceRef: "browser-x"}); err == nil {
		t.Fatal("dial to closed port must fail")
	}
	empty := &TCPRelayDialer{Address: func(Session) (string, error) { return "", nil }, Secret: testRelaySecret}
	if _, err := empty.Dial(context.Background(), Session{ID: "s", InstanceRef: "browser-x"}); err != ErrRelayUnavailable {
		t.Fatalf("empty address err = %v, want ErrRelayUnavailable", err)
	}
}

// waitGathered blocks until the peer connection finishes ICE gathering (the
// local description then embeds every candidate).
func waitGathered(t testing.TB, pc *webrtc.PeerConnection) {
	t.Helper()
	gathered := make(chan struct{})
	pc.OnICEGatheringStateChange(func(state webrtc.ICEGatheringState) {
		if state == webrtc.ICEGatheringStateComplete {
			select {
			case <-gathered:
			default:
				close(gathered)
			}
		}
	})
	select {
	case <-gathered:
	case <-time.After(20 * time.Second):
		t.Fatal("ICE gathering did not complete")
	}
}
