package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ubag/ubag/apps/gateway/internal/helper"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// The primary's RemoteNegotiator against (a) the REAL helper voice service of
// P5.10 over a gRPC connection, with a fake media endpoint and a fake worker, and
// (b) a scripted helper that can say what a real one never would (a stale event, a
// stream that breaks, a node that vanishes).

const (
	remoteTenant = "tenant-a"
	remoteNode   = "node-a"
	remoteEnv    = "env-1"
	remoteDigest = "a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7"
	remoteMaster = "master-relay-secret-never-sent-to-a-node"
)

func quietRemoteLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// remoteSession reserves one live session on (acct-1, env-1) and binds it to node-a.
func remoteSession(t *testing.T, store *MemoryStore, id string) Session {
	t.Helper()
	now := time.Now().UTC()
	if _, err := store.Reserve(t.Context(), ReserveRequest{
		SessionID: id, TenantID: remoteTenant, AppID: "app-1", Target: "chatgpt_web",
		Placements: []Placement{{Identity: "acct-1", Instance: remoteEnv}}, LeaseTTL: time.Hour, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	bound, err := store.BindNode(t.Context(), remoteTenant, id, remoteNode, time.Minute, now)
	if err != nil || bound.NodeID != remoteNode || bound.LeaseGeneration != 1 {
		t.Fatalf("bind = %+v, %v", bound, err)
	}
	return bound
}

func remoteConfig(store Store, dial RemoteDialFunc) RemoteConfig {
	return RemoteConfig{
		Store:  store,
		Minter: HelperVoiceMinter{Master: []byte(remoteMaster), ICE: &ICEConfig{STUNURLs: []string{"stun:stun.example.org:3478"}}},
		Dial:   dial,
		Endpoint: func(context.Context, string) (string, error) {
			return "bufnet", nil
		},
		ProfileRef: func(_ context.Context, tenantID, target, identityRef, nodeID string) (string, error) {
			if tenantID != remoteTenant || target != "chatgpt_web" || identityRef != "acct-1" || nodeID != remoteNode {
				return "", ErrNoProfile
			}
			return "pr_acct1", nil
		},
		WorkloadVersion: "w1", RegistryDigest: remoteDigest,
		LeaseWindow: 1200 * time.Millisecond, RenewEvery: 100 * time.Millisecond, MaxClockSkew: 200 * time.Millisecond,
		OfferTimeout: 3 * time.Second, RPCTimeout: 2 * time.Second,
	}
}

// ---- the real helper voice service -------------------------------------------------

// fakeMedia is the node's media endpoint (helper.VoiceMedia): the WebRTC side.
type fakeMedia struct {
	mu      sync.Mutex
	opens   []helper.VoiceCall
	sinks   map[string]helper.VoiceSink
	offers  []string
	reoffer int
	muted   map[string]bool
	closed  []string
}

func newFakeMedia() *fakeMedia {
	return &fakeMedia{sinks: map[string]helper.VoiceSink{}, muted: map[string]bool{}}
}

func (m *fakeMedia) Open(_ context.Context, call helper.VoiceCall, sdp string, sink helper.VoiceSink) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.opens = append(m.opens, call)
	m.offers = append(m.offers, sdp)
	m.sinks[call.Key()] = sink
	return "v=0\r\nanswer-of-" + call.NodeID, nil
}

func (m *fakeMedia) Reoffer(_ context.Context, key, sdp string, muted bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reoffer++
	m.offers = append(m.offers, sdp)
	m.muted[key] = muted
	return "v=0\r\nreanswer", nil
}

func (m *fakeMedia) SetMuted(key string, muted bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.muted[key] = muted
	return true
}

func (m *fakeMedia) Close(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = append(m.closed, key)
}

func (m *fakeMedia) opened() []helper.VoiceCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]helper.VoiceCall(nil), m.opens...)
}

func (m *fakeMedia) sink(key string) helper.VoiceSink {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sinks[key]
}

func (m *fakeMedia) isMuted(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.muted[key]
}

func (m *fakeMedia) closes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.closed)
}

// voiceWorker is the node's worker: voice.activate and voice.deactivate.
type voiceWorker struct {
	mu      sync.Mutex
	actions []string
	refs    []string
	// deactivateGate, when set, holds every voice.deactivate until it is closed: the
	// provider is still being torn down.
	deactivateGate chan struct{}
}

func (w *voiceWorker) Run(ctx context.Context, spec helper.AttemptSpec, emit helper.EmitFunc) error {
	var in struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal([]byte(spec.InputJSON), &in)
	w.mu.Lock()
	w.actions = append(w.actions, in.Action)
	w.refs = append(w.refs, spec.IdentityRef)
	gate := w.deactivateGate
	w.mu.Unlock()
	if in.Action == "deactivate" && gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}
	state := map[string]string{"activate": "activated", "deactivate": "deactivated"}[in.Action]
	return emit(helper.Event{
		Type:    helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL,
		Outcome: &helperv1.AttemptOutcome{Status: helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED, ResultJson: `{"state":"` + state + `"}`},
	})
}

func (w *voiceWorker) ran(action string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, a := range w.actions {
		if a == action {
			n++
		}
	}
	return n
}

// bufConn is a RemoteConn over an in-memory gRPC connection to the real service.
type bufConn struct {
	helperv1.HelperServiceClient
	helperv1.HelperVoiceServiceClient
	cc *grpc.ClientConn
}

func (c *bufConn) Close() error { return c.cc.Close() }

type remoteRig struct {
	t      *testing.T
	store  *MemoryStore
	media  *fakeMedia
	worker *voiceWorker
	srv    *helper.Server
	lis    *bufconn.Listener
	dials  atomic.Int64
	neg    *RemoteNegotiator
	sess   Session
}

func newRemoteRig(t *testing.T, mut ...func(*RemoteConfig, *helper.Config)) *remoteRig {
	t.Helper()
	r := &remoteRig{t: t, store: NewMemoryStore(), media: newFakeMedia(), worker: &voiceWorker{}}
	hcfg := helper.Config{
		NodeID: remoteNode, WorkloadVersion: "w1", RegistryDigest: remoteDigest, Logger: quietRemoteLogger(), MaxAttempts: 2,
		Runner: r.worker,
		Voice: &helper.VoiceConfig{Media: r.media, Environments: []helper.VoiceEnvironment{
			{ID: remoteEnv, CDPEndpoint: "http://127.0.0.1:9222", RelayAddr: "127.0.0.1:9099", RelayKeyFile: "relay-key"},
		}},
	}
	rcfg := remoteConfig(r.store, func(context.Context, string, string) (RemoteConn, error) { return r.dial() })
	for _, m := range mut {
		m(&rcfg, &hcfg)
	}
	var err error
	if r.srv, err = helper.NewServer(hcfg); err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	helperv1.RegisterHelperServiceServer(gs, r.srv)
	r.srv.RegisterVoice(gs)
	r.lis = bufconn.Listen(1 << 20)
	go func() { _ = gs.Serve(r.lis) }()
	r.sess = remoteSession(t, r.store, "voice_1700000000_aaaaaaaaaaaa")
	if r.neg, err = NewRemoteNegotiator(rcfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.neg.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.srv.Shutdown(ctx)
		gs.Stop()
	})
	return r
}

