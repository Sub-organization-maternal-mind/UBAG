package voice

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
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
// needed. A WebRTC data channel labelled "control" carries mute and status
// events alongside the audio.
//
// Relay protocol v2: each frame is a 4-byte little-endian length N followed
// by N bytes — one type byte, then the payload. Type 0x01 is one Opus packet;
// type 0x02 is a UTF-8 JSON control message. The first frame on a connection
// is always the gateway's control "hello" carrying a short-lived HMAC token
// (see RelayToken) that the relay verifies before touching any audio device;
// the relay answers control "ready" (or "error" and closes). Buffers are
// bounded; on pressure the OLDEST frame is dropped and counted, never
// buffered without limit.

const (
	// relayFrameHeaderBytes is the 4-byte little-endian length prefix.
	relayFrameHeaderBytes = 4
	// maxRelayFrameBytes bounds one frame (real Opus packets top out far
	// below this; the bound rejects relay protocol garbage).
	maxRelayFrameBytes = 64 << 10
	relayTypeAudio     = byte(0x01)
	relayTypeControl   = byte(0x02)
	// relayHelloTTL bounds how long a hello token is valid.
	relayHelloTTL = time.Minute
	// relayReadyTimeout bounds the wait for the relay's "ready" answer.
	relayReadyTimeout = 10 * time.Second
	// relayBusyRetries / relayBusyBackoff cover the short window in which a
	// replaced connection's relay slot is still being released.
	relayBusyRetries = 5
	relayBusyBackoff = 200 * time.Millisecond
	// micBufferDepth bounds in-flight client→provider frames.
	micBufferDepth = 64
	// opusFrameDuration is the frame duration written on the return track.
	opusFrameDuration = 20 * time.Millisecond
	// iceGatherTimeout bounds non-trickle candidate gathering per connect.
	iceGatherTimeout = 3 * time.Second
	// controlChannelLabel names the client's control data channel.
	controlChannelLabel = "control"
	// maxControlMessageBytes bounds one data-channel control message.
	maxControlMessageBytes = 1 << 10
)

// ErrRelayUnavailable reports that the browser/audio environment's relay is
// not reachable (container down, unmapped address, relay crashed).
var ErrRelayUnavailable = errors.New("voice: audio relay unreachable")

// ErrRelayBusy reports that the relay already serves another session: one
// audio environment hosts exactly one session at a time.
var ErrRelayBusy = errors.New("voice: audio relay busy")

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

// RelayToken is the hello credential for one session on one relay:
// hex(HMAC-SHA256(secret, "voice-relay|<session>|<exp unix>")).
func RelayToken(secret []byte, sessionID string, exp int64) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "voice-relay|%s|%d", sessionID, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// maxBoundGeneration keeps a lease generation exactly representable as a JSON
// number (and as a Python int on the relay side).
const maxBoundGeneration = 1<<53 - 1

// RelayTokenBound is the hello credential of a helper-hosted session: node and
// lease generation are part of the signed message, so the token verifies only
// for that node and generation:
// hex(HMAC-SHA256(key, "voice-relay|<session>|<exp unix>|<node>|<generation>")).
// The key is the primary-derived per-attempt relay key (DeriveAttemptKey),
// never the global relay secret.
func RelayTokenBound(key []byte, sessionID string, exp int64, nodeID string, generation uint64) string {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "voice-relay|%s|%d|%s|%d", sessionID, exp, nodeID, generation)
	return hex.EncodeToString(mac.Sum(nil))
}

// RelayDialer connects to one browser/audio environment's relay process.
type RelayDialer interface {
	Dial(ctx context.Context, session Session) (RelayConn, error)
}

// RelayConn is one bidirectional Opus frame stream to a browser/audio
// environment. Send forwards the client's mic frames; Recv yields the
// provider's speaker frames; Control sends a control message (mute). Close
// tears the connection down.
type RelayConn interface {
	Send(frame []byte) error
	Control(msg map[string]any) error
	Recv(ctx context.Context) ([]byte, error)
	Close() error
}

