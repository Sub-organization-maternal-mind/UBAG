package serve

import (
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
	"github.com/ubag/ubag/apps/gateway/internal/voice"
)

func twoInstancesOneHost() topology.Store {
	st := topology.NewMemoryStore()
	st.AddInstance(topology.BrowserInstance{InstanceID: "env-a", TenantID: "t1", RemoteEndpoint: "http://10.0.0.5:9223"})
	st.AddInstance(topology.BrowserInstance{InstanceID: "env-b", TenantID: "t1", RemoteEndpoint: "http://10.0.0.5:9323"})
	return st
}

func resolve(t *testing.T, topo topology.Store, instance string) (string, error) {
	t.Helper()
	return voiceRelayResolver(topo)(voice.Session{TenantID: "t1", InstanceRef: instance})
}

// Without an offset two environments on one host share host:9099 (legacy
// behaviour, unchanged); with it each derives its own relay port.
func TestVoiceRelayResolverTwoInstancesOneHost(t *testing.T) {
	t.Setenv("UBAG_VOICE_AUDIO_RELAY_ADDR", "")
	t.Setenv("UBAG_VOICE_AUDIO_RELAY_MAP", "")
	topo := twoInstancesOneHost()

	t.Setenv("UBAG_VOICE_AUDIO_RELAY_PORT_OFFSET", "")
	a, _ := resolve(t, topo, "env-a")
	b, _ := resolve(t, topo, "env-b")
	if a != "10.0.0.5:9099" || b != "10.0.0.5:9099" {
		t.Fatalf("default must stay fixed: %s %s", a, b)
	}

	t.Setenv("UBAG_VOICE_AUDIO_RELAY_PORT_OFFSET", "1000")
	a, _ = resolve(t, topo, "env-a")
	b, _ = resolve(t, topo, "env-b")
	if a != "10.0.0.5:10223" || b != "10.0.0.5:10323" {
		t.Fatalf("offset must derive per-environment ports: %s %s", a, b)
	}
}

func TestVoiceRelayResolverOffsetFailsClosedWithoutDerivablePort(t *testing.T) {
	t.Setenv("UBAG_VOICE_AUDIO_RELAY_ADDR", "")
	t.Setenv("UBAG_VOICE_AUDIO_RELAY_MAP", "")
	t.Setenv("UBAG_VOICE_AUDIO_RELAY_PORT_OFFSET", "1000")
	st := topology.NewMemoryStore()
	st.AddInstance(topology.BrowserInstance{InstanceID: "env-c", TenantID: "t1", RemoteEndpoint: "http://10.0.0.5"})
	st.AddInstance(topology.BrowserInstance{InstanceID: "env-d", TenantID: "t1", RemoteEndpoint: "http://10.0.0.5:65000"})
	for _, id := range []string{"env-c", "env-d"} {
		if addr, err := resolve(t, st, id); err == nil {
			t.Fatalf("%s resolved to %s; must fail closed", id, addr)
		}
	}
}

func TestVoiceRelayResolverExplicitMapStillWins(t *testing.T) {
	t.Setenv("UBAG_VOICE_AUDIO_RELAY_MAP", "env-a=relay-a:7000")
	t.Setenv("UBAG_VOICE_AUDIO_RELAY_PORT_OFFSET", "1000")
	if addr, _ := resolve(t, twoInstancesOneHost(), "env-a"); addr != "relay-a:7000" {
		t.Fatalf("addr = %s", addr)
	}
}
