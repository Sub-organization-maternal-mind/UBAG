package helper

import (
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth/authtest"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

func (h *testHost) voiceEnv(t *testing.T, kv ...string) []string {
	t.Helper()
	keyDir := filepath.Join(h.dir, "voice-keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return h.with(append([]string{
		EnvVoice + "=1",
		EnvVoiceEnvs + "=env-a=9222:9099,env-b=9223:9100",
		EnvVoiceKeyDir + "=" + keyDir,
		EnvVoiceUDPPorts + "=40000-40063",
		EnvVoiceNAT1To1 + "=203.0.113.7",
	}, kv...)...)
}

func TestVoiceSettingsAreOffByDefaultAndIgnoreVoiceVariables(t *testing.T) {
	h := newTestHost(t, "helper-1")
	st, err := SettingsFromEnv(h.with(EnvVoiceEnvs+"=garbage", EnvVoiceUDPPorts+"=nonsense"), h.lookup)
	if err != nil {
		t.Fatalf("voice off must not validate voice variables: %v", err)
	}
	if st.Voice.Enabled || len(st.Voice.Environments) != 0 {
		t.Fatalf("voice = %+v", st.Voice)
	}
	// Only a clear opt-in turns it on.
	for _, v := range []string{"", "0", "false", "off", "no", "maybe"} {
		st, err := SettingsFromEnv(h.with(EnvVoice+"="+v), h.lookup)
		if err != nil || st.Voice.Enabled {
			t.Fatalf("%s=%q: enabled=%v err=%v", EnvVoice, v, st.Voice.Enabled, err)
		}
	}
}

func TestVoiceSettingsFromEnv(t *testing.T) {
	h := newTestHost(t, "helper-1")
	st, err := SettingsFromEnv(h.voiceEnv(t, EnvVoiceServerViaTURN+"=true"), h.lookup)
	if err != nil {
		t.Fatal(err)
	}
	v := st.Voice
	if !v.Enabled || v.UDPMin != 40000 || v.UDPMax != 40063 || v.NAT1To1IP != "203.0.113.7" || !v.ServerViaTURN || len(v.Environments) != 2 {
		t.Fatalf("voice = %+v", v)
	}
	a := v.Environments[0]
	// Every environment is loopback by construction: there is no host to configure.
	if a.ID != "env-a" || a.CDPEndpoint != "http://127.0.0.1:9222" || a.RelayAddr != "127.0.0.1:9099" ||
		filepath.Base(a.RelayKeyFile) != "relay-key.9099" || filepath.Dir(a.RelayKeyFile) != filepath.Join(h.dir, "voice-keys") {
		t.Fatalf("environment = %+v", a)
	}
	for _, e := range v.Environments {
		if !strings.HasPrefix(e.CDPEndpoint, "http://127.0.0.1:") || !strings.HasPrefix(e.RelayAddr, "127.0.0.1:") {
			t.Fatalf("a non-loopback environment: %+v", e)
		}
	}
}

func TestVoiceSettingsRefuseWhatCannotWork(t *testing.T) {
	h := newTestHost(t, "helper-1")
	for name, tc := range map[string]struct {
		kv   []string
		want string
	}{
		"no environments":             {[]string{EnvVoiceEnvs + "="}, EnvVoiceEnvs},
		"an environment with a host":  {[]string{EnvVoiceEnvs + "=env-a=10.0.0.5:9222:9099"}, "<instance_ref>=<cdp_port>:<relay_port>"},
		"no instance name":            {[]string{EnvVoiceEnvs + "=9222:9099"}, EnvVoiceEnvs},
		"reused port":                 {[]string{EnvVoiceEnvs + "=env-a=9222:9099,env-b=9222:9100"}, "reuses a port"},
		"cdp equals relay":            {[]string{EnvVoiceEnvs + "=env-a=9222:9222"}, "reuses a port"},
		"privileged port":             {[]string{EnvVoiceEnvs + "=env-a=80:9099"}, "ports must be"},
		"port out of range":           {[]string{EnvVoiceEnvs + "=env-a=9222:70000"}, "ports must be"},
		"duplicate name":              {[]string{EnvVoiceEnvs + "=env-a=9222:9099,env-a=9223:9100"}, "twice"},
		"too many environments":       {[]string{EnvVoiceEnvs + "=a=9001:9101,b=9002:9102,c=9003:9103,d=9004:9104,e=9005:9105,f=9006:9106,g=9007:9107,h=9008:9108,i=9009:9109"}, "1..8"},
		"no key directory":            {[]string{EnvVoiceKeyDir + "=/nonexistent/keys"}, EnvVoiceKeyDir},
		"no UDP range":                {[]string{EnvVoiceUDPPorts + "="}, EnvVoiceUDPPorts},
		"inverted UDP range":          {[]string{EnvVoiceUDPPorts + "=40063-40000"}, EnvVoiceUDPPorts},
		"UDP range under 1024":        {[]string{EnvVoiceUDPPorts + "=100-200"}, EnvVoiceUDPPorts},
		"UDP range too small":         {[]string{EnvVoiceUDPPorts + "=40000-40010"}, "ports per audio environment"},
		"a hostname for NAT 1:1":      {[]string{EnvVoiceNAT1To1 + "=helper.example"}, EnvVoiceNAT1To1},
		"a loopback NAT 1:1":          {[]string{EnvVoiceNAT1To1 + "=127.0.0.1"}, EnvVoiceNAT1To1},
		"an IPv6 NAT 1:1":             {[]string{EnvVoiceNAT1To1 + "=2001:db8::1"}, EnvVoiceNAT1To1},
		"a wildcard NAT 1:1":          {[]string{EnvVoiceNAT1To1 + "=0.0.0.0"}, EnvVoiceNAT1To1},
		"the TURN secret in the env":  {[]string{"UBAG_VOICE_TURN_SECRET=x"}, "UBAG_VOICE_TURN_SECRET"},
		"the relay secret in the env": {[]string{"UBAG_VOICE_RELAY_SECRET=x"}, "UBAG_VOICE_RELAY_SECRET"},
	} {
		if _, err := SettingsFromEnv(h.voiceEnv(t, tc.kv...), h.lookup); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error containing %q, got %v", name, tc.want, err)
		}
	}
}

