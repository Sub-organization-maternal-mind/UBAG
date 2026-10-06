package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Helper-hosted voice, primary side (P5.11, UBAG_HELPER_VOICE, default off; ADR-0018).
//
// A session bound to a Helper Node (Session.NodeID, P5.9) has its media terminated
// on that node (decision D7): the browser, the audio environment, the relay and the
// WebRTC endpoint are co-located there, and the primary only keeps public signaling.
// RemoteNegotiator is the media plane for such a session. It slots in at
// httpapi.MediaNegotiator (the connect response is unchanged), and does four things:
//
//   - routes the client's SDP offer to the node that owns the session, minting the
//     per-attempt credentials (P5.7) that node needs and nothing else: no global
//     secret ever leaves the primary;
//   - fences every call by the session's (node, lease generation): mute, interrupt,
//     reconnect (an ICE restart), renew and terminate carry the VoiceFence of the
//     lease the voice store holds, so a replaced node, an older generation or another
//     tenant's session can never touch a call (HelperVoiceGuard);
//   - supervises each call it offered: it renews the media lease in the store FIRST
//     and the node's lease second (the node ends the call itself when renewals stop),
//     and declares the node lost when it has not answered for a whole lease window;
//   - turns the node's events into fenced primary writes: CONNECTED and the client's
//     mute become CommitTransition/CommitMuted, ENDED is the node's acknowledgement
//     that the provider left voice mode and releases the terminating hold. An event
//     of another attempt or generation is rejected, and the store's own fence
//     rejects a stale writer a second time.
//
// Control is stateless: it is addressed by the session record, so any replica (and a
// restarted primary) can mute or end a call. Only the supervisor, renewing and
// listening for events, lives in memory and is rebuilt by the next connect. A call
// does not survive a primary restart: it ends within one lease window.

const (
	// DefaultRemoteLeaseWindow is how long a renewal keeps both the store's media
	// lease and the node's own lease alive (the node ends the call at the end of it).
	DefaultRemoteLeaseWindow = 60 * time.Second
	// DefaultRemoteRenewEvery is the renewal cadence: a call survives two lost renewals.
	DefaultRemoteRenewEvery = 20 * time.Second
	// DefaultRemoteClockSkew is the clock difference tolerated between the primary
	// and a node; the node's lease is shortened by it so the node stops no later than
	// the store lets anyone else start.
	DefaultRemoteClockSkew = 5 * time.Second
	// DefaultRemoteOfferTimeout bounds an OfferVoice (media setup on the node).
	DefaultRemoteOfferTimeout = 15 * time.Second
	// DefaultRemoteRPCTimeout bounds every other unary call.
	DefaultRemoteRPCTimeout = 5 * time.Second
	// RemoteCallMaxAge is the life of the per-attempt credentials, counted from the
	// session's creation so every replica derives the same keys. The supervisor ends
	// the call just before then; a TURN-relayed leg cannot outlive it either.
	RemoteCallMaxAge = 4 * time.Hour

	remoteProtocolVersion = "ubag.helper.v1"
	remoteFeature         = "helper_voice"
	remoteEventDataMax    = 4 << 10
	remoteBackoffMin      = 200 * time.Millisecond
	remoteBackoffMax      = 5 * time.Second
)

var (
	// ErrNotNodeBound means the session is not hosted on a Helper Node.
	ErrNotNodeBound = errors.New("voice: session is not bound to a helper node")
	// ErrNodeUnavailable means the node could not be reached or refused the call for
	// a reason that may pass (down, draining, busy, incompatible, not voice capable).
	ErrNodeUnavailable = errors.New("voice: helper node unavailable")
	// ErrNoProfile means the account has no active profile on the session's node.
	ErrNoProfile = errors.New("voice: account has no active profile on the node")
	// ErrInterruptUnsupported means the node has no provider-side barge-in primitive
	// yet (P5.10 answers UNIMPLEMENTED); nothing was interrupted.
	ErrInterruptUnsupported = errors.New("voice: the helper node cannot interrupt the provider")

	endTokenPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
)

// RemoteConn is one verified connection to a Helper Node, as the negotiator uses
// it. helperclient.Conn satisfies it.
type RemoteConn interface {
	Handshake(ctx context.Context, in *helperv1.HandshakeRequest, opts ...grpc.CallOption) (*helperv1.HandshakeResponse, error)
	helperv1.HelperVoiceServiceClient
	Close() error
}

// RemoteDialFunc dials the node and verifies it is exactly nodeID (the mTLS dial of
// package helperclient). The connection is lazy: it fails on the first RPC.
type RemoteDialFunc func(ctx context.Context, nodeID, endpoint string) (RemoteConn, error)

