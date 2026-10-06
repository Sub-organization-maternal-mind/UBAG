// Package helperclient is the primary's side of the helper trust plane: the
// mutually authenticated dial to a Helper Node (decision D3, perf-fleet slice
// P4.14). The primary is the gRPC CLIENT and the helper the server behind mTLS,
// so this is the mirror of internal/helper/auth.go (which authenticates the
// primary) and of internal/helperauth (which authenticates nodes dialing in).
//
// What a dial proves about the helper it reached:
//
//   - its certificate chains to the fleet manager CA bundle
//     (UBAG_HELPER_CA_FILE) and carries the serverAuth extended key usage;
//   - its single URI SAN is exactly spiffe://ubag/node/<node id> for the node the
//     placement named. The CN, DNS names and IP addresses are ignored: the
//     helper is addressed by a WireGuard IP, never by a name a certificate could
//     vouch for, and a certificate for another node, even a valid one from the
//     same CA, is refused;
//   - it is a leaf, valid now, with a lifetime of at most 72 h (the manager CA's
//     maximum, plus a backdating slack);
//   - its SPKI SHA-256 is the current or next pin in the node registry (P4.4) and
//     the node is not revoked. The registry is read on every handshake; a store
//     error fails closed.
//
// TLS 1.3 only, no session resumption (a resumed session would skip the checks
// above), and the primary presents its own certificate (URI SAN
// spiffe://ubag/primary/<id>), which the helper checks.
package helperclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

const (
	// MaxCertLifetime is the longest NotAfter-NotBefore window accepted for a
	// helper certificate (decision D3), plus the same backdating slack the
	// listener side uses.
	MaxCertLifetime = 72 * time.Hour
	certSlack       = 10 * time.Minute

	defaultLookupTimeout = 2 * time.Second
	// keepaliveTime must not be below the helper's enforcement minimum (10 s).
	keepaliveTime    = 30 * time.Second
	keepaliveTimeout = 10 * time.Second
)

// Reasons a helper is refused. They go to the OnReject hook (log/metrics); the
// error a caller sees is the generic gRPC connection failure.
type Reason string

const (
	ReasonNoCert       Reason = "no_server_cert"
	ReasonBadChain     Reason = "bad_chain"
	ReasonBadIdentity  Reason = "bad_identity" // not exactly the node's URI SAN, a CA cert
	ReasonCertWindow   Reason = "cert_not_valid"
	ReasonCertLifetime Reason = "cert_lifetime"
	ReasonUnknownNode  Reason = "unknown_node"
	ReasonUnknownSPKI  Reason = "unknown_spki"
	ReasonRevoked      Reason = "revoked"
	ReasonStoreError   Reason = "store_error"
)

// Registry is the slice of nodes.Store the dial reads: the pinned identity of a
// node. nodes.Store satisfies it.
type Registry interface {
	GetRegistry(ctx context.Context, nodeID string) (nodes.RegistryEntry, error)
}

// Config configures a Dialer.
type Config struct {
	// CAs are the trust anchors that issue helper certificates (the fleet
	// manager CA bundle). Required.
	CAs *x509.CertPool
	// ClientCertificate returns the primary's own certificate for each
	// handshake (helperauth.KeyPair re-reads renewed files). Required.
	ClientCertificate func(*tls.CertificateRequestInfo) (*tls.Certificate, error)
	// Registry supplies the SPKI pins and revocation. Required.
	Registry Registry
	// Now is the clock for certificate windows (tests); nil = time.Now.
	Now func() time.Time
	// LookupTimeout bounds one registry read; <= 0 means 2 s.
	LookupTimeout time.Duration
	// OnReject observes every refused helper (log, metrics). Must not block. Nil
	// logs one warning line.
	OnReject func(reason Reason, nodeID string)
}

// Dialer opens verified connections to Helper Nodes. Safe for concurrent use.
type Dialer struct{ cfg Config }

// New validates cfg.
func New(cfg Config) (*Dialer, error) {
	if cfg.CAs == nil || cfg.ClientCertificate == nil || cfg.Registry == nil {
		return nil, errors.New("helperclient: CAs, ClientCertificate and Registry are required")
	}
	if cfg.LookupTimeout <= 0 {
		cfg.LookupTimeout = defaultLookupTimeout
	}
	return &Dialer{cfg: cfg}, nil
}

