package serve

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// helperPlane is the Helper Node trust plane: its own gRPC server behind its
// own mTLS listener (UBAG_HELPER_PLANE, default off). It is never the public
// plaintext grpcServer and registers no reflection. Services for helper RPCs
// (attempt assets, staging, ...) are registered on server by later slices.
type helperPlane struct {
	addr   string
	server *grpc.Server
}

// newHelperPlaneFromEnv builds the plane behind UBAG_HELPER_PLANE (default off:
// returns nil and nothing is bound). It is the second rung of the helper flag
// ladder and therefore requires UBAG_HELPER_NODES. It validates and loads all
// TLS material but does not bind the listener; the caller does that at start.
//
// Env: UBAG_HELPER_GRPC_ADDR (listen address; bind it to the WireGuard
// interface), UBAG_HELPER_CA_FILE (PEM trust anchors: the fleet manager CA that
// issues node certificates), UBAG_HELPER_TLS_CERT_FILE / UBAG_HELPER_TLS_KEY_FILE
// (this listener's own certificate, re-read when the files change).
func newHelperPlaneFromEnv(store nodes.Store) (*helperPlane, error) {
	if !envBool("UBAG_HELPER_PLANE") {
		return nil, nil
	}
	if store == nil {
		return nil, fmt.Errorf("UBAG_HELPER_PLANE=true requires UBAG_HELPER_NODES=true")
	}
	var addr, caFile, certFile, keyFile string
	for _, v := range []struct {
		name string
		dst  *string
	}{
		{"UBAG_HELPER_GRPC_ADDR", &addr},
		{"UBAG_HELPER_CA_FILE", &caFile},
		{"UBAG_HELPER_TLS_CERT_FILE", &certFile},
		{"UBAG_HELPER_TLS_KEY_FILE", &keyFile},
	} {
		if *v.dst = strings.TrimSpace(os.Getenv(v.name)); *v.dst == "" {
			return nil, fmt.Errorf("UBAG_HELPER_PLANE=true requires %s", v.name)
		}
	}
	pool, err := helperauth.LoadCAPool(caFile)
	if err != nil {
		return nil, err
	}
	keyPair, err := helperauth.NewKeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	auth := &helperauth.Authenticator{
		Registry: store,
		OnReject: func(reason helperauth.Reason, nodeID string) {
			// ponytail: one log line per rejection; P4.8 aggregates and audits.
			slog.Warn("helper plane rejected a peer", "reason", reason, "node_id", nodeID)
		},
	}
	return &helperPlane{addr: addr, server: auth.NewServer(auth.ServerTLSConfig(pool, keyPair.GetCertificate))}, nil
}

// stop shuts the plane down; a nil plane is a no-op. budget <= 0 closes
// connections immediately. Otherwise in-flight RPCs and streams get budget to
// finish before being cut (GracefulStop alone would wait on a stuck stream).
func (p *helperPlane) stop(budget time.Duration) {
	if p == nil {
		return
	}
	if budget <= 0 {
		p.server.Stop()
		return
	}
	timer := time.AfterFunc(budget, p.server.Stop)
	defer timer.Stop()
	p.server.GracefulStop()
}