func (r *remoteRig) dial() (RemoteConn, error) {
	r.dials.Add(1)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return r.lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &bufConn{HelperServiceClient: helperv1.NewHelperServiceClient(cc), HelperVoiceServiceClient: helperv1.NewHelperVoiceServiceClient(cc), cc: cc}, nil
}

func (r *remoteRig) stored() Session { return mustGet(r.t, r.store, remoteTenant, r.sess.ID) }

func (r *remoteRig) call() helper.VoiceCall {
	r.t.Helper()
	opened := r.media.opened()
	if len(opened) != 1 {
		r.t.Fatalf("the node opened %d calls, want 1", len(opened))
	}
	return opened[0]
}

func (r *remoteRig) offer() string {
	r.t.Helper()
	answer, err := r.neg.HandleOffer(r.t.Context(), r.sess, "v=0\r\noffer")
	if err != nil {
		r.t.Fatalf("HandleOffer: %v", err)
	}
	return answer
}

func TestRemoteOfferIsRoutedToTheSessionsNodeWithPerAttemptCredentialsOnly(t *testing.T) {
	r := newRemoteRig(t)
	if answer := r.offer(); !strings.Contains(answer, "answer-of-"+remoteNode) {
		t.Fatalf("answer = %q (it must be the node's)", answer)
	}
	call := r.call()
	if call.NodeID != remoteNode || call.SessionID != r.sess.ID || call.TenantID != remoteTenant || call.Generation != 1 ||
		call.AttemptID != AttemptIDFor(r.sess.ID, 1) || call.Target != "chatgpt_web" {
		t.Fatalf("the node was handed %+v", call)
	}
	if call.InstanceRef != remoteEnv || call.Env.ID != remoteEnv {
		t.Fatalf("the environment is the session's instance: %+v", call)
	}
	if call.IdentityRef != "pr_acct1" {
		t.Fatalf("the node's identity gate key is the profile_ref, got %q", call.IdentityRef)
	}
	// The node holds per-attempt keys, never the primary's secret, and the two keys differ.
	rk, mk := call.Credentials.RelayKey, call.Credentials.MediaKey
	if len(rk) == 0 || bytes.Equal(rk, mk) || bytes.Contains(rk, []byte(remoteMaster)) || bytes.Contains(mk, []byte(remoteMaster)) {
		t.Fatalf("credentials: relay=%q media=%q", rk, mk)
	}
	want, err := DeriveAttemptKey([]byte(remoteMaster), keyPurposeRelay, AttemptBinding{
		SessionID: r.sess.ID, TenantID: remoteTenant, AttemptID: call.AttemptID, NodeID: remoteNode, LeaseGeneration: 1,
		Expires: r.sess.CreatedAt.Add(RemoteCallMaxAge),
	})
	if err != nil || string(rk) != want {
		t.Fatalf("the relay key is not the derived per-attempt key: %v", err)
	}
	// The provider voice UI is activated by the node, once, on its own browser.
	eventually(t, "the node activates the provider", func() bool { return r.worker.ran("activate") == 1 })
	if r.worker.refs[0] != "pr_acct1" {
		t.Fatalf("the worker ran for identity %q", r.worker.refs[0])
	}
	// The store's media lease now follows the node's short lease, not the admission window.
	if got := r.stored(); got.MediaLeaseExpires.After(time.Now().Add(2 * time.Second)) {
		t.Fatalf("media lease %v was not brought in line with the lease window", got.MediaLeaseExpires)
	}
	// The credential the client gets verifies on the node (keyed by the media key, no app id on the node).
	cred, ok := r.neg.IssueMediaCredential(r.sess, time.Now().Add(time.Minute))
	if !ok {
		t.Fatal("no media credential")
	}
	node := HelperVoiceSpec{MediaKey: string(mk)}.MediaCredentialVerifier()
	if !node(Session{ID: r.sess.ID, TenantID: remoteTenant}, cred) {
		t.Fatal("the node cannot verify the client's media credential")
	}
	if node(Session{ID: r.sess.ID, TenantID: "tenant-b"}, cred) {
		t.Fatal("the media credential verified for another tenant")
	}
	// A restarted primary derives the same credential from the record alone.
	again, err := NewRemoteNegotiator(remoteConfig(r.store, nil2dial))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if c2, ok := again.IssueMediaCredential(r.sess, time.Now().Add(time.Minute)); !ok || !node(Session{ID: r.sess.ID, TenantID: remoteTenant}, c2) {
		t.Fatal("another replica cannot issue a credential the node verifies")
	}
}

func nil2dial(context.Context, string, string) (RemoteConn, error) { return nil, errors.New("no dial") }

func TestRemoteCallConnectsAndMutesThroughFencedEventsFromTheNode(t *testing.T) {
	r := newRemoteRig(t)
	r.offer()
	call := r.call()
	eventually(t, "activation", func() bool { return r.worker.ran("activate") == 1 })
	r.media.sink(call.Key()).PeerConnected() // the client's peer connection is up
	eventually(t, "connected", func() bool { return r.stored().Status == StatusConnected })

	// The client mutes itself over its data channel: the node reports it, the primary records it, fenced.
	r.media.sink(call.Key()).ClientMuted(true)
	eventually(t, "mute recorded", func() bool { return r.stored().Muted })
	r.media.sink(call.Key()).ClientMuted(false)
	eventually(t, "unmute recorded", func() bool { return !r.stored().Muted })
}

func TestRemoteMuteIsForwardedAndMustBeConfirmedByTheNode(t *testing.T) {
	r := newRemoteRig(t)
	r.offer()
	call := r.call()
	if err := r.neg.MuteSession(t.Context(), r.sess, true); err != nil {
		t.Fatalf("MuteSession: %v", err)
	}
	if !r.media.isMuted(call.Key()) {
		t.Fatal("the node's microphone path is not muted")
	}
	if err := r.neg.MuteSession(t.Context(), r.sess, false); err != nil || r.media.isMuted(call.Key()) {
		t.Fatalf("unmute: %v, muted=%v", err, r.media.isMuted(call.Key()))
	}
	// A node that cannot be reached must not be reported as muted (a replica with no supervised
	// call has to dial, and the dial fails).
	cfg := remoteConfig(r.store, nil2dial)
	other, err := NewRemoteNegotiator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.MuteSession(t.Context(), r.sess, true); !errors.Is(err, ErrNodeUnavailable) {
		t.Fatalf("mute with the node unreachable = %v, want ErrNodeUnavailable", err)
	}
	if r.media.isMuted(call.Key()) {
		t.Fatal("the unreachable replica's mute reached the node")
	}
}

func TestRemoteMuteBeforeTheMediaExistsRidesTheOffer(t *testing.T) {
	r := newRemoteRig(t)
	if err := r.neg.MuteSession(t.Context(), r.sess, true); err != nil {
		t.Fatalf("a mute before any media exists is not an error: %v", err)
	}
	r.sess.Muted = true // what the store hands the connect handler after the handler recorded the flag
	r.offer()
	call := r.call()
	if !r.media.isMuted(call.Key()) {
		t.Fatal("a session muted before it connected opened with a live microphone")
	}
}