// NewFromKeyPair is New with the primary's certificate served by a
// helperauth.KeyPair.
func NewFromKeyPair(cas *x509.CertPool, kp *helperauth.KeyPair, registry Registry) (*Dialer, error) {
	if kp == nil {
		return nil, errors.New("helperclient: a key pair is required")
	}
	return New(Config{
		CAs: cas, Registry: registry,
		ClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return kp.GetCertificate(nil) },
	})
}

// Conn is a connection to one Helper Node.
type Conn struct {
	helperv1.HelperServiceClient
	cc *grpc.ClientConn
}

// Close tears the connection down.
func (c *Conn) Close() error { return c.cc.Close() }

// Dial connects to the helper at endpoint (host:port) and requires it to be
// node nodeID. The connection is lazy: the TLS handshake, and with it every
// check in the package comment, happens on the first RPC, which fails fast
// (Unavailable) when the helper is not the node asked for.
func (d *Dialer) Dial(_ context.Context, nodeID, endpoint string) (*Conn, error) {
	if !nodes.ValidNodeID(nodeID) {
		return nil, errors.New("helperclient: invalid node id")
	}
	if host, port, err := net.SplitHostPort(endpoint); err != nil || host == "" || port == "" {
		return nil, errors.New("helperclient: endpoint must be host:port")
	}
	tlsCfg := &tls.Config{
		MinVersion:           tls.VersionTLS13,
		GetClientCertificate: d.cfg.ClientCertificate,
		Time:                 d.cfg.Now,
		// The helper is addressed by IP and its certificate names it by URI SAN, so
		// the standard host-name verification cannot apply. Every check is made in
		// VerifyConnection instead; nothing is trusted without it.
		InsecureSkipVerify: true, //nolint:gosec // verified in VerifyConnection below
		VerifyConnection:   func(cs tls.ConnectionState) error { return d.verify(cs, nodeID) },
	}
	cc, err := grpc.NewClient("passthrough:///"+endpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: keepaliveTime, Timeout: keepaliveTimeout, PermitWithoutStream: true}),
	)
	if err != nil {
		return nil, fmt.Errorf("helperclient: dial: %w", err)
	}
	return &Conn{HelperServiceClient: helperv1.NewHelperServiceClient(cc), cc: cc}, nil
}

func (d *Dialer) now() time.Time {
	if d.cfg.Now != nil {
		return d.cfg.Now()
	}
	return time.Now()
}

func (d *Dialer) reject(reason Reason, nodeID string, cause error) error {
	if d.cfg.OnReject != nil {
		d.cfg.OnReject(reason, nodeID)
	} else {
		slog.Warn("helper refused: its certificate does not match the node asked for", "reason", reason, "node_id", nodeID)
	}
	if cause != nil {
		return fmt.Errorf("helperclient: helper refused (%s): %w", reason, cause)
	}
	return fmt.Errorf("helperclient: helper refused (%s)", reason)
}

// verify is the whole server-certificate check (see the package comment).
func (d *Dialer) verify(cs tls.ConnectionState, nodeID string) error {
	if len(cs.PeerCertificates) == 0 {
		return d.reject(ReasonNoCert, nodeID, nil)
	}
	leaf := cs.PeerCertificates[0]
	now := d.now()

	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: d.cfg.CAs, Intermediates: inter, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		var invalid x509.CertificateInvalidError
		if errors.As(err, &invalid) && invalid.Reason == x509.Expired {
			return d.reject(ReasonCertWindow, nodeID, nil)
		}
		return d.reject(ReasonBadChain, nodeID, nil)
	}
	uri := nodes.NodeURISAN(nodeID)
	if leaf.IsCA || len(leaf.URIs) != 1 || leaf.URIs[0].String() != uri {
		return d.reject(ReasonBadIdentity, nodeID, nil)
	}
	if leaf.NotAfter.Sub(leaf.NotBefore) > MaxCertLifetime+certSlack {
		return d.reject(ReasonCertLifetime, nodeID, nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), d.cfg.LookupTimeout)
	defer cancel()
	entry, err := d.cfg.Registry.GetRegistry(ctx, nodeID)
	switch {
	case errors.Is(err, nodes.ErrNotFound):
		return d.reject(ReasonUnknownNode, nodeID, nil)
	case err != nil:
		return d.reject(ReasonStoreError, nodeID, err) // fail closed
	case entry.Revoked():
		return d.reject(ReasonRevoked, nodeID, nil)
	case !entry.Matches(uri, helperauth.SPKIHex(leaf)):
		return d.reject(ReasonUnknownSPKI, nodeID, nil)
	}
	return nil
}
