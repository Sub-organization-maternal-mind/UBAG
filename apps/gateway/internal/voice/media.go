package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// Media plane (gateway side).
//
// The gateway terminates the CLIENT's WebRTC connection and routes raw Opus
// frames to/from the provider browser's audio relay — it never transcodes.
// The browser container owns the audio stack: its relay process decodes
// client Opus into the virtual microphone (what the provider voice UI hears)
// and captures the sink monitor (what the provider says) back as Opus.
//
//	 client app ──WebRTC(Opus)──► gateway MediaHub ──framed Opus──► audio relay ──PCM──► virtual mic ──► provider
//	 client app ◄─WebRTC(Opus)── gateway MediaHub ◄─framed Opus── audio relay ◄─PCM── sink monitor ◄── provider
//
// Signaling is HTTP-only (POST /v1/voice/sessions/{id}/connect): the answer
// is gathered to completion (non-trickle ICE) so no candidate socket is
// needed. The relay connection is framed: 4-byte little-endian length, then
// that many bytes of one Opus packet. Buffers are bounded; on pressure the
// OLDEST frame is dropped and counted, never buffered without limit.

const (
	// relayFrameHeaderBytes is the 4-byte little-endian length prefix.
	relayFrameHeaderBytes = 4
	// maxRelayFrameBytes bounds one Opus frame (real Opus packets top out
	// far below this; the bound rejects relay protocol garbage).
	maxRelayFrameBytes = 64 << 10
	// micBufferDepth bounds in-flight client→provider frames.
	micBufferDepth = 64
	// opusFrameDuration is the frame duration written on the return track.
	opusFrameDuration = 20 * time.Millisecond
	// iceGatherTimeout bounds non-trickle candidate gathering per connect.
	iceGatherTimeout = 3 * time.Second
)

// ErrRelayUnavailable reports that the browser/audio environment's relay is
// not reachable (container down, unmapped address, relay crashed).
var ErrRelayUnavailable = errors.New("voice: audio relay unreachable")

// MediaMetrics is the dropped-frame / lifecycle counter sink the hub reports
// to; wired to the gateway metrics registry at serve time.
type MediaMetrics interface {
	AddFramesDropped(direction string, n int64)
	AddSessionsConnected()
	AddSessionsEnded(reason string)
}

type noopMediaMetrics struct{}

func (noopMediaMetrics) AddFramesDropped(string, int64) {}
func (noopMediaMetrics) AddSessionsConnected()          {}
func (noopMediaMetrics) AddSessionsEnded(string)        {}

// RelayDialer connects to one browser/audio environment's relay process.
type RelayDialer interface {
	Dial(ctx context.Context, instanceRef, sessionID string) (RelayConn, error)
}

// RelayConn is one bidirectional Opus frame stream to a browser/audio
// environment. Send forwards the client's mic frames; Recv yields the
// provider's speaker frames. Close tears the connection down.
type RelayConn interface {
	Send(frame []byte) error
	Recv(ctx context.Context) ([]byte, error)
	Close() error
}

// TCPRelayDialer dials the relay over TCP with the framed protocol. Address
// resolves an instance ref to host:port (injected; multi-instance tests and
// deployments differ here).
type TCPRelayDialer struct {
	Address     func(instanceRef string) (string, error)
	DialTimeout time.Duration
}

func (d *TCPRelayDialer) dialTimeout() time.Duration {
	if d.DialTimeout > 0 {
		return d.DialTimeout
	}
	return 3 * time.Second
}

func (d *TCPRelayDialer) Dial(ctx context.Context, instanceRef, _ string) (RelayConn, error) {
	if d.Address == nil {
		return nil, ErrRelayUnavailable
	}
	addr, err := d.Address(instanceRef)
	if err != nil || addr == "" {
		return nil, ErrRelayUnavailable
	}
	dialer := net.Dialer{Timeout: d.dialTimeout()}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRelayUnavailable, err)
	}
	return &TCPFrameConn{conn: conn}, nil
}

// TCPFrameConn implements RelayConn over a net.Conn with framed reads/writes.
type TCPFrameConn struct {
	conn net.Conn
	mu   sync.Mutex // serializes writes
}