// TCPRelayDialer dials the relay over TCP with the framed protocol. Address
// resolves a session's browser environment (its tenant + instance) to the
// relay's host:port (injected; deployments differ here). Secret authenticates the session to the relay;
// an empty secret fails closed.
type TCPRelayDialer struct {
	Address func(session Session) (string, error)
	Secret  []byte
	// NodeID and Generation, when both set, send a BOUND hello (helper-hosted
	// voice): Secret is then the per-attempt relay key and the token signs the
	// node and lease generation. Unset keeps the primary-hosted hello.
	NodeID       string
	Generation   uint64
	DialTimeout  time.Duration
	ReadyTimeout time.Duration
}

func (d *TCPRelayDialer) dialTimeout() time.Duration {
	if d.DialTimeout > 0 {
		return d.DialTimeout
	}
	return 3 * time.Second
}

func (d *TCPRelayDialer) readyTimeout() time.Duration {
	if d.ReadyTimeout > 0 {
		return d.ReadyTimeout
	}
	return relayReadyTimeout
}

func (d *TCPRelayDialer) Dial(ctx context.Context, session Session) (RelayConn, error) {
	// A bound hello needs both node and generation, within the wire bounds.
	if d.Address == nil || len(d.Secret) == 0 || (d.NodeID != "") != (d.Generation > 0) ||
		d.Generation > maxBoundGeneration || len(d.NodeID) > 128 || strings.Contains(d.NodeID, "|") {
		return nil, ErrRelayUnavailable
	}
	addr, err := d.Address(session)
	if err != nil || addr == "" {
		return nil, ErrRelayUnavailable
	}
	dialer := net.Dialer{Timeout: d.dialTimeout()}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRelayUnavailable, err)
	}
	fc := &TCPFrameConn{conn: conn}
	exp := time.Now().Add(relayHelloTTL).Unix()
	hello := map[string]any{"op": "hello", "session_id": session.ID, "exp": exp}
	if d.NodeID != "" {
		hello["node_id"], hello["generation"] = d.NodeID, d.Generation
		hello["token"] = RelayTokenBound(d.Secret, session.ID, exp, d.NodeID, d.Generation)
	} else {
		hello["token"] = RelayToken(d.Secret, session.ID, exp)
	}
	if err := fc.Control(hello); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrRelayUnavailable, err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(d.readyTimeout()))
	for {
		kind, payload, err := fc.readFrame()
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: %v", ErrRelayUnavailable, err)
		}
		if kind != relayTypeControl {
			continue
		}
		var msg struct{ Op, Reason string }
		_ = json.Unmarshal(payload, &msg)
		switch msg.Op {
		case "ready":
			_ = conn.SetReadDeadline(time.Time{})
			return fc, nil
		case "error":
			_ = conn.Close()
			if msg.Reason == "busy" {
				return nil, ErrRelayBusy
			}
			return nil, fmt.Errorf("%w: relay refused session (%s)", ErrRelayUnavailable, msg.Reason)
		}
	}
}

// TCPFrameConn implements RelayConn over a net.Conn with framed reads/writes.
type TCPFrameConn struct {
	conn net.Conn
	mu   sync.Mutex // serializes writes
}