func TestOpenWithVoiceNeedsAMediaEndpoint(t *testing.T) {
	h := newTestHost(t, "helper-1")
	st, err := SettingsFromEnv(h.voiceEnv(t), h.lookup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Open(nil, nil); err == nil || !strings.Contains(err.Error(), "helpervoice") {
		t.Fatalf("voice on without a media endpoint must refuse to start: %v", err)
	}
}

// The whole startup path with voice on, over a real TCP listener with real mTLS:
// the service is registered next to HelperService behind the same authentication,
// the handshake advertises it, and an OfferVoice reaches the media endpoint.
func TestOpenServesHelperVoiceServiceOverMTLS(t *testing.T) {
	h := newTestHost(t, "helper-1")
	st, err := SettingsFromEnv(h.voiceEnv(t), h.lookup)
	if err != nil {
		t.Fatal(err)
	}
	m := newFakeVoiceMedia()
	node, err := st.Open(newFakeRunner(voiceScript(nil, nil)), m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- node.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	dial := func(leaf *authtest.Leaf) *grpc.ClientConn {
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: h.ca.Pool, ServerName: "localhost"}
		if leaf != nil {
			cfg.Certificates = []tls.Certificate{leaf.TLS}
		}
		conn, err := grpc.NewClient(h.listen, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	primary := h.ca.Issue(t, authtest.Spec{NodeID: "primary-1", URIs: []string{PrimaryURISAN("primary-1")}, NotAfter: time.Now().Add(time.Hour)})
	intruder := h.ca.Issue(t, authtest.Spec{NodeID: "primary-2", URIs: []string{PrimaryURISAN("primary-2")}, NotAfter: time.Now().Add(time.Hour)})
	rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer rcancel()

	hs, err := helperv1.NewHelperServiceClient(dial(primary)).Handshake(rctx, &helperv1.HandshakeRequest{ProtocolVersion: ProtocolVersion})
	if err != nil || !strings.Contains(strings.Join(hs.GetFeatures(), ","), "helper_voice") {
		t.Fatalf("handshake = %v, %v", hs, err)
	}
	req := &helperv1.OfferVoiceRequest{
		Fence: &helperv1.VoiceFence{
			SessionId: vSession, TenantId: vTenant, AttemptId: vAttempt, NodeId: "helper-1", LeaseGeneration: 1,
			ExpiresAt: timestamppb.New(time.Now().Add(2 * time.Minute)),
		},
		Credentials: &helperv1.VoiceCredentials{RelayKey: relayKeyBytes, MediaKey: mediaKeyBytes},
		SdpOffer:    "v=0 over-mtls", IdentityRef: "ident-v1", InstanceRef: "env-b", Target: "chatgpt_web",
	}
	// A peer that is not the configured primary never reaches the service.
	if _, err := helperv1.NewHelperVoiceServiceClient(dial(intruder)).OfferVoice(rctx, req); err == nil {
		t.Fatal("OfferVoice by a peer that is not the configured primary must fail (refused in the TLS handshake)")
	}
	if _, err := helperv1.NewHelperVoiceServiceClient(dial(nil)).OfferVoice(rctx, req); err == nil {
		t.Fatal("OfferVoice without a client certificate must fail")
	}
	if m.openCount() != 0 {
		t.Fatal("an unauthenticated peer reached the media endpoint")
	}
	resp, err := helperv1.NewHelperVoiceServiceClient(dial(primary)).OfferVoice(rctx, req)
	if err != nil || resp.GetSdpAnswer() != "answer:v=0 over-mtls" {
		t.Fatalf("OfferVoice = %v, %v", resp, err)
	}
	// The call landed on the environment the primary named, never on another.
	if got := m.opened[0].Env; got.ID != "env-b" || got.RelayAddr != "127.0.0.1:9100" || got.CDPEndpoint != "http://127.0.0.1:9223" {
		t.Fatalf("environment = %+v", got)
	}
}