// RemoteConfig configures a RemoteNegotiator.
type RemoteConfig struct {
	// Store is the shared voice store: the lease generation and every write are
	// fenced by it.
	Store Store
	// Minter derives the per-attempt credentials. Its Master is the relay secret and
	// its ICE the deployment's NAT-traversal configuration (also what the client is
	// told to use).
	Minter HelperVoiceMinter
	// Dial opens a verified connection to a node.
	Dial RemoteDialFunc
	// Endpoint resolves where a node listens (the node store's last accepted
	// allocation); the manager is not asked.
	Endpoint func(ctx context.Context, nodeID string) (string, error)
	// ProfileRef resolves the node-side identity of an account: the tenant's ACTIVE
	// profile_ref for (target, identity) on the node, the key a text job on that
	// profile takes on the node's identity gate. ErrNoProfile when there is none.
	ProfileRef func(ctx context.Context, tenantID, target, identityRef, nodeID string) (string, error)

	// WorkloadVersion and RegistryDigest, when set, must equal the node's (the same
	// handshake checks as job dispatch); empty skips that check.
	WorkloadVersion string
	RegistryDigest  string

	// LeaseWindow, RenewEvery, MaxClockSkew, OfferTimeout and RPCTimeout default to
	// the Default* constants. LeaseWindow must exceed MaxClockSkew.
	LeaseWindow, RenewEvery, MaxClockSkew, OfferTimeout, RPCTimeout time.Duration
	// Now is the clock (nil = time.Now).
	Now func() time.Time

	// OnFenced observes a node write rejected as stale or fenced (reason is one of
	// "stale_generation", "attempt_mismatch", "commit_fenced"); OnRenewFailure a
	// failed renewal ("lost" or "error"). Both must not block; nil is a no-op.
	OnFenced       func(reason string)
	OnRenewFailure func(reason string)
}

func (c *RemoteConfig) defaults() error {
	if c.Store == nil || c.Dial == nil || c.Endpoint == nil || c.ProfileRef == nil {
		return errors.New("voice: a remote negotiator needs a store, a dialer, an endpoint resolver and a profile resolver")
	}
	for p, d := range map[*time.Duration]time.Duration{
		&c.LeaseWindow: DefaultRemoteLeaseWindow, &c.RenewEvery: DefaultRemoteRenewEvery, &c.MaxClockSkew: DefaultRemoteClockSkew,
		&c.OfferTimeout: DefaultRemoteOfferTimeout, &c.RPCTimeout: DefaultRemoteRPCTimeout,
	} {
		if *p <= 0 {
			*p = d
		}
	}
	if c.LeaseWindow <= c.MaxClockSkew || c.RenewEvery >= c.LeaseWindow-c.MaxClockSkew {
		return errors.New("voice: the renewal cadence must be shorter than the lease window minus the clock skew")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return nil
}

// RemoteNegotiator terminates the media of node-bound sessions on their Helper Node.
type RemoteNegotiator struct {
	cfg   RemoteConfig
	guard HelperVoiceGuard

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	calls  map[string]*remoteCall // supervised calls by session id
	wg     sync.WaitGroup
}

// NewRemoteNegotiator validates cfg. Nothing is dialed until a session connects.
func NewRemoteNegotiator(cfg RemoteConfig) (*RemoteNegotiator, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	n := &RemoteNegotiator{cfg: cfg, calls: map[string]*remoteCall{}}
	n.ctx, n.cancel = context.WithCancel(context.Background())
	n.guard = HelperVoiceGuard{Enabled: true, Store: cfg.Store, Current: n.current, Now: cfg.Now}
	return n, nil
}

// Close stops every supervisor and waits for them. The calls themselves are not
// ended: the nodes stop them when the renewals stop, within one lease window, and
// the store's media lease lapses the same way.
func (n *RemoteNegotiator) Close() {
	n.mu.Lock()
	n.closed = true
	n.mu.Unlock()
	n.cancel()
	n.wg.Wait()
}

// HostsNodeSessions marks this as a media plane that terminates a node-bound
// session's media on its node (httpapi.NodeMediaNegotiator).
func (n *RemoteNegotiator) HostsNodeSessions() bool { return true }

func (n *RemoteNegotiator) now() time.Time { return n.cfg.Now() }

func (n *RemoteNegotiator) track() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return false
	}
	n.wg.Add(1)
	return true
}

func (n *RemoteNegotiator) fenced(reason string) {
	if n.cfg.OnFenced != nil {
		n.cfg.OnFenced(reason)
	}
}

func (n *RemoteNegotiator) renewFailed(reason string) {
	if n.cfg.OnRenewFailure != nil {
		n.cfg.OnRenewFailure(reason)
	}
}

// helperDeadline is the lease deadline a node is told: the window, shortened by the
// clock-skew bound so the node stops no later than the store lets anyone else start.
func (n *RemoteNegotiator) helperDeadline(now time.Time) time.Time {
	return now.Add(n.cfg.LeaseWindow - n.cfg.MaxClockSkew)
}

// binding is the attempt a session's media runs as. It is a pure function of the
// session record (AttemptIDFor, and credentials that end RemoteCallMaxAge after the
// session was created), so every replica derives the same keys for the same lease.
func (n *RemoteNegotiator) binding(s Session) (AttemptBinding, error) {
	if s.NodeID == "" || s.LeaseGeneration == 0 {
		return AttemptBinding{}, ErrNotNodeBound
	}
	if s.CreatedAt.IsZero() {
		return AttemptBinding{}, ErrBadBinding
	}
	b := AttemptBinding{
		SessionID: s.ID, TenantID: s.TenantID, AttemptID: AttemptIDFor(s.ID, s.LeaseGeneration),
		NodeID: s.NodeID, LeaseGeneration: s.LeaseGeneration, Expires: s.CreatedAt.Add(RemoteCallMaxAge),
	}
	return b, b.Validate()
}

// guardBinding is the binding the guard checks: the lease is what is current, so its
// expiry is the lease window, not the end of the credentials (a call must stay
// controllable, and terminable, until it ends).
func (n *RemoteNegotiator) guardBinding(b AttemptBinding) AttemptBinding {
	b.Expires = n.now().Add(n.cfg.LeaseWindow)
	return b
}

