package helperauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth/authtest"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// fakeHelper stands in for the services later slices register on the plane. It
// answers with the identity the interceptor placed in the context, so a test
// sees exactly which node the server believes it is talking to.
type fakeHelper struct {
	helperv1.UnimplementedHelperServiceServer
	run func(*helperv1.RunAttemptRequest, grpc.ServerStreamingServer[helperv1.RunAttemptResponse]) error
}

func (f *fakeHelper) Handshake(ctx context.Context, _ *helperv1.HandshakeRequest) (*helperv1.HandshakeResponse, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Internal, "no identity in context")
	}
	return &helperv1.HandshakeResponse{NodeId: id.NodeID}, nil
}

func (f *fakeHelper) ReportCapacity(context.Context, *helperv1.ReportCapacityRequest) (*helperv1.ReportCapacityResponse, error) {
	return &helperv1.ReportCapacityResponse{}, nil
}

func (f *fakeHelper) RenewAttempt(context.Context, *helperv1.RenewAttemptRequest) (*helperv1.RenewAttemptResponse, error) {
	return &helperv1.RenewAttemptResponse{}, nil
}

func (f *fakeHelper) RunAttempt(req *helperv1.RunAttemptRequest, s grpc.ServerStreamingServer[helperv1.RunAttemptResponse]) error {
	return f.run(req, s)
}

type clock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.off += d
	c.mu.Unlock()
}

type env struct {
	t     *testing.T
	ca    *authtest.CA
	store *nodes.MemoryStore
	auth  *Authenticator
	clk   *clock
	addr  string
	srv   *grpc.Server

	mu      sync.Mutex
	rejects []Reason
}

func (e *env) reasons() []Reason {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Reason(nil), e.rejects...)
}

// newEnv starts a helper-plane server on loopback. With handshakeCheck=false the
// registry check at the TLS handshake is removed so the interceptor layer is
// exercised on its own (the CA chain is still enforced by TLS).
func newEnv(t *testing.T, handshakeCheck bool, svc helperv1.HelperServiceServer) *env {
	t.Helper()
	e := &env{t: t, ca: authtest.NewCA(t), store: nodes.NewMemoryStore(), clk: &clock{}}
	e.auth = &Authenticator{Registry: e.store, Now: e.clk.Now, OnReject: func(r Reason, _ string) {
		e.mu.Lock()
		e.rejects = append(e.rejects, r)
		e.mu.Unlock()
	}}
	serverLeaf := e.ca.Issue(t, authtest.Spec{NodeID: "primary"})
	cfg := e.auth.ServerTLSConfig(e.ca.Pool, func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &serverLeaf.TLS, nil })
	if !handshakeCheck {
		cfg.VerifyConnection = nil
	}
	e.srv = e.auth.NewServer(cfg)
	helperv1.RegisterHelperServiceServer(e.srv, svc)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.addr = lis.Addr().String()
	go func() { _ = e.srv.Serve(lis) }()
	t.Cleanup(e.srv.Stop)
	return e
}

func (e *env) register(id string, leaf *authtest.Leaf) {
	e.t.Helper()
	err := e.store.PutRegistry(context.Background(), nodes.RegistryEntry{NodeID: id, URISAN: nodes.NodeURISAN(id), SPKICurrent: leaf.SPKI}, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) dial(cert *tls.Certificate) helperv1.HelperServiceClient {
	return e.dialWith(cert, e.ca.Pool)
}

func (e *env) dialWith(cert *tls.Certificate, roots *x509.CertPool) helperv1.HelperServiceClient {
	e.t.Helper()
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "localhost"}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	conn, err := grpc.NewClient(e.addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = conn.Close() })
	return helperv1.NewHelperServiceClient(conn)
}