func TestRemoteInterruptIsRefusedHonestly(t *testing.T) {
	r := newRemoteRig(t)
	r.offer()
	if err := r.neg.InterruptSession(t.Context(), r.sess); !errors.Is(err, ErrInterruptUnsupported) {
		t.Fatalf("interrupt = %v, want ErrInterruptUnsupported (the node has no barge-in primitive)", err)
	}
}

func TestRemoteReconnectIsAnICERestartOnTheOwningNodeAndNeverReactivates(t *testing.T) {
	r := newRemoteRig(t)
	r.offer()
	eventually(t, "activation", func() bool { return r.worker.ran("activate") == 1 })
	dials := r.dials.Load()
	answer, err := r.neg.HandleOffer(t.Context(), r.sess, "v=0\r\nrestart")
	if err != nil || !strings.Contains(answer, "reanswer") {
		t.Fatalf("reconnect = %q, %v", answer, err)
	}
	if r.media.reoffer != 1 || len(r.media.opened()) != 1 {
		t.Fatalf("reoffers=%d opens=%d: an ICE restart must renegotiate the live call", r.media.reoffer, len(r.media.opened()))
	}
	if r.dials.Load() != dials {
		t.Fatal("the restart dialed again instead of using the call's connection")
	}
	time.Sleep(50 * time.Millisecond)
	if r.worker.ran("activate") != 1 {
		t.Fatalf("the provider voice UI was activated %d times", r.worker.ran("activate"))
	}
	// A replica that never saw the first offer (restart) offers again under the same fence: also a renegotiation.
	other, err := NewRemoteNegotiator(remoteConfig(r.store, func(context.Context, string, string) (RemoteConn, error) { return r.dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.HandleOffer(t.Context(), r.sess, "v=0\r\nafter-restart"); err != nil {
		t.Fatalf("offer after a restart: %v", err)
	}
	if len(r.media.opened()) != 1 || r.media.reoffer != 2 {
		t.Fatalf("opens=%d reoffers=%d: a second offer under the same fence must not start a second call", len(r.media.opened()), r.media.reoffer)
	}
}

func TestRemoteTerminateHoldsTheLeasesUntilTheNodeAcksTheDeactivation(t *testing.T) {
	r := newRemoteRig(t)
	gate := make(chan struct{})
	r.worker.deactivateGate = gate
	r.offer()
	eventually(t, "activation", func() bool { return r.worker.ran("activate") == 1 })
	ctx := t.Context()
	if err := r.store.BeginTerminate(ctx, remoteTenant, r.sess.ID, time.Now().UTC(), "terminated_by_client", TerminatingHoldMax); err != nil {
		t.Fatal(err)
	}
	r.neg.EndSession(ctx, r.stored(), "terminated_by_client") // the record already reads terminated
	eventually(t, "the node closes the media", func() bool { return r.media.closes() >= 1 })
	eventually(t, "the node starts deactivating the provider", func() bool { return r.worker.ran("deactivate") == 1 })
	// The media is down but the provider is still being torn down: the account and the
	// environment stay held, so a successor cannot activate on a half-closed provider UI.
	time.Sleep(150 * time.Millisecond)
	if holders, err := r.store.ListLeaseHolders(ctx, time.Now().UTC()); err != nil || len(holders) != 1 {
		t.Fatalf("holders = %+v, %v: the hold must outlive the media until the node acks", holders, err)
	}
	if blocked := reserveAt(t, r.store, "voice_blocked", remoteTenant, time.Now().UTC(), time.Hour, Placement{"acct-1", remoteEnv}); blocked.Status != StatusQueued {
		t.Fatalf("a successor took the account while the provider was still being torn down: %+v", blocked)
	}
	close(gate) // the provider left voice mode: the node writes ENDED, the primary releases the hold
	eventually(t, "the hold is released on the node's ack", func() bool {
		holders, err := r.store.ListLeaseHolders(ctx, time.Now().UTC())
		return err == nil && len(holders) == 0
	})
	// A successor can take the account at once.
	next := reserveAt(t, r.store, "voice_next", remoteTenant, time.Now().UTC(), time.Hour, Placement{"acct-1", remoteEnv})
	if next.Status != StatusConnecting {
		t.Fatalf("the successor queued behind a released hold: %+v", next)
	}
}

func TestRemoteTerminateFromAReplicaThatDidNotServeTheConnectStillReleasesTheHold(t *testing.T) {
	r := newRemoteRig(t)
	r.offer()
	eventually(t, "activation", func() bool { return r.worker.ran("activate") == 1 })
	other, err := NewRemoteNegotiator(remoteConfig(r.store, func(context.Context, string, string) (RemoteConn, error) { return r.dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := t.Context()
	if err := r.store.BeginTerminate(ctx, remoteTenant, r.sess.ID, time.Now().UTC(), "terminated_by_client", TerminatingHoldMax); err != nil {
		t.Fatal(err)
	}
	other.EndSession(ctx, r.stored(), "terminated_by_client")
	eventually(t, "the hold is released by the replica that ended the call", func() bool {
		holders, err := r.store.ListLeaseHolders(ctx, time.Now().UTC())
		return err == nil && len(holders) == 0
	})
}

func TestRemoteTerminateOfASessionThatNeverOfferedReleasesAtOnce(t *testing.T) {
	r := newRemoteRig(t)
	ctx := t.Context()
	if err := r.store.BeginTerminate(ctx, remoteTenant, r.sess.ID, time.Now().UTC(), "terminated_by_client", TerminatingHoldMax); err != nil {
		t.Fatal(err)
	}
	r.neg.EndSession(ctx, r.stored(), "terminated_by_client")
	eventually(t, "nothing runs on the node, so nothing is held", func() bool {
		holders, err := r.store.ListLeaseHolders(ctx, time.Now().UTC())
		return err == nil && len(holders) == 0
	})
	if len(r.media.opened()) != 0 || r.worker.ran("deactivate") != 0 {
		t.Fatal("ending a call that never started touched the node's provider")
	}
}

func TestRemoteMediaEndingOnItsOwnEndsTheRecord(t *testing.T) {
	r := newRemoteRig(t)
	r.offer()
	call := r.call()
	eventually(t, "activation", func() bool { return r.worker.ran("activate") == 1 })
	r.media.sink(call.Key()).MediaEnded("relay_recv_failed")
	eventually(t, "the session ends with the node's reason", func() bool { return r.stored().Status == StatusTerminated })
	if got := r.stored().LastError; got != "media_ended: relay_recv_failed" {
		t.Fatalf("last_error = %q", got)
	}
	// The node already tore the provider down (ENDED is written last): nothing is held.
	holders, _ := r.store.ListLeaseHolders(t.Context(), time.Now().UTC())
	if len(holders) != 0 {
		t.Fatalf("a call that ended on its own left %+v held", holders)
	}
	eventually(t, "the supervisor is gone", func() bool { return r.neg.callFor(r.sess.ID, call.AttemptID) == nil })
}

func TestRemoteNodeEndsTheCallItselfWhenRenewalsStopAndTheRecordFollows(t *testing.T) {
	r := newRemoteRig(t)
	r.offer()
	call := r.call()
	eventually(t, "activation", func() bool { return r.worker.ran("activate") == 1 })
	// While the primary renews the call outlives the whole lease window.
	time.Sleep(1500 * time.Millisecond)
	if r.stored().Status == StatusTerminated || r.media.closes() != 0 {
		t.Fatalf("a renewed call was ended: %+v closes=%d", r.stored(), r.media.closes())
	}
	// Now the primary stops (its supervisors are cancelled; nothing ends the call on the node).
	sup := r.neg.callFor(r.sess.ID, call.AttemptID)
	if sup == nil {
		t.Fatal("no supervised call")
	}
	sup.cancel()
	eventually(t, "the node's dead-man closes the media with no help from the primary", func() bool { return r.media.closes() >= 1 })
	eventually(t, "the node deactivates the provider itself", func() bool { return r.worker.ran("deactivate") == 1 })
}

func TestRemoteCrossTenantAndStaleLeasesNeverTouchTheNode(t *testing.T) {
	r := newRemoteRig(t)
	r.offer()
	call := r.call()
	ctx := t.Context()
	foreign := r.sess
	foreign.TenantID = "tenant-b"
	missing := r.sess
	missing.ID = "voice_1700000000_nothere00000"

	_, errForeign := r.neg.HandleOffer(ctx, foreign, "v=0")
	_, errMissing := r.neg.HandleOffer(ctx, missing, "v=0")
	if !errors.Is(errForeign, ErrNotFound) || !errors.Is(errMissing, ErrNotFound) || errForeign.Error() != errMissing.Error() {
		t.Fatalf("foreign = %v, missing = %v: another tenant's session must read as not found, identically", errForeign, errMissing)
	}
	errForeign, errMissing = r.neg.MuteSession(ctx, foreign, true), r.neg.MuteSession(ctx, missing, true)
	if !errors.Is(errForeign, ErrNotFound) || !errors.Is(errMissing, ErrNotFound) || errForeign.Error() != errMissing.Error() {
		t.Fatalf("mute: foreign = %v, missing = %v", errForeign, errMissing)
	}
	if r.media.isMuted(call.Key()) {
		t.Fatal("another tenant muted the call")
	}

	// The session is re-leased to another node: the old record's fence is stale and must never reach any node.
	rebound, err := r.store.BindNode(ctx, remoteTenant, r.sess.ID, "node-b", time.Minute, time.Now().UTC())
	if err != nil || rebound.LeaseGeneration != 2 {
		t.Fatalf("rebind: %+v, %v", rebound, err)
	}
	before := r.dials.Load()
	if err := r.neg.MuteSession(ctx, r.sess, true); !errors.Is(err, ErrNodeMismatch) {
		t.Fatalf("a stale mute = %v, want ErrNodeMismatch", err)
	}
	if _, err := r.neg.HandleOffer(ctx, r.sess, "v=0"); !errors.Is(err, ErrNodeMismatch) {
		t.Fatalf("a stale offer = %v, want ErrNodeMismatch", err)
	}
	r.neg.EndSession(ctx, r.sess, "x")
	time.Sleep(100 * time.Millisecond)
	if r.dials.Load() != before || r.media.isMuted(call.Key()) {
		t.Fatal("a stale lease reached a node")
	}
	// The old supervisor notices it is not the holder, ends ITS OWN call (under its own fence, so
	// a node that has moved on to the successor refuses it) and stops.
	eventually(t, "the stale supervisor stops", func() bool { return r.neg.callFor(r.sess.ID, call.AttemptID) == nil })
	eventually(t, "the replaced holder's own call is ended on its node", func() bool { return r.media.closes() >= 1 })
	if got := r.stored(); got.Status == StatusTerminated || got.NodeID != "node-b" {
		t.Fatalf("the stale supervisor changed the successor's session: %+v", got)
	}
}

func TestRemoteIncompatibleOrUnreachableNodesAreRefusedBeforeAnyMediaStarts(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*RemoteConfig, *helper.Config)
	}{
		{"another workload", func(c *RemoteConfig, _ *helper.Config) { c.WorkloadVersion = "w2" }},
		{"another registry", func(c *RemoteConfig, _ *helper.Config) { c.RegistryDigest = strings.Repeat("b", 64) }},
		{"no voice service on the node", func(_ *RemoteConfig, h *helper.Config) { h.Voice = nil }},
		{"no endpoint known", func(c *RemoteConfig, _ *helper.Config) {
			c.Endpoint = func(context.Context, string) (string, error) { return "", errors.New("unknown node") }
		}},
		{"no profile on the node", func(c *RemoteConfig, _ *helper.Config) {
			c.ProfileRef = func(context.Context, string, string, string, string) (string, error) { return "", ErrNoProfile }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRemoteRig(t, tc.mut)
			_, err := r.neg.HandleOffer(t.Context(), r.sess, "v=0")
			if err == nil {
				t.Fatal("the offer was accepted")
			}
			if len(r.media.opened()) != 0 || r.neg.callFor(r.sess.ID, AttemptIDFor(r.sess.ID, 1)) != nil {
				t.Fatal("a refused offer left a call behind")
			}
			if got := r.stored(); got.Status != StatusConnecting {
				t.Fatalf("a refused offer changed the session: %+v", got)
			}
		})
	}
}

func TestRemoteACallOlderThanItsCredentialsIsRefused(t *testing.T) {
	r := newRemoteRig(t)
	old := r.sess
	old.CreatedAt = time.Now().Add(-RemoteCallMaxAge - time.Minute)
	if _, err := r.neg.HandleOffer(t.Context(), old, "v=0"); !errors.Is(err, ErrAttemptExpired) {
		t.Fatalf("offer = %v, want ErrAttemptExpired", err)
	}
	if _, ok := r.neg.IssueMediaCredential(old, time.Now().Add(time.Minute)); ok {
		t.Fatal("a credential was issued past the attempt's expiry")
	}
	unbound := r.sess
	unbound.NodeID, unbound.LeaseGeneration = "", 0
	if _, err := r.neg.HandleOffer(t.Context(), unbound, "v=0"); !errors.Is(err, ErrNotNodeBound) {
		t.Fatalf("offer for a primary-hosted session = %v, want ErrNotNodeBound", err)
	}
}

// ---- routing -----------------------------------------------------------------------

type localMedia struct {
	mu       sync.Mutex
	offers   []string
	muted    map[string]bool
	dropped  []string
	iceFor   []string
	answerOf string
}

func (l *localMedia) HandleOffer(_ context.Context, s Session, _ string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.offers = append(l.offers, s.ID)
	return l.answerOf, nil
}
func (l *localMedia) Disconnect(id string) {
	l.mu.Lock()
	l.dropped = append(l.dropped, id)
	l.mu.Unlock()
}
func (l *localMedia) SetMuted(id string, muted bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.muted == nil {
		l.muted = map[string]bool{}
	}
	l.muted[id] = muted
	return true
}
func (l *localMedia) ClientICEServers(s Session) []ICEServer {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.iceFor = append(l.iceFor, s.ID)
	return []ICEServer{{URLs: []string{"stun:local.example.org:3478"}}}
}

func TestNodeRouterSendsNodeBoundSessionsToTheirNodeAndEverythingElseToThePrimaryHub(t *testing.T) {
	r := newRemoteRig(t)
	local := &localMedia{answerOf: "v=0\r\nlocal-answer"}
	router := &NodeRouter{Local: local, Remote: r.neg}
	if !router.HostsNodeSessions() {
		t.Fatal("a router with a remote negotiator hosts node sessions")
	}
	if answer, err := router.HandleOffer(t.Context(), r.sess, "v=0"); err != nil || !strings.Contains(answer, "answer-of-"+remoteNode) {
		t.Fatalf("node-bound offer = %q, %v", answer, err)
	}
	if len(local.offers) != 0 {
		t.Fatalf("the primary hub was handed a node-bound session: %v", local.offers)
	}
	hosted := Session{ID: "voice_hosted", TenantID: remoteTenant, Target: "chatgpt_web", Status: StatusConnecting}
	if answer, err := router.HandleOffer(t.Context(), hosted, "v=0"); err != nil || answer != "v=0\r\nlocal-answer" {
		t.Fatalf("primary-hosted offer = %q, %v", answer, err)
	}
	if len(r.media.opened()) != 1 {
		t.Fatalf("a primary-hosted session reached a node: %d calls", len(r.media.opened()))
	}
	if ice := router.ClientICEServers(hosted); len(ice) != 1 || ice[0].URLs[0] != "stun:local.example.org:3478" {
		t.Fatalf("hosted ICE servers = %+v", ice)
	}
	if ice := router.ClientICEServers(r.sess); len(ice) == 0 || ice[0].URLs[0] != "stun:stun.example.org:3478" {
		t.Fatalf("node ICE servers = %+v (the deployment's, not the hub's)", ice)
	}
	if !router.SetMuted("voice_hosted", true) || !local.muted["voice_hosted"] {
		t.Fatal("a primary-hosted mute did not reach the hub")
	}
	router.Disconnect("voice_hosted")
	if len(local.dropped) != 1 {
		t.Fatal("a primary-hosted disconnect did not reach the hub")
	}

	noRemote := &NodeRouter{Local: local}
	if noRemote.HostsNodeSessions() {
		t.Fatal("a router without a remote negotiator cannot host node sessions")
	}
	if _, err := noRemote.HandleOffer(t.Context(), r.sess, "v=0"); !errors.Is(err, ErrNotNodeBound) {
		t.Fatalf("node-bound offer without a remote = %v", err)
	}
}

// ---- a scripted helper: what a real node never says --------------------------------

// scriptedHelper is a RemoteConn whose event log, renewals and refusals the test controls.
type scriptedHelper struct {
	mu      sync.Mutex
	changed chan struct{}
	events  []*helperv1.VoiceEvent
	rpcs    []string
	afters  []uint64
	fences  []*helperv1.VoiceFence
	broken  int // streams attached before this generation of the stream break are cut

	hs          *helperv1.HandshakeResponse
	offerErr    error
	renewErr    func() error
	controlResp func(*helperv1.ControlVoiceRequest) (*helperv1.ControlVoiceResponse, error)
	closes      atomic.Int32
}

func newScriptedHelper() *scriptedHelper {
	return &scriptedHelper{changed: make(chan struct{}), hs: &helperv1.HandshakeResponse{
		ProtocolVersion: "ubag.helper.v1", NodeId: remoteNode, WorkloadVersion: "w1", RegistryDigest: remoteDigest,
		Features: []string{"helper_voice"},
	}}
}

func (h *scriptedHelper) log(rpc string, f *helperv1.VoiceFence) {
	h.mu.Lock()
	h.rpcs = append(h.rpcs, rpc)
	h.fences = append(h.fences, f)
	h.mu.Unlock()
}

func (h *scriptedHelper) count(rpc string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.rpcs {
		if r == rpc {
			n++
		}
	}
	return n
}

func (h *scriptedHelper) Handshake(_ context.Context, _ *helperv1.HandshakeRequest, _ ...grpc.CallOption) (*helperv1.HandshakeResponse, error) {
	return &helperv1.HandshakeResponse{
		ProtocolVersion: h.hs.GetProtocolVersion(), NodeId: h.hs.GetNodeId(), WorkloadVersion: h.hs.GetWorkloadVersion(),
		RegistryDigest: h.hs.GetRegistryDigest(), Features: h.hs.GetFeatures(), ServerTime: timestamppb.Now(),
	}, nil
}

func (h *scriptedHelper) OfferVoice(_ context.Context, in *helperv1.OfferVoiceRequest, _ ...grpc.CallOption) (*helperv1.OfferVoiceResponse, error) {
	h.log("offer", in.GetFence())
	if h.offerErr != nil {
		return nil, h.offerErr
	}
	return &helperv1.OfferVoiceResponse{SdpAnswer: "v=0\r\nscripted", RelayReady: true}, nil
}

func (h *scriptedHelper) ReconnectVoice(_ context.Context, in *helperv1.ReconnectVoiceRequest, _ ...grpc.CallOption) (*helperv1.ReconnectVoiceResponse, error) {
	h.log("reconnect", in.GetFence())
	return &helperv1.ReconnectVoiceResponse{SdpAnswer: "v=0\r\nrescripted"}, nil
}

func (h *scriptedHelper) ControlVoice(_ context.Context, in *helperv1.ControlVoiceRequest, _ ...grpc.CallOption) (*helperv1.ControlVoiceResponse, error) {
	h.log("control:"+in.GetOp().String(), in.GetFence())
	if h.controlResp != nil {
		return h.controlResp(in)
	}
	return &helperv1.ControlVoiceResponse{State: helperv1.VoiceState_VOICE_STATE_ENDED}, nil
}

func (h *scriptedHelper) RenewVoice(_ context.Context, in *helperv1.RenewVoiceRequest, _ ...grpc.CallOption) (*helperv1.RenewVoiceResponse, error) {
	h.log("renew", in.GetFence())
	if h.renewErr != nil {
		if err := h.renewErr(); err != nil {
			return nil, err
		}
	}
	return &helperv1.RenewVoiceResponse{LeaseGeneration: in.GetFence().GetLeaseGeneration(), ExpiresAt: in.GetExpiresAt(), State: helperv1.VoiceState_VOICE_STATE_CONNECTING}, nil
}

func (h *scriptedHelper) Close() error { h.closes.Add(1); return nil }

// emit appends one event to the node's log, as the node would.
func (h *scriptedHelper) emit(attempt string, generation uint64, t helperv1.VoiceEventType, endReason, dataJSON string) {
	h.mu.Lock()
	h.events = append(h.events, &helperv1.VoiceEvent{
		AttemptId: attempt, Sequence: uint64(len(h.events)) + 1, LeaseGeneration: generation, Type: t,
		CreatedAt: timestamppb.Now(), EndReason: endReason, DataJson: dataJSON,
	})
	close(h.changed)
	h.changed = make(chan struct{})
	h.mu.Unlock()
}

// breakStreams cuts every attached stream (a network blip); the next attach replays.
func (h *scriptedHelper) breakStreams() {
	h.mu.Lock()
	h.broken++
	close(h.changed)
	h.changed = make(chan struct{})
	h.mu.Unlock()
}

type scriptedStream struct {
	grpc.ClientStream
	h      *scriptedHelper
	ctx    context.Context
	after  uint64
	idx    int
	broken int
}

func (h *scriptedHelper) StreamVoiceEvents(ctx context.Context, in *helperv1.StreamVoiceEventsRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[helperv1.StreamVoiceEventsResponse], error) {
	h.log("stream", in.GetFence())
	h.mu.Lock()
	h.afters = append(h.afters, in.GetAfterSequence())
	broken := h.broken
	h.mu.Unlock()
	return &scriptedStream{h: h, ctx: ctx, after: in.GetAfterSequence(), broken: broken}, nil
}

func (s *scriptedStream) Recv() (*helperv1.StreamVoiceEventsResponse, error) {
	for {
		s.h.mu.Lock()
		if s.h.broken != s.broken {
			s.h.mu.Unlock()
			return nil, status.Error(codes.Unavailable, "the stream broke")
		}
		for s.idx < len(s.h.events) {
			ev := s.h.events[s.idx]
			s.idx++
			if ev.GetSequence() > s.after {
				s.h.mu.Unlock()
				return &helperv1.StreamVoiceEventsResponse{Event: ev}, nil
			}
		}
		more := s.h.changed
		s.h.mu.Unlock()
		select {
		case <-more:
		case <-s.ctx.Done():
			return nil, status.FromContextError(s.ctx.Err()).Err()
		}
	}
}

type scriptedRig struct {
	t     *testing.T
	store *MemoryStore
	h     *scriptedHelper
	neg   *RemoteNegotiator
	sess  Session
	att   string
}

func newScriptedRig(t *testing.T, mut ...func(*RemoteConfig, *scriptedHelper)) *scriptedRig {
	t.Helper()
	r := &scriptedRig{t: t, store: NewMemoryStore(), h: newScriptedHelper()}
	cfg := remoteConfig(r.store, func(context.Context, string, string) (RemoteConn, error) { return r.h, nil })
	for _, m := range mut {
		m(&cfg, r.h)
	}
	r.sess = remoteSession(t, r.store, "voice_1700000000_bbbbbbbbbbbb")
	r.att = AttemptIDFor(r.sess.ID, 1)
	var err error
	if r.neg, err = NewRemoteNegotiator(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.neg.Close)
	return r
}

func (r *scriptedRig) offer() {
	r.t.Helper()
	if _, err := r.neg.HandleOffer(r.t.Context(), r.sess, "v=0"); err != nil {
		r.t.Fatalf("HandleOffer: %v", err)
	}
}

func (r *scriptedRig) stored() Session { return mustGet(r.t, r.store, remoteTenant, r.sess.ID) }

func TestRemoteStaleGenerationAndForeignAttemptEventsAreRejected(t *testing.T) {
	var mu sync.Mutex
	fenced := map[string]int{}
	r := newScriptedRig(t, func(c *RemoteConfig, _ *scriptedHelper) {
		c.OnFenced = func(reason string) { mu.Lock(); fenced[reason]++; mu.Unlock() }
	})
	r.offer()
	connected := helperv1.VoiceEventType_VOICE_EVENT_TYPE_CONNECTED
	muted := helperv1.VoiceEventType_VOICE_EVENT_TYPE_MUTED

	// A node that held an older lease generation, and an attempt that is not this one.
	r.h.emit(r.att, 0, muted, "", "")
	r.h.emit(AttemptIDFor(r.sess.ID, 7), 1, connected, "", "")
	r.h.emit(r.att, 2, connected, "", "")
	eventually(t, "the three events were seen and rejected", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return fenced["stale_generation"] == 2 && fenced["attempt_mismatch"] == 1
	})
	if got := r.stored(); got.Status != StatusConnecting || got.Muted {
		t.Fatalf("a stale event changed the session: %+v", got)
	}
	// The current generation's event still applies afterwards.
	r.h.emit(r.att, 1, connected, "", "")
	eventually(t, "the current event is applied", func() bool { return r.stored().Status == StatusConnected })
}

func TestRemoteTheStoreRejectsAWritePastTheEventCheckToo(t *testing.T) {
	// The session is re-leased while the old supervisor is mid-stream: even an event that
	// looks current to the supervisor cannot write, because the store's fence is the authority.
	var mu sync.Mutex
	fenced := map[string]int{}
	r := newScriptedRig(t, func(c *RemoteConfig, _ *scriptedHelper) {
		c.OnFenced = func(reason string) { mu.Lock(); fenced[reason]++; mu.Unlock() }
		c.RenewEvery = 10 * time.Second // keep the renewal loop out of the way
		c.LeaseWindow = 30 * time.Second
	})
	r.offer()
	if _, err := r.store.BindNode(t.Context(), remoteTenant, r.sess.ID, "node-b", time.Minute, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	r.h.emit(r.att, 1, helperv1.VoiceEventType_VOICE_EVENT_TYPE_CONNECTED, "", "")
	eventually(t, "the store rejected the stale commit", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return fenced["commit_fenced"] == 1
	})
	if got := r.stored(); got.Status != StatusConnecting || got.NodeID != "node-b" {
		t.Fatalf("the stale holder changed the session: %+v", got)
	}
	eventually(t, "the supervisor of the replaced lease stops", func() bool { return r.neg.callFor(r.sess.ID, r.att) == nil })
}

func TestRemoteRenewsTheStoreFirstAndTheNodeSecondAndKeepsBoth(t *testing.T) {
	var order []string
	var mu sync.Mutex
	r := newScriptedRig(t)
	wrapped := &orderStore{Store: r.store, note: func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }}
	r.neg.cfg.Store = wrapped
	r.neg.guard.Store = wrapped
	r.h.renewErr = func() error { wrapped.note("node"); return nil }
	r.offer()
	first := r.stored().MediaLeaseExpires
	eventually(t, "several renewals", func() bool { return r.h.count("renew") >= 3 })
	if !r.stored().MediaLeaseExpires.After(first) {
		t.Fatalf("the media lease was not extended: %v -> %v", first, r.stored().MediaLeaseExpires)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, s := range order {
		if s == "node" && (i == 0 || order[i-1] != "store") {
			t.Fatalf("the node was renewed before the store: %v", order)
		}
	}
	for _, f := range r.h.fences {
		if f.GetAttemptId() != r.att || f.GetLeaseGeneration() != 1 || f.GetNodeId() != remoteNode || f.GetTenantId() != remoteTenant {
			t.Fatalf("a call carried the wrong fence: %+v", f)
		}
		if !f.GetExpiresAt().AsTime().After(time.Now()) {
			t.Fatalf("a call carried an expired fence: %+v", f)
		}
	}
}

// orderStore records when the media lease is renewed.
type orderStore struct {
	Store
	note func(string)
}

func (o *orderStore) RenewMediaLease(ctx context.Context, tenantID, sessionID string, fence LeaseFence, until, now time.Time) error {
	o.note("store")
	return o.Store.RenewMediaLease(ctx, tenantID, sessionID, fence, until, now)
}

func TestRemoteAnUnreachableNodeIsDeclaredLostAfterAWholeLeaseWindow(t *testing.T) {
	var mu sync.Mutex
	fails := map[string]int{}
	r := newScriptedRig(t, func(c *RemoteConfig, h *scriptedHelper) {
		c.LeaseWindow, c.RenewEvery, c.MaxClockSkew = 500*time.Millisecond, 50*time.Millisecond, 100*time.Millisecond
		c.OnRenewFailure = func(reason string) { mu.Lock(); fails[reason]++; mu.Unlock() }
		h.renewErr = func() error { return status.Error(codes.Unavailable, "the wireguard link is down") }
	})
	r.offer()
	eventually(t, "the node is declared lost", func() bool { return r.stored().Status == StatusTerminated })
	if got := r.stored().LastError; got != "helper_unreachable" {
		t.Fatalf("last_error = %q", got)
	}
	holders, _ := r.store.ListLeaseHolders(t.Context(), time.Now().UTC())
	if len(holders) != 0 {
		t.Fatalf("an unreachable node pinned the account: %+v", holders)
	}
	if r.h.count("control:VOICE_CONTROL_OP_TERMINATE") == 0 {
		t.Fatal("the node was not told to end the call (best effort)")
	}
	mu.Lock()
	defer mu.Unlock()
	if fails["error"] == 0 {
		t.Fatalf("renewal failures were not counted: %v", fails)
	}
}

func TestRemoteANodeThatNoLongerHoldsTheCallEndsTheRecordAtOnce(t *testing.T) {
	for _, code := range []codes.Code{codes.NotFound, codes.Aborted} {
		t.Run(code.String(), func(t *testing.T) {
			r := newScriptedRig(t, func(_ *RemoteConfig, h *scriptedHelper) {
				h.renewErr = func() error { return status.Error(code, "no such call") }
			})
			r.offer()
			eventually(t, "the record ends", func() bool { return r.stored().Status == StatusTerminated })
			if got := r.stored().LastError; got != "helper_lost" {
				t.Fatalf("last_error = %q", got)
			}
		})
	}
}

func TestRemoteTheCallDoesNotOutliveItsRecord(t *testing.T) {
	r := newScriptedRig(t)
	// The node acknowledges the end the way a real one does: TERMINATE closes the media, ENDED follows.
	r.h.controlResp = func(in *helperv1.ControlVoiceRequest) (*helperv1.ControlVoiceResponse, error) {
		if in.GetOp() == helperv1.VoiceControlOp_VOICE_CONTROL_OP_TERMINATE {
			r.h.emit(r.att, 1, helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED, "terminated", `{"deactivated":true}`)
		}
		return &helperv1.ControlVoiceResponse{State: helperv1.VoiceState_VOICE_STATE_ENDED}, nil
	}
	r.offer()
	// The sweeper (or another replica) ended the session: the next renewal tells the node to end the call.
	if err := r.store.Terminate(t.Context(), remoteTenant, r.sess.ID, time.Now().UTC(), "lease_expired"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the node is told to end the call", func() bool { return r.h.count("control:VOICE_CONTROL_OP_TERMINATE") == 1 })
	eventually(t, "the supervisor is gone and its connection closed", func() bool {
		return r.neg.callFor(r.sess.ID, r.att) == nil && r.h.closes.Load() >= 1
	})
}

func TestRemoteASupervisorOutlivesAnEndedRecordUntilTheNodeAcksTheTeardown(t *testing.T) {
	// A record that ends while the node is still deactivating the provider keeps its leases (the
	// hold) and its supervisor listening: ENDED is what releases them, not the next renewal tick.
	r := newScriptedRig(t)
	r.offer()
	ctx := t.Context()
	if err := r.store.BeginTerminate(ctx, remoteTenant, r.sess.ID, time.Now().UTC(), "terminated_by_client", TerminatingHoldMax); err != nil {
		t.Fatal(err)
	}
	// Several renewal ticks pass (the record is ended, so each is a conflict): the supervisor must not give up.
	eventually(t, "the node is told to end the call", func() bool { return r.h.count("control:VOICE_CONTROL_OP_TERMINATE") >= 1 })
	time.Sleep(300 * time.Millisecond)
	if r.neg.callFor(r.sess.ID, r.att) == nil {
		t.Fatal("the supervisor gave up before the node acked the teardown")
	}
	if holders, _ := r.store.ListLeaseHolders(ctx, time.Now().UTC()); len(holders) != 1 {
		t.Fatalf("holders = %+v: the hold must last until the node's ack", holders)
	}
	r.h.emit(r.att, 1, helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED, "terminated", `{"deactivated":true}`)
	eventually(t, "the ack releases the hold and ends the supervisor", func() bool {
		holders, _ := r.store.ListLeaseHolders(ctx, time.Now().UTC())
		return len(holders) == 0 && r.neg.callFor(r.sess.ID, r.att) == nil
	})
}

func TestRemoteTheEventStreamReattachesFromTheLastAppliedSequence(t *testing.T) {
	r := newScriptedRig(t)
	r.offer()
	r.h.emit(r.att, 1, helperv1.VoiceEventType_VOICE_EVENT_TYPE_STARTED, "", "")
	r.h.emit(r.att, 1, helperv1.VoiceEventType_VOICE_EVENT_TYPE_MUTED, "", "")
	eventually(t, "the mute is applied", func() bool { return r.stored().Muted })
	r.h.breakStreams()
	r.h.emit(r.att, 1, helperv1.VoiceEventType_VOICE_EVENT_TYPE_UNMUTED, "", "")
	eventually(t, "the unmute arrives after the re-attach", func() bool { return !r.stored().Muted })
	r.h.mu.Lock()
	afters := append([]uint64(nil), r.h.afters...)
	r.h.mu.Unlock()
	if len(afters) < 2 || afters[0] != 0 || afters[len(afters)-1] != 2 {
		t.Fatalf("stream attaches after sequences %v: the re-attach must resume after the last applied event", afters)
	}
}

func TestRemoteAReplayedEventIsAppliedOnce(t *testing.T) {
	r := newScriptedRig(t)
	r.offer()
	connected := helperv1.VoiceEventType_VOICE_EVENT_TYPE_CONNECTED
	r.h.emit(r.att, 1, connected, "", "")
	eventually(t, "connected", func() bool { return r.stored().Status == StatusConnected })
	// The node replays the whole log on a re-attach; sequences already applied are skipped, so
	// a later terminal write by someone else is not undone by an old CONNECTED.
	if err := r.store.Terminate(t.Context(), remoteTenant, r.sess.ID, time.Now().UTC(), "done"); err != nil {
		t.Fatal(err)
	}
	r.h.breakStreams()
	time.Sleep(300 * time.Millisecond)
	if got := r.stored(); got.Status != StatusTerminated {
		t.Fatalf("a replayed event revived the session: %+v", got)
	}
}

func TestRemoteEndedActivationFailureRecordsTheProviderState(t *testing.T) {
	r := newScriptedRig(t)
	r.offer()
	r.h.emit(r.att, 1, helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED, "activation_failed", `{"deactivated":true,"provider_state":"login_wall"}`)
	eventually(t, "the record ends", func() bool { return r.stored().Status == StatusTerminated })
	if got := r.stored().LastError; got != "activation_failed: login_wall" {
		t.Fatalf("last_error = %q", got)
	}
	// Free-form text in the node's event is never stored.
	r2 := newScriptedRig(t)
	r2.offer()
	r2.h.emit(r2.att, 1, helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED, "activation_failed", `{"provider_state":"<script>alert(1)</script> secret"}`)
	eventually(t, "the second record ends", func() bool { return r2.stored().Status == StatusTerminated })
	if got := r2.stored().LastError; got != "activation_failed" {
		t.Fatalf("last_error = %q: the node's text must never be stored", got)
	}
}

func TestRemoteAnEndedEventOfASupersededLeaseDoesNotReleaseTheSuccessorsHold(t *testing.T) {
	r := newScriptedRig(t, func(c *RemoteConfig, _ *scriptedHelper) {
		c.RenewEvery = 5 * time.Second
		c.LeaseWindow = 30 * time.Second
	})
	r.offer()
	ctx := t.Context()
	if _, err := r.store.BindNode(ctx, remoteTenant, r.sess.ID, remoteNode, time.Minute, time.Now().UTC()); err != nil { // generation 2, same node
		t.Fatal(err)
	}
	if err := r.store.BeginTerminate(ctx, remoteTenant, r.sess.ID, time.Now().UTC(), "x", TerminatingHoldMax); err != nil {
		t.Fatal(err)
	}
	r.h.emit(r.att, 1, helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED, "superseded", "")
	eventually(t, "the old supervisor stops", func() bool { return r.neg.callFor(r.sess.ID, r.att) == nil })
	holders, _ := r.store.ListLeaseHolders(ctx, time.Now().UTC())
	if len(holders) != 1 {
		t.Fatalf("the superseded generation's ENDED released the current generation's hold: %+v", holders)
	}
}

func TestRemoteAnIncompatibleHandshakeRefusesTheOfferBeforeAnyMedia(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*helperv1.HandshakeResponse)
	}{
		{"another protocol", func(h *helperv1.HandshakeResponse) { h.ProtocolVersion = "ubag.helper.v2" }},
		{"another node", func(h *helperv1.HandshakeResponse) { h.NodeId = "node-z" }},
		{"no voice", func(h *helperv1.HandshakeResponse) { h.Features = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newScriptedRig(t, func(_ *RemoteConfig, h *scriptedHelper) { tc.mut(h.hs) })
			if _, err := r.neg.HandleOffer(t.Context(), r.sess, "v=0"); !errors.Is(err, ErrNodeUnavailable) {
				t.Fatalf("offer = %v, want ErrNodeUnavailable", err)
			}
			if r.h.count("offer") != 0 {
				t.Fatal("OfferVoice was sent to an incompatible node")
			}
			if r.h.closes.Load() == 0 {
				t.Fatal("the connection to the refused node was not closed")
			}
		})
	}
	// Clock skew beyond the bound (the fence's expiry is a clock comparison).
	r := newScriptedRig(t, func(_ *RemoteConfig, h *scriptedHelper) {})
	skewed := &skewHelper{scriptedHelper: r.h}
	r.neg.cfg.Dial = func(context.Context, string, string) (RemoteConn, error) { return skewed, nil }
	if _, err := r.neg.HandleOffer(t.Context(), r.sess, "v=0"); !errors.Is(err, ErrNodeUnavailable) {
		t.Fatalf("a node whose clock is an hour off: %v", err)
	}
}

type skewHelper struct{ *scriptedHelper }

func (s *skewHelper) Handshake(ctx context.Context, in *helperv1.HandshakeRequest, o ...grpc.CallOption) (*helperv1.HandshakeResponse, error) {
	hs, err := s.scriptedHelper.Handshake(ctx, in, o...)
	if err == nil {
		hs.ServerTime = timestamppb.New(time.Now().Add(-time.Hour))
	}
	return hs, err
}

func TestRemoteAFailedOfferLeavesNoSupervisorAndTheRecordUntouched(t *testing.T) {
	r := newScriptedRig(t, func(_ *RemoteConfig, h *scriptedHelper) {
		h.offerErr = status.Error(codes.Unavailable, "the audio environment already hosts a call")
	})
	_, err := r.neg.HandleOffer(t.Context(), r.sess, "v=0")
	if !errors.Is(err, ErrNodeUnavailable) || codeOf(err) != codes.Unavailable {
		t.Fatalf("offer = %v", err)
	}
	if r.neg.callFor(r.sess.ID, r.att) != nil || r.stored().Status != StatusConnecting || r.h.closes.Load() == 0 {
		t.Fatal("a failed offer left state behind")
	}
	if strings.Contains(err.Error(), "audio environment") {
		t.Fatalf("the node's text reached the caller's error: %v", err)
	}
}

func TestRemoteAnOfferForASessionThatEndedMeanwhileEndsTheCallItJustStarted(t *testing.T) {
	r := newScriptedRig(t)
	// The record is terminated after the guard but before the offer returns.
	r.h.controlResp = func(*helperv1.ControlVoiceRequest) (*helperv1.ControlVoiceResponse, error) {
		return &helperv1.ControlVoiceResponse{State: helperv1.VoiceState_VOICE_STATE_ENDED}, nil
	}
	hook := &endingStore{Store: r.store, onRenew: func() {
		_ = r.store.Terminate(context.Background(), remoteTenant, r.sess.ID, time.Now().UTC(), "terminated_by_client")
	}}
	r.neg.cfg.Store = hook
	if _, err := r.neg.HandleOffer(t.Context(), r.sess, "v=0"); err == nil {
		t.Fatal("the offer succeeded for a session that had already ended")
	}
	if r.h.count("control:VOICE_CONTROL_OP_TERMINATE") != 1 {
		t.Fatalf("the call that was just started was not ended on the node: %v", r.h.rpcs)
	}
	if r.neg.callFor(r.sess.ID, r.att) != nil {
		t.Fatal("a supervisor was registered for an ended session")
	}
}

// endingStore terminates the session just before the first media-lease renewal.
type endingStore struct {
	Store
	once    sync.Once
	onRenew func()
}

func (e *endingStore) RenewMediaLease(ctx context.Context, tenantID, sessionID string, fence LeaseFence, until, now time.Time) error {
	e.once.Do(e.onRenew)
	return e.Store.RenewMediaLease(ctx, tenantID, sessionID, fence, until, now)
}

func TestRemoteCloseStopsEverySupervisor(t *testing.T) {
	r := newScriptedRig(t)
	r.offer()
	if r.neg.callFor(r.sess.ID, r.att) == nil {
		t.Fatal("no supervisor")
	}
	r.neg.Close()
	if r.neg.callFor(r.sess.ID, r.att) != nil || r.h.closes.Load() == 0 {
		t.Fatal("Close left a supervisor or a connection behind")
	}
	// A closed negotiator starts no call: the offer fails and the call it just started on the node is ended.
	if _, err := r.neg.HandleOffer(context.Background(), r.sess, "v=0"); !errors.Is(err, ErrNodeUnavailable) {
		t.Fatalf("an offer to a closed negotiator = %v, want ErrNodeUnavailable", err)
	}
	if r.neg.callFor(r.sess.ID, r.att) != nil {
		t.Fatal("a closed negotiator started a supervisor")
	}
	if r.h.count("control:VOICE_CONTROL_OP_TERMINATE") == 0 {
		t.Fatal("the call started by the refused offer was not ended on the node")
	}
}

func TestRemoteConfigRefusesAnImpossibleCadence(t *testing.T) {
	cfg := remoteConfig(NewMemoryStore(), nil2dial)
	cfg.RenewEvery = cfg.LeaseWindow // renewing as slowly as the lease lasts can never keep a call alive
	if _, err := NewRemoteNegotiator(cfg); err == nil {
		t.Fatal("a renewal cadence as long as the lease was accepted")
	}
	if _, err := NewRemoteNegotiator(RemoteConfig{}); err == nil {
		t.Fatal("an empty configuration was accepted")
	}
}
