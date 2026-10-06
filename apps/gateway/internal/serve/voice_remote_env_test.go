package serve

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	"github.com/ubag/ubag/apps/gateway/internal/voice"
	"github.com/ubag/ubag/apps/gateway/internal/voiceplace"
)

// The primary-side media plane of helper-hosted voice (P5.11) exists exactly when the
// node placer does (UBAG_HELPER_VOICE with the whole ladder), and refuses to start
// half configured: it dials with the dispatch identity and checks the node's workload
// and adapter registry like job dispatch does.
func TestVoiceRemoteFromEnv(t *testing.T) {
	good := writeDispatchEnv(t)
	missing := filepath.Join(t.TempDir(), "missing")
	placer := &voiceplace.Placer{Profiles: helperauth.NewMemoryProfileStore()}
	store := voice.NewMemoryStore()
	cases := []struct {
		name    string
		placer  *voiceplace.Placer
		store   voice.Store
		nodes   nodes.Store
		mutate  func(*dispatchEnv)
		wantRun bool
		wantErr string
	}{
		{name: "flag off (no placer) is inert and needs nothing", store: store},
		{name: "no voice store is inert", placer: placer},
		{name: "needs the node store", placer: placer, store: store, wantErr: "UBAG_HELPER_NODES"},
		{name: "needs the CA", placer: placer, store: store, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.ca = "" }, wantErr: "UBAG_HELPER_CA_FILE"},
		{name: "needs the client certificate", placer: placer, store: store, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.cert = "" }, wantErr: "UBAG_HELPER_CLIENT_CERT_FILE"},
		{name: "needs the client key", placer: placer, store: store, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.key = "" }, wantErr: "UBAG_HELPER_CLIENT_KEY_FILE"},
		{name: "unreadable CA", placer: placer, store: store, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.ca = missing }, wantErr: "CA bundle"},
		{name: "unreadable adapter registry", placer: placer, store: store, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.adapters = missing }, wantErr: "registry digest"},
		{name: "configured", placer: placer, store: store, nodes: nodes.NewMemoryStore(), wantRun: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := good
			if tc.mutate != nil {
				tc.mutate(&env)
			}
			setDispatchEnv(t, "true", env)
			t.Setenv("UBAG_VOICE_RELAY_SECRET", "relay-secret-for-the-test")
			var st voice.Store
			if tc.store != nil {
				st = tc.store
			}
			neg, err := newVoiceRemoteFromEnv(st, tc.nodes, tc.placer)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if (neg != nil) != tc.wantRun {
				t.Fatalf("negotiator = %v, want one: %v", neg, tc.wantRun)
			}
			if neg != nil {
				defer neg.Close()
				if !neg.HostsNodeSessions() {
					t.Fatal("the negotiator must host node sessions")
				}
			}
		})
	}
}

// A node's endpoint is resolved from the node store's last accepted allocation, and
// a node the store does not know is not dialed.
func TestVoiceRemoteResolvesTheNodeFromTheStore(t *testing.T) {
	setDispatchEnv(t, "true", writeDispatchEnv(t))
	t.Setenv("UBAG_VOICE_RELAY_SECRET", "relay-secret-for-the-test")
	store := voice.NewMemoryStore()
	neg, err := newVoiceRemoteFromEnv(store, nodes.NewMemoryStore(), &voiceplace.Placer{Profiles: helperauth.NewMemoryProfileStore()})
	if err != nil || neg == nil {
		t.Fatalf("negotiator = %v, %v", neg, err)
	}
	defer neg.Close()
	// A session bound to a node the store has never heard of cannot be offered: the
	// endpoint lookup fails before anything is dialed.
	sess, err := store.Reserve(context.Background(), voice.ReserveRequest{
		SessionID: "voice_1700000000_aaaaaaaaaaaa", TenantID: "t1", Target: "chatgpt_web",
		Placements: []voice.Placement{{Identity: "acct-1", Instance: "env-1"}}, LeaseTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := store.BindNode(context.Background(), "t1", sess.ID, "node-x", time.Minute, sess.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := neg.HandleOffer(context.Background(), bound, "v=0"); err == nil {
		t.Fatal("an offer for a node with no allocation was accepted")
	}
}
