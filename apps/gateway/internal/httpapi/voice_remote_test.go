package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
	voice "github.com/ubag/ubag/apps/gateway/internal/voice"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// Helper-hosted voice through the HTTP surface (P5.11): the connect handler routes a
// node-bound session's offer to its node through voice.NodeRouter, signs the media
// credential with the per-attempt key, confirms mute with the node before it records
// it, and a terminate holds the leases until the node acks the teardown. The node is
// a fake helper stream; the real negotiator (voice.RemoteNegotiator) is in the loop.

// nodeConn is a fake Helper Node connection (voice.RemoteConn).
type nodeConn struct {
	mu       sync.Mutex
	offers   []*helperv1.OfferVoiceRequest
	controls []*helperv1.ControlVoiceRequest
	renews   int
	closes   int
	events   chan *helperv1.VoiceEvent
	seq      uint64

	offerErr   error
	controlErr error
	// endGate, when set, delays the node's ENDED until it is closed: the provider is
	// still being torn down.
	endGate chan struct{}
}

func newNodeConn() *nodeConn { return &nodeConn{events: make(chan *helperv1.VoiceEvent, 16)} }

func (n *nodeConn) Handshake(context.Context, *helperv1.HandshakeRequest, ...grpc.CallOption) (*helperv1.HandshakeResponse, error) {
	return &helperv1.HandshakeResponse{
		ProtocolVersion: "ubag.helper.v1", NodeId: "node-a", Features: []string{"helper_voice"}, ServerTime: timestamppb.Now(),
	}, nil
}

func (n *nodeConn) OfferVoice(_ context.Context, in *helperv1.OfferVoiceRequest, _ ...grpc.CallOption) (*helperv1.OfferVoiceResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.offers = append(n.offers, in)
	if n.offerErr != nil {
		return nil, n.offerErr
	}
	return &helperv1.OfferVoiceResponse{SdpAnswer: "v=0\r\no=- answered-by-peer\r\n", RelayReady: true}, nil
}

func (n *nodeConn) ReconnectVoice(context.Context, *helperv1.ReconnectVoiceRequest, ...grpc.CallOption) (*helperv1.ReconnectVoiceResponse, error) {
	return &helperv1.ReconnectVoiceResponse{SdpAnswer: "v=0\r\no=- reanswered-by-peer\r\n"}, nil
}

func (n *nodeConn) ControlVoice(_ context.Context, in *helperv1.ControlVoiceRequest, _ ...grpc.CallOption) (*helperv1.ControlVoiceResponse, error) {
	n.mu.Lock()
	n.controls = append(n.controls, in)
	err, gate := n.controlErr, n.endGate
	n.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if in.GetOp() == helperv1.VoiceControlOp_VOICE_CONTROL_OP_TERMINATE {
		fence := in.GetFence()
		go func() { // the node writes ENDED after the provider left voice mode
			if gate != nil {
				<-gate
			}
			n.emit(fence.GetAttemptId(), fence.GetLeaseGeneration(), helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED, "terminated")
		}()
	}
	return &helperv1.ControlVoiceResponse{State: helperv1.VoiceState_VOICE_STATE_ENDED, Muted: in.GetOp() == helperv1.VoiceControlOp_VOICE_CONTROL_OP_MUTE}, nil
}

func (n *nodeConn) RenewVoice(_ context.Context, in *helperv1.RenewVoiceRequest, _ ...grpc.CallOption) (*helperv1.RenewVoiceResponse, error) {
	n.mu.Lock()
	n.renews++
	n.mu.Unlock()
	return &helperv1.RenewVoiceResponse{LeaseGeneration: in.GetFence().GetLeaseGeneration(), ExpiresAt: in.GetExpiresAt()}, nil
}

func (n *nodeConn) Close() error { n.mu.Lock(); n.closes++; n.mu.Unlock(); return nil }

