package voice

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Helper-hosted voice credentials (P5.7, decision D7).
//
// A Helper Node terminates the voice media itself, so it must never hold a
// secret that could mint credentials for anyone else: not the app secret, not
// the relay secret, not the TURN shared secret. The primary instead DERIVES,
// per voice attempt, everything the helper needs from the attempt's binding
// (session, tenant, attempt, node, lease generation, expiry):
//
//   - a relay key that authenticates the helper-local relay hello (the hello
//     also signs node and generation, see RelayTokenBound);
//   - a media key that verifies the client's control-channel credential
//     (VerifyMediaCredential, the same credential format the connect response
//     already returns, so the OpenAPI response is unchanged);
//   - TURN credentials minted by the primary that expire with the attempt.
//
// The keys are domain separated and bound to the whole binding, so none of them
// verifies another attempt, node, generation or purpose. A leaked key costs one
// attempt until its expiry.

const (
	keyPurposeRelay = "relay"
	keyPurposeMedia = "media"
)

var (
	// ErrBadBinding means the attempt binding is incomplete or malformed.
	ErrBadBinding = errors.New("voice: invalid attempt binding")
	// ErrHelperVoiceDisabled is returned while UBAG_HELPER_VOICE is off.
	ErrHelperVoiceDisabled = errors.New("voice: helper voice is disabled")
	// ErrStaleGeneration means the caller's fence is not the lease the primary
	// holds (older or unknown generation, or another attempt): a stale writer.
	ErrStaleGeneration = errors.New("voice: stale lease generation")
	// ErrNodeMismatch means the fence names a different Helper Node.
	ErrNodeMismatch = errors.New("voice: node does not hold the lease")
	// ErrAttemptExpired means the fence's expiry has passed.
	ErrAttemptExpired = errors.New("voice: attempt expired")
)

// AttemptBinding is the identity of one leased voice attempt on one Helper Node.
type AttemptBinding struct {
	SessionID       string
	TenantID        string
	AttemptID       string
	NodeID          string
	LeaseGeneration uint64
	Expires         time.Time
}

// Validate rejects anything that could make the derivation ambiguous: empty
// parts, the "|" separator inside a part, an out-of-range generation.
func (b AttemptBinding) Validate() error {
	for _, part := range []string{b.SessionID, b.TenantID, b.AttemptID, b.NodeID} {
		if part == "" || len(part) > 128 || strings.Contains(part, "|") {
			return ErrBadBinding
		}
	}
	if b.LeaseGeneration == 0 || b.LeaseGeneration > maxBoundGeneration || b.Expires.IsZero() {
		return ErrBadBinding
	}
	return nil
}

// DeriveAttemptKey derives the per-attempt key for one purpose
// ("relay" or "media") as a hex string, ready to be used as the relay's
// UBAG_VOICE_RELAY_SECRET or the media credential key:
// hex(HMAC-SHA256(master, "ubag-voice-attempt|v1|<purpose>|<session>|<tenant>|<attempt>|<node>|<generation>|<expiry unix>")).
func DeriveAttemptKey(master []byte, purpose string, b AttemptBinding) (string, error) {
	if len(master) == 0 {
		return "", ErrRelayUnavailable
	}
	if err := b.Validate(); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, master)
	fmt.Fprintf(mac, "ubag-voice-attempt|v1|%s|%s|%s|%s|%s|%d|%d",
		purpose, b.SessionID, b.TenantID, b.AttemptID, b.NodeID, b.LeaseGeneration, b.Expires.Unix())
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// HelperVoiceSpec is everything the primary sends a helper to host one voice
// attempt. It deliberately has no field for a global secret.
type HelperVoiceSpec struct {
	Binding AttemptBinding `json:"binding"`
	// RelayKey and MediaKey are per-attempt derived keys (hex).
	RelayKey   string      `json:"relay_key"`
	MediaKey   string      `json:"media_key"`
	ICEServers []ICEServer `json:"ice_servers,omitempty"`
}

// HelperVoiceMinter mints HelperVoiceSpecs. Master is the primary's relay
// secret and ICE its deployment ICE configuration (its TURN shared secret is
// used only here, on the primary, to mint time-limited credentials).
type HelperVoiceMinter struct {
	Master []byte
	ICE    *ICEConfig
}

// Mint derives the spec for one attempt. TURN username/credential expire at
// the attempt's expiry (never later), and a binding that is already expired is
// refused.
func (m HelperVoiceMinter) Mint(b AttemptBinding, now time.Time) (HelperVoiceSpec, error) {
	relayKey, err := DeriveAttemptKey(m.Master, keyPurposeRelay, b)
	if err != nil {
		return HelperVoiceSpec{}, err
	}
	mediaKey, err := DeriveAttemptKey(m.Master, keyPurposeMedia, b)
	if err != nil {
		return HelperVoiceSpec{}, err
	}
	if !b.Expires.After(now) {
		return HelperVoiceSpec{}, ErrAttemptExpired
	}
	spec := HelperVoiceSpec{Binding: b, RelayKey: relayKey, MediaKey: mediaKey}
	if c := m.ICE; c != nil {
		if len(c.STUNURLs) > 0 {
			spec.ICEServers = append(spec.ICEServers, ICEServer{URLs: c.STUNURLs})
		}
		if len(c.TURNURLs) > 0 && c.TURNSecret != "" {
			user, cred := c.turnCredentialsUntil(b.SessionID+":"+b.AttemptID, b.Expires)
			spec.ICEServers = append(spec.ICEServers, ICEServer{URLs: c.TURNURLs, Username: user, Credential: cred})
		}
	}
	return spec, nil
}

