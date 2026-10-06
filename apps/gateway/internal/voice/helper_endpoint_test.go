package voice

import (
	"strings"
	"testing"
	"time"
)

// A helper-hosted endpoint gets its ICE servers pre-minted by the primary: they
// replace the secret-minted ones, and a TURN entry is used only when the
// endpoint is allowed to allocate a relay itself.
func TestICEFixedServersCarryNoSecretAndTURNNeedsServerViaTURN(t *testing.T) {
	fixed := []ICEServer{
		{URLs: []string{"stun:stun.example:3478"}},
		{URLs: []string{"turn:turn.example:3478"}, Username: "1700000000:s:att_1", Credential: "minted"},
	}
	cfg := &ICEConfig{FixedServers: fixed}
	got := cfg.serverICEServers("voice_1", time.Now())
	if len(got) != 1 || got[0].Username != "" || got[0].URLs[0] != "stun:stun.example:3478" {
		t.Fatalf("without ServerViaTURN only STUN may be used, got %+v", got)
	}
	cfg.ServerViaTURN = true
	got = cfg.serverICEServers("voice_1", time.Now())
	if len(got) != 2 || got[1].Username != "1700000000:s:att_1" || got[1].Credential != "minted" {
		t.Fatalf("with ServerViaTURN the pre-minted TURN entry is used verbatim, got %+v", got)
	}
	// FixedServers win over the legacy fields: nothing is minted locally.
	cfg.TURNURLs, cfg.TURNSecret = []string{"turn:other.example:3478"}, "local-secret"
	got = cfg.serverICEServers("voice_1", time.Now())
	if len(got) != 2 || got[1].Credential != "minted" {
		t.Fatalf("a locally minted credential replaced the fixed one: %+v", got)
	}
}

// A Helper Node cannot know the session's app id, so it verifies with an empty
// AppID: the credential's own (HMAC-covered) app field is used. The tenant,
// session, key and expiry bindings all still hold.
func TestHelperVerifierWithoutAppIDStillBindsEverythingElse(t *testing.T) {
	now := time.Now().UTC()
	b := testBinding(now)
	spec, err := testMinter().Mint(b, now)
	if err != nil {
		t.Fatal(err)
	}
	minted := Session{ID: b.SessionID, TenantID: b.TenantID, AppID: "app-1"}
	cred := IssueMediaCredential([]byte(spec.MediaKey), minted, now.Add(5*time.Minute))
	verify := spec.MediaCredentialVerifier()

	helperView := Session{ID: b.SessionID, TenantID: b.TenantID} // no AppID
	if !verify(helperView, cred) {
		t.Fatal("a valid credential must verify on a helper that does not know the app id")
	}
	if verify(Session{ID: "voice_other", TenantID: b.TenantID}, cred) {
		t.Error("credential must stay bound to its session")
	}
	if verify(Session{ID: b.SessionID, TenantID: "tenant-b"}, cred) {
		t.Error("credential must stay bound to its tenant")
	}
	if verify(helperView, IssueMediaCredential([]byte("app-secret"), minted, now.Add(5*time.Minute))) {
		t.Error("a credential under another key must not verify")
	}
	if verify(helperView, strings.Replace(cred, "|app-1|", "|app-2|", 1)) {
		t.Error("tampering with the app field must break the HMAC")
	}
	// A caller that DOES know the app id keeps the strict app binding.
	if verify(Session{ID: b.SessionID, TenantID: b.TenantID, AppID: "app-2"}, cred) {
		t.Error("a known app id must still have to match")
	}
}
