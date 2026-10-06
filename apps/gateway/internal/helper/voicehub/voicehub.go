// Package voicehub is the Helper Node's media endpoint for helper-hosted voice
// (P5.10, decision D7): helper.VoiceMedia over voice.MediaHub, the same WebRTC
// endpoint the primary runs for primary-hosted voice, pointed at a relay on
// THIS node's loopback.
//
//	client ──WebRTC(Opus)──► MediaHub (this node, bounded UDP range, NAT 1:1, TURN)
//	                            │  framed Opus, 127.0.0.1
//	                            ▼
//	                  audio relay ──► PulseAudio virtual mic ──► browser ──► provider
//
// Browser, audio environment, relay and WebRTC endpoint are co-located, so audio
// never crosses the primary-helper WireGuard link (156-193 ms RTT would break the
// 100 ms budget). One call has one exclusive audio environment: the service picks
// it, this package dials its relay.
//
// The node holds no global secret. For each call the primary derives a relay key
// bound to session, tenant, attempt, node, lease generation and expiry; this
// package hands it to the call's relay through a key file the relay re-reads on
// every hello (UBAG_VOICE_RELAY_KEY_FILE, no relay restart per call), dials the
// relay with the bound hello, verifies the client's control-channel credential
// with the per-attempt media key, and uses the TURN credentials the primary
// minted for the attempt (voice.ICEConfig.FixedServers) instead of a TURN secret.
// Closing a call removes the key file.
//
// It links the voice package and therefore pion and the voice stores, which is why
// it is NOT part of the default ubag-helper binary: only a build with the
// helpervoice tag wires it (cmd/ubag-helper/voicemedia_on.go).
package voicehub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/ubag/ubag/apps/gateway/internal/helper"
	"github.com/ubag/ubag/apps/gateway/internal/voice"
)

// Config configures a Hub.
type Config struct {
	// Environments are the node's audio environments. New refuses to start when a
	// relay key file's directory is not writable, and removes key files left behind
	// by a previous run: a relay with no key refuses every session.
	Environments []helper.VoiceEnvironment
	// UDPMin and UDPMax bound the UDP ports media binds (the range the fleet
	// manager opens); both zero leaves the OS range (tests only).
	UDPMin, UDPMax uint16
	// NAT1To1IP is advertised as the node's host candidate.
	NAT1To1IP string
	// ServerViaTURN lets the endpoint allocate a TURN relay candidate with the
	// credentials the primary minted for the attempt.
	ServerViaTURN bool
	// IncludeLoopback also gathers loopback candidates: tests and single-host
	// development only.
	IncludeLoopback bool
}

// Hub implements helper.VoiceMedia.
type Hub struct {
	cfg Config

	mu    sync.Mutex
	calls map[string]*call // by helper.VoiceCall.Key
}

var _ helper.VoiceMedia = (*Hub)(nil)

// call is one session's media: its own MediaHub (its dialer and authorizer carry
// the per-attempt keys) and the key file the relay reads.
type call struct {
	hub     *voice.MediaHub
	session voice.Session
	keyFile string
	key     []byte // the relay key written to keyFile: only that key is ever removed
	closed  bool   // Close ran: an Open still in flight must undo itself
}

// New validates cfg and prepares the key directories.
func New(cfg Config) (*Hub, error) {
	for _, e := range cfg.Environments {
		if e.RelayKeyFile == "" {
			return nil, fmt.Errorf("voicehub: environment %q has no relay key file", e.ID)
		}
		_ = os.Remove(e.RelayKeyFile) // a previous run's key must not outlive it
		probe := e.RelayKeyFile + ".probe"
		if err := os.WriteFile(probe, nil, 0o600); err != nil {
			return nil, fmt.Errorf("voicehub: the relay key directory of %q is not writable: %w", e.ID, err)
		}
		_ = os.Remove(probe)
	}
	return &Hub{cfg: cfg, calls: map[string]*call{}}, nil
}

// Active reports how many calls hold media (tests and diagnostics).
func (h *Hub) Active() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

