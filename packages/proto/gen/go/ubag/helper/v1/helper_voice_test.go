package helperv1

import (
	"encoding/hex"
	"testing"

	"google.golang.org/protobuf/proto"
)

// Golden bytes pin field numbers and wire types of VoiceFence: renumbering a
// field is wire-breaking and must fail here as well as in the buf breaking check.
const goldenVoiceFenceHex = "0a02" + "7331" + "1202" + "7431" + "1a05" + "6174743132" + "2204" + "6e6f6465" + "2807"

func TestVoiceFenceGoldenWireBytes(t *testing.T) {
	f := &VoiceFence{SessionId: "s1", TenantId: "t1", AttemptId: "att12", NodeId: "node", LeaseGeneration: 7}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(b); got != goldenVoiceFenceHex {
		t.Fatalf("voice fence wire bytes changed:\n got %s\nwant %s", got, goldenVoiceFenceHex)
	}
}

// The helper is handed per-attempt keys, never a global secret: the credentials
// message has exactly relay_key, media_key and ice_servers.
func TestVoiceCredentialsCarryOnlyPerAttemptMaterial(t *testing.T) {
	fields := (&VoiceCredentials{}).ProtoReflect().Descriptor().Fields()
	got := map[string]bool{}
	for i := 0; i < fields.Len(); i++ {
		got[string(fields.Get(i).Name())] = true
	}
	for _, want := range []string{"relay_key", "media_key", "ice_servers"} {
		if !got[want] {
			t.Fatalf("VoiceCredentials lost field %s", want)
		}
	}
	if len(got) != 3 {
		t.Fatalf("VoiceCredentials gained fields (check they are not global secrets): %v", got)
	}
}
