package voice

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // coturn's REST-credential scheme mandates HMAC-SHA1
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

// ICEConfig is the deployment's NAT-traversal configuration for the voice media
// plane. HTTPS signaling alone does not carry WebRTC audio: a client outside
// the Docker host needs the gateway's media UDP ports to be reachable
// (PortMin..PortMax published, NAT1To1IP set to the public address) and, for
// restrictive client networks, a TURN relay.
//
// TURN credentials are never static: with a shared secret (coturn
// "use-auth-secret") the gateway mints a time-limited username/credential per
// session, so a leaked credential expires on its own.
type ICEConfig struct {
	// STUNURLs / TURNURLs are handed to CLIENTS (and, when ServerViaTURN is
	// set, used by the gateway itself).
	STUNURLs   []string
	TURNURLs   []string
	TURNSecret string
	// CredentialTTL bounds minted TURN credentials (default 10 minutes).
	CredentialTTL time.Duration
	// NAT1To1IP, when set, is advertised as the gateway's host candidate: the
	// public address clients can actually reach.
	NAT1To1IP string
	// PortMin/PortMax restrict the gateway's ephemeral UDP ports so exactly
	// that range needs publishing. Both zero leaves the OS range.
	PortMin, PortMax uint16
	// IncludeLoopback also gathers loopback candidates. Tests and single-host
	// development only: it lets two peers on one machine connect without any
	// routable interface (CI runners).
	IncludeLoopback bool
	// ServerViaTURN makes the gateway allocate a relay candidate too, for
	// gateways with no publicly reachable UDP.
	ServerViaTURN bool
}

// ICEServer is the JSON shape clients feed straight into RTCPeerConnection.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

func (c *ICEConfig) ttl() time.Duration {
	if c != nil && c.CredentialTTL > 0 {
		return c.CredentialTTL
	}
	return 10 * time.Minute
}

// turnCredentials mints the coturn REST credential pair for a session:
// username "<expiry unix>:<session id>", credential base64(HMAC-SHA1).
func (c *ICEConfig) turnCredentials(sessionID string, now time.Time) (username, credential string) {
	username = fmt.Sprintf("%d:%s", now.Add(c.ttl()).Unix(), sessionID)
	mac := hmac.New(sha1.New, []byte(c.TURNSecret)) //nolint:gosec
	mac.Write([]byte(username))
	return username, base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// ClientICEServers lists the ICE servers a client should use for the session.
func (c *ICEConfig) ClientICEServers(session Session, now time.Time) []ICEServer {
	if c == nil {
		return nil
	}
	var out []ICEServer
	if len(c.STUNURLs) > 0 {
		out = append(out, ICEServer{URLs: c.STUNURLs})
	}
	if len(c.TURNURLs) > 0 && c.TURNSecret != "" {
		user, cred := c.turnCredentials(session.ID, now)
		out = append(out, ICEServer{URLs: c.TURNURLs, Username: user, Credential: cred})
	}
	return out
}

// serverICEServers lists the servers the gateway itself uses.
func (c *ICEConfig) serverICEServers(sessionID string, now time.Time) []webrtc.ICEServer {
	if c == nil {
		return nil
	}
	var out []webrtc.ICEServer
	if len(c.STUNURLs) > 0 {
		out = append(out, webrtc.ICEServer{URLs: c.STUNURLs})
	}
	if c.ServerViaTURN && len(c.TURNURLs) > 0 && c.TURNSecret != "" {
		user, cred := c.turnCredentials(sessionID, now)
		out = append(out, webrtc.ICEServer{URLs: c.TURNURLs, Username: user, Credential: cred})
	}
	return out
}

// newAPI builds the pion API with the deployment's NAT settings (UDP4 only,
// bounded port range, optional public-address mapping).
func (c *ICEConfig) newAPI() (*webrtc.API, error) {
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	if c != nil {
		se.SetIncludeLoopbackCandidate(c.IncludeLoopback)
		if c.PortMin != 0 || c.PortMax != 0 {
			if err := se.SetEphemeralUDPPortRange(c.PortMin, c.PortMax); err != nil {
				return nil, fmt.Errorf("voice: media UDP port range: %w", err)
			}
		}
		if ip := strings.TrimSpace(c.NAT1To1IP); ip != "" {
			se.SetNAT1To1IPs([]string{ip}, webrtc.ICECandidateTypeHost)
		}
	}
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, registry); err != nil {
		return nil, err
	}
	return webrtc.NewAPI(webrtc.WithSettingEngine(se), webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(registry)), nil
}
