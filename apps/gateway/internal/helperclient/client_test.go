package helperclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubag/ubag/apps/gateway/internal/helper"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth/authtest"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

const (
	testNode   = "helper-1"
	testPrim   = "primary-1"
	testDigest = "a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7"
)

// rig is a real ubag-helper service (internal/helper) behind real mTLS on a
// loopback TCP port, the way the primary meets it in production.
type rig struct {
	t        *testing.T
	ca       *authtest.CA
	node     *authtest.Leaf // the certificate the helper serves
	primary  *authtest.Leaf // the certificate the primary presents
	registry *nodes.MemoryStore
	srv      *helper.Server
	grpcSrv  *grpc.Server
	addr     string
}

type rigOptions struct {
	serverCA   *authtest.CA // issues the helper's certificate (default: the shared CA)
	serverSpec *authtest.Spec
	runner     helper.Runner
	cfg        func(*helper.Config)
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newRig(t *testing.T, opt rigOptions) *rig {
	t.Helper()
	r := &rig{t: t, ca: authtest.NewCA(t), registry: nodes.NewMemoryStore()}
	issuer := r.ca
	if opt.serverCA != nil {
		issuer = opt.serverCA
	}
	spec := authtest.Spec{NodeID: testNode, NotAfter: time.Now().Add(48 * time.Hour)}
	if opt.serverSpec != nil {
		spec = *opt.serverSpec
	}
	r.node = issuer.Issue(t, spec)
	r.primary = r.ca.Issue(t, authtest.Spec{NodeID: testPrim, URIs: []string{helper.PrimaryURISAN(testPrim)}, NotAfter: time.Now().Add(48 * time.Hour)})
	if err := r.registry.PutRegistry(context.Background(), nodes.RegistryEntry{
		NodeID: testNode, URISAN: nodes.NodeURISAN(testNode), SPKICurrent: r.node.SPKI,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	cfg := helper.Config{
		NodeID: testNode, WorkloadVersion: "w1", RegistryDigest: testDigest, Logger: quiet(), MaxAttempts: 2,
		KillGrace: time.Second, DeadlineGrace: time.Second, Runner: opt.runner,
	}
	if opt.cfg != nil {
		opt.cfg(&cfg)
	}
	var err error
	if r.srv, err = helper.NewServer(cfg); err != nil {
		t.Fatal(err)
	}
	auth, err := helper.NewPrimaryAuth(helper.PrimaryURISAN(testPrim), nil)
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg := auth.ServerTLSConfig(r.ca.Pool, func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &r.node.TLS, nil })
	r.grpcSrv = auth.NewGRPCServer(tlsCfg, r.srv)
	r.srv.RegisterVoice(r.grpcSrv) // a no-op unless the test's config enables voice
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.addr = lis.Addr().String()
	go func() { _ = r.grpcSrv.Serve(lis) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.srv.Shutdown(ctx)
		r.grpcSrv.Stop()
	})
	return r
}

// dialer builds a Dialer that presents leaf (the primary's certificate) and
// records every rejection.
func (r *rig) dialer(leaf *authtest.Leaf, reg Registry, rejects *rejectLog) *Dialer {
	r.t.Helper()
	if reg == nil {
		reg = r.registry
	}
	d, err := New(Config{
		CAs: r.ca.Pool, Registry: reg,
		ClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &leaf.TLS, nil },
		OnReject:          rejects.add,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return d
}

type rejectLog struct {
	mu      sync.Mutex
	reasons []Reason
}

func (l *rejectLog) add(reason Reason, _ string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.reasons = append(l.reasons, reason)
	l.mu.Unlock()
}

func (l *rejectLog) has(reason Reason) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.reasons {
		if r == reason {
			return true
		}
	}
	return false
}

func handshake(t *testing.T, d *Dialer, addr string) (*helperv1.HandshakeResponse, error) {
	t.Helper()
	conn, err := d.Dial(context.Background(), testNode, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return conn.Handshake(ctx, &helperv1.HandshakeRequest{ProtocolVersion: helper.ProtocolVersion, NodeId: testNode})
}

func TestDialReachesTheHelperOverMutualTLS(t *testing.T) {
	r := newRig(t, rigOptions{})
	resp, err := handshake(t, r.dialer(r.primary, nil, nil), r.addr)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if resp.GetNodeId() != testNode || resp.GetWorkloadVersion() != "w1" || resp.GetRegistryDigest() != testDigest {
		t.Fatalf("handshake = %v", resp)
	}
}

// The primary refuses to talk to anything but the node it asked for, on the
// strength of the certificate alone.
func TestDialRefusesAHelperThatIsNotTheNodeAskedFor(t *testing.T) {
	long := time.Now().Add(48 * time.Hour)
	otherCA := authtest.NewCA(t)
	cases := []struct {
		name   string
		opt    rigOptions
		mutate func(*rig)
		reg    func(*rig) Registry
		want   Reason
	}{
		{name: "an SPKI that is not pinned", mutate: func(r *rig) {
			other := r.ca.Issue(t, authtest.Spec{NodeID: testNode})
			_ = r.registry.PutRegistry(context.Background(), nodes.RegistryEntry{NodeID: testNode, URISAN: nodes.NodeURISAN(testNode), SPKICurrent: other.SPKI}, time.Now())
		}, want: ReasonUnknownSPKI},
		{name: "a revoked node", mutate: func(r *rig) { _ = r.registry.RevokeNode(context.Background(), testNode, time.Now()) }, want: ReasonRevoked},
		{name: "a node the registry does not know", reg: func(*rig) Registry { return nodes.NewMemoryStore() }, want: ReasonUnknownNode},
		{name: "a registry that cannot be read", reg: func(*rig) Registry { return brokenRegistry{} }, want: ReasonStoreError},
		{name: "another node's certificate", opt: rigOptions{serverSpec: &authtest.Spec{NodeID: "helper-2", NotAfter: long}}, want: ReasonBadIdentity},
		{name: "a CN that names the node without a URI SAN", opt: rigOptions{serverSpec: &authtest.Spec{NodeID: testNode, URIs: []string{}, NotAfter: long}}, want: ReasonBadIdentity},
		{name: "two URI SANs", opt: rigOptions{serverSpec: &authtest.Spec{NodeID: testNode, URIs: []string{nodes.NodeURISAN(testNode), nodes.NodeURISAN("helper-2")}, NotAfter: long}}, want: ReasonBadIdentity},
		{name: "a CA certificate as the leaf", opt: rigOptions{serverSpec: &authtest.Spec{NodeID: testNode, IsCA: true, NotAfter: long}}, want: ReasonBadIdentity},
		{name: "a certificate from another authority", opt: rigOptions{serverCA: otherCA}, want: ReasonBadChain},
		{name: "a certificate without the serverAuth usage", opt: rigOptions{serverSpec: &authtest.Spec{NodeID: testNode, EKUs: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, NotAfter: long}}, want: ReasonBadChain},
		{name: "an expired certificate", opt: rigOptions{serverSpec: &authtest.Spec{NodeID: testNode, NotBefore: time.Now().Add(-3 * time.Hour), NotAfter: time.Now().Add(-time.Hour)}}, want: ReasonCertWindow},
		{name: "a certificate valid for longer than 72 hours", opt: rigOptions{serverSpec: &authtest.Spec{NodeID: testNode, NotAfter: time.Now().Add(100 * time.Hour)}}, want: ReasonCertLifetime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.opt)
			if tc.mutate != nil {
				tc.mutate(r)
			}
			var reg Registry = r.registry
			if tc.reg != nil {
				reg = tc.reg(r)
			}
			rejects := &rejectLog{}
			_, err := handshake(t, r.dialer(r.primary, reg, rejects), r.addr)
			if status.Code(err) != codes.Unavailable {
				t.Fatalf("Handshake error = %v, want a refused connection (Unavailable)", err)
			}
			if !rejects.has(tc.want) {
				t.Fatalf("rejections = %v, want %q", rejects.reasons, tc.want)
			}
		})
	}
}

