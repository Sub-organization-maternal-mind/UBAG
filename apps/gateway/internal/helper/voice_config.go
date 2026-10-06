package helper

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The voice half of the helper's configuration surface (P5.10). Like the rest of
// it, none of this is a secret: the per-call relay, media and TURN credentials
// arrive from the primary inside each OfferVoice and live only for that call.
const (
	// EnvVoice turns HelperVoiceService on. Default off. On the helper it is read
	// alone: the UBAG_HELPER_NODES < UBAG_HELPER_PLANE < UBAG_HELPER_DISPATCH <
	// UBAG_HELPER_VOICE ladder is the primary's, which only dials the service when
	// the whole ladder is on.
	EnvVoice = "UBAG_HELPER_VOICE"
	// EnvVoiceEnvs lists this node's exclusive audio environments, comma separated,
	// each "<instance_ref>=<cdp_port>:<relay_port>": the name the primary addresses
	// it by, the local browser's CDP port and its audio relay's port. There is no
	// host: both are always 127.0.0.1, so a WAN CDP endpoint cannot be configured.
	EnvVoiceEnvs = "UBAG_HELPER_VOICE_ENVS"
	// EnvVoiceKeyDir is the directory the per-call relay key file of each
	// environment lives in: <dir>/relay-key.<relay_port>, which is also that relay's
	// UBAG_VOICE_RELAY_KEY_FILE.
	EnvVoiceKeyDir = "UBAG_HELPER_VOICE_KEY_DIR"
	// EnvVoiceUDPPorts is "<min>-<max>", the bounded UDP range media binds in; the
	// fleet manager opens exactly that range to the internet.
	EnvVoiceUDPPorts = "UBAG_HELPER_VOICE_UDP_PORTS"
	// EnvVoiceNAT1To1 is the node's public IPv4 address, advertised as its host
	// candidate (optional; without it only the interface addresses are advertised).
	EnvVoiceNAT1To1 = "UBAG_HELPER_VOICE_NAT_1TO1_IP"
	// EnvVoiceServerViaTURN lets the node allocate a TURN relay candidate too, for
	// nodes with no publicly reachable UDP (optional; the TURN credentials come from
	// the primary per attempt).
	EnvVoiceServerViaTURN = "UBAG_HELPER_VOICE_SERVER_VIA_TURN"

	// minVoiceUDPPortsPerEnv is the floor of the UDP range per environment: every
	// ICE candidate of a call takes a port.
	minVoiceUDPPortsPerEnv = 8
	minVoicePort           = 1024
)

// VoiceSettings is the validated voice configuration of one ubag-helper process.
type VoiceSettings struct {
	Enabled        bool
	Environments   []VoiceEnvironment
	UDPMin, UDPMax uint16
	NAT1To1IP      string
	ServerViaTURN  bool
}

func voiceFlag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// parseVoiceSettings validates the voice variables. Off (the default) ignores
// all of them.
func parseVoiceSettings(env map[string]string) (VoiceSettings, error) {
	v := VoiceSettings{Enabled: voiceFlag(env[EnvVoice])}
	if !v.Enabled {
		return VoiceSettings{}, nil
	}
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	keyDir := env[EnvVoiceKeyDir]
	if fi, err := os.Stat(keyDir); keyDir == "" || err != nil || !fi.IsDir() {
		fail("%s must name an existing directory for the per-call relay key files", EnvVoiceKeyDir)
	}
	used := map[int]bool{}
	for _, entry := range strings.Split(env[EnvVoiceEnvs], ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		ref, ports, ok := strings.Cut(entry, "=")
		cdpS, relayS, ok2 := strings.Cut(ports, ":")
		cdp, err1 := strconv.Atoi(cdpS)
		relay, err2 := strconv.Atoi(relayS)
		switch {
		case !ok || !ok2 || err1 != nil || err2 != nil || !idTokenRe.MatchString(ref):
			fail("%s entry %q must be <instance_ref>=<cdp_port>:<relay_port>", EnvVoiceEnvs, entry)
		case cdp < minVoicePort || cdp > 65535 || relay < minVoicePort || relay > 65535:
			fail("%s entry %q: ports must be %d..65535", EnvVoiceEnvs, entry, minVoicePort)
		case used[cdp] || used[relay] || cdp == relay:
			fail("%s entry %q reuses a port: every CDP and relay port must be distinct", EnvVoiceEnvs, entry)
		case slices.ContainsFunc(v.Environments, func(e VoiceEnvironment) bool { return e.ID == ref }):
			fail("%s names %q twice", EnvVoiceEnvs, ref)
		default:
			used[cdp], used[relay] = true, true
			v.Environments = append(v.Environments, VoiceEnvironment{
				ID:           ref,
				CDPEndpoint:  "http://127.0.0.1:" + strconv.Itoa(cdp),
				RelayAddr:    "127.0.0.1:" + strconv.Itoa(relay),
				RelayKeyFile: filepath.Join(keyDir, "relay-key."+strconv.Itoa(relay)),
			})
		}
	}
	if n := len(v.Environments); n < 1 || n > maxAttemptsCeiling {
		fail("%s must list 1..%d audio environments", EnvVoiceEnvs, maxAttemptsCeiling)
	}

	loS, hiS, ok := strings.Cut(env[EnvVoiceUDPPorts], "-")
	lo, err1 := strconv.Atoi(loS)
	hi, err2 := strconv.Atoi(hiS)
	switch {
	case !ok || err1 != nil || err2 != nil || lo < minVoicePort || hi > 65535 || lo > hi:
		fail("%s must be <min>-<max> within %d..65535: media needs a bounded UDP range the fleet manager can open", EnvVoiceUDPPorts, minVoicePort)
	case hi-lo+1 < minVoiceUDPPortsPerEnv*len(v.Environments):
		fail("%s needs at least %d ports per audio environment", EnvVoiceUDPPorts, minVoiceUDPPortsPerEnv)
	default:
		v.UDPMin, v.UDPMax = uint16(lo), uint16(hi)
	}
	if ip := env[EnvVoiceNAT1To1]; ip != "" {
		addr, err := netip.ParseAddr(ip)
		if err != nil || !addr.Is4() || addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() {
			fail("%s must be one public IPv4 address", EnvVoiceNAT1To1)
		}
		v.NAT1To1IP = ip
	}
	v.ServerViaTURN = voiceFlag(env[EnvVoiceServerViaTURN])
	if len(errs) > 0 {
		return VoiceSettings{}, errors.Join(errs...)
	}
	return v, nil
}
