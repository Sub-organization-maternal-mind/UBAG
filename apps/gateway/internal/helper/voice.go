package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// Helper-hosted voice (P5.10, decision D7): ubag.helper.v1.HelperVoiceService.
//
// The browser, the audio environment, the relay and the WebRTC endpoint are all
// on THIS node, so audio never crosses the WireGuard link (a relay hop over the
// 156-193 ms primary-helper RTT would break the 100 ms budget). The primary keeps
// public signaling: it forwards the client's SDP offer to OfferVoice and returns
// the answer. This file is the service core: it owns what must hold whatever
// terminates the media (fencing, the exclusive environment, the dead-man, the
// ordering of teardown and the event log). The media itself sits behind
// VoiceMedia (internal/helper/voicehub: voice.MediaHub on a loopback relay) and
// the provider voice UI is driven through the same Runner as every other
// attempt, against this node's own loopback CDP endpoint.
//
// Trust: the node holds no global secret. Everything call-specific arrives in
// VoiceCredentials, derived by the primary per attempt and bound to the whole
// VoiceFence; the node keeps it only for the life of the call.

const (
	// DefaultVoiceActivateTimeout / DefaultVoiceDeactivateTimeout are the bounds the
	// primary applies to the same provider voice jobs (httpapi voiceControlTimeout and
	// voiceDeactivateTimeout; the latter is also voice.TerminatingHoldMax, so the
	// primary's terminating hold outlives a normal deactivation). Duplicated because
	// the node cannot import the primary-side packages.
	DefaultVoiceActivateTimeout   = 75 * time.Second
	DefaultVoiceDeactivateTimeout = 45 * time.Second

	maxVoiceOfferBytes = 64 << 10 // one SDP offer
	maxVoiceEvents     = 256      // per call; one slot is always kept for ENDED
	maxVoiceICEServers = 8
	maxVoiceICEURLs    = 4
	maxVoiceICEField   = 256
	minVoiceKeyBytes   = 16 // a derived key is 64 hex characters
	maxVoiceKeyBytes   = 256
	// maxVoiceGeneration keeps a lease generation exactly representable as a JSON
	// number on the relay (voice.maxBoundGeneration).
	maxVoiceGeneration = 1<<53 - 1
)