type brokenRegistry struct{}

func (brokenRegistry) GetRegistry(context.Context, string) (nodes.RegistryEntry, error) {
	return nodes.RegistryEntry{}, errors.New("database unavailable")
}

// During a certificate rotation the registry holds two pins; a helper that
// already serves the new certificate is accepted.
func TestDialAcceptsTheNextPinDuringARotation(t *testing.T) {
	r := newRig(t, rigOptions{})
	old := r.ca.Issue(t, authtest.Spec{NodeID: testNode})
	if err := r.registry.PutRegistry(context.Background(), nodes.RegistryEntry{
		NodeID: testNode, URISAN: nodes.NodeURISAN(testNode), SPKICurrent: old.SPKI, SPKINext: r.node.SPKI,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := handshake(t, r.dialer(r.primary, nil, nil), r.addr); err != nil {
		t.Fatalf("Handshake with the next pin: %v", err)
	}
}

// The helper only serves the primary: a certificate that is not
// spiffe://ubag/primary/<id> (a node's, say) gets nothing.
func TestDialPresentsThePrimaryIdentityTheHelperRequires(t *testing.T) {
	r := newRig(t, rigOptions{})
	imposter := r.ca.Issue(t, authtest.Spec{NodeID: "helper-9", NotAfter: time.Now().Add(48 * time.Hour)})
	if _, err := handshake(t, r.dialer(imposter, nil, nil), r.addr); status.Code(err) != codes.Unavailable {
		t.Fatalf("a node certificate was accepted as the primary: %v", err)
	}
	if _, err := handshake(t, r.dialer(r.primary, nil, nil), r.addr); err != nil {
		t.Fatalf("the primary's own certificate was refused: %v", err)
	}
}

func TestDialValidatesItsArguments(t *testing.T) {
	r := newRig(t, rigOptions{})
	d := r.dialer(r.primary, nil, nil)
	for name, args := range map[string][2]string{
		"bad node id":      {"../x", r.addr},
		"empty node id":    {"", r.addr},
		"endpoint no port": {testNode, "10.0.0.2"},
		"endpoint no host": {testNode, ":7443"},
		"empty endpoint":   {testNode, ""},
	} {
		if conn, err := d.Dial(context.Background(), args[0], args[1]); err == nil {
			_ = conn.Close()
			t.Fatalf("%s: accepted", name)
		}
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("an empty config was accepted")
	}
}
