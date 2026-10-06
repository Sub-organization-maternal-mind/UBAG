package helperclient

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/helper"
	"github.com/ubag/ubag/apps/gateway/internal/voice"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// The primary's voice.RemoteNegotiator against the real ubag-helper voice service over
// real mutual TLS on loopback (P5.11): the verified dial carries the voice RPCs, the
// offer, the fenced events and the teardown acknowledgement cross a real gRPC
// connection. Only the browser (a worker script) and the WebRTC endpoint (a fake
// helper.VoiceMedia) are fake.

type e2eMedia struct {
	mu    sync.Mutex
	calls []helper.VoiceCall
	sinks map[string]helper.VoiceSink
	shut  int
}

func (m *e2eMedia) Open(_ context.Context, call helper.VoiceCall, _ string, sink helper.VoiceSink) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sinks == nil {
		m.sinks = map[string]helper.VoiceSink{}
	}
	m.calls = append(m.calls, call)
	m.sinks[call.Key()] = sink
	return "v=0\r\nanswered-on-the-helper", nil
}
func (m *e2eMedia) Reoffer(context.Context, string, string, bool) (string, error) {
	return "v=0\r\nre-answered", nil
}
func (m *e2eMedia) SetMuted(string, bool) bool { return true }
func (m *e2eMedia) Close(string)               { m.mu.Lock(); m.shut++; m.mu.Unlock() }

func (m *e2eMedia) sink() (helper.VoiceSink, helper.VoiceCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) != 1 {
		return helper.VoiceSink{}, helper.VoiceCall{}
	}
	return m.sinks[m.calls[0].Key()], m.calls[0]
}