// current is the guard's view of the lease: what the voice store holds.
func (n *RemoteNegotiator) current(ctx context.Context, tenantID, sessionID string) (HelperVoiceLease, bool) {
	s, ok, err := n.cfg.Store.Get(ctx, tenantID, sessionID)
	if err != nil || !ok || s.NodeID == "" || s.LeaseGeneration == 0 {
		return HelperVoiceLease{}, false
	}
	return HelperVoiceLease{AttemptID: AttemptIDFor(s.ID, s.LeaseGeneration), NodeID: s.NodeID, LeaseGeneration: s.LeaseGeneration}, true
}

func (b AttemptBinding) leaseFence() LeaseFence {
	return LeaseFence{NodeID: b.NodeID, Generation: b.LeaseGeneration}
}

func (n *RemoteNegotiator) fenceMsg(b AttemptBinding, now time.Time) *helperv1.VoiceFence {
	return &helperv1.VoiceFence{
		SessionId: b.SessionID, TenantId: b.TenantID, AttemptId: b.AttemptID, NodeId: b.NodeID,
		LeaseGeneration: b.LeaseGeneration, ExpiresAt: timestamppb.New(n.helperDeadline(now)),
	}
}

func (n *RemoteNegotiator) rpcCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, n.cfg.RPCTimeout)
}

// ---- connections --------------------------------------------------------------

// connect dials the node. handshake adds the compatibility checks an offer needs
// (protocol, node, voice support, workload, registry, clock); a control call only
// needs the verified dial.
func (n *RemoteNegotiator) connect(ctx context.Context, nodeID string, handshake bool) (RemoteConn, error) {
	endpoint, err := n.cfg.Endpoint(ctx, nodeID)
	if err != nil || endpoint == "" {
		return nil, fmt.Errorf("%w: no endpoint is known for the node", ErrNodeUnavailable)
	}
	conn, err := n.cfg.Dial(ctx, nodeID, endpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: dial: %v", ErrNodeUnavailable, err)
	}
	if !handshake {
		return conn, nil
	}
	hctx, cancel := n.rpcCtx(ctx)
	defer cancel()
	hs, err := conn.Handshake(hctx, &helperv1.HandshakeRequest{ProtocolVersion: remoteProtocolVersion, NodeId: nodeID})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: handshake: %s", ErrNodeUnavailable, status.Code(err))
	}
	if reason := n.incompatible(hs, nodeID); reason != "" {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %s", ErrNodeUnavailable, reason)
	}
	return conn, nil
}

func (n *RemoteNegotiator) incompatible(hs *helperv1.HandshakeResponse, nodeID string) string {
	skew := n.now().Sub(hs.GetServerTime().AsTime())
	if skew < 0 {
		skew = -skew
	}
	switch {
	case hs.GetProtocolVersion() != remoteProtocolVersion:
		return "helper_protocol"
	case hs.GetNodeId() != "" && hs.GetNodeId() != nodeID:
		return "helper_node_mismatch" // a cross-check only: the certificate already named the node
	case !slices.Contains(hs.GetFeatures(), remoteFeature):
		return "helper_voice_unsupported"
	case n.cfg.WorkloadVersion != "" && hs.GetWorkloadVersion() != n.cfg.WorkloadVersion:
		return "helper_workload_version"
	case n.cfg.RegistryDigest != "" && hs.GetRegistryDigest() != n.cfg.RegistryDigest:
		return "helper_registry_digest"
	case !hs.GetServerTime().IsValid() || skew > n.cfg.MaxClockSkew:
		return "helper_clock_skew"
	}
	return ""
}

// callFor returns the supervised call of exactly this attempt, if this replica has one.
func (n *RemoteNegotiator) callFor(sessionID, attemptID string) *remoteCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	if c := n.calls[sessionID]; c != nil && c.b.AttemptID == attemptID {
		return c
	}
	return nil
}

// connFor is the connection to a session's node: the supervised call's own, or a
// fresh one the caller must close through the returned func.
func (n *RemoteNegotiator) connFor(ctx context.Context, b AttemptBinding) (RemoteConn, *remoteCall, func(), error) {
	if c := n.callFor(b.SessionID, b.AttemptID); c != nil {
		return c.conn, c, func() {}, nil
	}
	conn, err := n.connect(ctx, b.NodeID, false)
	if err != nil {
		return nil, nil, func() {}, err
	}
	return conn, nil, func() { _ = conn.Close() }, nil
}

// ---- the media plane ----------------------------------------------------------