func ctx5(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func wantCode(t *testing.T, err error, want ...codes.Code) {
	t.Helper()
	got := status.Code(err)
	for _, w := range want {
		if got == w {
			return
		}
	}
	t.Fatalf("code = %v (err %v), want one of %v", got, err, want)
}

func TestAcceptsRegisteredNode(t *testing.T) {
	e := newEnv(t, true, &fakeHelper{})
	leaf := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1"})
	e.register("helper-1", leaf)
	resp, err := e.dial(&leaf.TLS).Handshake(ctx5(t), &helperv1.HandshakeRequest{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if resp.GetNodeId() != "helper-1" {
		t.Fatalf("identity = %q, want helper-1", resp.GetNodeId())
	}
	if r := e.reasons(); len(r) != 0 {
		t.Fatalf("unexpected rejections: %v", r)
	}
}

// Every way a peer can fail to be a registered Helper Node, at both layers: the
// TLS handshake and (handshake check removed) the per-RPC interceptor.
func TestRejectsUntrustedPeers(t *testing.T) {
	otherCA := authtest.NewCA(t)
	cases := []struct {
		name string
		// issue returns the client cert to present and registers whatever the
		// registry should know; nil cert means "present nothing".
		issue func(t *testing.T, e *env) *tls.Certificate
		// tlsLayer: Go's TLS stack itself refuses (so even interceptor-only
		// servers fail the connection, with Unavailable).
		tlsLayer bool
		reason   Reason // what the auth hook must record when we are the ones refusing
	}{
		{name: "no client cert", tlsLayer: true, issue: func(t *testing.T, e *env) *tls.Certificate { return nil }},
		{name: "wrong CA", tlsLayer: true, issue: func(t *testing.T, e *env) *tls.Certificate {
			l := otherCA.Issue(t, authtest.Spec{NodeID: "helper-1"})
			e.register("helper-1", l)
			return &l.TLS
		}},
		{name: "expired", tlsLayer: true, issue: func(t *testing.T, e *env) *tls.Certificate {
			l := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1", NotBefore: time.Now().Add(-3 * time.Hour), NotAfter: time.Now().Add(-time.Hour)})
			e.register("helper-1", l)
			return &l.TLS
		}},
		{name: "not yet valid", tlsLayer: true, issue: func(t *testing.T, e *env) *tls.Certificate {
			l := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1", NotBefore: time.Now().Add(time.Hour), NotAfter: time.Now().Add(2 * time.Hour)})
			e.register("helper-1", l)
			return &l.TLS
		}},
		{name: "server-only EKU", tlsLayer: true, issue: func(t *testing.T, e *env) *tls.Certificate {
			l := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1", EKUs: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
			e.register("helper-1", l)
			return &l.TLS
		}},
		{name: "unknown SPKI (right node, other key)", reason: ReasonUnknownSPKI, issue: func(t *testing.T, e *env) *tls.Certificate {
			e.register("helper-1", e.ca.Issue(t, authtest.Spec{NodeID: "helper-1"})) // pins a different key
			return &e.ca.Issue(t, authtest.Spec{NodeID: "helper-1"}).TLS
		}},
		{name: "unknown node", reason: ReasonUnknownNode, issue: func(t *testing.T, e *env) *tls.Certificate {
			return &e.ca.Issue(t, authtest.Spec{NodeID: "ghost"}).TLS
		}},
		{name: "revoked", reason: ReasonRevoked, issue: func(t *testing.T, e *env) *tls.Certificate {
			l := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1"})
			e.register("helper-1", l)
			if err := e.store.RevokeNode(context.Background(), "helper-1", time.Now()); err != nil {
				t.Fatal(err)
			}
			return &l.TLS
		}},
		{name: "lifetime over 72h", reason: ReasonCertLifetime, issue: func(t *testing.T, e *env) *tls.Certificate {
			l := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1", NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(100 * time.Hour)})
			e.register("helper-1", l)
			return &l.TLS
		}},
		{name: "CN names a registered node, URI SAN does not", reason: ReasonUnknownNode, issue: func(t *testing.T, e *env) *tls.Certificate {
			e.register("helper-1", e.ca.Issue(t, authtest.Spec{NodeID: "helper-1"}))
			return &e.ca.Issue(t, authtest.Spec{CN: "helper-1", URIs: []string{"spiffe://ubag/node/helper-2"}}).TLS
		}},
		{name: "CN only, no URI SAN", reason: ReasonBadIdentity, issue: func(t *testing.T, e *env) *tls.Certificate {
			l := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1", URIs: []string{}})
			e.register("helper-1", l)
			return &l.TLS
		}},
		{name: "two URI SANs", reason: ReasonBadIdentity, issue: func(t *testing.T, e *env) *tls.Certificate {
			l := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1", URIs: []string{"spiffe://ubag/node/helper-1", "spiffe://ubag/node/helper-2"}})
			e.register("helper-1", l)
			return &l.TLS
		}},
		{name: "foreign trust domain", reason: ReasonBadIdentity, issue: func(t *testing.T, e *env) *tls.Certificate {
			l := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1", URIs: []string{"spiffe://other/node/helper-1"}})
			e.register("helper-1", l)
			return &l.TLS
		}},
		{name: "URI SAN with extra path", reason: ReasonBadIdentity, issue: func(t *testing.T, e *env) *tls.Certificate {
			l := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1", URIs: []string{"spiffe://ubag/node/helper-1/extra"}})
			e.register("helper-1", l)
			return &l.TLS
		}},
		{name: "CA certificate as leaf", reason: ReasonBadIdentity, issue: func(t *testing.T, e *env) *tls.Certificate {
			l := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1", IsCA: true})
			e.register("helper-1", l)
			return &l.TLS
		}},
	}
	for _, tc := range cases {
		for _, layer := range []string{"handshake", "interceptor"} {
			t.Run(tc.name+"/"+layer, func(t *testing.T) {
				e := newEnv(t, layer == "handshake", &fakeHelper{})
				cert := tc.issue(t, e)
				_, err := e.dial(cert).Handshake(ctx5(t), &helperv1.HandshakeRequest{})
				switch {
				case tc.tlsLayer || layer == "handshake":
					wantCode(t, err, codes.Unavailable)
				default:
					wantCode(t, err, codes.Unauthenticated)
				}
				if tc.reason != "" {
					found := false
					for _, r := range e.reasons() {
						found = found || r == tc.reason
					}
					if !found {
						t.Fatalf("reasons = %v, want %q recorded", e.reasons(), tc.reason)
					}
				}
			})
		}
	}
}

// The CN is never an identity: a cert whose CN names another node still acts as
// the node in its URI SAN.
func TestCNIsIgnored(t *testing.T) {
	e := newEnv(t, true, &fakeHelper{})
	leaf := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1", CN: "helper-2"})
	e.register("helper-1", leaf)
	e.register("helper-2", e.ca.Issue(t, authtest.Spec{NodeID: "helper-2"}))
	resp, err := e.dial(&leaf.TLS).Handshake(ctx5(t), &helperv1.HandshakeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetNodeId() != "helper-1" {
		t.Fatalf("identity = %q, want helper-1 (CN must be ignored)", resp.GetNodeId())
	}
}

func TestRejectsMessageNodeIDMismatch(t *testing.T) {
	e := newEnv(t, true, &fakeHelper{run: func(*helperv1.RunAttemptRequest, grpc.ServerStreamingServer[helperv1.RunAttemptResponse]) error {
		return status.Error(codes.AlreadyExists, "handler reached")
	}})
	leaf := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1"})
	e.register("helper-1", leaf)
	c := e.dial(&leaf.TLS)
	ctx := ctx5(t)

	// Bare node_id fields: own id and empty pass, another id is refused.
	for id, want := range map[string]codes.Code{"helper-1": codes.OK, "": codes.OK, "helper-2": codes.PermissionDenied} {
		_, err := c.Handshake(ctx, &helperv1.HandshakeRequest{NodeId: id})
		wantCode(t, err, want)
	}
	_, err := c.ReportCapacity(ctx, &helperv1.ReportCapacityRequest{NodeId: "helper-2"})
	wantCode(t, err, codes.PermissionDenied)

	// Fenced unary RPC: a foreign or missing node id in the fence is refused.
	for id, want := range map[string]codes.Code{"helper-1": codes.OK, "helper-2": codes.PermissionDenied, "": codes.PermissionDenied} {
		_, err := c.RenewAttempt(ctx, &helperv1.RenewAttemptRequest{Fence: &helperv1.Fence{NodeId: id}})
		wantCode(t, err, want)
	}

	// Fenced server stream: the request message is checked before the handler runs.
	for id, want := range map[string]codes.Code{"helper-1": codes.AlreadyExists, "helper-2": codes.PermissionDenied, "": codes.PermissionDenied} {
		s, err := c.RunAttempt(ctx, &helperv1.RunAttemptRequest{Fence: &helperv1.Fence{NodeId: id}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Recv()
		wantCode(t, err, want)
	}
	if got := countReason(e.reasons(), ReasonNodeMismatch); got != 6 {
		t.Fatalf("node_id_mismatch rejections = %d, want 6 (%v)", got, e.reasons())
	}
}

func countReason(rs []Reason, want Reason) (n int) {
	for _, r := range rs {
		if r == want {
			n++
		}
	}
	return n
}

// stepper drives a RunAttempt stream one event at a time: each value sent on
// steps makes the handler emit one event; the handler's result lands in done.
func stepper() (*fakeHelper, chan struct{}, chan error) {
	steps, done := make(chan struct{}), make(chan error, 1)
	var seq uint64
	return &fakeHelper{run: func(_ *helperv1.RunAttemptRequest, s grpc.ServerStreamingServer[helperv1.RunAttemptResponse]) error {
		for range steps {
			seq++
			if err := s.Send(&helperv1.RunAttemptResponse{Event: &helperv1.AttemptEvent{Sequence: seq}}); err != nil {
				done <- err
				return err
			}
		}
		done <- nil
		return nil
	}}, steps, done
}

func TestRevocationEndsLiveStreamWithinOneMessage(t *testing.T) {
	svc, steps, done := stepper()
	e := newEnv(t, true, svc)
	leaf := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1"})
	e.register("helper-1", leaf)
	ctx := ctx5(t)
	s, err := e.dial(&leaf.TLS).RunAttempt(ctx, &helperv1.RunAttemptRequest{Fence: &helperv1.Fence{NodeId: "helper-1"}})
	if err != nil {
		t.Fatal(err)
	}
	steps <- struct{}{}
	if ev, err := s.Recv(); err != nil || ev.GetEvent().GetSequence() != 1 {
		t.Fatalf("first event = %v, %v", ev, err)
	}

	if err := e.store.RevokeNode(context.Background(), "helper-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	steps <- struct{}{} // the very next message must not get through
	_, err = s.Recv()
	wantCode(t, err, codes.Unauthenticated)
	select {
	case herr := <-done:
		wantCode(t, herr, codes.Unauthenticated)
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not unwind")
	}
	if countReason(e.reasons(), ReasonRevoked) == 0 {
		t.Fatalf("revocation not recorded: %v", e.reasons())
	}
}

// Rotation overlap: with the old pin current and the new one next, a live
// stream on the old certificate keeps flowing while the new certificate
// connects; promoting the new pin ends the overlap and the old stream stops at
// its next message while the new certificate keeps working.
func TestRotationOverlapKeepsLiveStreamThenPromoteEndsIt(t *testing.T) {
	svc, steps, done := stepper()
	e := newEnv(t, true, svc)
	oldLeaf := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1"})
	newLeaf := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1"})
	e.register("helper-1", oldLeaf)
	ctx := ctx5(t)

	s, err := e.dial(&oldLeaf.TLS).RunAttempt(ctx, &helperv1.RunAttemptRequest{Fence: &helperv1.Fence{NodeId: "helper-1"}})
	if err != nil {
		t.Fatal(err)
	}
	steps <- struct{}{}
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}

	// The new certificate is not accepted until its pin is registered as next.
	if _, err := e.dial(&newLeaf.TLS).Handshake(ctx, &helperv1.HandshakeRequest{}); err == nil {
		t.Fatal("new certificate accepted before its pin was registered")
	}
	entry := nodes.RegistryEntry{NodeID: "helper-1", URISAN: nodes.NodeURISAN("helper-1"), SPKICurrent: oldLeaf.SPKI, SPKINext: newLeaf.SPKI}
	if err := e.store.PutRegistry(context.Background(), entry, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.dial(&newLeaf.TLS).Handshake(ctx, &helperv1.HandshakeRequest{}); err != nil {
		t.Fatalf("new certificate rejected during overlap: %v", err)
	}
	steps <- struct{}{} // the old stream is not dropped by the overlap
	if ev, err := s.Recv(); err != nil || ev.GetEvent().GetSequence() != 2 {
		t.Fatalf("old stream during overlap = %v, %v", ev, err)
	}

	if err := e.store.PromoteSPKI(context.Background(), "helper-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	steps <- struct{}{}
	_, err = s.Recv()
	wantCode(t, err, codes.Unauthenticated)
	<-done
	if _, err := e.dial(&newLeaf.TLS).Handshake(ctx, &helperv1.HandshakeRequest{}); err != nil {
		t.Fatalf("new certificate rejected after promotion: %v", err)
	}
	if _, err := e.dial(&oldLeaf.TLS).Handshake(ctx, &helperv1.HandshakeRequest{}); err == nil {
		t.Fatal("old certificate accepted after promotion")
	}
}

// A certificate that expires while its stream is open stops the stream at the
// next message even though TLS only checks validity at the handshake.
func TestExpiryEndsLiveStream(t *testing.T) {
	svc, steps, done := stepper()
	e := newEnv(t, true, svc)
	leaf := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1"})
	e.register("helper-1", leaf)
	s, err := e.dial(&leaf.TLS).RunAttempt(ctx5(t), &helperv1.RunAttemptRequest{Fence: &helperv1.Fence{NodeId: "helper-1"}})
	if err != nil {
		t.Fatal(err)
	}
	steps <- struct{}{}
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}
	e.clk.advance(2 * time.Hour)
	steps <- struct{}{}
	_, err = s.Recv()
	wantCode(t, err, codes.Unauthenticated)
	<-done
	if countReason(e.reasons(), ReasonCertWindow) == 0 {
		t.Fatalf("expiry not recorded: %v", e.reasons())
	}
}

// A registry outage fails closed with Unavailable (no auth decision, no leak).
type brokenRegistry struct{}

func (brokenRegistry) GetRegistry(context.Context, string) (nodes.RegistryEntry, error) {
	return nodes.RegistryEntry{}, os.ErrDeadlineExceeded
}

func TestStoreErrorFailsClosed(t *testing.T) {
	ca := authtest.NewCA(t)
	leaf := ca.Issue(t, authtest.Spec{NodeID: "helper-1"})
	var reasons []Reason
	a := &Authenticator{Registry: brokenRegistry{}, OnReject: func(r Reason, _ string) { reasons = append(reasons, r) }}
	_, err := a.Verify(context.Background(), leaf.X509)
	if err == nil || len(reasons) != 1 || reasons[0] != ReasonStoreError {
		t.Fatalf("err = %v reasons = %v, want store_error rejection", err, reasons)
	}
	wantCode(t, statusFor(err), codes.Unavailable)
}

// The interceptors on a server without TLS client certificates (plaintext) must
// refuse everything: wiring them to the wrong server fails closed.
func TestPlaintextConnectionIsRejected(t *testing.T) {
	a := &Authenticator{Registry: nodes.NewMemoryStore()}
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(a.UnaryInterceptor()), grpc.ChainStreamInterceptor(a.StreamInterceptor()))
	helperv1.RegisterHelperServiceServer(srv, &fakeHelper{})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, err = helperv1.NewHelperServiceClient(conn).Handshake(ctx5(t), &helperv1.HandshakeRequest{})
	wantCode(t, err, codes.Unauthenticated)
}

// No reflection, and an unknown service answers Unimplemented only to a peer
// that authenticated; a revoked peer (interceptor-only server) sees
// Unauthenticated, so the service surface is not probeable.
func TestNoReflectionAndUnknownServiceIsGated(t *testing.T) {
	e := newEnv(t, false, &fakeHelper{})
	info := e.srv.GetServiceInfo()
	if _, ok := info["ubag.helper.v1.HelperService"]; !ok || len(info) != 1 {
		t.Fatalf("services = %v, want only ubag.helper.v1.HelperService", info)
	}
	good := e.ca.Issue(t, authtest.Spec{NodeID: "helper-1"})
	e.register("helper-1", good)
	revoked := e.ca.Issue(t, authtest.Spec{NodeID: "helper-2"})
	e.register("helper-2", revoked)
	if err := e.store.RevokeNode(context.Background(), "helper-2", time.Now()); err != nil {
		t.Fatal(err)
	}
	invoke := func(cert *tls.Certificate) error {
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: e.ca.Pool, ServerName: "localhost", Certificates: []tls.Certificate{*cert}}
		conn, err := grpc.NewClient(e.addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.Invoke(ctx5(t), "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", new(helperv1.HandshakeRequest), new(helperv1.HandshakeResponse))
	}
	wantCode(t, invoke(&good.TLS), codes.Unimplemented)
	wantCode(t, invoke(&revoked.TLS), codes.Unauthenticated)
}

func TestServerTLSConfigIsTLS13WithRequiredClientCerts(t *testing.T) {
	a := &Authenticator{Registry: nodes.NewMemoryStore()}
	cfg := a.ServerTLSConfig(x509.NewCertPool(), nil)
	if cfg.MinVersion != tls.VersionTLS13 || cfg.ClientAuth != tls.RequireAndVerifyClientCert || !cfg.SessionTicketsDisabled || cfg.VerifyConnection == nil {
		t.Fatalf("weak helper-plane TLS config: %+v", cfg)
	}
}

func TestLoadCAPool(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadCAPool(filepath.Join(dir, "missing.pem")); err == nil {
		t.Fatal("missing CA file accepted")
	}
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCAPool(empty); err == nil {
		t.Fatal("CA file without certificates accepted")
	}
	ca := authtest.NewCA(t)
	if _, err := LoadCAPool(ca.WriteCA(t, dir, "ca.pem")); err != nil {
		t.Fatalf("valid CA: %v", err)
	}
}

func TestKeyPairReloadsRenewedCertificate(t *testing.T) {
	dir := t.TempDir()
	ca := authtest.NewCA(t)
	first := ca.Issue(t, authtest.Spec{NodeID: "primary"})
	certFile, keyFile := first.WriteFiles(t, dir, "server")
	kp, err := NewKeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	kp.reloadEvery = 0
	got, _ := kp.GetCertificate(nil)
	if SPKIHex(got.Leaf) != first.SPKI {
		t.Fatal("did not serve the first certificate")
	}

	second := ca.Issue(t, authtest.Spec{NodeID: "primary"})
	second.WriteFiles(t, dir, "server")
	later := time.Now().Add(time.Minute)
	for _, f := range []string{certFile, keyFile} {
		if err := os.Chtimes(f, later, later); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ = kp.GetCertificate(nil); SPKIHex(got.Leaf) != second.SPKI {
		t.Fatal("did not pick up the renewed certificate")
	}

	// A broken rewrite keeps serving the last good pair.
	if err := os.WriteFile(certFile, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	later = later.Add(time.Minute)
	if err := os.Chtimes(certFile, later, later); err != nil {
		t.Fatal(err)
	}
	if got, _ = kp.GetCertificate(nil); SPKIHex(got.Leaf) != second.SPKI {
		t.Fatal("dropped the last good certificate after a bad reload")
	}
	if _, err := NewKeyPair(certFile, keyFile); err == nil {
		t.Fatal("startup accepted a broken key pair")
	}
}
