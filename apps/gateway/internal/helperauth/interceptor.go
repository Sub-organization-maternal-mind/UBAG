package helperauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

const (
	maxRecvMsgBytes      = 1 << 20
	maxConcurrentStreams = 64
	handshakeTimeout     = 10 * time.Second
)

type identityKey struct{}

// IdentityFromContext returns the authenticated node of the current RPC. It is
// the only trustworthy source of the caller's node id inside a handler.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// NewServer builds the helper-plane grpc.Server: TLS from tlsCfg (see
// ServerTLSConfig), the auth interceptors, and nothing else. No reflection and
// no health service are registered; unknown services answer Unimplemented only
// after the caller has authenticated. Later slices register their services on
// the returned server before Serve.
func (a *Authenticator) NewServer(tlsCfg *tls.Config) *grpc.Server {
	return grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.ChainUnaryInterceptor(a.UnaryInterceptor()),
		grpc.ChainStreamInterceptor(a.StreamInterceptor()),
		grpc.UnknownServiceHandler(func(any, grpc.ServerStream) error {
			return status.Error(codes.Unimplemented, "unknown service")
		}),
		grpc.MaxRecvMsgSize(maxRecvMsgBytes),
		grpc.MaxConcurrentStreams(maxConcurrentStreams),
		grpc.ConnectionTimeout(handshakeTimeout),
	)
}

// UnaryInterceptor authenticates the peer, then rejects a request whose node_id
// does not match the certificate identity.
func (a *Authenticator) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id, _, err := a.authenticate(ctx)
		if err != nil {
			return nil, err
		}
		if err := a.checkNodeID(id, req); err != nil {
			return nil, err
		}
		return handler(context.WithValue(ctx, identityKey{}, id), req)
	}
}

// StreamInterceptor authenticates at stream open and then re-runs the full
// check (certificate window, registry) around every message in either
// direction, so a revocation or a pin rotation ends a live stream at its next
// message. The stream context is also cancelled when the certificate expires.
func (a *Authenticator) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		id, leaf, err := a.authenticate(ss.Context())
		if err != nil {
			return err
		}
		ctx, cancel := context.WithDeadline(context.WithValue(ss.Context(), identityKey{}, id), leaf.NotAfter)
		defer cancel()
		return handler(srv, &guardedStream{ServerStream: ss, ctx: ctx, cancel: cancel, a: a, leaf: leaf, id: id})
	}
}

// authenticate resolves the TLS peer certificate of an RPC. A connection
// without a verified client certificate (plaintext, or TLS without client auth)
// is rejected, so wiring this interceptor to the wrong server fails closed.
func (a *Authenticator) authenticate(ctx context.Context) (Identity, *x509.Certificate, error) {
	var leaf *x509.Certificate
	if p, ok := peer.FromContext(ctx); ok {
		if ti, ok := p.AuthInfo.(credentials.TLSInfo); ok && len(ti.State.PeerCertificates) > 0 {
			leaf = ti.State.PeerCertificates[0]
		}
	}
	id, err := a.Verify(ctx, leaf)
	if err != nil {
		return Identity{}, nil, statusFor(err)
	}
	return id, leaf, nil
}

// statusFor maps a rejection to the status the peer sees. The reason is
// deliberately not disclosed (it would be an oracle for probing the registry).
func statusFor(err error) error {
	var re *RejectError
	if errors.As(err, &re) && re.Reason == ReasonStoreError {
		return status.Error(codes.Unavailable, "helper authentication unavailable")
	}
	return status.Error(codes.Unauthenticated, "helper authentication failed")
}

// checkNodeID compares the node_id a message claims with the certificate. The
// certificate is authoritative; a claim can only agree or be refused.
//   - A Fence (mutating RPCs) must carry the node id: empty is a mismatch.
//   - A bare node_id field (Handshake, ReportCapacity, Drain) may be empty.
func (a *Authenticator) checkNodeID(id Identity, msg any) error {
	var claimed string
	var required bool
	switch m := msg.(type) {
	case interface{ GetFence() *helperv1.Fence }:
		if f := m.GetFence(); f != nil {
			claimed, required = f.GetNodeId(), true
		}
	case interface{ GetFence() *helperv1.VoiceFence }:
		if f := m.GetFence(); f != nil {
			claimed, required = f.GetNodeId(), true
		}
	case interface{ GetNodeId() string }:
		claimed = m.GetNodeId()
	}
	if (claimed == "" && !required) || claimed == id.NodeID {
		return nil
	}
	a.reject(ReasonNodeMismatch, id.NodeID, nil)
	return status.Error(codes.PermissionDenied, "node_id does not match the certificate identity")
}

// guardedStream re-authenticates around every message. On any failure it also
// cancels the stream context so a handler blocked elsewhere unwinds.
type guardedStream struct {
	grpc.ServerStream
	ctx    context.Context
	cancel context.CancelFunc
	a      *Authenticator
	leaf   *x509.Certificate
	id     Identity
}

func (s *guardedStream) Context() context.Context { return s.ctx }

func (s *guardedStream) recheck() error {
	if err := s.ctx.Err(); err != nil { // client gone or certificate expired: not an auth event
		return status.FromContextError(err).Err()
	}
	if _, err := s.a.Verify(s.ctx, s.leaf); err != nil {
		s.cancel()
		return statusFor(err)
	}
	return nil
}

func (s *guardedStream) SendMsg(m any) error {
	if err := s.recheck(); err != nil {
		return err
	}
	return s.ServerStream.SendMsg(m)
}

func (s *guardedStream) RecvMsg(m any) error {
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return err
	}
	if err := s.recheck(); err != nil {
		return err
	}
	if err := s.a.checkNodeID(s.id, m); err != nil {
		s.cancel()
		return err
	}
	return nil
}