// HandleOffer terminates the client's media for a node-bound, lease-holding session
// on its node and returns the SDP answer. The first offer starts the call (and its
// supervisor); a later one on a supervised call is an ICE restart (ReconnectVoice).
// The node activates the provider's voice UI itself, after the answer.
func (n *RemoteNegotiator) HandleOffer(ctx context.Context, s Session, sdpOffer string) (string, error) {
	b, err := n.binding(s)
	if err != nil {
		return "", err
	}
	if _, err := n.guard.Authorize(ctx, n.guardBinding(b)); err != nil {
		return "", err
	}
	now := n.now()
	spec, err := n.cfg.Minter.Mint(b, now)
	if err != nil {
		return "", err
	}

	if c := n.callFor(s.ID, b.AttemptID); c != nil {
		rctx, cancel := context.WithTimeout(ctx, n.cfg.OfferTimeout)
		defer cancel()
		resp, err := c.conn.ReconnectVoice(rctx, &helperv1.ReconnectVoiceRequest{Fence: n.fenceMsg(b, now), SdpOffer: sdpOffer})
		if err != nil {
			return "", n.callError("reconnect", err)
		}
		return resp.GetSdpAnswer(), nil
	}

	profile, err := n.cfg.ProfileRef(ctx, s.TenantID, s.Target, s.IdentityRef, s.NodeID)
	if err != nil {
		return "", err
	}
	conn, err := n.connect(ctx, s.NodeID, true)
	if err != nil {
		return "", err
	}
	req := &helperv1.OfferVoiceRequest{
		Fence: n.fenceMsg(b, now), SdpOffer: sdpOffer, Target: s.Target, IdentityRef: profile, InstanceRef: s.InstanceRef,
		Credentials: &helperv1.VoiceCredentials{RelayKey: []byte(spec.RelayKey), MediaKey: []byte(spec.MediaKey)},
	}
	for _, ice := range spec.ICEServers {
		req.Credentials.IceServers = append(req.Credentials.IceServers,
			&helperv1.VoiceIceServer{Urls: ice.URLs, Username: ice.Username, Credential: ice.Credential})
	}
	octx, cancel := context.WithTimeout(ctx, n.cfg.OfferTimeout)
	resp, err := conn.OfferVoice(octx, req)
	cancel()
	if err != nil {
		_ = conn.Close()
		return "", n.callError("offer", err)
	}
	if resp.GetSdpAnswer() == "" {
		_ = conn.Close()
		return "", fmt.Errorf("%w: the node returned no answer", ErrNodeUnavailable)
	}

	c := n.newCall(n.ctx, b, conn)
	// From here the call exists on the node: any failure must end it there.
	abort := func(err error) (string, error) {
		c.terminateRemote("offer_aborted")
		c.cancel()
		_ = conn.Close()
		return "", err
	}
	// The store's media lease now follows the node's: a primary that dies leaves both to lapse together.
	if err := n.cfg.Store.RenewMediaLease(ctx, s.TenantID, s.ID, b.leaseFence(), now.Add(n.cfg.LeaseWindow), now); err != nil {
		return abort(err)
	}
	if s.Muted { // a mute set before the media existed must govern the microphone from the start
		rctx, cancel := n.rpcCtx(ctx)
		_, err := conn.ControlVoice(rctx, &helperv1.ControlVoiceRequest{Fence: n.fenceMsg(b, now), Op: helperv1.VoiceControlOp_VOICE_CONTROL_OP_MUTE})
		cancel()
		if err != nil {
			return abort(n.callError("mute", err))
		}
	}
	switch n.register(c) {
	case registerClosed:
		return abort(fmt.Errorf("%w: the gateway is shutting down", ErrNodeUnavailable))
	case registerShared:
		c.cancel() // a concurrent offer for the same lease already supervises the call
		_ = conn.Close()
	}
	return resp.GetSdpAnswer(), nil
}

// remoteCallError is a node RPC failure as the caller sees it: a sentinel (stale
// lease, unknown session, node unavailable) plus the gRPC code. The node's own text
// never leaves the log.
type remoteCallError struct {
	op   string
	code codes.Code
	kind error
}

func (e *remoteCallError) Error() string { return fmt.Sprintf("%v: %s: %s", e.kind, e.op, e.code) }
func (e *remoteCallError) Unwrap() error { return e.kind }

func codeOf(err error) codes.Code {
	var ce *remoteCallError
	if errors.As(err, &ce) {
		return ce.code
	}
	return codes.OK
}

// callError classifies a node RPC failure for the caller.
func (n *RemoteNegotiator) callError(op string, err error) error {
	code := status.Code(err)
	slog.Warn("helper voice call failed", "op", op, "code", code.String())
	kind := ErrNodeUnavailable
	switch code {
	case codes.Aborted:
		kind = ErrStaleGeneration
	case codes.NotFound:
		kind = ErrNotFound
	}
	return &remoteCallError{op: op, code: code, kind: kind}
}

// ClientICEServers are the STUN/TURN servers the client configures its peer
// connection with (the deployment's, TURN credentials minted for this session).
func (n *RemoteNegotiator) ClientICEServers(s Session) []ICEServer {
	return n.cfg.Minter.ICE.ClientICEServers(s, n.now())
}

// IssueMediaCredential is the connect response's media credential for a node-bound
// session: signed with the per-attempt media key the node verifies, since the node
// holds no app secret. ok=false when the session cannot be bound or its credentials
// ended.
func (n *RemoteNegotiator) IssueMediaCredential(s Session, expires time.Time) (string, bool) {
	b, err := n.binding(s)
	if err != nil {
		return "", false
	}
	spec, err := n.cfg.Minter.Mint(b, n.now())
	if err != nil {
		return "", false
	}
	return IssueMediaCredential([]byte(spec.MediaKey), s, expires), true
}

// MuteSession applies mute to the call's live media on its node. It returns once the
// node has applied it: a mute the node did not confirm must never be reported as
// done. A node with no call yet (the media is not up) is fine: the flag rides the
// offer.
func (n *RemoteNegotiator) MuteSession(ctx context.Context, s Session, muted bool) error {
	op := helperv1.VoiceControlOp_VOICE_CONTROL_OP_UNMUTE
	if muted {
		op = helperv1.VoiceControlOp_VOICE_CONTROL_OP_MUTE
	}
	err := n.control(ctx, s, op, "")
	if codeOf(err) == codes.NotFound {
		return nil // the node holds no call yet: the flag rides the offer
	}
	return err
}

// InterruptSession asks the node to stop the provider's current speech without
// ending the call. The node has no such primitive yet (ErrInterruptUnsupported).
func (n *RemoteNegotiator) InterruptSession(ctx context.Context, s Session) error {
	err := n.control(ctx, s, helperv1.VoiceControlOp_VOICE_CONTROL_OP_INTERRUPT, "")
	if codeOf(err) == codes.Unimplemented {
		return ErrInterruptUnsupported
	}
	return err
}

