package nodes

import (
	"crypto/subtle"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxNodes bounds every table in the store; it equals the allocation_list
// maxItems in node-allocation.schema.json.
const MaxNodes = 256

// URISANPrefix is the URI SAN scheme a helper certificate carries; the node
// identity is the remainder (spiffe://ubag/node/<node_id>).
const URISANPrefix = "spiffe://ubag/node/"

// Allocation states and reservation states as the manager publishes them.
const (
	StateActive   = "active"
	StateDraining = "draining"
	StateRevoked  = "revoked"

	ReservationKnown   = "known"
	ReservationUnknown = "unknown"
)

var (
	nodeIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	regionRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	spkiRe   = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// NodeURISAN is the URI SAN identity for a node id.
func NodeURISAN(nodeID string) string { return URISANPrefix + nodeID }

// NodeIDFromURISAN returns the node id carried by a helper URI SAN.
func NodeIDFromURISAN(uri string) (string, bool) {
	id, ok := strings.CutPrefix(uri, URISANPrefix)
	return id, ok && nodeIDRe.MatchString(id)
}

// Allocation is one accepted node-allocation grant (node-allocation.schema.json
// v1, already net of the manager's reservations) plus when UBAG accepted it.
// There are no tenant or job ids: they never cross this interface.
type Allocation struct {
	NodeID              string
	Region              string
	Endpoint            string // WireGuard host:port of the helper's mTLS gRPC server
	URISAN              string
	SPKISHA256          string // optional pin carried by the grant; the registry is the authority
	CPUMillis           int
	MemoryBytes         int64
	ReservationState    string // known | unknown
	State               string // active | draining | revoked
	MaxBrowserWorkloads int
	VoiceCapable        bool
	UDPPortMin          int
	UDPPortMax          int
	NATIP               string
	ValidUntil          time.Time
	Generation          int64
	AcceptedAt          time.Time // stamped by the store on every accepted apply
}

// Grant converts to the subset the placement logic (Evaluate) reads.
func (a Allocation) Grant() Grant {
	return Grant{State: a.State, ReservationState: a.ReservationState,
		MaxBrowserWorkloads: a.MaxBrowserWorkloads, ValidUntil: a.ValidUntil}
}

// Validate enforces the schema plus the cross-field rules JSON Schema cannot
// express (node_id equals the URI SAN identity, udp range order, voice needs a
// range and NAT address). It never panics and fails closed on anything odd.
func (a Allocation) Validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
	}
	switch {
	case !nodeIDRe.MatchString(a.NodeID):
		return bad("node_id")
	case !regionRe.MatchString(a.Region):
		return bad("region")
	case !validEndpoint(a.Endpoint):
		return bad("endpoint")
	case a.URISAN != NodeURISAN(a.NodeID):
		return bad("uri_san must equal %s<node_id>", URISANPrefix)
	case a.SPKISHA256 != "" && !spkiRe.MatchString(a.SPKISHA256):
		return bad("spki_sha256")
	case a.CPUMillis < 0 || a.CPUMillis > 1_000_000:
		return bad("cpu_millis")
	case a.MemoryBytes < 0 || a.MemoryBytes > 17592186044416:
		return bad("memory_bytes")
	case a.ReservationState != ReservationKnown && a.ReservationState != ReservationUnknown:
		return bad("reservation_state")
	case a.State != StateActive && a.State != StateDraining && a.State != StateRevoked:
		return bad("state")
	case a.MaxBrowserWorkloads < 0 || a.MaxBrowserWorkloads > 64:
		return bad("max_browser_workloads")
	case a.ValidUntil.IsZero():
		return bad("valid_until")
	case a.Generation < 0 || a.Generation > 9007199254740991:
		return bad("generation")
	}
	if a.NATIP != "" && net.ParseIP(a.NATIP) == nil {
		return bad("nat_ip")
	}
	hasRange := a.UDPPortMin != 0 || a.UDPPortMax != 0
	if hasRange && (a.UDPPortMin < 1024 || a.UDPPortMax > 65535 || a.UDPPortMin > a.UDPPortMax) {
		return bad("udp_port_range")
	}
	if a.VoiceCapable && (!hasRange || a.NATIP == "") {
		return bad("voice_capable requires udp_port_range and nat_ip")
	}
	return nil
}