func (n *nodeConn) emit(attempt string, generation uint64, t helperv1.VoiceEventType, endReason string) {
	n.mu.Lock()
	n.seq++
	ev := &helperv1.VoiceEvent{AttemptId: attempt, Sequence: n.seq, LeaseGeneration: generation, Type: t, EndReason: endReason, CreatedAt: timestamppb.Now()}
	n.mu.Unlock()
	n.events <- ev
}

type nodeStream struct {
	grpc.ClientStream
	ctx context.Context
	n   *nodeConn
}

func (n *nodeConn) StreamVoiceEvents(ctx context.Context, _ *helperv1.StreamVoiceEventsRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[helperv1.StreamVoiceEventsResponse], error) {
	return &nodeStream{ctx: ctx, n: n}, nil
}

func (s *nodeStream) Recv() (*helperv1.StreamVoiceEventsResponse, error) {
	select {
	case ev := <-s.n.events:
		return &helperv1.StreamVoiceEventsResponse{Event: ev}, nil
	case <-s.ctx.Done():
		return nil, status.FromContextError(s.ctx.Err()).Err()
	}
}

func (n *nodeConn) controlsOf(op helperv1.VoiceControlOp) []*helperv1.ControlVoiceRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []*helperv1.ControlVoiceRequest
	for _, c := range n.controls {
		if c.GetOp() == op {
			out = append(out, c)
		}
	}
	return out
}

func (n *nodeConn) offered() []*helperv1.OfferVoiceRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]*helperv1.OfferVoiceRequest(nil), n.offers...)
}

// remoteEnv is a placementEnv whose media plane is the real node router over a fake node.
type remoteEnv struct {
	*placementEnv
	node *nodeConn
	hub  *fakeMediaNegotiator
	neg  *voice.RemoteNegotiator
}