// control sends one fenced, live-session control command.
func (n *RemoteNegotiator) control(ctx context.Context, s Session, op helperv1.VoiceControlOp, reason string) error {
	b, err := n.binding(s)
	if err != nil {
		return err
	}
	if _, err := n.guard.Authorize(ctx, n.guardBinding(b)); err != nil {
		return err
	}
	conn, _, closeConn, err := n.connFor(ctx, b)
	if err != nil {
		return err
	}
	defer closeConn()
	rctx, cancel := n.rpcCtx(ctx)
	defer cancel()
	if _, err := conn.ControlVoice(rctx, &helperv1.ControlVoiceRequest{Fence: n.fenceMsg(b, n.now()), Op: op, Reason: reason}); err != nil {
		return n.callError(op.String(), err)
	}
	return nil
}

// EndSession ends the session's call on its node and, when the voice store holds a
// terminating hold for it, releases the hold once the node acknowledges that the
// provider left voice mode (the ENDED event). It returns at once and works in the
// background: the node closes the media as soon as it receives the command, and a
// client's terminate never waits for a node. A call that is no longer the store's
// current lease is left alone (a successor owns the node).
func (n *RemoteNegotiator) EndSession(ctx context.Context, s Session, reason string) {
	b, err := n.binding(s)
	if err != nil || !n.track() {
		return
	}
	go func() {
		defer n.wg.Done()
		n.endSession(context.WithoutCancel(ctx), b, reason)
	}()
}

func (n *RemoteNegotiator) endSession(ctx context.Context, b AttemptBinding, reason string) {
	ctx, cancel := context.WithTimeout(ctx, n.cfg.RPCTimeout+TerminatingHoldMax+n.cfg.RPCTimeout)
	defer cancel()
	stop := context.AfterFunc(n.ctx, cancel)
	defer stop()

	if _, err := n.guard.AuthorizeEnding(ctx, n.guardBinding(b)); err != nil {
		slog.Info("not ending a helper voice call: it is not the session's current lease", "session_id", b.SessionID, "error", err)
		return
	}
	conn, sup, closeConn, err := n.connFor(ctx, b)
	if err != nil {
		slog.Warn("ending a helper voice call: the node is unreachable; its own lease will end the call", "session_id", b.SessionID, "error", err)
		return
	}
	defer closeConn()
	rctx, rcancel := n.rpcCtx(ctx)
	resp, err := conn.ControlVoice(rctx, &helperv1.ControlVoiceRequest{
		Fence: n.fenceMsg(b, n.now()), Op: helperv1.VoiceControlOp_VOICE_CONTROL_OP_TERMINATE, Reason: endToken(reason),
	})
	rcancel()
	if err != nil {
		slog.Warn("ending a helper voice call failed; its own lease will end the call", "session_id", b.SessionID, "code", status.Code(err).String())
		return
	}
	switch {
	case resp.GetState() == helperv1.VoiceState_VOICE_STATE_UNKNOWN:
		// Nothing runs on the node (never offered, or it restarted): no teardown to wait for.
		n.releaseHold(ctx, b)
		if sup != nil {
			sup.finish()
		}
	case sup != nil:
		// The supervisor's event stream sees ENDED and releases the hold.
	default:
		// No supervisor on this replica (another served the connect, or a restart):
		// listen for the node's acknowledgement just long enough to release the hold.
		n.newCall(ctx, b, conn).events()
	}
}

func (n *RemoteNegotiator) releaseHold(ctx context.Context, b AttemptBinding) {
	err := n.cfg.Store.ReleaseHold(ctx, b.TenantID, b.SessionID, b.leaseFence(), n.now())
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrStaleGeneration) && !errors.Is(err, ErrNodeMismatch) {
		slog.Warn("releasing the voice terminating hold failed", "session_id", b.SessionID, "error", err)
	}
}

// Disconnect ends the supervised call of a session this replica serves, from the
// lease sweeper (the session was already ended in the store).
func (n *RemoteNegotiator) Disconnect(sessionID string) {
	n.mu.Lock()
	c := n.calls[sessionID]
	n.mu.Unlock()
	if c == nil || !n.track() {
		return
	}
	go func() {
		defer n.wg.Done()
		c.terminateRemote("disconnect")
		c.finish()
	}()
}

// ---- supervised calls ---------------------------------------------------------

// remoteCall is one call this replica offered: its connection to the node, its
// renewal loop and its event stream.
type remoteCall struct {
	n    *RemoteNegotiator
	b    AttemptBinding
	conn RemoteConn

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	finished bool
	ended    bool   // the node reported ENDED
	after    uint64 // last event sequence applied
}

func (n *RemoteNegotiator) newCall(parent context.Context, b AttemptBinding, conn RemoteConn) *remoteCall {
	c := &remoteCall{n: n, b: b, conn: conn}
	c.ctx, c.cancel = context.WithCancel(parent)
	return c
}

type registration int

const (
	registerStarted registration = iota // c is the session's supervised call now
	registerShared                      // a call of the same lease is already supervised: nothing was started
	registerClosed                      // the negotiator is closed: nothing was started
)