// Open implements helper.VoiceMedia.
func (h *Hub) Open(ctx context.Context, vc helper.VoiceCall, sdpOffer string, sink helper.VoiceSink) (string, error) {
	spec := voice.HelperVoiceSpec{
		Binding: voice.AttemptBinding{
			SessionID: vc.SessionID, TenantID: vc.TenantID, AttemptID: vc.AttemptID, NodeID: vc.NodeID,
			LeaseGeneration: vc.Generation, Expires: vc.Expires,
		},
		RelayKey: string(vc.Credentials.RelayKey), MediaKey: string(vc.Credentials.MediaKey),
	}
	if err := spec.Binding.Validate(); err != nil {
		return "", err
	}
	for _, s := range vc.Credentials.ICEServers {
		spec.ICEServers = append(spec.ICEServers, voice.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	addr := vc.Env.RelayAddr
	c := &call{
		keyFile: vc.Env.RelayKeyFile, key: slices.Clone(vc.Credentials.RelayKey),
		session: voice.Session{
			ID: vc.SessionID, TenantID: vc.TenantID, Target: vc.Target, IdentityRef: vc.IdentityRef, InstanceRef: vc.InstanceRef,
			Mode: voice.ModeLive, Status: voice.StatusConnecting,
		},
		hub: &voice.MediaHub{
			Dialer: spec.RelayDialer(func(voice.Session) (string, error) { return addr, nil }),
			ICE: &voice.ICEConfig{
				PortMin: h.cfg.UDPMin, PortMax: h.cfg.UDPMax, NAT1To1IP: h.cfg.NAT1To1IP,
				ServerViaTURN: h.cfg.ServerViaTURN, IncludeLoopback: h.cfg.IncludeLoopback, FixedServers: spec.ICEServers,
			},
			AuthorizeControl: spec.MediaCredentialVerifier(),
			OnConnected:      func(voice.Session) { sink.PeerConnected() },
			OnClosed:         func(_ voice.Session, reason string) { sink.MediaEnded(reason) },
			OnMute:           func(_ voice.Session, muted bool) { sink.ClientMuted(muted) },
		},
	}

	h.mu.Lock()
	if h.calls[vc.Key()] != nil {
		h.mu.Unlock()
		return "", errors.New("voicehub: the call already has media")
	}
	h.calls[vc.Key()] = c // registered first, so a Close during the offer finds it
	h.mu.Unlock()

	if err := writeKeyFile(c.keyFile, vc.Credentials.RelayKey); err != nil {
		h.Close(vc.Key())
		return "", fmt.Errorf("%w: handing the relay its key: %v", helper.ErrVoiceRelayUnavailable, err)
	}
	answer, err := c.hub.HandleOffer(ctx, c.session, sdpOffer)
	if err != nil {
		h.Close(vc.Key())
		if errors.Is(err, voice.ErrRelayUnavailable) || errors.Is(err, voice.ErrRelayBusy) {
			return "", fmt.Errorf("%w: %v", helper.ErrVoiceRelayUnavailable, err)
		}
		return "", err
	}
	h.mu.Lock()
	closed := c.closed
	h.mu.Unlock()
	if closed { // Close ran while HandleOffer was registering the media: undo it
		c.hub.Close()
		return "", errors.New("voicehub: the call was closed during the offer")
	}
	return answer, nil
}

// Reoffer implements helper.VoiceMedia: the MediaHub replaces the old client leg
// (and re-dials the relay, which re-reads the same key file).
func (h *Hub) Reoffer(ctx context.Context, key, sdpOffer string, muted bool) (string, error) {
	h.mu.Lock()
	c := h.calls[key]
	h.mu.Unlock()
	if c == nil {
		return "", errors.New("voicehub: no media for the call")
	}
	session := c.session
	session.Muted = muted
	answer, err := c.hub.HandleOffer(ctx, session, sdpOffer)
	if err != nil && (errors.Is(err, voice.ErrRelayUnavailable) || errors.Is(err, voice.ErrRelayBusy)) {
		return "", fmt.Errorf("%w: %v", helper.ErrVoiceRelayUnavailable, err)
	}
	return answer, err
}

// SetMuted implements helper.VoiceMedia.
func (h *Hub) SetMuted(key string, muted bool) bool {
	h.mu.Lock()
	c := h.calls[key]
	h.mu.Unlock()
	return c != nil && c.hub.SetMuted(c.session.ID, muted)
}

// Close implements helper.VoiceMedia: media stops now and the relay key is gone.
func (h *Hub) Close(key string) {
	h.mu.Lock()
	c := h.calls[key]
	delete(h.calls, key)
	if c != nil {
		c.closed = true
	}
	h.mu.Unlock()
	if c == nil {
		return
	}
	c.hub.Disconnect(c.session.ID)
	c.hub.Close()
	removeKeyFile(c.keyFile, c.key)
}

// CloseAll ends every call (process shutdown).
func (h *Hub) CloseAll() {
	h.mu.Lock()
	keys := make([]string, 0, len(h.calls))
	for k := range h.calls {
		keys = append(keys, k)
	}
	h.mu.Unlock()
	for _, k := range keys {
		h.Close(k)
	}
}

// removeKeyFile deletes the relay key file only while it still holds THIS call's
// key: a successor on the same environment may already have written its own.
func removeKeyFile(path string, key []byte) {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, key) {
		_ = os.Remove(path)
	}
}

// writeKeyFile replaces path with key (mode 0600) atomically, so the relay never
// reads a half-written key. ponytail: 0600 means the relay must run as the same
// user (or share the user namespace); a group-readable mode if that ever differs.
func writeKeyFile(path string, key []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(key)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(name, 0o600)); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
