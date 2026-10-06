// Command ubag-helper is the Helper Node service of the shared browser fleet:
// an mTLS gRPC server (ubag.helper.v1) that the UBAG primary dials over
// WireGuard to run attempts on this host's own Chrome. It is a separate binary,
// not started by the gateway and not part of the default deployment.
//
// It holds no database credentials, queue or object-store access, application
// secret or remote-browser endpoint: it refuses to start when its environment
// carries any, when it has no local Chrome, when its certificate does not name
// its node id, or when it is told to listen on a wildcard address. Configuration
// is the UBAG_HELPER_* variables documented in internal/helper (config.go).
//
// Until P4.12 plugs the warm-daemon worker pool in as the Runner, the service
// answers Handshake, ReportCapacity, InspectAttempt and Drain, and refuses
// every RunAttempt as Unavailable.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/ubag/ubag/apps/gateway/internal/helper"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ubag-helper:", err)
		os.Exit(1)
	}
}

func run() error {
	st, err := helper.SettingsFromEnv(os.Environ(), exec.LookPath)
	if err != nil {
		return err
	}
	node, err := st.Open(nil) // ponytail: no runner until P4.12; attempts are refused Unavailable
	if err != nil {
		return err
	}
	slog.Info("ubag-helper starting", "node_id", st.NodeID, "listen", node.Listener.Addr().String(),
		"version", helper.Version, "workload_version", st.WorkloadVersion, "registry_digest", node.RegistryDigest,
		"chrome", st.ChromeBin, "max_attempts", st.MaxAttempts, "primary_identity", st.PrimaryURISAN)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return node.Run(ctx)
}