// register makes c the session's supervised call and starts it. An older generation's
// call is retired: its node is told to end it.
func (n *RemoteNegotiator) register(c *remoteCall) registration {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return registerClosed
	}
	old := n.calls[c.b.SessionID]
	if old != nil && old.b.AttemptID == c.b.AttemptID {
		n.mu.Unlock()
		return registerShared
	}
	n.calls[c.b.SessionID] = c
	n.wg.Add(2)
	if old != nil {
		n.wg.Add(1)
	}
	n.mu.Unlock()
	if old != nil {
		go func() {
			defer n.wg.Done()
			old.terminateRemote("superseded")
			old.finish()
		}()
	}
	go func() {
		defer n.wg.Done()
		if c.renewLoop() {
			// The record ended and the node was told to end the call: keep listening for its
			// ENDED, which releases the terminating hold, for as long as the hold can last.
			t := time.AfterFunc(TerminatingHoldMax+n.cfg.RPCTimeout, c.finish)
			defer t.Stop()
			<-c.ctx.Done()
			return
		}
		c.finish()
	}()
	go func() { defer n.wg.Done(); defer c.finish(); c.events() }()
	return registerStarted
}

// finish stops the call's loops and releases its connection. It is idempotent.
func (c *remoteCall) finish() {
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}
	c.finished = true
	c.mu.Unlock()
	c.cancel()
	c.n.mu.Lock()
	if c.n.calls[c.b.SessionID] == c {
		delete(c.n.calls, c.b.SessionID)
	}
	c.n.mu.Unlock()
	_ = c.conn.Close()
}

// terminateRemote asks the node to end the call, best effort: it is used when the
// store has already ended the session, so the node's own lease is the backstop.
func (c *remoteCall) terminateRemote(reason string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), c.n.cfg.RPCTimeout)
	defer cancel()
	if _, err := c.conn.ControlVoice(ctx, &helperv1.ControlVoiceRequest{
		Fence: c.n.fenceMsg(c.b, c.n.now()), Op: helperv1.VoiceControlOp_VOICE_CONTROL_OP_TERMINATE, Reason: endToken(reason),
	}); err != nil {
		slog.Warn("telling a helper node to end a voice call failed; its own lease will end it", "session_id", c.b.SessionID, "code", status.Code(err).String())
	}
}

// endRecord ends the session's record because its call is over or gone. With hold
// the account and environment stay reserved until the node acks that the provider
// left voice mode (a call still being torn down); without it they free at once (the
// node is gone, or already acked). It never touches a session another holder owns:
// the call's fence must still be the store's.
func (c *remoteCall) endRecord(reason string, hold bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), c.n.cfg.RPCTimeout)
	defer cancel()
	s, ok, err := c.n.cfg.Store.Get(ctx, c.b.TenantID, c.b.SessionID)
	if err != nil || !ok || s.checkFence(c.b.leaseFence()) != nil {
		return
	}
	d := time.Duration(0)
	if hold {
		d = TerminatingHoldMax
	}
	if err := c.n.cfg.Store.BeginTerminate(ctx, c.b.TenantID, c.b.SessionID, c.n.now(), reason, d); err != nil {
		slog.Warn("ending a voice session whose helper call is over failed", "session_id", c.b.SessionID, "error", err)
	}
}

// renewLoop keeps the call alive: the store's media lease first, the node's lease
// second. Until the node answers a renewal for a whole lease window it is declared
// lost and the session ends, so an unreachable node cannot pin an account. It
// returns true when the record has ended and the node was told to end the call: the
// event stream must then stay up to hear the node's ENDED.
func (c *remoteCall) renewLoop() (linger bool) {
	n := c.n
	lastOK := n.now()
	ticker := time.NewTicker(n.cfg.RenewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return false
		case <-ticker.C:
		}
		now := n.now()
		if !now.Before(c.b.Expires.Add(-n.cfg.RenewEvery)) { // the credentials end: end the call before the node refuses it
			c.endRecord("call_time_limit", true) // the hold exists before the node's ack can arrive
			c.terminateRemote("call_time_limit")
			return true
		}
		err := n.cfg.Store.RenewMediaLease(c.ctx, c.b.TenantID, c.b.SessionID, c.b.leaseFence(), now.Add(n.cfg.LeaseWindow), now)
		switch {
		case err == nil:
		case errors.Is(err, ErrStaleGeneration), errors.Is(err, ErrNodeMismatch), errors.Is(err, ErrBadBinding):
			// A newer holder owns the session. Our call, under OUR fence, is no longer
			// wanted: tell its node (a node that already moved on to the newer
			// generation refuses the stale fence, so a successor is never touched).
			n.fenced("stale_generation")
			slog.Warn("a voice call's lease is no longer current; its supervisor stops", "session_id", c.b.SessionID)
			c.terminateRemote("superseded")
			return false
		case errors.Is(err, ErrNotFound):
			c.terminateRemote("session_ended")
			return false
		case errors.Is(err, ErrConflict):
			// Ended (an explicit terminate, a sweep) or its media lease lapsed: the call must not
			// outlive the record. A record that is still live ends holding its leases until the
			// node acks; one that already ended keeps the hold it has.
			c.endRecord("media_lease_expired", true)
			c.terminateRemote("session_ended")
			return true
		default:
			// The store is unreachable: do not extend the node's life beyond the store's.
			n.renewFailed("error")
			continue
		}
		rctx, cancel := n.rpcCtx(c.ctx)
		_, err = c.conn.RenewVoice(rctx, &helperv1.RenewVoiceRequest{
			Fence: n.fenceMsg(c.b, now), ExpiresAt: timestamppb.New(n.helperDeadline(now)),
		})
		cancel()
		switch status.Code(err) {
		case codes.OK:
			lastOK = now
		case codes.NotFound, codes.Aborted:
			// The node holds no such call (it restarted) or its lease already ran out: no ack will come.
			n.renewFailed("lost")
			slog.Warn("a helper node no longer holds the voice call", "session_id", c.b.SessionID, "code", status.Code(err).String())
			c.endRecord("helper_lost", false)
			return false
		default:
			n.renewFailed("error")
			if now.Sub(lastOK) >= n.cfg.LeaseWindow {
				slog.Warn("a helper node has not answered for a whole lease window; declaring the call lost", "session_id", c.b.SessionID)
				c.endRecord("helper_unreachable", false)
				c.terminateRemote("helper_unreachable")
				return false
			}
		}
	}
}

