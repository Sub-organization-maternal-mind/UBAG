package voice

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testBinding(now time.Time) AttemptBinding {
	return AttemptBinding{
		SessionID: "voice_1", TenantID: "tenant-a", AttemptID: "att_1", NodeID: "node-a",
		LeaseGeneration: 3, Expires: now.Add(15 * time.Minute),
	}
}

func testMinter() HelperVoiceMinter {
	return HelperVoiceMinter{
		Master: []byte("relay-master-secret"),
		ICE: &ICEConfig{
			STUNURLs: []string{"stun:stun.example:3478"}, TURNURLs: []string{"turn:turn.example:3478"},
			TURNSecret: "turn-shared-secret", CredentialTTL: time.Minute,
		},
	}
}

func TestDeriveAttemptKeyIsBoundToEveryField(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	base := testBinding(now)
	master := []byte("m")
	want, err := DeriveAttemptKey(master, keyPurposeRelay, base)
	if err != nil || len(want) != 64 {
		t.Fatalf("key = %q, %v", want, err)
	}
	if again, _ := DeriveAttemptKey(master, keyPurposeRelay, base); again != want {
		t.Fatal("derivation is not deterministic")
	}
	mutations := map[string]func(*AttemptBinding){
		"session":    func(b *AttemptBinding) { b.SessionID = "voice_2" },
		"tenant":     func(b *AttemptBinding) { b.TenantID = "tenant-b" },
		"attempt":    func(b *AttemptBinding) { b.AttemptID = "att_2" },
		"node":       func(b *AttemptBinding) { b.NodeID = "node-b" },
		"generation": func(b *AttemptBinding) { b.LeaseGeneration++ },
		"expiry":     func(b *AttemptBinding) { b.Expires = b.Expires.Add(time.Second) },
	}
	for name, mutate := range mutations {
		b := base
		mutate(&b)
		if got, _ := DeriveAttemptKey(master, keyPurposeRelay, b); got == want {
			t.Errorf("key does not change with %s", name)
		}
	}
	if media, _ := DeriveAttemptKey(master, keyPurposeMedia, base); media == want {
		t.Error("relay and media keys must be domain separated")
	}
	if other, _ := DeriveAttemptKey([]byte("other"), keyPurposeRelay, base); other == want {
		t.Error("key does not depend on the master")
	}
}

func TestDeriveAttemptKeyRejectsAmbiguousBindings(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	bad := map[string]func(*AttemptBinding){
		"empty node":     func(b *AttemptBinding) { b.NodeID = "" },
		"separator":      func(b *AttemptBinding) { b.TenantID = "a|b" },
		"zero gen":       func(b *AttemptBinding) { b.LeaseGeneration = 0 },
		"huge gen":       func(b *AttemptBinding) { b.LeaseGeneration = maxBoundGeneration + 1 },
		"no expiry":      func(b *AttemptBinding) { b.Expires = time.Time{} },
		"overlong field": func(b *AttemptBinding) { b.SessionID = strings.Repeat("a", 129) },
	}
	for name, mutate := range bad {
		b := testBinding(now)
		mutate(&b)
		if _, err := DeriveAttemptKey([]byte("m"), keyPurposeRelay, b); !errors.Is(err, ErrBadBinding) {
			t.Errorf("%s: err = %v, want ErrBadBinding", name, err)
		}
	}
	if _, err := DeriveAttemptKey(nil, keyPurposeRelay, testBinding(now)); err == nil {
		t.Error("an empty master must fail closed")
	}
}