func (c *TCPFrameConn) writeFrame(kind byte, payload []byte) error {
	if len(payload) == 0 || len(payload)+1 > maxRelayFrameBytes {
		return errors.New("voice: relay frame size out of range")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := make([]byte, relayFrameHeaderBytes+1+len(payload))
	binary.LittleEndian.PutUint32(buf, uint32(1+len(payload)))
	buf[relayFrameHeaderBytes] = kind
	copy(buf[relayFrameHeaderBytes+1:], payload)
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.conn.Write(buf)
	return err
}

func (c *TCPFrameConn) readFrame() (byte, []byte, error) {
	header := make([]byte, relayFrameHeaderBytes)
	if _, err := io.ReadFull(c.conn, header); err != nil {
		return 0, nil, err
	}
	length := binary.LittleEndian.Uint32(header)
	// Protocol v2 (conformance/fixtures/voice-relay/v2.json): length counts the
	// type byte, so 1 (empty payload) is a valid frame; 0 and > max are not.
	if length < 1 || length > maxRelayFrameBytes {
		return 0, nil, errors.New("voice: relay protocol violation (frame length)")
	}
	frame := make([]byte, length)
	if _, err := io.ReadFull(c.conn, frame); err != nil {
		return 0, nil, err
	}
	if frame[0] != relayTypeAudio && frame[0] != relayTypeControl {
		return 0, nil, errors.New("voice: relay protocol violation (frame type)")
	}
	return frame[0], frame[1:], nil
}

func (c *TCPFrameConn) Send(frame []byte) error { return c.writeFrame(relayTypeAudio, frame) }

func (c *TCPFrameConn) Control(msg map[string]any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.writeFrame(relayTypeControl, raw)
}

func (c *TCPFrameConn) Recv(ctx context.Context) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetReadDeadline(deadline)
	}
	for {
		kind, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch kind {
		case relayTypeAudio:
			if len(payload) == 0 {
				continue // empty audio packet: dropped, never a protocol error
			}
			return payload, nil
		case relayTypeControl:
			var msg struct{ Op, Reason string }
			if json.Unmarshal(payload, &msg) == nil && msg.Op == "error" {
				return nil, fmt.Errorf("voice: relay error: %s", msg.Reason)
			}
		default:
			return nil, errors.New("voice: relay protocol violation (frame type)")
		}
	}
}

func (c *TCPFrameConn) Close() error { return c.conn.Close() }

// MediaHub implements the gateway's media negotiator: one pion PeerConnection
// per voice session, wired to the browser/audio environment's relay.
//
// Lifecycle callbacks are optional and run on their own goroutine:
//   - OnConnected: the client's peer connection reached the connected state;
//   - OnClosed: the media path ended on its own (peer failure, relay loss,
//     client track end) — NOT for explicit Disconnect or reconnect
//     replacement, whose callers already own the session's fate;
//   - OnMute: the client toggled mute over the control data channel.
type MediaHub struct {
	Dialer  RelayDialer
	ICE     *ICEConfig // NAT traversal (nil = host candidates on the OS port range)
	Metrics MediaMetrics

	OnConnected func(Session)
	// AuthorizeControl verifies the credential a client presents on the
	// control data channel for its session. Nil fails closed: the channel then
	// ignores every command.
	AuthorizeControl func(s Session, credential string) bool
	// OnEnded fires once for EVERY end of a media path (explicit disconnect,
	// peer or relay failure, shutdown) except reconnect replacement, which
	// keeps the session's provider voice alive.
	OnEnded  func(s Session, reason string)
	OnClosed func(s Session, reason string)
	OnMute   func(s Session, muted bool)

	mu       sync.Mutex
	sessions map[string]*mediaSession
}

func (h *MediaHub) metrics() MediaMetrics {
	if h.Metrics == nil {
		return noopMediaMetrics{}
	}
	return h.Metrics
}

func (h *MediaHub) fire(fn func()) {
	if fn != nil {
		go fn()
	}
}

// dial opens the relay connection; when replacing a previous connection it
// tolerates the brief window in which the relay is still releasing its slot.
func (h *MediaHub) dial(ctx context.Context, session Session, replacing bool) (RelayConn, error) {
	attempts := 1
	if replacing {
		attempts = relayBusyRetries
	}
	var err error
	for i := 0; i < attempts; i++ {
		var relay RelayConn
		relay, err = h.Dialer.Dial(ctx, session)
		if err == nil || !errors.Is(err, ErrRelayBusy) {
			return relay, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(relayBusyBackoff):
		}
	}
	return nil, err
}