func validEndpoint(s string) bool {
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" || len(s) > 270 {
		return false
	}
	p, err := strconv.Atoi(port)
	return err == nil && p >= 1 && p <= 65535
}

// sameExceptValidity reports whether two grants of the same generation differ
// at most in valid_until (a renewal); anything else must bump the generation.
func (a Allocation) sameExceptValidity(b Allocation) bool {
	a.ValidUntil, b.ValidUntil = time.Time{}, time.Time{}
	a.AcceptedAt, b.AcceptedAt = time.Time{}, time.Time{}
	return a == b
}

// HelperState is the per-node runtime state placement needs between
// heartbeats: liveness, the pressure hysteresis, the earned ramp and the host
// size (the allocation schema does not carry host size; the ceiling table does
// need it, and unknown size means ceiling 0, so fail closed).
type HelperState struct {
	NodeID            string
	LastHeartbeat     time.Time
	PressureReduced   bool
	PressureCalmSince time.Time // zero unless a recovery window is open
	RampedLimit       int       // 0 = no history (a new helper starts at NewHelperWorkloads)
	HostCores         int
	HostMemoryBytes   int64
}

// Pressure rebuilds the hysteresis state.
func (s HelperState) Pressure() Pressure {
	return Pressure{Reduced: s.PressureReduced, calmSince: s.PressureCalmSince}
}

// WithPressure returns a copy carrying p.
func (s HelperState) WithPressure(p Pressure) HelperState {
	s.PressureReduced, s.PressureCalmSince = p.Reduced, p.calmSince
	return s
}

func (s HelperState) validate() error {
	if !nodeIDRe.MatchString(s.NodeID) || s.LastHeartbeat.IsZero() || s.RampedLimit < 0 || s.RampedLimit > 64 ||
		s.HostCores < 0 || s.HostMemoryBytes < 0 {
		return fmt.Errorf("%w: helper state", ErrInvalid)
	}
	return nil
}

// RegistryEntry is the pinned certificate identity for one node: the URI SAN
// plus the SPKI SHA-256 pins for the current and (during rotation) the next
// certificate. Revocation is sticky; a re-admitted helper gets a new node id.
type RegistryEntry struct {
	NodeID      string
	URISAN      string
	SPKICurrent string // lowercase hex SHA-256; empty = none
	SPKINext    string
	RevokedAt   time.Time // zero = not revoked
	UpdatedAt   time.Time // stamped by the store
}

func (e RegistryEntry) Revoked() bool { return !e.RevokedAt.IsZero() }

func (e RegistryEntry) validate() error {
	if !nodeIDRe.MatchString(e.NodeID) || e.URISAN != NodeURISAN(e.NodeID) ||
		(e.SPKICurrent != "" && !spkiRe.MatchString(e.SPKICurrent)) ||
		(e.SPKINext != "" && !spkiRe.MatchString(e.SPKINext)) {
		return fmt.Errorf("%w: registry entry", ErrInvalid)
	}
	return nil
}

// Matches reports whether a presented certificate identity is acceptable:
// not revoked, the URI SAN is this node's, and the SPKI hash equals the
// current or next pin. An empty pin never matches (fail closed).
func (e RegistryEntry) Matches(uriSAN, spkiHex string) bool {
	if e.Revoked() || spkiHex == "" || uriSAN != e.URISAN {
		return false
	}
	cur := e.SPKICurrent != "" && subtle.ConstantTimeCompare([]byte(spkiHex), []byte(e.SPKICurrent)) == 1
	next := e.SPKINext != "" && subtle.ConstantTimeCompare([]byte(spkiHex), []byte(e.SPKINext)) == 1
	return cur || next
}
