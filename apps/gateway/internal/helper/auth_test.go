package helper

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth/authtest"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

func TestNewPrimaryAuthValidatesIdentityAndPins(t *testing.T) {
	pin := strings.Repeat("a", 64)
	for name, tc := range map[string]struct {
		uri  string
		pins []string
		ok   bool
	}{
		"primary identity":           {PrimaryURISAN("primary-1"), nil, true},
		"two pins (rotation)":        {PrimaryURISAN("primary-1"), []string{pin, strings.Repeat("b", 64)}, true},
		"a Helper Node identity":     {NodeURISAN("helper-1"), nil, false},
		"empty":                      {"", nil, false},
		"foreign trust domain":       {"spiffe://other/primary/p1", nil, false},
		"path after the id":          {PrimaryURISAN("p1") + "/x", nil, false},
		"empty id":                   {PrimaryURISANPrefix, nil, false},
		"three pins":                 {PrimaryURISAN("p1"), []string{pin, pin, pin}, false},
		"upper-case pin":             {PrimaryURISAN("p1"), []string{strings.ToUpper(pin)}, false},
		"short pin":                  {PrimaryURISAN("p1"), []string{"abcd"}, false},
		"https is not an identity":   {"https://ubag/primary/p1", nil, false},
		"id with a traversal":        {PrimaryURISAN("../x"), nil, false},
		"id starting with a dot":     {PrimaryURISAN(".hidden"), nil, false},
		"id longer than 64 chars":    {PrimaryURISAN(strings.Repeat("x", 65)), nil, false},
		"identity with whitespace":   {PrimaryURISAN("p 1"), nil, false},
		"identity with a query part": {PrimaryURISAN("p1") + "?x=1", nil, false},
	} {
		if _, err := NewPrimaryAuth(tc.uri, tc.pins); (err == nil) != tc.ok {
			t.Errorf("%s: ok=%v, err=%v", name, tc.ok, err)
		}
	}
}

func TestPrimaryAuthVerify(t *testing.T) {
	ca := authtest.NewCA(t)
	now := time.Now()
	issue := func(mut func(*authtest.Spec)) *authtest.Leaf {
		s := authtest.Spec{NodeID: "primary-1", URIs: []string{PrimaryURISAN("primary-1")}}
		if mut != nil {
			mut(&s)
		}
		return ca.Issue(t, s)
	}
	good := issue(nil)
	pinOf := func(l *authtest.Leaf) []string { return []string{l.SPKI} }
	other := issue(nil) // a different key: a different SPKI

	for _, tc := range []struct {
		name   string
		leaf   *x509.Certificate
		pins   []string
		reason string // "" = accepted
	}{
		{"valid primary", good.X509, nil, ""},
		{"valid, pinned", good.X509, pinOf(good), ""},
		{"valid, pinned with the next pin in second place", good.X509, []string{other.SPKI, good.SPKI}, ""},
		{"valid but not the pinned key", good.X509, pinOf(other), ReasonUnknownSPKI},
		{"no certificate", nil, nil, ReasonNoCert},
		{"CN names the primary but no URI SAN", issue(func(s *authtest.Spec) { s.CN = "primary-1"; s.URIs = []string{} }).X509, nil, ReasonBadIdentity},
		{"a Helper Node certificate (same CA, clientAuth)", issue(func(s *authtest.Spec) { s.URIs = []string{NodeURISAN("helper-2")} }).X509, nil, ReasonBadIdentity},
		{"another primary id", issue(func(s *authtest.Spec) { s.URIs = []string{PrimaryURISAN("primary-2")} }).X509, nil, ReasonBadIdentity},
		{"foreign trust domain", issue(func(s *authtest.Spec) { s.URIs = []string{"spiffe://other/primary/primary-1"} }).X509, nil, ReasonBadIdentity},
		{"two URI SANs", issue(func(s *authtest.Spec) { s.URIs = []string{PrimaryURISAN("primary-1"), NodeURISAN("helper-2")} }).X509, nil, ReasonBadIdentity},
		{"CA certificate", issue(func(s *authtest.Spec) { s.IsCA = true }).X509, nil, ReasonBadIdentity},
		{"server-only EKU", issue(func(s *authtest.Spec) { s.EKUs = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} }).X509, nil, ReasonBadIdentity},
		{"expired", issue(func(s *authtest.Spec) { s.NotBefore, s.NotAfter = now.Add(-3*time.Hour), now.Add(-time.Hour) }).X509, nil, ReasonCertWindow},
		{"not yet valid", issue(func(s *authtest.Spec) { s.NotBefore, s.NotAfter = now.Add(time.Hour), now.Add(2*time.Hour) }).X509, nil, ReasonCertWindow},
		{"valid for 100 hours", issue(func(s *authtest.Spec) { s.NotBefore, s.NotAfter = now.Add(-time.Minute), now.Add(100*time.Hour) }).X509, nil, ReasonCertLifetime},
		{"valid for exactly 72 hours", issue(func(s *authtest.Spec) {
			s.NotBefore, s.NotAfter = now.Add(-time.Minute), now.Add(72*time.Hour-time.Minute)
		}).X509, nil, ""},
		{"72 hours plus the backdating slack", issue(func(s *authtest.Spec) { s.NotBefore, s.NotAfter = now.Add(-10*time.Minute), now.Add(72*time.Hour) }).X509, nil, ""},
		{"beyond the backdating slack", issue(func(s *authtest.Spec) { s.NotBefore, s.NotAfter = now.Add(-30*time.Minute), now.Add(72*time.Hour) }).X509, nil, ReasonCertLifetime},
	} {
		auth, err := NewPrimaryAuth(PrimaryURISAN("primary-1"), tc.pins)
		if err != nil {
			t.Fatal(err)
		}
		var seen string
		auth.OnReject = func(r string) { seen = r }
		err = auth.Verify(tc.leaf)
		var re *RejectError
		switch {
		case tc.reason == "" && err != nil:
			t.Errorf("%s: want accepted, got %v", tc.name, err)
		case tc.reason != "" && (!errors.As(err, &re) || re.Reason != tc.reason || seen != tc.reason):
			t.Errorf("%s: want reason %q, got %v (hook saw %q)", tc.name, tc.reason, err, seen)
		}
	}
}