// events follows the node's event stream until it ENDS (or the call stops), and
// re-attaches with replay from the last applied sequence when the stream breaks.
func (c *remoteCall) events() {
	backoff := remoteBackoffMin
	for c.ctx.Err() == nil {
		stream, err := c.conn.StreamVoiceEvents(c.ctx, &helperv1.StreamVoiceEventsRequest{
			Fence: c.n.fenceMsg(c.b, c.n.now()), AfterSequence: c.lastSequence(),
		})
		progressed := false
		if err == nil {
			progressed, err = c.drain(stream)
		}
		c.mu.Lock()
		done := c.ended
		c.mu.Unlock()
		if done || c.ctx.Err() != nil {
			return
		}
		switch status.Code(err) {
		case codes.NotFound, codes.Aborted:
			// The node has no such call: nothing is left to end, nothing will ack.
			slog.Warn("a helper node holds no voice call for the event stream", "session_id", c.b.SessionID, "code", status.Code(err).String())
			c.endRecord("helper_lost", false)
			c.finish()
			return
		}
		if progressed {
			backoff = remoteBackoffMin
		}
		t := time.NewTimer(backoff)
		select {
		case <-c.ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(backoff*2, remoteBackoffMax)
	}
}

func (c *remoteCall) lastSequence() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.after
}

// drain applies the stream's events. It reports whether any arrived, and the error
// that ended the stream (nil after ENDED).
func (c *remoteCall) drain(stream grpc.ServerStreamingClient[helperv1.StreamVoiceEventsResponse]) (bool, error) {
	progressed := false
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return progressed, nil
			}
			return progressed, err
		}
		progressed = true
		if c.apply(msg.GetEvent()) {
			return progressed, nil
		}
	}
}

// apply turns one node event into a fenced primary write. It reports true when the
// call is over (ENDED, or a newer holder owns the session).
func (c *remoteCall) apply(ev *helperv1.VoiceEvent) bool {
	n := c.n
	switch {
	case ev == nil:
		return false
	case ev.GetAttemptId() != c.b.AttemptID:
		n.fenced("attempt_mismatch")
		slog.Warn("rejected a helper voice event of another attempt", "session_id", c.b.SessionID)
		return false
	case ev.GetLeaseGeneration() != c.b.LeaseGeneration:
		n.fenced("stale_generation")
		slog.Warn("rejected a helper voice event of another lease generation", "session_id", c.b.SessionID)
		return false
	}
	c.mu.Lock()
	if ev.GetSequence() <= c.after { // a replay of what was already applied
		c.mu.Unlock()
		return false
	}
	c.after = ev.GetSequence()
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), n.cfg.RPCTimeout)
	defer cancel()
	var err error
	switch ev.GetType() {
	case helperv1.VoiceEventType_VOICE_EVENT_TYPE_CONNECTED:
		// The node reports CONNECTED only when the provider is verified ready AND the
		// client's peer connection is up (the primary-hosted rule).
		err = n.cfg.Store.CommitTransition(ctx, c.b.TenantID, c.b.SessionID, c.b.leaseFence(), StatusConnecting, StatusConnected, n.now(), "")
	case helperv1.VoiceEventType_VOICE_EVENT_TYPE_MUTED:
		err = n.cfg.Store.CommitMuted(ctx, c.b.TenantID, c.b.SessionID, c.b.leaseFence(), true, n.now())
	case helperv1.VoiceEventType_VOICE_EVENT_TYPE_UNMUTED:
		err = n.cfg.Store.CommitMuted(ctx, c.b.TenantID, c.b.SessionID, c.b.leaseFence(), false, n.now())
	case helperv1.VoiceEventType_VOICE_EVENT_TYPE_WARNING:
		slog.Warn("helper voice warning", "session_id", c.b.SessionID, "data", truncateEventData(ev.GetDataJson()))
	case helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED:
		c.handleEnded(ctx, ev)
		return true
	}
	switch {
	case err == nil, errors.Is(err, ErrConflict), errors.Is(err, ErrNotFound):
		// ErrConflict: already connected, or the session ended meanwhile.
	case errors.Is(err, ErrStaleGeneration), errors.Is(err, ErrNodeMismatch):
		n.fenced("commit_fenced")
		slog.Warn("the voice store rejected a helper event as stale; the supervisor stops", "session_id", c.b.SessionID, "type", ev.GetType().String())
		c.finish()
		return true
	default:
		slog.Warn("applying a helper voice event failed", "session_id", c.b.SessionID, "type", ev.GetType().String(), "error", err)
	}
	return false
}

