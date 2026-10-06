package helper

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// This file is the helper side of the trust plane (ADR-0010 covers the primary
// side, where the helper is the CLIENT; here the roles are reversed, decision
// D3: the primary dials the helper). The helper authenticates the PRIMARY from
// the client certificate:
//
//   - the chain must reach the fleet manager CA (UBAG_HELPER_CA_FILE);
//   - the leaf must carry exactly one URI SAN, spiffe://ubag/primary/<id>,
//     equal to the configured primary identity. A Helper Node certificate
//     (spiffe://ubag/node/<id>) is also CA-issued and clientAuth-capable, so
//     chain validity alone would let one compromised helper dispatch work to
//     every other helper; the exact URI SAN closes that;
//   - the leaf is not a CA, has the clientAuth EKU and lives at most 72h (the
//     manager CA's maximum, plus a backdating slack);
//   - optionally the SPKI SHA-256 is one of at most two configured pins. Certs
//     live at most 72h, so a pin only helps when the primary re-uses its key.
//
// The certificate is the only identity source: the CN and other SANs are ignored.

const (
	// PrimaryURISANPrefix and NodeURISANPrefix are the identity namespaces. A
	// primary identity can never be a node identity and vice versa.
	PrimaryURISANPrefix = "spiffe://ubag/primary/"
	NodeURISANPrefix    = "spiffe://ubag/node/"

	// MaxCertLifetime is the longest NotAfter-NotBefore accepted (decision D3).
	MaxCertLifetime   = 72 * time.Hour
	certBackdateSlack = 10 * time.Minute

	maxRecvMsgBytes      = 1 << 20
	maxConcurrentStreams = 32
	handshakeTimeout     = 10 * time.Second
)

// PrimaryURISAN is the identity URI of the primary with the given id.
func PrimaryURISAN(id string) string { return PrimaryURISANPrefix + id }

// NodeURISAN is the identity URI of the Helper Node with the given id.
func NodeURISAN(id string) string { return NodeURISANPrefix + id }

// Reasons a peer is rejected. They go to the OnReject hook (log/metrics); the
// peer only ever sees a generic status.
const (
	ReasonNoCert       = "no_client_cert"
	ReasonBadIdentity  = "bad_identity"
	ReasonCertWindow   = "cert_not_valid"
	ReasonCertLifetime = "cert_lifetime"
	ReasonUnknownSPKI  = "unknown_spki"
)

// RejectError is the typed failure of PrimaryAuth.Verify.
type RejectError struct{ Reason string }

func (e *RejectError) Error() string { return "helper: peer rejected: " + e.Reason }

// PrimaryAuth authenticates the primary. Safe for concurrent use.
type PrimaryAuth struct {
	uriSAN string
	pins   []string
	// Now is the clock for certificate windows (tests); nil = time.Now.
	Now func() time.Time
	// OnReject observes every rejection. It must not block.
	OnReject func(reason string)
}

// NewPrimaryAuth builds the check for one primary identity. spkiPins may be
// empty (no pin) or hold one or two lowercase-hex SHA-256 values (current and
// next, for a rotation overlap).
func NewPrimaryAuth(uriSAN string, spkiPins []string) (*PrimaryAuth, error) {
	id, ok := strings.CutPrefix(uriSAN, PrimaryURISANPrefix)
	if !ok || !nodeIDRe.MatchString(id) {
		return nil, fmt.Errorf("primary identity must be %s<id> with id matching %s", PrimaryURISANPrefix, nodeIDRe)
	}
	if len(spkiPins) > 2 {
		return nil, errors.New("at most two primary SPKI pins (current and next)")
	}
	for _, pin := range spkiPins {
		if !sha256Re.MatchString(pin) {
			return nil, errors.New("a primary SPKI pin must be 64 lowercase hex characters")
		}
	}
	return &PrimaryAuth{uriSAN: uriSAN, pins: slices.Clone(spkiPins)}, nil
}