// HandleOffer negotiates one client connection. The SDP answer embeds every
// ICE candidate (gathering completes before return), keeping signaling
// HTTP-only. A second offer for a session that already has media REPLACES the
// old connection (reconnect/ICE restart): the old one is torn down first so
// the single-session relay slot is free, and its stale callbacks can no
// longer touch the replacement.
func (h *MediaHub) HandleOffer(ctx context.Context, session Session, sdpOffer string) (string, error) {
	h.mu.Lock()
	old := h.sessions[session.ID]
	delete(h.sessions, session.ID)
	h.mu.Unlock()
	if old != nil {
		h.closeSession(old, "reconnect_replaced", false)
	}

	relay, err := h.dial(ctx, session, old != nil)
	if err != nil {
		return "", err
	}
	api, err := h.ICE.newAPI()
	if err != nil {
		_ = relay.Close()
		return "", err
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: h.ICE.serverICEServers(session.ID, time.Now())})
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
		id:      session.ID,
		session: session,
		relay:   relay,
		pc:      pc,
		mic:     make(chan []byte, micBufferDepth),
		done:    make(chan struct{}),
		track:   speakerTrack,
	}
	ms.muted.Store(session.Muted)
	// Callbacks capture THIS ms: a stale callback from a replaced connection
	// closes only its own generation, never the replacement.
	closeSession := func(reason string) { hub.closeSession(ms, reason, true) }

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

	if session.Muted {
		_ = relay.Control(map[string]any{"op": "mute", "muted": true})
	}

	// The client's microphone arrives as an Opus track; its RTP payloads are
	// the frames the relay consumes verbatim (Opus-in-RTP payload = packet).
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		defer closeSession("client_track_ended")
		for {
			pkt, _, readErr := track.ReadRTP()
			if readErr != nil {
				return
			}
			if ms.muted.Load() {
				// Muted audio never reaches the provider's microphone.
				hub.metrics().AddFramesDropped("mic_muted", 1)
				continue
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
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != controlChannelLabel {
			return
		}
		ms.setControl(dc)
		dc.OnOpen(func() {
			ms.sendEvent(map[string]any{"event": "status", "state": "connected", "muted": ms.muted.Load()})
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if len(msg.Data) > maxControlMessageBytes {
				return
			}
			var cmd struct {
				Op         string `json:"op"`
				Muted      *bool  `json:"muted"`
				Credential string `json:"credential"`
			}
			if json.Unmarshal(msg.Data, &cmd) != nil {
				return
			}
			if cmd.Op == "auth" {
				ok := hub.AuthorizeControl != nil && hub.AuthorizeControl(ms.session, cmd.Credential)
				ms.controlAuthed.Store(ok)
				ms.sendEvent(map[string]any{"event": "auth", "ok": ok})
				return
			}
			if !ms.controlAuthed.Load() {
				ms.sendEvent(map[string]any{"event": "error", "reason": "unauthorized"})
				return
			}
			switch cmd.Op {
			case "mute":
				if cmd.Muted != nil {
					hub.applyMute(ms, *cmd.Muted)
					hub.fire(func() {
						if hub.OnMute != nil {
							hub.OnMute(ms.session, *cmd.Muted)
						}
					})
				}
			case "ping":
				ms.sendEvent(map[string]any{"event": "pong"})
			}
		})
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			hub.metrics().AddSessionsConnected()
			if hub.OnConnected != nil {
				session := ms.session
				hub.fire(func() { hub.OnConnected(session) })
			}
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			closeSession("peer_connection_" + state.String())
		}
	})

	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdpOffer}); err != nil {
		hub.closeSession(ms, "bad_offer", false)
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
		hub.closeSession(ms, "answer_error", false)
		return "", fmt.Errorf("voice: create answer: %w", err)
	}
	if err = pc.SetLocalDescription(answer); err != nil {
		hub.closeSession(ms, "answer_error", false)
		return "", fmt.Errorf("voice: local description: %w", err)
	}
	select {
	case <-gatherDone:
	case <-time.After(iceGatherTimeout):
	case <-ctx.Done():
		hub.closeSession(ms, "offer_cancelled", false)
		return "", ctx.Err()
	}

	go ms.pumpMicToRelay(hub)
	go ms.pumpRelayToSpeaker(hub)
	return pc.LocalDescription().SDP, nil
}

// Disconnect tears down the media path for a session (voice handlers call
// this on explicit termination; cleanup never waits for ICE to notice). The
// caller owns the session record, so OnClosed does not fire.
func (h *MediaHub) Disconnect(sessionID string) {
	h.mu.Lock()
	ms, ok := h.sessions[sessionID]
	h.mu.Unlock()
	if ok {
		h.closeSession(ms, "session_terminated", false)
	}
}