// The helper spec carries no global secret and its TURN credential expires with
// the attempt, not later.
func TestHelperVoiceSpecHoldsNoGlobalSecretAndExpiresWithTheAttempt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := testBinding(now)
	m := testMinter()
	spec, err := m.Mint(b, now)
	if err != nil {
		t.Fatal(err)
	}

	var scan func(reflect.Type, string)
	scan = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Slice || typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || typ.PkgPath() == "time" {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if strings.Contains(strings.ToLower(f.Name), "secret") {
				t.Errorf("%s.%s: the helper spec must not carry a shared secret", path, f.Name)
			}
			scan(f.Type, path+"."+f.Name)
		}
	}
	scan(reflect.TypeOf(spec), "HelperVoiceSpec")

	raw, _ := json.Marshal(spec)
	for _, secret := range []string{"relay-master-secret", "turn-shared-secret", "dev-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("spec JSON leaks %q", secret)
		}
	}

	if len(spec.ICEServers) != 2 || spec.ICEServers[0].Username != "" {
		t.Fatalf("ice servers = %+v (want STUN plain + TURN with creds)", spec.ICEServers)
	}
	turn := spec.ICEServers[1]
	expiry, label, _ := strings.Cut(turn.Username, ":")
	if n, err := strconv.ParseInt(expiry, 10, 64); err != nil || n != b.Expires.Unix() {
		t.Fatalf("TURN username %q expires at %s, want the attempt expiry %d (not the %s default TTL)", turn.Username, expiry, b.Expires.Unix(), m.ICE.CredentialTTL)
	}
	if label != "voice_1:att_1" || turn.Credential == "" {
		t.Fatalf("TURN username label = %q", label)
	}
	if want, _ := m.ICE.turnCredentialsUntil("voice_1:att_1", b.Expires); want != turn.Username {
		t.Fatal("TURN credential is not the coturn REST pair")
	}
}

func TestMintRefusesExpiredBindingAndSkipsTURNWithoutSecret(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := testBinding(now)
	if _, err := testMinter().Mint(b, b.Expires.Add(time.Second)); !errors.Is(err, ErrAttemptExpired) {
		t.Fatalf("expired mint err = %v", err)
	}
	m := testMinter()
	m.ICE.TURNSecret = ""
	spec, err := m.Mint(b, now)
	if err != nil || len(spec.ICEServers) != 1 || spec.ICEServers[0].Username != "" {
		t.Fatalf("spec without TURN secret = %+v, %v", spec.ICEServers, err)
	}
}

// The helper verifies the client's control-channel credential with its
// per-attempt media key alone: no app secret exists on the helper.
func TestControlChannelCredentialVerifiesWithoutAppSecret(t *testing.T) {
	now := time.Now().UTC()
	b := testBinding(now)
	spec, err := testMinter().Mint(b, now)
	if err != nil {
		t.Fatal(err)
	}
	session := Session{ID: b.SessionID, TenantID: b.TenantID, AppID: "app-1"}
	cred := IssueMediaCredential([]byte(spec.MediaKey), session, now.Add(5*time.Minute))
	verify := spec.MediaCredentialVerifier()
	if !verify(session, cred) {
		t.Fatal("media-key credential rejected by the media-key verifier")
	}

	// The same session cannot be authorized by the app secret's credential,
	// by another attempt's key, or by the relay key.
	if verify(session, IssueMediaCredential([]byte("dev-secret"), session, now.Add(5*time.Minute))) {
		t.Error("an app-secret credential must not verify on a helper")
	}
	otherGen := b
	otherGen.LeaseGeneration++
	other, _ := testMinter().Mint(otherGen, now)
	if verify(session, IssueMediaCredential([]byte(other.MediaKey), session, now.Add(5*time.Minute))) {
		t.Error("another generation's media key must not verify")
	}
	if verify(session, IssueMediaCredential([]byte(spec.RelayKey), session, now.Add(5*time.Minute))) {
		t.Error("the relay key must not mint media credentials")
	}
	if verify(Session{ID: "voice_other", TenantID: b.TenantID, AppID: "app-1"}, cred) {
		t.Error("credential must be bound to its session")
	}
	if verify(session, IssueMediaCredential([]byte(spec.MediaKey), session, now.Add(-time.Second))) {
		t.Error("expired credential must fail")
	}
	if (HelperVoiceSpec{}).MediaCredentialVerifier()(session, cred) {
		t.Error("an empty key must fail closed")
	}
}

func TestHelperSpecRelayDialerSendsBoundHello(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	spec, _ := testMinter().Mint(testBinding(now), now)
	d := spec.RelayDialer(func(Session) (string, error) { return "127.0.0.1:1", nil })
	if string(d.Secret) != spec.RelayKey || d.NodeID != "node-a" || d.Generation != 3 {
		t.Fatalf("dialer = %+v", d)
	}
}

