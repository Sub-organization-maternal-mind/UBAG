// Package helperauth is the Helper Node trust plane (UBAG_HELPER_PLANE, default
// off): a separate mTLS gRPC listener whose peers are identified ONLY by the
// URI SAN of their client certificate (spiffe://ubag/node/<node_id>), pinned by
// SPKI hash in the P4.4 node registry and re-checked on every RPC and every
// stream message. The plaintext public gRPC server never serves these RPCs.
//
// Certificate profile (the fleet manager's CA must issue exactly this): leaf,
// not a CA, one URI SAN, extended key usage clientAuth, validity of at most 72h
// (plus a small backdating slack). The subject CN and every other SAN are
// ignored: a CN can never name a node.
package helperauth

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

const (
	// MaxCertLifetime is the longest NotAfter-NotBefore window accepted
	// (decision D3: manager CA issues certs of at most 72h).
	MaxCertLifetime = 72 * time.Hour
	// certBackdateSlack tolerates CAs that backdate NotBefore for clock skew.
	certBackdateSlack = 10 * time.Minute
)

// Reason says why a peer was rejected. It is for logs and audit hooks; the peer
// only ever sees a generic status.
type Reason string

const (
	ReasonNoCert       Reason = "no_client_cert"
	ReasonBadIdentity  Reason = "bad_identity"   // not exactly one well-formed node URI SAN, a CA cert, or no clientAuth EKU
	ReasonCertWindow   Reason = "cert_not_valid" // expired or not yet valid
	ReasonCertLifetime Reason = "cert_lifetime"  // validity window longer than MaxCertLifetime
	ReasonUnknownNode  Reason = "unknown_node"
	ReasonUnknownSPKI  Reason = "unknown_spki" // SPKI hash is neither the current nor the next pin
	ReasonRevoked      Reason = "revoked"
	ReasonStoreError   Reason = "store_error"
	ReasonNodeMismatch Reason = "node_id_mismatch" // message node_id differs from the certificate identity
)

// RejectError is the typed failure of every check in this package.
type RejectError struct {
	Reason Reason
	NodeID string // only set when it parsed from the certificate
	Err    error  // underlying cause (store errors), never sent to the peer
}

func (e *RejectError) Error() string { return "helperauth: rejected: " + string(e.Reason) }
func (e *RejectError) Unwrap() error { return e.Err }

// Identity is the authenticated Helper Node behind a connection.
type Identity struct {
	NodeID     string
	URISAN     string
	SPKISHA256 string    // lowercase hex SHA-256 of the leaf SubjectPublicKeyInfo
	NotAfter   time.Time // leaf expiry
}

// SPKIHex is the pin format used by the registry.
func SPKIHex(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// Registry is the slice of nodes.Store the trust plane reads on every check.
// A caching wrapper would delay revocation by its TTL; none is used by default.
type Registry interface {
	GetRegistry(ctx context.Context, nodeID string) (nodes.RegistryEntry, error)
}

// Authenticator resolves certificates to Identities. The zero clock and hook
// are valid (time.Now, no hook).
type Authenticator struct {
	Registry Registry
	// Now is the clock for validity checks (tests); nil = time.Now.
	Now func() time.Time
	// OnReject observes every rejection (audit/metrics hook, P4.8). nodeID is
	// empty unless it parsed from the certificate. Must not block.
	OnReject func(reason Reason, nodeID string)
	// LookupTimeout bounds one registry read; <= 0 means 2s.
	LookupTimeout time.Duration
}

func (a *Authenticator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Authenticator) reject(reason Reason, nodeID string, cause error) error {
	if a.OnReject != nil {
		a.OnReject(reason, nodeID)
	}
	return &RejectError{Reason: reason, NodeID: nodeID, Err: cause}
}

// Verify checks a leaf certificate that already passed chain verification:
// well-formed identity, currently valid and short-lived, then the registry
// (known, not revoked, SPKI equals the current or next pin). It is called at
// the TLS handshake and again for every RPC and stream message, so a revocation
// or pin rotation takes effect on the next message.
func (a *Authenticator) Verify(ctx context.Context, leaf *x509.Certificate) (Identity, error) {
	if leaf == nil {
		return Identity{}, a.reject(ReasonNoCert, "", nil)
	}
	id, bad := leafIdentity(leaf)
	if bad != "" {
		return Identity{}, a.reject(bad, "", nil)
	}
	now := a.now()
	switch {
	case now.Before(leaf.NotBefore) || now.After(leaf.NotAfter):
		return Identity{}, a.reject(ReasonCertWindow, id.NodeID, nil)
	case leaf.NotAfter.Sub(leaf.NotBefore) > MaxCertLifetime+certBackdateSlack:
		return Identity{}, a.reject(ReasonCertLifetime, id.NodeID, nil)
	}
	timeout := a.LookupTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entry, err := a.Registry.GetRegistry(ctx, id.NodeID)
	switch {
	case errors.Is(err, nodes.ErrNotFound):
		return Identity{}, a.reject(ReasonUnknownNode, id.NodeID, nil)
	case err != nil:
		return Identity{}, a.reject(ReasonStoreError, id.NodeID, err)
	case entry.Revoked():
		return Identity{}, a.reject(ReasonRevoked, id.NodeID, nil)
	case !entry.Matches(id.URISAN, id.SPKISHA256):
		return Identity{}, a.reject(ReasonUnknownSPKI, id.NodeID, nil)
	}
	return id, nil
}

// leafIdentity extracts the identity from the certificate alone. The URI SAN is
// the only identity source: exactly one is required and the CN is never read.
func leafIdentity(leaf *x509.Certificate) (Identity, Reason) {
	if leaf.IsCA || len(leaf.URIs) != 1 || !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		return Identity{}, ReasonBadIdentity
	}
	uri := leaf.URIs[0].String()
	nodeID, ok := nodes.NodeIDFromURISAN(uri)
	if !ok {
		return Identity{}, ReasonBadIdentity
	}
	return Identity{NodeID: nodeID, URISAN: uri, SPKISHA256: SPKIHex(leaf), NotAfter: leaf.NotAfter}, ""
}
