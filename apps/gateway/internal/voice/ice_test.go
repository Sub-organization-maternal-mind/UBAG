package voice

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTURNCredentialsAreTimeLimitedCoturnRESTPairs(t *testing.T) {
	cfg := &ICEConfig{TURNURLs: []string{"turn:turn.example:3478?transport=udp"}, TURNSecret: "s3cret", CredentialTTL: 5 * time.Minute, STUNURLs: []string{"stun:stun.example:3478"}}
	now := time.Unix(1_700_000_000, 0)
	servers := cfg.ClientICEServers(Session{ID: "voice_1"}, now)
	if len(servers) != 2 || servers[0].Username != "" || servers[1].Username == "" {
		t.Fatalf("servers = %+v (want STUN without credentials, TURN with)", servers)
	}
	turn := servers[1]
	expiry, _, _ := strings.Cut(turn.Username, ":")
	if n, err := strconv.ParseInt(expiry, 10, 64); err != nil || n != now.Add(5*time.Minute).Unix() {
		t.Fatalf("username %q does not carry the expiry", turn.Username)
	}
	mac := hmac.New(sha1.New, []byte("s3cret")) //nolint:gosec
	mac.Write([]byte(turn.Username))
	if want := base64.StdEncoding.EncodeToString(mac.Sum(nil)); turn.Credential != want {
		t.Fatalf("credential does not match coturn's HMAC-SHA1 scheme")
	}
	if other := cfg.ClientICEServers(Session{ID: "voice_2"}, now)[1]; other.Credential == turn.Credential {
		t.Fatal("credentials must be per session")
	}
}

func TestICEWithoutSecretOffersNoTURNAndNilConfigIsSafe(t *testing.T) {
	cfg := &ICEConfig{TURNURLs: []string{"turn:turn.example:3478"}}
	if got := cfg.ClientICEServers(Session{ID: "s"}, time.Now()); len(got) != 0 {
		t.Fatalf("TURN offered without a secret: %+v", got)
	}
	var nilCfg *ICEConfig
	if nilCfg.ClientICEServers(Session{}, time.Now()) != nil || nilCfg.serverICEServers("s", time.Now()) != nil {
		t.Fatal("nil config must yield no servers")
	}
	if _, err := nilCfg.newAPI(); err != nil {
		t.Fatalf("nil config newAPI: %v", err)
	}
}

func TestICEPortRangeAndNATMappingBuildAnAPI(t *testing.T) {
	cfg := &ICEConfig{PortMin: 40000, PortMax: 40010, NAT1To1IP: "203.0.113.7"}
	if _, err := cfg.newAPI(); err != nil {
		t.Fatalf("newAPI: %v", err)
	}
	bad := &ICEConfig{PortMin: 5000, PortMax: 4000}
	if _, err := bad.newAPI(); err == nil {
		t.Fatal("an inverted port range must be rejected")
	}
}
