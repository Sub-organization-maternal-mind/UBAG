package voice

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// fakeRelay accepts one framed connection and echoes every received frame
// back (standing in for the browser container's mic→speaker loopback).
type fakeRelay struct {
	listener net.Listener
	mu       sync.Mutex
	frames   [][]byte
}

func startFakeRelay(t *testing.T) (*fakeRelay, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	relay := &fakeRelay{listener: listener}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go relay.serve(conn)
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return relay, listener.Addr().String()
}

func (r *fakeRelay) serve(conn net.Conn) {
	defer conn.Close()
	header := make([]byte, relayFrameHeaderBytes)
	for {
		if _, err := readFull(conn, header); err != nil {
			return
		}
		length := binary.LittleEndian.Uint32(header)
		if length == 0 || length > maxRelayFrameBytes {
			return
		}
		frame := make([]byte, length)
		if _, err := readFull(conn, frame); err != nil {
			return
		}
		r.mu.Lock()
		r.frames = append(r.frames, frame)
		r.mu.Unlock()
		// Echo it back (speaker direction).
		out := make([]byte, relayFrameHeaderBytes+len(frame))
		binary.LittleEndian.PutUint32(out, uint32(len(frame)))
		copy(out[relayFrameHeaderBytes:], frame)
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
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
		Dialer:  &TCPRelayDialer{Address: func(string) (string, error) { return addr, nil }},
		Metrics: metrics,
	}

	// Client-side peer connection with one Opus track.
	clientPC, err := webrtc.NewPeerConnection(webrtc.Configuration{})
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

	// Wait for the client track to reach the hub's OnTrack (negotiation).
	deadline := time.Now().Add(5 * time.Second)
	sent := 0
	for time.Now().Before(deadline) && sent < 5 {
		if err := micTrack.WriteSample(media.Sample{Data: []byte{byte(sent), 0xAA, 0xBB}, Duration: opusFrameDuration}); err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		sent++
		time.Sleep(10 * time.Millisecond)
	}
	if sent == 0 {
		t.Fatal("client could not send any sample (track not ready)")
	}

	// Relay must have received the client's Opus payloads.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		relay.mu.Lock()
		n := len(relay.frames)
		relay.mu.Unlock()
		if n >= sent {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	relay.mu.Lock()
	got := len(relay.frames)
	relay.mu.Unlock()
	if got < 3 {
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
	dialer := &TCPRelayDialer{Address: func(string) (string, error) { return "127.0.0.1:1", nil }, DialTimeout: 200 * time.Millisecond}
	if _, err := dialer.Dial(context.Background(), "browser-x", "s"); err == nil {
		t.Fatal("dial to closed port must fail")
	}
	empty := &TCPRelayDialer{Address: func(string) (string, error) { return "", nil }}
	if _, err := empty.Dial(context.Background(), "browser-x", "s"); err != ErrRelayUnavailable {
		t.Fatalf("empty address err = %v, want ErrRelayUnavailable", err)
	}
}

// waitGathered blocks until the peer connection finishes ICE gathering (the
// local description then embeds every candidate).
func waitGathered(t *testing.T, pc *webrtc.PeerConnection) {
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
	case <-time.After(5 * time.Second):
		t.Fatal("ICE gathering did not complete")
	}
}