// SetMuted applies the mute flag to a live media path: muted microphone
// frames are dropped at the gateway and the relay is told as well.
func (h *MediaHub) SetMuted(sessionID string, muted bool) bool {
	h.mu.Lock()
	ms, ok := h.sessions[sessionID]
	h.mu.Unlock()
	if ok {
		h.applyMute(ms, muted)
	}
	return ok
}

func (h *MediaHub) applyMute(ms *mediaSession, muted bool) {
	ms.muted.Store(muted)
	_ = ms.relay.Control(map[string]any{"op": "mute", "muted": muted})
	ms.sendEvent(map[string]any{"event": "mute", "muted": muted})
}

// ClientICEServers lists the ICE servers (STUN, and TURN with freshly minted
// time-limited credentials) a client should use for the session.
func (h *MediaHub) ClientICEServers(session Session) []ICEServer {
	return h.ICE.ClientICEServers(session, time.Now())
}

// Sessions lists the sessions that currently hold media on THIS replica; the
// reconciler compares them with the shared store so media never outlives its
// lease (explicit terminate or sweep on another replica, owner loss).
func (h *MediaHub) Sessions() []Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Session, 0, len(h.sessions))
	for _, ms := range h.sessions {
		out = append(out, ms.session)
	}
	return out
}

// ActiveSessions reports how many sessions currently hold media.
func (h *MediaHub) ActiveSessions() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}

// Close tears down every media session (gateway shutdown).
func (h *MediaHub) Close() {
	h.mu.Lock()
	all := make([]*mediaSession, 0, len(h.sessions))
	for _, ms := range h.sessions {
		all = append(all, ms)
	}
	h.mu.Unlock()
	for _, ms := range all {
		h.closeSession(ms, "gateway_shutdown", false)
	}
}

// closeSession ends ONE media generation. It only unmaps the session when the
// mapped entry is this very generation, and it is idempotent.
func (h *MediaHub) closeSession(ms *mediaSession, reason string, notify bool) {
	h.mu.Lock()
	if h.sessions[ms.id] == ms {
		delete(h.sessions, ms.id)
	}
	h.mu.Unlock()
	ms.closeOnce.Do(func() {
		close(ms.done)
		_ = ms.relay.Close()
		_ = ms.pc.Close()
		h.metrics().AddSessionsEnded(reason)
		if reason != "reconnect_replaced" && h.OnEnded != nil {
			session := ms.session
			h.fire(func() { h.OnEnded(session, reason) })
		}
		if notify && h.OnClosed != nil {
			session := ms.session
			h.fire(func() { h.OnClosed(session, reason) })
		}
	})
}

type mediaSession struct {
	id        string
	session   Session
	relay     RelayConn
	pc        *webrtc.PeerConnection
	track     *webrtc.TrackLocalStaticSample
	mic       chan []byte
	done      chan struct{}
	closeOnce sync.Once
	muted     atomic.Bool
	// controlAuthed is set once the client proves its scoped media credential
	// on the control data channel.
	controlAuthed atomic.Bool

	ctrlMu  sync.Mutex
	control *webrtc.DataChannel
}

func (ms *mediaSession) setControl(dc *webrtc.DataChannel) {
	ms.ctrlMu.Lock()
	ms.control = dc
	ms.ctrlMu.Unlock()
}

// sendEvent writes a JSON event on the control data channel when it is open.
func (ms *mediaSession) sendEvent(event map[string]any) {
	ms.ctrlMu.Lock()
	dc := ms.control
	ms.ctrlMu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	if raw, err := json.Marshal(event); err == nil {
		_ = dc.SendText(string(raw))
	}
}

func (ms *mediaSession) pumpMicToRelay(h *MediaHub) {
	for {
		select {
		case <-ms.done:
			return
		case frame := <-ms.mic:
			if ms.muted.Load() {
				h.metrics().AddFramesDropped("mic_muted", 1)
				continue
			}
			if err := ms.relay.Send(frame); err != nil {
				h.closeSession(ms, "relay_send_failed", true)
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
				h.closeSession(ms, "relay_recv_failed", true)
				return
			}
		}
		if err := ms.track.WriteSample(media.Sample{Data: frame, Duration: opusFrameDuration}); err != nil {
			select {
			case <-ms.done:
				return
			default:
				h.closeSession(ms, "track_write_failed", true)
				return
			}
		}
	}
}