func (c *TCPFrameConn) Send(frame []byte) error {
	if len(frame) == 0 || len(frame) > maxRelayFrameBytes {
		return errors.New("voice: relay frame size out of range")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	header := make([]byte, relayFrameHeaderBytes)
	binary.LittleEndian.PutUint32(header, uint32(len(frame)))
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(frame)
	return err
}

func (c *TCPFrameConn) Recv(ctx context.Context) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetReadDeadline(deadline)
	}
	header := make([]byte, relayFrameHeaderBytes)
	if _, err := io.ReadFull(c.conn, header); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint32(header)
	if length == 0 || length > maxRelayFrameBytes {
		return nil, errors.New("voice: relay protocol violation (frame length)")
	}
	frame := make([]byte, length)
	if _, err := io.ReadFull(c.conn, frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func (c *TCPFrameConn) Close() error { return c.conn.Close() }

// MediaHub implements the gateway's media negotiator: one pion PeerConnection
// per voice session, wired to the browser/audio environment's relay.
type MediaHub struct {
	Dialer     RelayDialer
	ICEServers []webrtc.ICEServer
	Metrics    MediaMetrics

	mu       sync.Mutex
	sessions map[string]*mediaSession
}

func (h *MediaHub) metrics() MediaMetrics {
	if h.Metrics == nil {
		return noopMediaMetrics{}
	}
	return h.Metrics
}

// HandleOffer negotiates one client connection. The SDP answer embeds every
// ICE candidate (gathering completes before return), keeping signaling
// HTTP-only.
func (h *MediaHub) HandleOffer(ctx context.Context, session Session, sdpOffer string) (string, error) {
	relay, err := h.Dialer.Dial(ctx, session.InstanceRef, session.ID)
	if err != nil {
		return "", err
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: h.ICEServers})
	if err != nil {
		_ = relay.Close()
		return "", fmt.Errorf("voice: peer connection: %w", err)
	}
	speakerTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "provider-audio", "ubag-voice",
	)
	if err != nil {
		_ = relay.Close()
		_ = pc.Close()
		return "", fmt.Errorf("voice: speaker track: %w", err)
	}
	if _, err = pc.AddTrack(speakerTrack); err != nil {
		_ = relay.Close()
		_ = pc.Close()
		return "", fmt.Errorf("voice: add speaker track: %w", err)
	}

	hub := h
	ms := &mediaSession{
		id:    session.ID,
		relay: relay,
		pc:    pc,
		mic:   make(chan []byte, micBufferDepth),
		done:  make(chan struct{}),
		track: speakerTrack,
	}
	closeSession := func(reason string) { hub.closeSession(ms, reason) }

	h.mu.Lock()
	if h.sessions == nil {
		h.sessions = map[string]*mediaSession{}
	}
	if _, exists := h.sessions[session.ID]; exists {
		h.mu.Unlock()
		_ = relay.Close()
		_ = pc.Close()
		return "", errors.New("voice: session already has a media connection")
	}
	h.sessions[session.ID] = ms
	h.mu.Unlock()

	// The client's microphone arrives as an Opus track; its RTP payloads are
	// the frames the relay consumes verbatim (Opus-in-RTP payload = packet).
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		defer closeSession("client_track_ended")
		for {
			pkt, _, readErr := track.ReadRTP()
			if readErr != nil {
				return
			}
			select {
			case ms.mic <- pkt.Payload:
			default:
				// Buffer full: drop the OLDEST frame (freshest audio is most
				// valuable for live voice) and count it.
				select {
				case <-ms.mic:
					hub.metrics().AddFramesDropped("mic", 1)
				default:
				}
				select {
				case ms.mic <- pkt.Payload:
				default:
					hub.metrics().AddFramesDropped("mic", 1)
				}
			}
		}
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			hub.metrics().AddSessionsConnected()
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			closeSession("peer_connection_" + state.String())
		}
	})

	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdpOffer}); err != nil {
		closeSession("bad_offer")
		return "", fmt.Errorf("voice: remote description: %w", err)
	}
	gatherDone := make(chan struct{})
	pc.OnICEGatheringStateChange(func(state webrtc.ICEGatheringState) {
		if state == webrtc.ICEGatheringStateComplete {
			select {
			case <-gatherDone:
			default:
				close(gatherDone)
			}
		}
	})
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		closeSession("answer_error")
		return "", fmt.Errorf("voice: create answer: %w", err)
	}
	if err = pc.SetLocalDescription(answer); err != nil {
		closeSession("answer_error")
		return "", fmt.Errorf("voice: local description: %w", err)
	}
	select {
	case <-gatherDone:
	case <-time.After(iceGatherTimeout):
	case <-ctx.Done():
		closeSession("offer_cancelled")
		return "", ctx.Err()
	}

	go ms.pumpMicToRelay(hub)
	go ms.pumpRelayToSpeaker(hub)
	return pc.LocalDescription().SDP, nil
}

// Disconnect tears down the media path for a session (voice handlers call
// this on explicit termination; cleanup never waits for ICE to notice).
func (h *MediaHub) Disconnect(sessionID string) {
	h.mu.Lock()
	ms, ok := h.sessions[sessionID]
	if ok {
		delete(h.sessions, sessionID)
	}
	h.mu.Unlock()
	if ok {
		h.closeSession(ms, "session_terminated")
	}
}

func (h *MediaHub) closeSession(ms *mediaSession, reason string) {
	h.mu.Lock()
	_, live := h.sessions[ms.id]
	delete(h.sessions, ms.id)
	h.mu.Unlock()
	ms.closeOnce.Do(func() {
		close(ms.done)
		_ = ms.relay.Close()
		_ = ms.pc.Close()
		h.metrics().AddSessionsEnded(reason)
	})
	_ = live
}

type mediaSession struct {
	id        string
	relay     RelayConn
	pc        *webrtc.PeerConnection
	track     *webrtc.TrackLocalStaticSample
	mic       chan []byte
	done      chan struct{}
	closeOnce sync.Once
	dropped   atomic.Int64
}

func (ms *mediaSession) pumpMicToRelay(h *MediaHub) {
	for {
		select {
		case <-ms.done:
			return
		case frame := <-ms.mic:
			if err := ms.relay.Send(frame); err != nil {
				h.closeSession(ms, "relay_send_failed")
				return
			}
		}
	}
}

func (ms *mediaSession) pumpRelayToSpeaker(h *MediaHub) {
	readCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-ms.done:
			cancel()
		case <-readCtx.Done():
		}
	}()
	for {
		select {
		case <-ms.done:
			return
		default:
		}
		frame, err := ms.relay.Recv(readCtx)
		if err != nil {
			select {
			case <-ms.done:
				return
			default:
				h.closeSession(ms, "relay_recv_failed")
				return
			}
		}
		if err := ms.track.WriteSample(media.Sample{Data: frame, Duration: opusFrameDuration}); err != nil {
			select {
			case <-ms.done:
				return
			default:
				h.closeSession(ms, "track_write_failed")
				return
			}
		}
	}
}