var (
	voiceTenantRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{0,127}$`)
	iceURLRe      = regexp.MustCompile(`^(stun|stuns|turn|turns):\S{1,250}$`)
	endTokenRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

	// Why a call was ended. The first cause wins; each maps to a stable end_reason.
	errVoiceTerminated  = errors.New("voice call terminated by the primary")
	errVoiceActivation  = errors.New("provider voice activation failed")
	errVoiceOfferFailed = errors.New("voice offer failed")

	errVoiceCallEnded = status.Error(codes.FailedPrecondition, "the voice call has ended")

	// ErrVoiceRelayUnavailable is what a VoiceMedia returns when the call's audio
	// relay cannot be reached or refuses the call (down, busy, not ready). The
	// service answers Unavailable; the primary may retry.
	ErrVoiceRelayUnavailable = errors.New("voice: audio relay unavailable")
)

// voiceMediaEnded is the cause of a call whose media path ended on its own.
type voiceMediaEnded struct{ reason string }

func (e *voiceMediaEnded) Error() string { return "voice media ended: " + e.reason }

// VoiceEnvironment is one exclusive browser and audio environment of this node:
// a browser with its own PulseAudio devices and audio relay, reachable only on
// loopback (there is no host field, so a WAN endpoint cannot be configured). It
// hosts at most one call at a time: the virtual microphone and the speaker
// monitor are environment-wide devices.
type VoiceEnvironment struct {
	// ID is the instance_ref the primary addresses this environment by.
	ID string
	// CDPEndpoint is where the local worker attaches to run voice.activate and
	// voice.deactivate (http://127.0.0.1:<port>).
	CDPEndpoint string
	// RelayAddr is where the media endpoint dials the audio relay (127.0.0.1:<port>).
	RelayAddr string
	// RelayKeyFile is where the per-call relay key is handed to that relay (its
	// UBAG_VOICE_RELAY_KEY_FILE). The media endpoint owns writing and removing it.
	RelayKeyFile string
}

// VoiceICEServer is one STUN or TURN entry. A TURN entry carries a username and
// credential the PRIMARY minted for this attempt; they expire with it.
type VoiceICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

// VoiceCredentials are the per-attempt secrets of one call. They live only in
// this process, only for the life of the call, and are never logged or printed.
type VoiceCredentials struct {
	// RelayKey authenticates the relay hello (bound to node and lease generation).
	RelayKey []byte
	// MediaKey verifies the client's control-channel credential.
	MediaKey   []byte
	ICEServers []VoiceICEServer
}

func (VoiceCredentials) String() string       { return "VoiceCredentials{redacted}" }
func (VoiceCredentials) GoString() string     { return "VoiceCredentials{redacted}" }
func (VoiceCredentials) LogValue() slog.Value { return slog.StringValue("redacted") }

// VoiceCall is what the media endpoint needs to host one call. Credentials is
// set only for VoiceMedia.Open.
type VoiceCall struct {
	SessionID, TenantID, AttemptID, NodeID string
	Generation                             uint64
	// Expires is the fence's expiry: the end of the attempt's credentials.
	Expires                          time.Time
	Target, IdentityRef, InstanceRef string
	Env                              VoiceEnvironment
	Credentials                      VoiceCredentials
}

// Key identifies the call to its media endpoint. It names the attempt AND its
// lease generation, not the session: a newer generation of the same session is a
// different call, so a late Close of the older one can never take its successor's
// media down.
func (c VoiceCall) Key() string { return VoiceCallKey(c.AttemptID, c.Generation) }

// VoiceCallKey is VoiceCall.Key for an attempt and generation.
func VoiceCallKey(attemptID string, generation uint64) string {
	return fmt.Sprintf("%s#%d", attemptID, generation)
}

// VoiceSink is how a media endpoint reports back about one call. Every func may
// be called from any goroutine, at any time, including after Close (ignored).
type VoiceSink struct {
	// PeerConnected: the client's peer connection reached the connected state.
	PeerConnected func()
	// MediaEnded: the media path ended on its own (peer failure, relay loss,
	// client track end). NOT called for Close or a reconnect replacement.
	MediaEnded func(reason string)
	// ClientMuted: the client toggled mute over its control data channel.
	ClientMuted func(muted bool)
}

// VoiceMedia terminates one call's WebRTC media on this node. The service never
// touches pion: internal/helper/voicehub implements it over voice.MediaHub.
type VoiceMedia interface {
	// Open starts the call's media: it hands call.Credentials.RelayKey to the call's
	// relay, dials it with the bound hello, applies the client's SDP offer and
	// returns the answer. A failure leaves nothing behind.
	Open(ctx context.Context, call VoiceCall, sdpOffer string, sink VoiceSink) (answer string, err error)
	// Reoffer replaces the client leg of the call (VoiceCall.Key) with a new SDP
	// offer (ICE restart, network change); the relay and the provider voice stay.
	// muted is the call's mute state to carry over.
	Reoffer(ctx context.Context, key, sdpOffer string, muted bool) (answer string, err error)
	// SetMuted applies mute to live media and reports whether there was any.
	SetMuted(key string, muted bool) bool
	// Close ends the call's media at once and removes everything the call put on
	// disk (the relay key). Idempotent; an unknown key is a no-op.
	Close(key string)
}

// VoiceConfig enables HelperVoiceService on a Server. A nil Config.Voice means
// the service is not registered at all (UBAG_HELPER_VOICE off, the default).
type VoiceConfig struct {
	Media        VoiceMedia
	Environments []VoiceEnvironment
	// ActivateTimeout and DeactivateTimeout bound the provider voice jobs
	// (defaults DefaultVoiceActivateTimeout / DefaultVoiceDeactivateTimeout).
	ActivateTimeout   time.Duration
	DeactivateTimeout time.Duration
}

func (v *VoiceConfig) defaults() error {
	if v.Media == nil {
		return errors.New("helper: voice needs a media endpoint")
	}
	if len(v.Environments) == 0 || len(v.Environments) > maxAttemptsCeiling {
		return fmt.Errorf("helper: voice needs 1..%d audio environments", maxAttemptsCeiling)
	}
	seen := map[string]bool{}
	for _, e := range v.Environments {
		if !idTokenRe.MatchString(e.ID) || e.CDPEndpoint == "" || e.RelayAddr == "" || e.RelayKeyFile == "" || seen[e.ID] {
			return errors.New("helper: every voice environment needs a unique id, a CDP endpoint, a relay address and a relay key file")
		}
		seen[e.ID] = true
	}
	if v.ActivateTimeout <= 0 {
		v.ActivateTimeout = DefaultVoiceActivateTimeout
	}
	if v.DeactivateTimeout <= 0 {
		v.DeactivateTimeout = DefaultVoiceDeactivateTimeout
	}
	return nil
}

func (v *VoiceConfig) env(id string) (VoiceEnvironment, bool) {
	i := slices.IndexFunc(v.Environments, func(e VoiceEnvironment) bool { return e.ID == id })
	if i < 0 {
		return VoiceEnvironment{}, false
	}
	return v.Environments[i], true
}

// VoiceService returns the HelperVoiceService implementation, or nil when voice
// is not configured (the gRPC server then answers Unimplemented for it).
func (s *Server) VoiceService() helperv1.HelperVoiceServiceServer {
	if s.cfg.Voice == nil {
		return nil
	}
	return &voiceService{s: s}
}

// RegisterVoice registers the service on srv when voice is configured. Call it
// before srv.Serve.
func (s *Server) RegisterVoice(srv grpc.ServiceRegistrar) {
	if v := s.VoiceService(); v != nil {
		helperv1.RegisterHelperVoiceServiceServer(srv, v)
	}
}

type voiceService struct {
	helperv1.UnimplementedHelperVoiceServiceServer
	s *Server
}

// voiceCall is one leased voice attempt. Its life is tied to its lease, not to
// any RPC: events go to a bounded log that streams read from.
type voiceCall struct {
	s    *Server
	info VoiceCall // never holds Credentials: the media endpoint keeps them
	key  identityKey

	ctx    context.Context
	cancel context.CancelCauseFunc

	offerMu sync.Mutex // serialises Open / Reoffer

	mu              sync.Mutex
	state           helperv1.VoiceState
	muted           bool
	leaseExpiry     time.Time
	leaseTimer      Timer
	cause           error
	endData         map[string]any
	providerReady   bool
	peerUp          bool
	connected       bool
	activateStarted bool
	activateDone    chan struct{}
	events          []*helperv1.VoiceEvent
	changed         chan struct{}
	ended           bool // the ENDED event is written and everything is released
	endedAt         time.Time
	discard         bool // the first offer failed: forget the call once released
	done            chan struct{}
}

func (c *voiceCall) now() time.Time { return c.s.clock.Now() }

func (c *voiceCall) mediaKey() string { return c.info.Key() }

// live reports whether the call can still be acted on (nobody ended it yet).
func (c *voiceCall) live() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cause == nil
}

func (c *voiceCall) armLease() {
	d := max(c.leaseExpiry.Sub(c.now()), 0)
	if c.leaseTimer == nil {
		c.leaseTimer = c.s.clock.AfterFunc(d, c.onLeaseTimer)
		return
	}
	c.leaseTimer.Reset(d)
}

// onLeaseTimer is the dead-man: a call that is not renewed is ended by the node
// itself, with no help from the primary (which may be unreachable, which is
// exactly when this matters). The media stops first; teardown follows.
func (c *voiceCall) onLeaseTimer() {
	c.mu.Lock()
	if c.cause != nil {
		c.mu.Unlock()
		return
	}
	if c.now().Before(c.leaseExpiry) { // renewed after this timer was armed
		c.armLease()
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	c.end(errLeaseExpired)
}

// extendLease moves the lease forward (never back), clamped to now+LeaseMaxTTL.
// It reports false when the call can no longer be renewed: its lease already
// lapsed (a late renew must not resurrect it) or it is being ended.
func (c *voiceCall) extendLease(want time.Time) (expiry time.Time, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return c.leaseExpiry, true
	}
	now := c.now()
	if c.cause != nil || !now.Before(c.leaseExpiry) {
		return c.leaseExpiry, false
	}
	if limit := now.Add(c.s.cfg.LeaseMaxTTL); want.After(limit) {
		want = limit
	}
	if want.After(c.leaseExpiry) {
		c.leaseExpiry = want
		c.armLease()
	}
	return c.leaseExpiry, true
}

// end stops the call from outside. The media is closed synchronously, so audio
// stops before end returns; the provider UI teardown and the release of the
// environment and identity follow in finish. The first cause wins.
func (c *voiceCall) end(cause error) {
	c.mu.Lock()
	if c.cause != nil {
		c.mu.Unlock()
		return
	}
	c.cause = cause
	c.state = helperv1.VoiceState_VOICE_STATE_ENDED
	if c.leaseTimer != nil {
		c.leaseTimer.Stop()
	}
	c.mu.Unlock()
	c.cancel(cause)
	c.s.cfg.Voice.Media.Close(c.mediaKey())
	go c.finish()
}

// finish tears the call down in the order that keeps a successor safe: the
// activation job is awaited, the provider's voice UI is deactivated (bounded), and
// only then are the environment and the identity released and ENDED written. The
// ENDED event is therefore the node's acknowledgement that the provider session is
// no longer in voice mode (the primary's terminating hold waits for it).
func (c *voiceCall) finish() {
	s := c.s
	defer s.wg.Done()
	c.mu.Lock()
	done, asked := c.activateDone, c.activateStarted
	c.mu.Unlock()
	if done != nil {
		<-done
	}
	deactivated := false
	if asked {
		state, err := c.runControl(context.Background(), "deactivate", s.cfg.Voice.DeactivateTimeout)
		deactivated = err == nil && state == "deactivated"
		if !deactivated {
			c.mu.Lock()
			c.appendLocked(helperv1.VoiceEventType_VOICE_EVENT_TYPE_WARNING, map[string]any{"code": "deactivate_failed"}, "")
			c.mu.Unlock()
		}
	}
	s.mu.Lock()
	if s.voiceEnvBusy[c.info.Env.ID] == c {
		delete(s.voiceEnvBusy, c.info.Env.ID)
	}
	c.mu.Lock()
	discard := c.discard
	c.mu.Unlock()
	if discard && s.voiceCalls[c.info.SessionID] == c {
		delete(s.voiceCalls, c.info.SessionID)
	}
	s.mu.Unlock()
	s.gate.release(c.key, c.info.AttemptID, false, s.clock.Now())

	c.mu.Lock()
	data := map[string]any{"deactivated": deactivated}
	for k, v := range c.endData {
		data[k] = v
	}
	reason := voiceEndReason(c.cause)
	c.appendLocked(helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED, data, reason)
	c.ended, c.endedAt = true, c.now()
	c.mu.Unlock()
	close(c.done)
	c.cancel(nil)
	s.log.Info("helper voice call ended", "session_id", c.info.SessionID, "attempt_id", c.info.AttemptID,
		"generation", c.info.Generation, "end_reason", reason, "deactivated", deactivated)
}

func voiceEndReason(cause error) string {
	var media *voiceMediaEnded
	switch {
	case errors.Is(cause, errVoiceTerminated):
		return "terminated"
	case errors.Is(cause, errLeaseExpired):
		return "lease_expired"
	case errors.Is(cause, errSuperseded):
		return "superseded"
	case errors.Is(cause, errShutdown):
		return "shutdown"
	case errors.Is(cause, errDrainGrace):
		return "drain_grace_elapsed"
	case errors.Is(cause, errVoiceActivation):
		return "activation_failed"
	case errors.Is(cause, errVoiceOfferFailed):
		return "offer_failed"
	case errors.As(cause, &media):
		return "media_ended"
	}
	return "ended"
}

// appendLocked adds one event. Past the bound only ENDED is still written.
func (c *voiceCall) appendLocked(t helperv1.VoiceEventType, data map[string]any, endReason string) {
	if t != helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED && len(c.events) >= maxVoiceEvents-1 {
		return
	}
	if c.ended {
		return
	}
	var dataJSON string
	if len(data) > 0 {
		if b, err := json.Marshal(data); err == nil && len(b) <= maxEventDataBytes {
			dataJSON = string(b)
		}
	}
	c.events = append(c.events, &helperv1.VoiceEvent{
		AttemptId: c.info.AttemptID, Sequence: uint64(len(c.events)) + 1, LeaseGeneration: c.info.Generation,
		Type: t, CreatedAt: timestamppb.New(c.now()), DataJson: dataJSON, EndReason: endReason,
	})
	close(c.changed)
	c.changed = make(chan struct{})
}

// maybeConnectedLocked advances to CONNECTED once the provider is verified ready
// AND the client's peer connection is up (never from SDP creation or a click
// alone: the same rule the primary applies to primary-hosted voice).
func (c *voiceCall) maybeConnectedLocked() {
	if c.cause != nil || c.connected || !c.providerReady || !c.peerUp {
		return
	}
	c.connected = true
	c.state = helperv1.VoiceState_VOICE_STATE_CONNECTED
	c.appendLocked(helperv1.VoiceEventType_VOICE_EVENT_TYPE_CONNECTED, nil, "")
}

func (c *voiceCall) onPeerConnected() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cause == nil {
		c.peerUp = true
		c.maybeConnectedLocked()
	}
}

func (c *voiceCall) setMuted(muted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cause != nil || c.muted == muted {
		return
	}
	c.muted = muted
	t := helperv1.VoiceEventType_VOICE_EVENT_TYPE_UNMUTED
	if muted {
		t = helperv1.VoiceEventType_VOICE_EVENT_TYPE_MUTED
	}
	c.appendLocked(t, nil, "")
}

func (c *voiceCall) sink() VoiceSink {
	return VoiceSink{
		PeerConnected: c.onPeerConnected,
		MediaEnded:    func(reason string) { c.endMediaEnded(reason) },
		ClientMuted:   c.setMuted,
	}
}

func (c *voiceCall) endMediaEnded(reason string) {
	if !endTokenRe.MatchString(reason) {
		reason = "unknown"
	}
	c.mu.Lock()
	if c.endData == nil {
		c.endData = map[string]any{}
	}
	c.endData["reason"] = reason
	c.mu.Unlock()
	c.end(&voiceMediaEnded{reason: reason})
}

// beginActivation starts the provider voice UI on this node's own browser, once.
// It runs as a control job on the node's Runner (the same pool, identity lock and
// worker as every other attempt) against the environment's loopback CDP endpoint:
// no WAN CDP endpoint ever exists. A failure ends the call; success is half of
// CONNECTED.
func (c *voiceCall) beginActivation() {
	c.mu.Lock()
	if c.cause != nil || c.activateStarted {
		c.mu.Unlock()
		return
	}
	c.activateStarted = true
	done := make(chan struct{})
	c.activateDone = done
	c.mu.Unlock()
	go func() {
		defer close(done)
		state, err := c.runControl(c.ctx, "activate", c.s.cfg.Voice.ActivateTimeout)
		if c.ctx.Err() != nil {
			return // the call was ended meanwhile; finish() deactivates
		}
		if err != nil || state != "activated" {
			c.mu.Lock()
			c.endData = map[string]any{"provider_state": providerStateToken(state)}
			c.mu.Unlock()
			c.s.log.Warn("helper voice activation failed", "session_id", c.info.SessionID, "attempt_id", c.info.AttemptID, "provider_state", providerStateToken(state))
			c.end(errVoiceActivation)
			return
		}
		c.mu.Lock()
		c.providerReady = true
		c.maybeConnectedLocked()
		c.mu.Unlock()
	}()
}

func providerStateToken(state string) string {
	if !endTokenRe.MatchString(state) {
		return "failed"
	}
	return state
}

// runControl runs voice.activate or voice.deactivate on the node's Runner and
// returns the worker's verified state ("activated", "deactivated", "login_wall",
// or "failed"). The runner's error text and the worker's messages never leave
// this function.
func (c *voiceCall) runControl(ctx context.Context, action string, timeout time.Duration) (state string, err error) {
	s := c.s
	if s.cfg.Runner == nil {
		return "failed", errNoRun
	}
	input, _ := json.Marshal(map[string]any{
		"provider_id": c.info.Target, "cdp_endpoint": c.info.Env.CDPEndpoint, "action": action,
	})
	spec := AttemptSpec{
		JobID: "voice-" + c.info.AttemptID + "-" + action, AttemptID: c.info.AttemptID, Generation: c.info.Generation,
		Provider: c.info.Target, Target: c.info.Target, CommandType: "voice." + action,
		IdentityRef: c.info.IdentityRef, InputJSON: string(input), OptionsJSON: "{}", Deadline: timeout,
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var outcome *helperv1.AttemptOutcome
	emit := func(ev Event) error {
		if outcome != nil {
			return errAttemptEnded
		}
		if ev.Type == helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL {
			outcome = ev.Outcome
		}
		return nil
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = errors.New("runner panicked")
				s.log.Error("helper voice control job panicked", "session_id", c.info.SessionID, "panic", truncate(fmt.Sprint(r), 200))
			}
		}()
		err = s.cfg.Runner.Run(rctx, spec, emit)
	}()
	if outcome == nil {
		return "failed", errors.New("the control job ended without a terminal event")
	}
	switch outcome.GetStatus() {
	case helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED:
		var res struct {
			State string `json:"state"`
		}
		if json.Unmarshal([]byte(outcome.GetResultJson()), &res) != nil || res.State == "" {
			return "failed", errors.New("the control job reported no state")
		}
		return res.State, nil
	default:
		if outcome.GetErrorCode() == "helper_worker_blocked" {
			return "login_wall", errors.New("the provider needs a manual login")
		}
		return "failed", errors.New("the control job failed")
	}
}

// ---- admission ---------------------------------------------------------------

func (s *Server) checkVoiceFence(f *helperv1.VoiceFence) error {
	switch {
	case f == nil:
		return invalid("fence is required")
	case f.GetNodeId() != s.cfg.NodeID:
		return status.Error(codes.PermissionDenied, "fence.node_id does not match this Helper Node")
	case !idTokenRe.MatchString(f.GetSessionId()), !voiceTenantRe.MatchString(f.GetTenantId()),
		!attemptRe.MatchString(f.GetAttemptId()), f.GetLeaseGeneration() == 0, f.GetLeaseGeneration() > maxVoiceGeneration:
		return invalid("fence needs session_id, tenant_id, attempt_id (att_...) and a lease_generation")
	case !f.GetExpiresAt().IsValid():
		return invalid("fence.expires_at is required")
	case !f.GetExpiresAt().AsTime().After(s.clock.Now()):
		return status.Error(codes.DeadlineExceeded, "fence.expires_at has passed")
	}
	return nil
}

// validateVoiceOffer checks every field of an OfferVoice request the node acts on.
// All of it is untrusted input as far as the node is concerned: bounded, shaped,
// never logged.
func (s *Server) validateVoiceOffer(req *helperv1.OfferVoiceRequest) (VoiceCall, error) {
	f := req.GetFence()
	if err := s.checkVoiceFence(f); err != nil {
		return VoiceCall{}, err
	}
	cr := req.GetCredentials()
	switch {
	case !nameRe.MatchString(req.GetTarget()) || isAntigravity(req.GetTarget()):
		return VoiceCall{}, invalid("target must be a short identifier the node may run")
	case !idTokenRe.MatchString(req.GetIdentityRef()):
		return VoiceCall{}, invalid("identity_ref must be an opaque token")
	case !idTokenRe.MatchString(req.GetInstanceRef()):
		return VoiceCall{}, invalid("instance_ref must be an opaque token")
	case len(req.GetTraceId()) > 128:
		return VoiceCall{}, invalid("trace_id is too long")
	case req.GetSdpOffer() == "" || len(req.GetSdpOffer()) > maxVoiceOfferBytes:
		return VoiceCall{}, invalid("sdp_offer is required and bounded")
	case len(cr.GetRelayKey()) < minVoiceKeyBytes || len(cr.GetRelayKey()) > maxVoiceKeyBytes ||
		len(cr.GetMediaKey()) < minVoiceKeyBytes || len(cr.GetMediaKey()) > maxVoiceKeyBytes ||
		string(cr.GetRelayKey()) == string(cr.GetMediaKey()):
		return VoiceCall{}, invalid("credentials need distinct per-attempt relay_key and media_key")
	case len(cr.GetIceServers()) > maxVoiceICEServers:
		return VoiceCall{}, invalid("too many ice_servers")
	}
	ice, err := validateVoiceICE(cr.GetIceServers())
	if err != nil {
		return VoiceCall{}, err
	}
	env, ok := s.cfg.Voice.env(req.GetInstanceRef())
	if !ok {
		return VoiceCall{}, status.Error(codes.FailedPrecondition, "instance_ref names no audio environment on this Helper Node")
	}
	return VoiceCall{
		SessionID: f.GetSessionId(), TenantID: f.GetTenantId(), AttemptID: f.GetAttemptId(), NodeID: f.GetNodeId(),
		Generation: f.GetLeaseGeneration(), Expires: f.GetExpiresAt().AsTime(),
		Target: req.GetTarget(), IdentityRef: req.GetIdentityRef(), InstanceRef: req.GetInstanceRef(), Env: env,
		Credentials: VoiceCredentials{
			RelayKey: slices.Clone(cr.GetRelayKey()), MediaKey: slices.Clone(cr.GetMediaKey()), ICEServers: ice,
		},
	}, nil
}

func validateVoiceICE(in []*helperv1.VoiceIceServer) ([]VoiceICEServer, error) {
	out := make([]VoiceICEServer, 0, len(in))
	for _, e := range in {
		if len(e.GetUrls()) == 0 || len(e.GetUrls()) > maxVoiceICEURLs ||
			len(e.GetUsername()) > maxVoiceICEField || len(e.GetCredential()) > maxVoiceICEField {
			return nil, invalid("ice_servers entries need 1..4 urls and short credentials")
		}
		turn := false
		for _, u := range e.GetUrls() {
			if len(u) > maxVoiceICEField || !iceURLRe.MatchString(u) {
				return nil, invalid("ice_servers urls must be stun:, stuns:, turn: or turns: URLs")
			}
			turn = turn || u[:4] == "turn"
		}
		srv := VoiceICEServer{URLs: slices.Clone(e.GetUrls())}
		if turn {
			if e.GetUsername() == "" || e.GetCredential() == "" {
				return nil, invalid("a TURN ice_server needs the primary-minted username and credential")
			}
			srv.Username, srv.Credential = e.GetUsername(), e.GetCredential()
		}
		out = append(out, srv)
	}
	return out, nil
}

// admitVoice registers the call for a first OfferVoice, or returns the live call a
// repeated OfferVoice under the same fence refers to (fresh == false).
func (s *Server) admitVoice(call VoiceCall) (c *voiceCall, fresh bool, err error) {
	now := s.clock.Now()
	expiry := call.Expires
	if limit := now.Add(s.cfg.LeaseMaxTTL); expiry.After(limit) {
		expiry = limit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false, errClosed
	}
	s.sweepVoiceLocked()
	if cur := s.voiceCalls[call.SessionID]; cur != nil {
		switch {
		case cur.info.TenantID != call.TenantID:
			return nil, false, status.Error(codes.Aborted, "stale lease generation or attempt_id mismatch")
		case cur.info.AttemptID == call.AttemptID && cur.info.Generation == call.Generation:
			if !cur.live() {
				return nil, false, errVoiceCallEnded
			}
			if cur.info.Target != call.Target || cur.info.IdentityRef != call.IdentityRef || cur.info.InstanceRef != call.InstanceRef {
				return nil, false, status.Error(codes.FailedPrecondition, "target, identity_ref or instance_ref differs from the call")
			}
			cur.extendLease(expiry) // like RenewVoice
			return cur, false, nil
		case call.Generation <= cur.info.Generation:
			// A lower generation is a stale writer; the same generation under another
			// attempt id is a second claim on one lease. Neither runs.
			return nil, false, status.Error(codes.Aborted, "stale lease generation or attempt_id mismatch")
		}
	}
	if s.draining {
		return nil, false, errDrained
	}
	if s.cfg.Runner == nil { // the provider voice UI cannot be activated: refuse before any media starts
		return nil, false, errNoRun
	}
	if cur := s.voiceCalls[call.SessionID]; cur != nil { // a newer generation fences the older holder out
		cur.end(errSuperseded)
	}
	if holder := s.voiceEnvBusy[call.Env.ID]; holder != nil {
		return nil, false, status.Error(codes.Unavailable, "the audio environment already hosts a call")
	}
	key := identityKey{provider: call.Target, ref: call.IdentityRef}
	switch err := s.gate.acquire(key, call.AttemptID, now); {
	case errors.Is(err, errIdentityBusy), errors.Is(err, errCapacity):
		return nil, false, status.Error(codes.Unavailable, err.Error())
	case err != nil:
		return nil, false, status.Error(codes.Internal, "admission failed")
	}
	info := call
	info.Credentials = VoiceCredentials{} // the media endpoint holds them; the service never keeps a copy
	ctx, cancel := context.WithCancelCause(context.Background())
	c = &voiceCall{
		s: s, info: info, key: key, ctx: ctx, cancel: cancel, state: helperv1.VoiceState_VOICE_STATE_CONNECTING,
		leaseExpiry: expiry, changed: make(chan struct{}), done: make(chan struct{}),
	}
	c.mu.Lock()
	c.armLease()
	c.mu.Unlock()
	s.voiceCalls[call.SessionID], s.voiceEnvBusy[call.Env.ID] = c, c
	s.wg.Add(1) // released by finish
	s.log.Info("helper voice call accepted", "session_id", call.SessionID, "attempt_id", call.AttemptID,
		"generation", call.Generation, "node_id", call.NodeID, "instance_ref", call.InstanceRef)
	return c, true, nil
}

// sweepVoiceLocked forgets ended calls that outlived Retain, and the oldest ones
// beyond MaxRetained. Callers hold s.mu.
func (s *Server) sweepVoiceLocked() {
	type gone struct {
		c  *voiceCall
		at time.Time
	}
	now := s.clock.Now()
	var finished []gone
	for id, c := range s.voiceCalls {
		c.mu.Lock()
		ended, at := c.ended, c.endedAt
		c.mu.Unlock()
		if !ended {
			continue
		}
		if now.Sub(at) >= s.cfg.Retain {
			delete(s.voiceCalls, id)
			continue
		}
		finished = append(finished, gone{c, at})
	}
	if len(finished) > s.cfg.MaxRetained {
		slices.SortFunc(finished, func(x, y gone) int { return x.at.Compare(y.at) })
		for _, g := range finished[:len(finished)-s.cfg.MaxRetained] {
			delete(s.voiceCalls, g.c.info.SessionID)
		}
	}
}

func (s *Server) liveVoiceCalls() []*voiceCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*voiceCall
	for _, c := range s.voiceCalls {
		if c.live() {
			out = append(out, c)
		}
	}
	return out
}

// lookupVoice resolves the call a fenced RPC names. The fence must be exactly the
// call's: another tenant's session is indistinguishable from a missing one, and
// another attempt or generation is a stale (or forged) writer.
func (s *Server) lookupVoice(f *helperv1.VoiceFence) (*voiceCall, error) {
	if err := s.checkVoiceFence(f); err != nil {
		return nil, err
	}
	s.mu.Lock()
	c := s.voiceCalls[f.GetSessionId()]
	s.mu.Unlock()
	if c == nil || c.info.TenantID != f.GetTenantId() {
		return nil, status.Error(codes.NotFound, "unknown voice session")
	}
	if c.info.AttemptID != f.GetAttemptId() || c.info.Generation != f.GetLeaseGeneration() {
		return nil, status.Error(codes.Aborted, "stale lease generation or attempt_id mismatch")
	}
	return c, nil
}

// ---- RPCs --------------------------------------------------------------------

// OfferVoice admits the call, starts its media (relay, WebRTC) and returns the SDP
// answer. The provider voice UI is activated afterwards, in the background: the
// client waits on the answer, not on a provider page. A repeated OfferVoice under
// the same fence renegotiates the media of the live call and never activates twice.
func (v *voiceService) OfferVoice(ctx context.Context, req *helperv1.OfferVoiceRequest) (*helperv1.OfferVoiceResponse, error) {
	s := v.s
	call, err := s.validateVoiceOffer(req)
	if err != nil {
		return nil, err
	}
	c, fresh, err := s.admitVoice(call)
	if err != nil {
		return nil, err
	}
	if !fresh {
		answer, err := c.reoffer(ctx, req.GetSdpOffer())
		if err != nil {
			return nil, err
		}
		return &helperv1.OfferVoiceResponse{SdpAnswer: answer, RelayReady: true}, nil
	}
	answer, err := c.open(ctx, call, req.GetSdpOffer())
	if err != nil {
		// The primary got an error: nothing of this call may linger, so a retry
		// under the same fence starts clean.
		c.mu.Lock()
		c.discard = true
		c.mu.Unlock()
		c.end(errVoiceOfferFailed)
		return nil, err
	}
	c.mu.Lock()
	c.appendLocked(helperv1.VoiceEventType_VOICE_EVENT_TYPE_STARTED, nil, "")
	c.mu.Unlock()
	c.beginActivation()
	return &helperv1.OfferVoiceResponse{SdpAnswer: answer, RelayReady: true}, nil
}

// withCall bounds an RPC-scoped operation by the call's own life as well.
func (c *voiceCall) withCall(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}

func (c *voiceCall) open(ctx context.Context, call VoiceCall, sdpOffer string) (string, error) {
	c.offerMu.Lock()
	defer c.offerMu.Unlock()
	if !c.live() {
		return "", errVoiceCallEnded
	}
	octx, cancel := c.withCall(ctx)
	defer cancel()
	answer, err := c.s.cfg.Voice.Media.Open(octx, call, sdpOffer, c.sink())
	if err != nil {
		c.s.cfg.Voice.Media.Close(c.mediaKey())
		return "", c.mediaError(ctx, err)
	}
	if !c.live() { // ended while the offer was in flight: end() may have closed before Open registered
		c.s.cfg.Voice.Media.Close(c.mediaKey())
		return "", errVoiceCallEnded
	}
	return answer, nil
}

func (c *voiceCall) reoffer(ctx context.Context, sdpOffer string) (string, error) {
	c.offerMu.Lock()
	defer c.offerMu.Unlock()
	c.mu.Lock()
	if c.cause != nil {
		c.mu.Unlock()
		return "", errVoiceCallEnded
	}
	muted := c.muted
	c.peerUp, c.connected = false, false
	c.state = helperv1.VoiceState_VOICE_STATE_CONNECTING
	c.appendLocked(helperv1.VoiceEventType_VOICE_EVENT_TYPE_RECONNECTING, nil, "")
	c.mu.Unlock()
	octx, cancel := c.withCall(ctx)
	defer cancel()
	answer, err := c.s.cfg.Voice.Media.Reoffer(octx, c.mediaKey(), sdpOffer, muted)
	if err != nil {
		return "", c.mediaError(ctx, err)
	}
	if !c.live() {
		c.s.cfg.Voice.Media.Close(c.mediaKey())
		return "", errVoiceCallEnded
	}
	return answer, nil
}

// mediaError maps a media endpoint failure to a status without forwarding its
// text (it can carry SDP or network detail).
func (c *voiceCall) mediaError(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return status.FromContextError(ctx.Err()).Err()
	case errors.Is(err, ErrVoiceRelayUnavailable):
		return status.Error(codes.Unavailable, "the audio relay is unavailable")
	}
	c.s.log.Warn("helper voice media failed", "session_id", c.info.SessionID, "attempt_id", c.info.AttemptID, "error", truncate(err.Error(), 200))
	return status.Error(codes.Internal, "the media endpoint failed")
}

// ReconnectVoice re-establishes the client media leg of a live call.
func (v *voiceService) ReconnectVoice(ctx context.Context, req *helperv1.ReconnectVoiceRequest) (*helperv1.ReconnectVoiceResponse, error) {
	c, err := v.s.lookupVoice(req.GetFence())
	if err != nil {
		return nil, err
	}
	if req.GetSdpOffer() == "" || len(req.GetSdpOffer()) > maxVoiceOfferBytes {
		return nil, invalid("sdp_offer is required and bounded")
	}
	answer, err := c.reoffer(ctx, req.GetSdpOffer())
	if err != nil {
		return nil, err
	}
	return &helperv1.ReconnectVoiceResponse{SdpAnswer: answer}, nil
}

// ControlVoice applies one command. Mute and unmute are fenced writes; terminate
// closes the media before it returns and is idempotent (an unknown session reads
// as UNKNOWN, not an error: there is nothing left to end). Interrupt has no
// provider-side primitive yet (the worker and the relay cannot stop the provider's
// current speech), so it is refused rather than acknowledged.
func (v *voiceService) ControlVoice(_ context.Context, req *helperv1.ControlVoiceRequest) (*helperv1.ControlVoiceResponse, error) {
	op := req.GetOp()
	switch op {
	case helperv1.VoiceControlOp_VOICE_CONTROL_OP_MUTE, helperv1.VoiceControlOp_VOICE_CONTROL_OP_UNMUTE,
		helperv1.VoiceControlOp_VOICE_CONTROL_OP_INTERRUPT, helperv1.VoiceControlOp_VOICE_CONTROL_OP_TERMINATE:
	default:
		return nil, invalid("op is required")
	}
	c, err := v.s.lookupVoice(req.GetFence())
	if err != nil {
		if op == helperv1.VoiceControlOp_VOICE_CONTROL_OP_TERMINATE && status.Code(err) == codes.NotFound {
			return &helperv1.ControlVoiceResponse{State: helperv1.VoiceState_VOICE_STATE_UNKNOWN}, nil
		}
		return nil, err
	}
	switch op {
	case helperv1.VoiceControlOp_VOICE_CONTROL_OP_INTERRUPT:
		return nil, status.Error(codes.Unimplemented, "interrupt is not supported: there is no provider-side barge-in primitive on this node")
	case helperv1.VoiceControlOp_VOICE_CONTROL_OP_TERMINATE:
		c.end(errVoiceTerminated)
	default:
		muted := op == helperv1.VoiceControlOp_VOICE_CONTROL_OP_MUTE
		if c.live() {
			c.setMuted(muted)
			c.s.cfg.Voice.Media.SetMuted(c.mediaKey(), muted)
		}
	}
	st, m := c.snapshot()
	return &helperv1.ControlVoiceResponse{State: st, Muted: m}, nil
}

func (c *voiceCall) snapshot() (helperv1.VoiceState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state, c.muted
}

// RenewVoice extends the call's lease. Only the exact fence the node holds may
// renew; a lapsed lease is never revived.
func (v *voiceService) RenewVoice(_ context.Context, req *helperv1.RenewVoiceRequest) (*helperv1.RenewVoiceResponse, error) {
	c, err := v.s.lookupVoice(req.GetFence())
	if err != nil {
		return nil, err
	}
	if !req.GetExpiresAt().IsValid() {
		return nil, invalid("expires_at is required")
	}
	expiry, ok := c.extendLease(req.GetExpiresAt().AsTime())
	st, _ := c.snapshot()
	if !ok && st != helperv1.VoiceState_VOICE_STATE_ENDED {
		return nil, status.Error(codes.Aborted, "the voice lease already expired")
	}
	return &helperv1.RenewVoiceResponse{LeaseGeneration: c.info.Generation, ExpiresAt: timestamppb.New(expiry), State: st}, nil
}

// StreamVoiceEvents replays the call's events after after_sequence and follows
// them until ENDED (or the caller goes away). The call itself never depends on a
// stream: a broken one loses nothing.
func (v *voiceService) StreamVoiceEvents(req *helperv1.StreamVoiceEventsRequest, stream grpc.ServerStreamingServer[helperv1.StreamVoiceEventsResponse]) error {
	c, err := v.s.lookupVoice(req.GetFence())
	if err != nil {
		return err
	}
	ctx := stream.Context()
	for idx := 0; ; {
		c.mu.Lock()
		evs, more, ended := c.events[idx:], c.changed, c.ended
		c.mu.Unlock()
		for _, e := range evs {
			idx++
			if e.GetSequence() <= req.GetAfterSequence() {
				continue
			}
			if err := stream.Send(&helperv1.StreamVoiceEventsResponse{Event: e}); err != nil {
				return err
			}
		}
		if ended {
			return nil
		}
		select {
		case <-more:
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		}
	}
}