func TestRemoteNegotiatorHostsACallOnTheRealHelperOverMutualTLS(t *testing.T) {
	media := &e2eMedia{}
	worker := helper.RunnerFunc(func(_ context.Context, spec helper.AttemptSpec, emit helper.EmitFunc) error {
		state := "activated"
		if strings.Contains(spec.InputJSON, `"deactivate"`) {
			state = "deactivated"
		}
		return emit(helper.Event{
			Type:    helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL,
			Outcome: &helperv1.AttemptOutcome{Status: helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED, ResultJson: `{"state":"` + state + `"}`},
		})
	})
	r := newRig(t, rigOptions{runner: worker, cfg: func(c *helper.Config) {
		c.Voice = &helper.VoiceConfig{Media: media, Environments: []helper.VoiceEnvironment{
			{ID: "env-1", CDPEndpoint: "http://127.0.0.1:9222", RelayAddr: "127.0.0.1:9099", RelayKeyFile: "relay-key"},
		}}
	}})
	dialer := r.dialer(r.primary, nil, nil)

	store := voice.NewMemoryStore()
	now := time.Now().UTC()
	if _, err := store.Reserve(t.Context(), voice.ReserveRequest{
		SessionID: "voice_1700000000_aaaaaaaaaaaa", TenantID: "tenant_a", AppID: "app_a", Target: "chatgpt_web",
		Placements: []voice.Placement{{Identity: "acct-1", Instance: "env-1"}}, LeaseTTL: time.Hour, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	sess, err := store.BindNode(t.Context(), "tenant_a", "voice_1700000000_aaaaaaaaaaaa", testNode, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	neg, err := voice.NewRemoteNegotiator(voice.RemoteConfig{
		Store:  store,
		Minter: voice.HelperVoiceMinter{Master: []byte("primary-only-master-secret")},
		Dial: func(ctx context.Context, nodeID, endpoint string) (voice.RemoteConn, error) {
			conn, err := dialer.Dial(ctx, nodeID, endpoint)
			if err != nil {
				return nil, err
			}
			return conn, nil
		},
		Endpoint:        func(context.Context, string) (string, error) { return r.addr, nil },
		ProfileRef:      func(context.Context, string, string, string, string) (string, error) { return "pr_acct1", nil },
		WorkloadVersion: "w1", RegistryDigest: testDigest,
		LeaseWindow: 1200 * time.Millisecond, RenewEvery: 100 * time.Millisecond, MaxClockSkew: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer neg.Close()

	answer, err := neg.HandleOffer(t.Context(), sess, "v=0\r\noffer")
	if err != nil || !strings.Contains(answer, "answered-on-the-helper") {
		t.Fatalf("HandleOffer = %q, %v", answer, err)
	}
	sink, call := media.sink()
	if call.NodeID != testNode || call.Generation != 1 || call.IdentityRef != "pr_acct1" || call.InstanceRef != "env-1" {
		t.Fatalf("the helper hosted %+v", call)
	}

	// The node's CONNECTED (provider ready AND the peer up) is a fenced write on the primary.
	sink.PeerConnected()
	waitFor(t, "connected", func() bool {
		s, _, _ := store.Get(t.Context(), "tenant_a", sess.ID)
		return s.Status == voice.StatusConnected
	})

	// Terminate: the record holds its leases until the node acks that the provider left voice mode.
	if err := store.BeginTerminate(t.Context(), "tenant_a", sess.ID, time.Now().UTC(), "terminated_by_client", voice.TerminatingHoldMax); err != nil {
		t.Fatal(err)
	}
	neg.EndSession(t.Context(), sess, "terminated_by_client")
	waitFor(t, "the hold is released on the node's ack", func() bool {
		holders, err := store.ListLeaseHolders(t.Context(), time.Now().UTC())
		return err == nil && len(holders) == 0
	})
	media.mu.Lock()
	closed := media.shut
	media.mu.Unlock()
	if closed == 0 {
		t.Fatal("the helper's media was not closed")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
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

// A negotiator that holds a certificate the helper's registry does not pin never gets a
// connection: the voice RPCs ride the same verified dial as every other call.
func TestRemoteNegotiatorRefusesAHelperThatIsNotTheNodeAskedFor(t *testing.T) {
	r := newRig(t, rigOptions{cfg: func(c *helper.Config) {
		c.Runner = helper.RunnerFunc(func(context.Context, helper.AttemptSpec, helper.EmitFunc) error { return nil })
		c.Voice = &helper.VoiceConfig{Media: &e2eMedia{}, Environments: []helper.VoiceEnvironment{
			{ID: "env-1", CDPEndpoint: "http://127.0.0.1:9222", RelayAddr: "127.0.0.1:9099", RelayKeyFile: "relay-key"},
		}}
	}})
	var rejects rejectLog
	dialer := r.dialer(r.primary, nil, &rejects)
	store := voice.NewMemoryStore()
	now := time.Now().UTC()
	if _, err := store.Reserve(t.Context(), voice.ReserveRequest{
		SessionID: "voice_1700000000_bbbbbbbbbbbb", TenantID: "tenant_a", Target: "chatgpt_web",
		Placements: []voice.Placement{{Identity: "acct-1", Instance: "env-1"}}, LeaseTTL: time.Hour, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	// The session is bound to ANOTHER node id than the certificate the helper presents.
	sess, err := store.BindNode(t.Context(), "tenant_a", "voice_1700000000_bbbbbbbbbbbb", "helper-2", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	neg, err := voice.NewRemoteNegotiator(voice.RemoteConfig{
		Store:  store,
		Minter: voice.HelperVoiceMinter{Master: []byte("primary-only-master-secret")},
		Dial: func(ctx context.Context, nodeID, endpoint string) (voice.RemoteConn, error) {
			conn, err := dialer.Dial(ctx, nodeID, endpoint)
			if err != nil {
				return nil, err
			}
			return conn, nil
		},
		Endpoint:    func(context.Context, string) (string, error) { return r.addr, nil },
		ProfileRef:  func(context.Context, string, string, string, string) (string, error) { return "pr_acct1", nil },
		LeaseWindow: 1200 * time.Millisecond, RenewEvery: 100 * time.Millisecond, MaxClockSkew: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer neg.Close()
	if _, err := neg.HandleOffer(t.Context(), sess, "v=0"); err == nil {
		t.Fatal("an offer was accepted by a helper whose certificate names another node")
	}
}
