package serve

import (
	"context"
	"crypto/tls"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth/authtest"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

type planeFiles struct{ addr, ca, cert, key string }

func writePlaneFiles(t *testing.T, ca *authtest.CA) planeFiles {
	t.Helper()
	dir := t.TempDir()
	cert, key := ca.Issue(t, authtest.Spec{NodeID: "primary"}).WriteFiles(t, dir, "server")
	return planeFiles{addr: "127.0.0.1:0", ca: ca.WriteCA(t, dir, "ca.pem"), cert: cert, key: key}
}

func setPlaneEnv(t *testing.T, flag string, f planeFiles) {
	t.Helper()
	t.Setenv("UBAG_HELPER_PLANE", flag)
	t.Setenv("UBAG_HELPER_GRPC_ADDR", f.addr)
	t.Setenv("UBAG_HELPER_CA_FILE", f.ca)
	t.Setenv("UBAG_HELPER_TLS_CERT_FILE", f.cert)
	t.Setenv("UBAG_HELPER_TLS_KEY_FILE", f.key)
}

func TestHelperPlaneFromEnv(t *testing.T) {
	ca := authtest.NewCA(t)
	good := writePlaneFiles(t, ca)
	missing := filepath.Join(t.TempDir(), "missing")
	cases := []struct {
		name      string
		flag      string
		store     nodes.Store
		mutate    func(*planeFiles)
		wantPlane bool
		wantErr   string
	}{
		{name: "unset is inert", flag: "", store: nodes.NewMemoryStore()},
		{name: "off is inert even when configured", flag: "false", store: nodes.NewMemoryStore()},
		{name: "off needs no node store", flag: "false", store: nil},
		{name: "requires UBAG_HELPER_NODES", flag: "true", store: nil, wantErr: "UBAG_HELPER_NODES"},
		{name: "requires address", flag: "true", store: nodes.NewMemoryStore(), mutate: func(f *planeFiles) { f.addr = "" }, wantErr: "UBAG_HELPER_GRPC_ADDR"},
		{name: "requires CA", flag: "true", store: nodes.NewMemoryStore(), mutate: func(f *planeFiles) { f.ca = "" }, wantErr: "UBAG_HELPER_CA_FILE"},
		{name: "requires cert", flag: "true", store: nodes.NewMemoryStore(), mutate: func(f *planeFiles) { f.cert = "" }, wantErr: "UBAG_HELPER_TLS_CERT_FILE"},
		{name: "requires key", flag: "true", store: nodes.NewMemoryStore(), mutate: func(f *planeFiles) { f.key = "" }, wantErr: "UBAG_HELPER_TLS_KEY_FILE"},
		{name: "unreadable CA", flag: "true", store: nodes.NewMemoryStore(), mutate: func(f *planeFiles) { f.ca = missing }, wantErr: "CA bundle"},
		{name: "unreadable key pair", flag: "true", store: nodes.NewMemoryStore(), mutate: func(f *planeFiles) { f.cert = missing }, wantErr: "key pair"},
		{name: "configured", flag: "true", store: nodes.NewMemoryStore(), wantPlane: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := good
			if tc.mutate != nil {
				tc.mutate(&f)
			}
			setPlaneEnv(t, tc.flag, f)
			plane, err := newHelperPlaneFromEnv(tc.store)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if (plane != nil) != tc.wantPlane {
				t.Fatalf("plane = %v, wantPlane %v", plane, tc.wantPlane)
			}
			plane.stop(0) // nil-safe
		})
	}
}

// The plane serves mTLS only and applies the registry: a registered node gets
// past authentication (and then Unimplemented, as no service is registered
// yet), a node the registry does not know is refused, and there is no
// reflection service to enumerate.
func TestHelperPlaneServesOnlyAuthenticatedNodes(t *testing.T) {
	ca := authtest.NewCA(t)
	store := nodes.NewMemoryStore()
	setPlaneEnv(t, "true", writePlaneFiles(t, ca))
	plane, err := newHelperPlaneFromEnv(store)
	if err != nil || plane == nil {
		t.Fatalf("plane = %v, err = %v", plane, err)
	}
	if info := plane.server.GetServiceInfo(); len(info) != 0 {
		t.Fatalf("services registered on the bare plane: %v", info)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = plane.server.Serve(lis) }()
	t.Cleanup(func() { plane.stop(0) })

	known := ca.Issue(t, authtest.Spec{NodeID: "helper-1"})
	if err := store.PutRegistry(context.Background(), nodes.RegistryEntry{NodeID: "helper-1", URISAN: nodes.NodeURISAN("helper-1"), SPKICurrent: known.SPKI}, time.Now()); err != nil {
		t.Fatal(err)
	}
	call := func(cert *tls.Certificate) error {
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: ca.Pool, ServerName: "localhost"}
		if cert != nil {
			cfg.Certificates = []tls.Certificate{*cert}
		}
		conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = helperv1.NewHelperServiceClient(conn).Handshake(ctx, &helperv1.HandshakeRequest{})
		return err
	}
	if got := status.Code(call(&known.TLS)); got != codes.Unimplemented {
		t.Fatalf("registered node: code = %v, want Unimplemented (authenticated, no service yet)", got)
	}
	if got := status.Code(call(&ca.Issue(t, authtest.Spec{NodeID: "ghost"}).TLS)); got != codes.Unavailable {
		t.Fatalf("unregistered node: code = %v, want Unavailable (refused at the handshake)", got)
	}
	if got := status.Code(call(nil)); got != codes.Unavailable {
		t.Fatalf("no client cert: code = %v, want Unavailable", got)
	}
}