// RelayDialer returns the dialer a helper-side MediaHub uses for this attempt:
// its hello is signed with the per-attempt relay key and bound to node and
// generation.
func (s HelperVoiceSpec) RelayDialer(address func(Session) (string, error)) *TCPRelayDialer {
	return &TCPRelayDialer{
		Address:    address,
		Secret:     []byte(s.RelayKey),
		NodeID:     s.Binding.NodeID,
		Generation: s.Binding.LeaseGeneration,
	}
}

// MediaCredentialVerifier returns the control-channel authorizer for a hub
// (MediaHub.AuthorizeControl) that checks credentials with the per-attempt
// media key alone: no app secret is involved.
func (s HelperVoiceSpec) MediaCredentialVerifier() func(Session, string) bool {
	key := []byte(s.MediaKey)
	return func(session Session, credential string) bool {
		return len(key) > 0 && VerifyMediaCredential(key, session, credential, time.Now().UTC())
	}
}

// IssueMediaCredential derives a short-lived credential scoped to ONE session:
// "voice-media|<tenant>|<app>|<session>|<expiryUnix>|<hmac>". It authorizes the
// client's control data channel (mute, ping). Primary-hosted voice keys it with
// the app secret; helper-hosted voice with the per-attempt media key.
func IssueMediaCredential(key []byte, session Session, expires time.Time) string {
	payload := fmt.Sprintf("voice-media|%s|%s|%s|%d", session.TenantID, session.AppID, session.ID, expires.Unix())
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return payload + "|" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyMediaCredential validates a credential against the session it must
// belong to. A credential minted for another tenant, app or session, under
// another key, or expired or altered, fails closed.
func VerifyMediaCredential(key []byte, session Session, credential string, now time.Time) bool {
	parts := strings.Split(credential, "|")
	if len(parts) != 6 || parts[0] != "voice-media" ||
		parts[1] != session.TenantID || parts[2] != session.AppID || parts[3] != session.ID {
		return false
	}
	expiresUnix, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || now.Unix() > expiresUnix {
		return false
	}
	expected := IssueMediaCredential(key, session, time.Unix(expiresUnix, 0))
	return hmac.Equal([]byte(expected), []byte(credential))
}

// HelperVoiceEnabled reports whether helper-hosted voice is on. It needs the
// whole flag ladder (UBAG_HELPER_NODES < UBAG_HELPER_PLANE < UBAG_HELPER_DISPATCH
// < UBAG_HELPER_VOICE); anything else, including unset, is off.
func HelperVoiceEnabled(getenv func(string) string) bool {
	for _, name := range []string{"UBAG_HELPER_NODES", "UBAG_HELPER_PLANE", "UBAG_HELPER_DISPATCH", "UBAG_HELPER_VOICE"} {
		switch strings.ToLower(strings.TrimSpace(getenv(name))) {
		case "1", "true", "on", "yes":
		default:
			return false
		}
	}
	return true
}

// HelperVoiceLease is the lease the primary currently holds for a session's
// voice attempt (supplied by the attempt/lease layer).
type HelperVoiceLease struct {
	AttemptID       string
	NodeID          string
	LeaseGeneration uint64
}

// HelperVoiceGuard authorizes the primary-side half of every helper voice
// control RPC (mute, interrupt, terminate, renew, reconnect, event stream):
// the session must exist for the TENANT (another tenant's session is
// indistinguishable from a missing one) and the fence must be exactly the lease
// the primary holds, unexpired. A stale or future generation, another attempt
// or another node is refused before anything reaches the helper.
type HelperVoiceGuard struct {
	Enabled bool
	Store   Store
	Current func(ctx context.Context, tenantID, sessionID string) (HelperVoiceLease, bool)
	Now     func() time.Time
}

// Authorize returns the session when the fence is current.
func (g HelperVoiceGuard) Authorize(ctx context.Context, b AttemptBinding) (Session, error) {
	if !g.Enabled {
		return Session{}, ErrHelperVoiceDisabled
	}
	if err := b.Validate(); err != nil {
		return Session{}, err
	}
	session, ok, err := g.Store.Get(ctx, b.TenantID, b.SessionID)
	if err != nil {
		return Session{}, err
	}
	if !ok || !session.Status.Active() {
		return Session{}, ErrNotFound
	}
	lease, ok := HelperVoiceLease{}, false
	if g.Current != nil {
		lease, ok = g.Current(ctx, b.TenantID, b.SessionID)
	}
	if !ok {
		return Session{}, ErrNotFound
	}
	now := time.Now()
	if g.Now != nil {
		now = g.Now()
	}
	switch {
	case lease.NodeID != b.NodeID:
		return Session{}, ErrNodeMismatch
	case lease.AttemptID != b.AttemptID || lease.LeaseGeneration != b.LeaseGeneration:
		return Session{}, ErrStaleGeneration
	case now.After(b.Expires):
		return Session{}, ErrAttemptExpired
	}
	return session, nil
}
