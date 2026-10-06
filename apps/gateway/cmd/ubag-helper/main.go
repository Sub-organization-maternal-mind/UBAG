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
// Attempts run on a bounded pool of warm worker processes (internal/helper/runner
// over internal/workerdaemon, the P3.6 pool): UBAG_HELPER_MAX_ATTEMPTS slots,
// starting at one, one active operation per provider identity. The binary links
// gRPC, the proto and those two packages only, never internal/executor.
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
	"github.com/ubag/ubag/apps/gateway/internal/helper/runner"
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
	if st.WorkerScript == "" {
		return fmt.Errorf("%s is required: the path of the worker daemon script", helper.EnvWorkerScript)
	}
	if fi, err := os.Stat(st.WorkerScript); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: %q is not a readable file", helper.EnvWorkerScript, st.WorkerScript)
	}
	// ponytail: no AssetSource yet. The RunAttempt contract carries no attempt token
	// for the P4.10 staging client, so an attempt that declares attachments ends
	// failed (helper_assets_unavailable) instead of running without them; the dial
	// slice supplies the source.
	rn, err := runner.New(runner.Config{
		Python: st.WorkerPython, Script: st.WorkerScript, Slots: st.MaxAttempts, ProfileRoot: st.ProfileRoot,
	})
	if err != nil {
		return err
	}
	defer rn.Close()
	node, err := st.Open(rn)
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