func TestServerTLSConfigIsTLS13WithRequiredClientCertsAndNoTickets(t *testing.T) {
	auth, _ := NewPrimaryAuth(PrimaryURISAN("primary-1"), nil)
	cfg := auth.ServerTLSConfig(x509.NewCertPool(), nil)
	if cfg.MinVersion != tls.VersionTLS13 || cfg.ClientAuth != tls.RequireAndVerifyClientCert || !cfg.SessionTicketsDisabled {
		t.Fatalf("config: %+v", cfg)
	}
	if err := cfg.VerifyConnection(tls.ConnectionState{}); err == nil {
		t.Fatal("a connection with no client certificate must be refused")
	}
}

// The helper side of "wrong cert rejected": over a real mTLS handshake against
// the real server, only the configured primary gets through.
func TestMTLSAdmitsOnlyTheConfiguredPrimary(t *testing.T) {
	r := newRig(t, newFakeRunner(blockingScript))
	handshake := func(c helperv1.HelperServiceClient) error {
		_, err := c.Handshake(r.ctx(), &helperv1.HandshakeRequest{ProtocolVersion: ProtocolVersion})
		return err
	}
	if err := handshake(r.client); err != nil {
		t.Fatalf("the primary must be admitted: %v", err)
	}

	otherCA := authtest.NewCA(t)
	long := time.Now().Add(48 * time.Hour)
	for _, tc := range []struct {
		name   string
		leaf   *authtest.Leaf
		reason string // the reason the helper recorded; "" = refused by the TLS stack before our check
	}{
		{"no client certificate", nil, ""},
		{"a primary certificate from another CA", otherCA.Issue(t, authtest.Spec{NodeID: "primary-1", URIs: []string{PrimaryURISAN("primary-1")}, NotAfter: long}), ""},
		{"a Helper Node certificate from the right CA", r.ca.Issue(t, authtest.Spec{NodeID: "helper-2", NotAfter: long}), ReasonBadIdentity},
		{"another primary id from the right CA", r.ca.Issue(t, authtest.Spec{NodeID: "x", URIs: []string{PrimaryURISAN("primary-2")}, NotAfter: long}), ReasonBadIdentity},
		{"a CN-only certificate", r.ca.Issue(t, authtest.Spec{NodeID: "primary-1", URIs: []string{}, NotAfter: long}), ReasonBadIdentity},
	} {
		before := len(r.rejected())
		client, _ := r.dial(tc.leaf)
		if err := handshake(client); err == nil {
			t.Errorf("%s: must be refused", tc.name)
			continue
		}
		if tc.reason != "" {
			got := r.rejected()
			if len(got) != before+1 || got[len(got)-1] != tc.reason {
				t.Errorf("%s: want reason %q recorded, got %v", tc.name, tc.reason, got[before:])
			}
		}
	}

	// A pin turns a CA-valid primary certificate with another key away.
	r.auth.pins = []string{strings.Repeat("0", 64)}
	pinned, _ := r.dial(r.primary)
	if err := handshake(pinned); err == nil {
		t.Error("a certificate whose SPKI is not pinned must be refused")
	}
	if got := r.rejected(); got[len(got)-1] != ReasonUnknownSPKI {
		t.Errorf("want %s recorded, got %v", ReasonUnknownSPKI, got)
	}
	r.auth.pins = []string{r.primary.SPKI}
	if c, _ := r.dial(r.primary); handshake(c) != nil {
		t.Error("the pinned primary must be admitted")
	}
	r.auth.pins = nil

	// An expired certificate is refused on a new connection.
	r.clock.Advance(49 * time.Hour)
	if c, _ := r.dial(r.primary); handshake(c) == nil {
		t.Error("an expired certificate must be refused")
	}
}