func remoteMediaServer(t *testing.T, accounts int, mutate ...func(*Config, *placementEnv)) *remoteEnv {
	t.Helper()
	re := &remoteEnv{node: newNodeConn(), hub: &fakeMediaNegotiator{answer: "v=0\r\no=- hub-answer\r\n"}}
	re.placementEnv = placementServer(t, accounts, func(c *Config, e *placementEnv) {
		store := voice.NewMemoryStore()
		c.VoiceStore = store
		var err error
		re.neg, err = voice.NewRemoteNegotiator(voice.RemoteConfig{
			Store:  store,
			Minter: voice.HelperVoiceMinter{Master: []byte("relay-master-secret"), ICE: &voice.ICEConfig{STUNURLs: []string{"stun:stun.example.org:3478"}}},
			Dial: func(context.Context, string, string) (voice.RemoteConn, error) {
				return voice.RemoteConn(connAdapter{re.node}), nil
			},
			Endpoint: func(context.Context, string) (string, error) { return placementWire, nil },
			ProfileRef: func(ctx context.Context, tenantID, target, identity, nodeID string) (string, error) {
				bindings, err := e.profiles.List(ctx, tenantID, target)
				if err != nil {
					return "", err
				}
				for _, b := range bindings {
					if b.IdentityRef == identity && b.NodeID == nodeID {
						return b.ProfileRef, nil
					}
				}
				return "", voice.ErrNoProfile
			},
			LeaseWindow: 1200 * time.Millisecond, RenewEvery: 100 * time.Millisecond, MaxClockSkew: 200 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(re.neg.Close)
		c.VoiceMedia = &voice.NodeRouter{Local: re.hub, Remote: re.neg}
		for _, fn := range mutate {
			fn(c, e)
		}
	})
	return re
}

// connAdapter lets the one fake satisfy voice.RemoteConn on every dial.
type connAdapter struct{ *nodeConn }

func (a connAdapter) Close() error { return nil } // the supervisor's own close is not the test's

func (re *remoteEnv) connectBody(t *testing.T, id string) (int, voiceSessionConnectResponse, string) {
	t.Helper()
	code, body := re.connect(t, id)
	var resp voiceSessionConnectResponse
	_ = json.Unmarshal([]byte(body), &resp)
	return code, resp, body
}

func (re *remoteEnv) eventually(t *testing.T, what string, cond func() bool) {
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

func (re *remoteEnv) holders(t *testing.T) int {
	t.Helper()
	h, err := re.srv.voice.ListLeaseHolders(t.Context(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return len(h)
}

func TestVoiceRemoteConnectRoutesTheOfferToTheSessionsNodeAndSignsWithThePerAttemptKey(t *testing.T) {
	re := remoteMediaServer(t, 1)
	code, _, s := re.create(t, "acct-helper-1")
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	stored := re.stored(t, s.ID)

	code, resp, body := re.connectBody(t, s.ID)
	if code != http.StatusOK || !strings.Contains(resp.SDPAnswer, "answered-by-peer") {
		t.Fatalf("connect = %d %s: the answer must be the node's", code, body)
	}
	if len(re.hub.offers) != 0 {
		t.Fatal("the primary's hub was handed a node-bound session")
	}
	offers := re.node.offered()
	if len(offers) != 1 {
		t.Fatalf("the node saw %d offers", len(offers))
	}
	f := offers[0].GetFence()
	if f.GetNodeId() != "node-a" || f.GetSessionId() != s.ID || f.GetLeaseGeneration() != 1 || f.GetAttemptId() != voice.AttemptIDFor(s.ID, 1) {
		t.Fatalf("fence = %+v", f)
	}
	if offers[0].GetInstanceRef() != stored.InstanceRef || offers[0].GetTarget() != placementTarget {
		t.Fatalf("offer = %+v", offers[0])
	}
	// The node is told the account by its profile_ref, the key a text job takes there.
	bindings, _ := re.profiles.List(t.Context(), placementTenant, placementTarget)
	if offers[0].GetIdentityRef() != bindings[0].ProfileRef {
		t.Fatalf("identity_ref = %q, want the profile_ref", offers[0].GetIdentityRef())
	}
	// The credential in the connect response is the one the node verifies: keyed by the
	// per-attempt media key, not the app secret.
	mediaKey := offers[0].GetCredentials().GetMediaKey()
	if len(mediaKey) == 0 || strings.Contains(string(mediaKey), "dev-secret") {
		t.Fatalf("media key = %q", mediaKey)
	}
	forNode := voice.Session{ID: s.ID, TenantID: placementTenant}
	nodeVerifies := voice.HelperVoiceSpec{MediaKey: string(mediaKey)}.MediaCredentialVerifier()
	if !nodeVerifies(forNode, resp.MediaCredential) {
		t.Fatal("the node cannot verify the connect response's media credential")
	}
	if VerifyVoiceMediaCredential("dev-secret", stored, resp.MediaCredential, time.Now().UTC()) {
		t.Fatal("a node session's credential must not be keyed by the app secret")
	}
	if len(resp.ICEServers) == 0 || resp.ICEServers[0].URLs[0] != "stun:stun.example.org:3478" {
		t.Fatalf("ice servers = %+v", resp.ICEServers)
	}
	assertNoInternalNames(t, re.placementEnv, "connect response", body)
	if re.node.renews == 0 {
		re.eventually(t, "the supervisor renews the node's lease", func() bool {
			re.node.mu.Lock()
			defer re.node.mu.Unlock()
			return re.node.renews > 0
		})
	}
}

func TestVoiceRemotePrimaryHostedSessionsStillGoToThePrimaryHub(t *testing.T) {
	re := remoteMediaServer(t, 1)
	// A second account with no profile on any node, on its own primary browser.
	re.topo.AddInstance(topologyInstance("browser-primary"))
	re.topo.AddContext(topologyContext("ctx-primary", "browser-primary", "acct-primary"))
	code, _, s := re.create(t, "acct-primary")
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	if stored := re.stored(t, s.ID); stored.NodeID != "" {
		t.Fatalf("a primary-hosted account was bound to a node: %+v", stored)
	}
	code, resp, body := re.connectBody(t, s.ID)
	if code != http.StatusOK || !strings.Contains(resp.SDPAnswer, "hub-answer") || len(re.node.offered()) != 0 {
		t.Fatalf("connect = %d %s offers-to-node=%d", code, body, len(re.node.offered()))
	}
	if !VerifyVoiceMediaCredential("dev-secret", re.stored(t, s.ID), resp.MediaCredential, time.Now().UTC()) {
		t.Fatal("a primary-hosted session keeps its app-secret credential")
	}
}

func TestVoiceRemoteMuteIsConfirmedByTheNodeBeforeItIsRecorded(t *testing.T) {
	re := remoteMediaServer(t, 1)
	_, _, s := re.create(t, "acct-helper-1")
	if code, _ := re.connect(t, s.ID); code != http.StatusOK {
		t.Fatal("connect failed")
	}
	mute := func(muted string) (int, string) {
		rec := doJSON(re.h, http.MethodPost, "/v1/voice/sessions/"+s.ID+"/mute", `{"muted":`+muted+`}`, authHeaders(""))
		return rec.Code, rec.Body.String()
	}
	if code, body := mute("true"); code != http.StatusOK {
		t.Fatalf("mute = %d %s", code, body)
	}
	got := re.node.controlsOf(helperv1.VoiceControlOp_VOICE_CONTROL_OP_MUTE)
	if len(got) != 1 || got[0].GetFence().GetNodeId() != "node-a" || got[0].GetFence().GetLeaseGeneration() != 1 {
		t.Fatalf("the node got %+v", got)
	}
	if !re.stored(t, s.ID).Muted {
		t.Fatal("a confirmed mute was not recorded")
	}

	// A node that cannot confirm the unmute: the microphone state is unknown, so the answer is
	// an error and the record keeps what the node last confirmed.
	re.node.mu.Lock()
	re.node.controlErr = status.Error(codes.Unavailable, "the link is down")
	re.node.mu.Unlock()
	code, body := mute("false")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "UBAG-VOICE-MEDIA-UNAVAILABLE-007") {
		t.Fatalf("unmute with the node unreachable = %d %s", code, body)
	}
	if !re.stored(t, s.ID).Muted {
		t.Fatal("an unconfirmed unmute was recorded")
	}
	assertNoInternalNames(t, re.placementEnv, "mute refusal", body)
}

func TestVoiceRemoteTerminateHoldsTheLeasesUntilTheNodeAcksTheTeardown(t *testing.T) {
	re := remoteMediaServer(t, 1, func(c *Config, _ *placementEnv) {
		c.VoiceProviderActivation, c.VoiceTerminatingHold = true, true
	})
	gate := make(chan struct{})
	re.node.endGate = gate
	_, _, s := re.create(t, "acct-helper-1")
	if code, _ := re.connect(t, s.ID); code != http.StatusOK {
		t.Fatal("connect failed")
	}
	re.terminate(t, s.ID)
	if got := re.stored(t, s.ID); got.Status != voice.StatusTerminated || got.InstanceRef != "" {
		t.Fatalf("the record must read terminated with no leases: %+v", got)
	}
	re.eventually(t, "the node is told to end the call", func() bool {
		return len(re.node.controlsOf(helperv1.VoiceControlOp_VOICE_CONTROL_OP_TERMINATE)) == 1
	})
	time.Sleep(150 * time.Millisecond)
	if re.holders(t) != 1 || re.nodes.Used("node-a") != 1 {
		t.Fatalf("holders=%d used=%d: the hold and the node slot must outlive the media until the node acks", re.holders(t), re.nodes.Used("node-a"))
	}
	// A new call on the account queues while the provider is still being torn down.
	code, _, second := re.create(t, "acct-helper-1")
	if code != http.StatusAccepted || second.Status != voice.StatusQueued {
		t.Fatalf("a successor took the account during teardown: %d %+v", code, second)
	}

	close(gate) // the provider left voice mode: ENDED
	re.eventually(t, "the hold is released", func() bool { return re.holders(t) == 0 })
	re.eventually(t, "the node slot is returned", func() bool {
		re.srv.releaseVoiceNodeHolds(t.Context())
		return re.nodes.Used("node-a") == 0
	})
}

func TestVoiceRemoteTerminateWithoutTheHoldFlagReleasesAtOnce(t *testing.T) {
	re := remoteMediaServer(t, 1) // UBAG_HELPER_VOICE hold off: the leases free as before
	_, _, s := re.create(t, "acct-helper-1")
	if code, _ := re.connect(t, s.ID); code != http.StatusOK {
		t.Fatal("connect failed")
	}
	re.terminate(t, s.ID)
	if re.holders(t) != 0 || re.nodes.Used("node-a") != 0 {
		t.Fatalf("holders=%d used=%d", re.holders(t), re.nodes.Used("node-a"))
	}
	re.eventually(t, "the node is still told to end the call", func() bool {
		return len(re.node.controlsOf(helperv1.VoiceControlOp_VOICE_CONTROL_OP_TERMINATE)) == 1
	})
}

func TestVoiceRemoteAnotherTenantsSessionReadsAsNotFoundOnEveryRoute(t *testing.T) {
	re := remoteMediaServer(t, 1)
	// A node-bound session that belongs to another tenant.
	ctx := t.Context()
	now := time.Now().UTC()
	if _, err := re.srv.voice.Reserve(ctx, voice.ReserveRequest{
		SessionID: "voice_1700000000_foreign000000", TenantID: "tenant_other", AppID: "app-x", Target: placementTarget,
		Placements: []voice.Placement{{Identity: "acct-other", Instance: "env-other"}}, LeaseTTL: time.Hour, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := re.srv.voice.BindNode(ctx, "tenant_other", "voice_1700000000_foreign000000", "node-a", time.Minute, now); err != nil {
		t.Fatal(err)
	}
	type route struct{ method, path, body string }
	routes := func(id string) []route {
		return []route{
			{http.MethodGet, "/v1/voice/sessions/" + id, ""},
			{http.MethodPost, "/v1/voice/sessions/" + id + "/connect", `{"sdp_offer":"v=0"}`},
			{http.MethodPost, "/v1/voice/sessions/" + id + "/mute", `{"muted":true}`},
			{http.MethodPost, "/v1/voice/sessions/" + id + "/renew", `{}`},
			{http.MethodPost, "/v1/voice/sessions/" + id + "/terminate", `{}`},
		}
	}
	foreign, missing := routes("voice_1700000000_foreign000000"), routes("voice_1700000000_missing000000")
	for i := range foreign {
		a := doJSON(re.h, foreign[i].method, foreign[i].path, foreign[i].body, authHeaders(""))
		b := doJSON(re.h, missing[i].method, missing[i].path, missing[i].body, authHeaders(""))
		if a.Code != http.StatusNotFound || a.Code != b.Code || !strings.Contains(a.Body.String(), "UBAG-VOICE-SESSION-NOT-FOUND-006") ||
			errorCodeOf(a.Body.String()) != errorCodeOf(b.Body.String()) {
			t.Fatalf("%s %s: foreign = %d %s, missing = %d %s: another tenant's session must read exactly as a missing one",
				foreign[i].method, foreign[i].path, a.Code, a.Body.String(), b.Code, b.Body.String())
		}
	}
	if len(re.node.offered()) != 0 || len(re.node.controls) != 0 {
		t.Fatal("another tenant's request reached a node")
	}
	if got, ok, _ := re.srv.voice.Get(ctx, "tenant_other", "voice_1700000000_foreign000000"); !ok || got.Status == voice.StatusTerminated {
		t.Fatalf("another tenant terminated the session: %+v", got)
	}
}

func topologyInstance(id string) topology.BrowserInstance {
	return topology.BrowserInstance{InstanceID: id, TenantID: placementTenant, State: "ready"}
}

func topologyContext(id, instance, identity string) topology.ProviderContext {
	return topology.ProviderContext{
		ContextID: id, InstanceID: instance, TenantID: placementTenant, TargetID: placementTarget,
		IdentityRef: identity, LoginState: "authenticated",
	}
}

func errorCodeOf(body string) string {
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	return out.Error.Code
}

func TestVoiceRemoteAStaleLeaseIsNeverForwardedToANode(t *testing.T) {
	re := remoteMediaServer(t, 1)
	_, _, s := re.create(t, "acct-helper-1")
	if code, _ := re.connect(t, s.ID); code != http.StatusOK {
		t.Fatal("connect failed")
	}
	// The session is re-leased (generation 2, another node) between the handler's read of the
	// record and its call: simulate by rebinding, then calling with the old record's fence.
	stale := re.stored(t, s.ID)
	if _, err := re.srv.voice.BindNode(t.Context(), placementTenant, s.ID, "node-b", time.Minute, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	before := len(re.node.controlsOf(helperv1.VoiceControlOp_VOICE_CONTROL_OP_MUTE))
	node, ok := nodeSessionMedia(re.srv.voiceMedia)
	if !ok {
		t.Fatal("the media plane must be node capable")
	}
	if err := node.MuteSession(t.Context(), stale, true); !errors.Is(err, voice.ErrNodeMismatch) {
		t.Fatalf("a stale mute = %v, want ErrNodeMismatch", err)
	}
	if len(re.node.controlsOf(helperv1.VoiceControlOp_VOICE_CONTROL_OP_MUTE)) != before {
		t.Fatal("a stale mute reached the node")
	}
}

func TestVoiceRemoteConnectFailsClosedWhenTheNodeRefusesTheOffer(t *testing.T) {
	re := remoteMediaServer(t, 1)
	re.node.offerErr = status.Error(codes.Unavailable, "the audio environment already hosts a call at 10.0.0.9")
	_, _, s := re.create(t, "acct-helper-1")
	code, body := re.connect(t, s.ID)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "UBAG-VOICE-MEDIA-UNAVAILABLE-007") {
		t.Fatalf("connect = %d %s", code, body)
	}
	if strings.Contains(body, "10.0.0.9") || strings.Contains(body, "audio environment") {
		t.Fatalf("the node's text reached the client: %s", body)
	}
	assertNoInternalNames(t, re.placementEnv, "refusal", body)
	if got := re.stored(t, s.ID); got.Status != voice.StatusConnecting {
		t.Fatalf("a refused offer must leave the session retryable: %+v", got)
	}
	// The client retries once the node is free.
	re.node.mu.Lock()
	re.node.offerErr = nil
	re.node.mu.Unlock()
	if code, body := re.connect(t, s.ID); code != http.StatusOK {
		t.Fatalf("retry = %d %s", code, body)
	}
}

func TestVoiceRemoteTheNodesCONNECTEDEventMarksTheSessionConnected(t *testing.T) {
	re := remoteMediaServer(t, 1)
	_, _, s := re.create(t, "acct-helper-1")
	if code, _ := re.connect(t, s.ID); code != http.StatusOK {
		t.Fatal("connect failed")
	}
	re.node.emit(voice.AttemptIDFor(s.ID, 1), 1, helperv1.VoiceEventType_VOICE_EVENT_TYPE_CONNECTED, "")
	re.eventually(t, "the session is connected", func() bool { return re.stored(t, s.ID).Status == voice.StatusConnected })
	// Another generation's CONNECTED is rejected: the record keeps what the current holder said.
	re.node.emit(voice.AttemptIDFor(s.ID, 1), 9, helperv1.VoiceEventType_VOICE_EVENT_TYPE_MUTED, "")
	time.Sleep(150 * time.Millisecond)
	if re.stored(t, s.ID).Muted {
		t.Fatal("a stale-generation event muted the session")
	}
}