func (p *PrimaryAuth) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Verify checks a leaf certificate that already chained to the CA.
func (p *PrimaryAuth) Verify(leaf *x509.Certificate) error {
	if reason := p.check(leaf); reason != "" {
		if p.OnReject != nil {
			p.OnReject(reason)
		}
		return &RejectError{Reason: reason}
	}
	return nil
}

func (p *PrimaryAuth) check(leaf *x509.Certificate) string {
	switch {
	case leaf == nil:
		return ReasonNoCert
	case leaf.IsCA, len(leaf.URIs) != 1, leaf.URIs[0].String() != p.uriSAN,
		!slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth):
		return ReasonBadIdentity
	}
	if now := p.now(); now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return ReasonCertWindow
	}
	if leaf.NotAfter.Sub(leaf.NotBefore) > MaxCertLifetime+certBackdateSlack {
		return ReasonCertLifetime
	}
	if len(p.pins) > 0 && !slices.Contains(p.pins, spkiHex(leaf)) {
		return ReasonUnknownSPKI
	}
	return ""
}

// ServerTLSConfig is the helper's listener configuration: TLS 1.3 only, a
// client certificate is required and must chain to clientCAs, and the leaf must
// pass Verify before the handshake completes. Session tickets are off so a
// resumed session never outlives a certificate window.
func (p *PrimaryAuth) ServerTLSConfig(clientCAs *x509.CertPool, getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		ClientAuth:             tls.RequireAndVerifyClientCert,
		ClientCAs:              clientCAs,
		GetCertificate:         getCert,
		SessionTicketsDisabled: true,
		Time:                   p.Now,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return p.Verify(nil)
			}
			return p.Verify(cs.PeerCertificates[0])
		},
	}
}

// NewGRPCServer builds the helper's grpc.Server: mTLS from tlsCfg, the auth
// interceptors, svc registered, and nothing else. No reflection and no health
// service exist; an unknown service answers Unimplemented only after the peer
// authenticated. Keepalive reaps half-open connections to a primary that
// vanished.
func (p *PrimaryAuth) NewGRPCServer(tlsCfg *tls.Config, svc helperv1.HelperServiceServer) *grpc.Server {
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.ChainUnaryInterceptor(p.UnaryInterceptor()),
		grpc.ChainStreamInterceptor(p.StreamInterceptor()),
		grpc.UnknownServiceHandler(func(any, grpc.ServerStream) error {
			return status.Error(codes.Unimplemented, "unknown service")
		}),
		grpc.MaxRecvMsgSize(maxRecvMsgBytes),
		grpc.MaxConcurrentStreams(maxConcurrentStreams),
		grpc.ConnectionTimeout(handshakeTimeout),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
	)
	helperv1.RegisterHelperServiceServer(srv, svc)
	return srv
}

// UnaryInterceptor authenticates the peer of every unary RPC.
func (p *PrimaryAuth) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if _, err := p.authenticate(ctx); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor authenticates the peer of every stream and ends the stream
// context when the peer's certificate expires (the attempt itself is not tied
// to the stream and keeps running on its lease).
func (p *PrimaryAuth) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		leaf, err := p.authenticate(ss.Context())
		if err != nil {
			return err
		}
		ctx, cancel := context.WithDeadline(ss.Context(), leaf.NotAfter)
		defer cancel()
		return handler(srv, &boundedStream{ServerStream: ss, ctx: ctx})
	}
}

type boundedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *boundedStream) Context() context.Context { return s.ctx }

// authenticate resolves the TLS peer certificate of an RPC. A connection with
// no verified client certificate (plaintext, or TLS without client auth) is
// rejected, so wiring the interceptors to the wrong server fails closed.
func (p *PrimaryAuth) authenticate(ctx context.Context) (*x509.Certificate, error) {
	var leaf *x509.Certificate
	if pr, ok := peer.FromContext(ctx); ok {
		if ti, ok := pr.AuthInfo.(credentials.TLSInfo); ok && len(ti.State.PeerCertificates) > 0 {
			leaf = ti.State.PeerCertificates[0]
		}
	}
	if err := p.Verify(leaf); err != nil {
		return nil, status.Error(codes.Unauthenticated, "primary authentication failed")
	}
	return leaf, nil
}