// Wiring the interceptors to a server without verified TLS must fail closed.
func TestInterceptorsRejectAnUnauthenticatedConnection(t *testing.T) {
	auth, _ := NewPrimaryAuth(PrimaryURISAN("primary-1"), nil)
	srv, err := NewServer(Config{NodeID: "helper-1", WorkloadVersion: "w1", RegistryDigest: testDigest, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer(grpc.ChainUnaryInterceptor(auth.UnaryInterceptor()), grpc.ChainStreamInterceptor(auth.StreamInterceptor()))
	helperv1.RegisterHelperServiceServer(g, srv)
	lis := bufconn.Listen(1 << 16)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := helperv1.NewHelperServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.Handshake(ctx, &helperv1.HandshakeRequest{ProtocolVersion: ProtocolVersion}); codeOf(err) != "Unauthenticated" {
		t.Fatalf("unary: %v", err)
	}
	stream, err := c.RunAttempt(ctx, &helperv1.RunAttemptRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); codeOf(err) != "Unauthenticated" {
		t.Fatalf("stream: %v", err)
	}
}

func TestNoReflectionAndUnknownServicesAreUnimplementedOnlyAfterAuthentication(t *testing.T) {
	r := newRig(t, newFakeRunner(blockingScript))
	_, conn := r.dial(r.primary)
	for _, method := range []string{"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", "/ubag.helper.v1.Other/Do", "/grpc.health.v1.Health/Check"} {
		if err := conn.Invoke(r.ctx(), method, &emptypb.Empty{}, &emptypb.Empty{}); codeOf(err) != "Unimplemented" {
			t.Errorf("%s: want Unimplemented, got %v", method, err)
		}
	}
	// The voice service lives in its own proto (P5.7) and is not registered here.
	if err := conn.Invoke(r.ctx(), "/ubag.helper.v1.HelperVoiceService/OfferVoice", &emptypb.Empty{}, &emptypb.Empty{}); codeOf(err) != "Unimplemented" {
		t.Errorf("voice RPCs must not be served: %v", err)
	}
	// An unauthenticated peer never gets as far as learning that.
	anon, anonConn := r.dial(nil)
	_ = anon
	if err := anonConn.Invoke(r.ctx(), "/ubag.helper.v1.Other/Do", &emptypb.Empty{}, &emptypb.Empty{}); codeOf(err) == "Unimplemented" {
		t.Error("an unauthenticated peer must be refused before service lookup")
	}
}

// An attempt lives on its lease, not on the stream or the peer certificate: when
// the primary's certificate expires the stream ends, the attempt keeps running
// and a primary with a fresh certificate re-attaches.
func TestStreamEndsWithThePeerCertificateButTheAttemptLives(t *testing.T) {
	r := newRig(t, newFakeRunner(blockingScript))
	short := r.ca.Issue(t, authtest.Spec{NodeID: "primary-1", URIs: []string{PrimaryURISAN("primary-1")}, NotAfter: time.Now().Add(1500 * time.Millisecond)})
	client, _ := r.dial(short)
	ctx := r.ctx()
	stream, err := client.RunAttempt(ctx, r.runReq("att_a", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("admission: %v", err)
	}
	if _, err := stream.Recv(); codeOf(err) != "DeadlineExceeded" {
		t.Fatalf("the stream must end when the peer certificate expires: %v", err)
	}
	if in := r.inspect("job-1", "att_a"); in.GetState() != stRunning {
		t.Fatalf("the attempt must keep running: %v", in)
	}
	again := r.open(r.runReq("att_a", 1))
	if e := again.admitted(); e.GetSequence() != 1 {
		t.Fatalf("re-attach with a fresh certificate must replay from sequence 1: %v", e)
	}
}