func TestHelperVoiceEnabledNeedsTheWholeLadder(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if HelperVoiceEnabled(env(nil)) {
		t.Fatal("default must be off")
	}
	voiceOnly := map[string]string{"UBAG_HELPER_VOICE": "true"}
	if HelperVoiceEnabled(env(voiceOnly)) {
		t.Fatal("voice without the lower flags must stay off")
	}
	all := map[string]string{"UBAG_HELPER_NODES": "1", "UBAG_HELPER_PLANE": "on", "UBAG_HELPER_DISPATCH": "true", "UBAG_HELPER_VOICE": "true"}
	if !HelperVoiceEnabled(env(all)) {
		t.Fatal("full ladder must enable")
	}
	all["UBAG_HELPER_DISPATCH"] = "off"
	if HelperVoiceEnabled(env(all)) {
		t.Fatal("a broken ladder must stay off")
	}
}

func guardFixture(t *testing.T) (HelperVoiceGuard, Session, AttemptBinding) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewMemoryStore()
	session, err := store.Reserve(ctx, ReserveRequest{
		SessionID: "voice_1", TenantID: "tenant-a", AppID: "app-1", Target: "chatgpt_web",
		Placements: []Placement{{Identity: "id-1", Instance: "inst-1"}}, LeaseTTL: time.Hour, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := AttemptBinding{
		SessionID: session.ID, TenantID: "tenant-a", AttemptID: "att_1", NodeID: "node-a",
		LeaseGeneration: 3, Expires: now.Add(time.Hour),
	}
	g := HelperVoiceGuard{
		Enabled: true, Store: store, Now: func() time.Time { return now },
		Current: func(_ context.Context, tenantID, sessionID string) (HelperVoiceLease, bool) {
			if tenantID != "tenant-a" || sessionID != "voice_1" {
				return HelperVoiceLease{}, false
			}
			return HelperVoiceLease{AttemptID: "att_1", NodeID: "node-a", LeaseGeneration: 3}, true
		},
	}
	return g, session, b
}

func TestGuardAdmitsOnlyTheCurrentFence(t *testing.T) {
	ctx := context.Background()
	g, session, b := guardFixture(t)
	if got, err := g.Authorize(ctx, b); err != nil || got.ID != session.ID {
		t.Fatalf("current fence: %v, %v", got.ID, err)
	}

	stale := b
	stale.LeaseGeneration = 2
	future := b
	future.LeaseGeneration = 4
	otherAttempt := b
	otherAttempt.AttemptID = "att_2"
	otherNode := b
	otherNode.NodeID = "node-b"
	expired := b
	expired.Expires = g.Now().Add(-time.Second)
	cases := []struct {
		name string
		b    AttemptBinding
		want error
	}{
		{"stale generation", stale, ErrStaleGeneration},
		{"future generation", future, ErrStaleGeneration},
		{"other attempt", otherAttempt, ErrStaleGeneration},
		{"other node", otherNode, ErrNodeMismatch},
		{"expired", expired, ErrAttemptExpired},
	}
	for _, tc := range cases {
		if _, err := g.Authorize(ctx, tc.b); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// A session of another tenant and a session that does not exist are the same
// not-found, so a probe learns nothing.
func TestGuardCrossTenantLookupIsIndistinguishableFromMissing(t *testing.T) {
	ctx := context.Background()
	g, _, b := guardFixture(t)
	foreign := b
	foreign.TenantID = "tenant-b"
	missing := b
	missing.SessionID = "voice_nope"
	_, errForeign := g.Authorize(ctx, foreign)
	_, errMissing := g.Authorize(ctx, missing)
	if !errors.Is(errForeign, ErrNotFound) || !errors.Is(errMissing, ErrNotFound) || errForeign.Error() != errMissing.Error() {
		t.Fatalf("foreign = %v, missing = %v (want the same ErrNotFound)", errForeign, errMissing)
	}

	noLease := g
	noLease.Current = nil
	if _, err := noLease.Authorize(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no lease = %v, want ErrNotFound", err)
	}
}

func TestGuardIsOffByDefaultAndRefusesEndedSessions(t *testing.T) {
	ctx := context.Background()
	g, session, b := guardFixture(t)
	off := g
	off.Enabled = false
	if _, err := off.Authorize(ctx, b); !errors.Is(err, ErrHelperVoiceDisabled) {
		t.Fatalf("disabled guard = %v", err)
	}
	if err := g.Store.Terminate(ctx, session.TenantID, session.ID, time.Now().UTC(), "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Authorize(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("terminated session = %v, want ErrNotFound", err)
	}
	bad := b
	bad.NodeID = ""
	if _, err := g.Authorize(ctx, bad); !errors.Is(err, ErrBadBinding) {
		t.Fatalf("malformed fence = %v", err)
	}
}