// handleEnded is the node's acknowledgement that the call is over and the provider
// left voice mode. A terminating hold is released; a record still live is ended (the
// call ended on its own: media failure, lease, activation) and frees its leases at
// once, since nothing is left to tear down.
func (c *remoteCall) handleEnded(ctx context.Context, ev *helperv1.VoiceEvent) {
	n := c.n
	c.mu.Lock()
	c.ended = true
	c.mu.Unlock()
	err := n.cfg.Store.ReleaseHold(ctx, c.b.TenantID, c.b.SessionID, c.b.leaseFence(), n.now())
	switch {
	case err == nil, errors.Is(err, ErrNotFound):
	case errors.Is(err, ErrConflict):
		if err := n.cfg.Store.Terminate(ctx, c.b.TenantID, c.b.SessionID, n.now(), endEventReason(ev)); err != nil {
			slog.Warn("ending a voice session after its helper call ended failed", "session_id", c.b.SessionID, "error", err)
		}
	case errors.Is(err, ErrStaleGeneration), errors.Is(err, ErrNodeMismatch):
		n.fenced("commit_fenced") // superseded by a newer lease: not this call's session any more
	default:
		slog.Warn("releasing the voice terminating hold failed", "session_id", c.b.SessionID, "error", err)
	}
	c.finish()
}

// ---- event text ---------------------------------------------------------------

func endToken(s string) string {
	if !endTokenPattern.MatchString(s) {
		return "ended"
	}
	return s
}

func truncateEventData(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// endEventReason is the last_error recorded for a session whose call ended on its
// own: the node's end_reason token, with the provider state or media reason it
// attached, in the form primary-hosted voice already records ("activation_failed:
// login_wall"). Every part is a validated token; nothing else of the event is kept.
func endEventReason(ev *helperv1.VoiceEvent) string {
	reason := endToken(ev.GetEndReason())
	if len(ev.GetDataJson()) > remoteEventDataMax {
		return reason
	}
	var data struct {
		ProviderState string `json:"provider_state"`
		Reason        string `json:"reason"`
	}
	if json.Unmarshal([]byte(ev.GetDataJson()), &data) != nil {
		return reason
	}
	switch {
	case reason == "activation_failed" && endTokenPattern.MatchString(data.ProviderState):
		return reason + ": " + data.ProviderState
	case reason == "media_ended" && endTokenPattern.MatchString(data.Reason):
		return reason + ": " + data.Reason
	}
	return reason
}

// ---- routing ------------------------------------------------------------------

// LocalMedia is the primary's own media plane (voice.MediaHub): it terminates the
// media of sessions hosted on this gateway.
type LocalMedia interface {
	HandleOffer(ctx context.Context, session Session, sdpOffer string) (string, error)
}

// NodeRouter is the gateway's media plane when helper-hosted voice is on: a session
// bound to a Helper Node (Session.NodeID) goes to the RemoteNegotiator, every other
// session to the primary's own hub, exactly as before.
type NodeRouter struct {
	Local  LocalMedia
	Remote *RemoteNegotiator
}

// HostsNodeSessions marks the router as a media plane that can host node sessions.
func (r *NodeRouter) HostsNodeSessions() bool { return r.Remote != nil }

// HandleOffer routes by the session's node.
func (r *NodeRouter) HandleOffer(ctx context.Context, s Session, sdpOffer string) (string, error) {
	if s.NodeID != "" {
		if r.Remote == nil {
			return "", ErrNotNodeBound
		}
		return r.Remote.HandleOffer(ctx, s, sdpOffer)
	}
	if r.Local == nil {
		return "", ErrRelayUnavailable
	}
	return r.Local.HandleOffer(ctx, s, sdpOffer)
}

// ClientICEServers are the servers the client of the session configures.
func (r *NodeRouter) ClientICEServers(s Session) []ICEServer {
	if s.NodeID != "" && r.Remote != nil {
		return r.Remote.ClientICEServers(s)
	}
	if p, ok := r.Local.(interface{ ClientICEServers(Session) []ICEServer }); ok {
		return p.ClientICEServers(s)
	}
	return nil
}

// Disconnect closes the media of a session on this replica, wherever it is hosted.
func (r *NodeRouter) Disconnect(sessionID string) {
	if d, ok := r.Local.(interface{ Disconnect(string) }); ok {
		d.Disconnect(sessionID)
	}
	if r.Remote != nil {
		r.Remote.Disconnect(sessionID)
	}
}

// SetMuted applies mute to a session whose media this gateway hosts itself. A node
// session is muted through MuteSession, which can report failure.
func (r *NodeRouter) SetMuted(sessionID string, muted bool) bool {
	if m, ok := r.Local.(interface{ SetMuted(string, bool) bool }); ok {
		return m.SetMuted(sessionID, muted)
	}
	return false
}

// MuteSession mutes a node session's live media.
func (r *NodeRouter) MuteSession(ctx context.Context, s Session, muted bool) error {
	if r.Remote == nil {
		return ErrNotNodeBound
	}
	return r.Remote.MuteSession(ctx, s, muted)
}

// InterruptSession asks a node session's provider to stop speaking.
func (r *NodeRouter) InterruptSession(ctx context.Context, s Session) error {
	if r.Remote == nil {
		return ErrNotNodeBound
	}
	return r.Remote.InterruptSession(ctx, s)
}

// EndSession ends a node session's call on its node.
func (r *NodeRouter) EndSession(ctx context.Context, s Session, reason string) {
	if r.Remote != nil {
		r.Remote.EndSession(ctx, s, reason)
	}
}

// IssueMediaCredential issues a node session's media credential.
func (r *NodeRouter) IssueMediaCredential(s Session, expires time.Time) (string, bool) {
	if r.Remote == nil {
		return "", false
	}
	return r.Remote.IssueMediaCredential(s, expires)
}
