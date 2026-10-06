package serve

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/helper"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/helperclient"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// newHelperRemoteFromEnv builds the primary's remote runner behind
// UBAG_HELPER_DISPATCH (default off: returns nil and the consumer runs every job
// locally, byte-for-byte as before). It is the third rung of the helper flag
// ladder (UBAG_HELPER_NODES < UBAG_HELPER_PLANE < UBAG_HELPER_DISPATCH) and
// needs the attempt ledger (UBAG_EXECUTOR_ATTEMPTS): a dispatched attempt is
// fenced by its lease generation or it does not run.
//
// Env (none is a secret; key material is a file):
//
//	UBAG_HELPER_CA_FILE            fleet manager CA bundle: trust anchors for helper certificates (shared with the plane)
//	UBAG_HELPER_CLIENT_CERT_FILE   the primary's dial certificate; URI SAN spiffe://ubag/primary/<id>, clientAuth (re-read when renewed)
//	UBAG_HELPER_CLIENT_KEY_FILE    its private key
//	UBAG_HELPER_WORKLOAD_VERSION   the helper workload (image + code) this primary dispatches to
//	UBAG_ADAPTERS_DIR              this primary's adapter registry (default ./adapters); its digest must equal the helper's
//
// The picker is the only thing still missing for jobs to move: the placer
// (P4.17) or a manager adaptor supplies one. Until it does, NoHelperPicker
// places nothing and every job runs locally, which is logged once at start.
func newHelperRemoteFromEnv(jobs jobstore.Store, nodeStore nodes.Store, plane *helperPlane, auditStore audit.Store, picker executor.HelperPicker) (*executor.RemoteWorkerRunner, error) {
	if !envBool("UBAG_HELPER_DISPATCH") {
		return nil, nil
	}
	if plane == nil || nodeStore == nil {
		return nil, fmt.Errorf("UBAG_HELPER_DISPATCH=true requires UBAG_HELPER_PLANE=true (and UBAG_HELPER_NODES=true)")
	}
	if !envBool("UBAG_EXECUTOR_ATTEMPTS") {
		return nil, fmt.Errorf("UBAG_HELPER_DISPATCH=true requires UBAG_EXECUTOR_ATTEMPTS=true (the attempt ledger fences a helper's writes)")
	}
	store, ok := jobs.(executor.RemoteStore)
	if !ok {
		return nil, fmt.Errorf("UBAG_HELPER_DISPATCH=true needs a job store with the attempt ledger (postgres, or memory for tests)")
	}
	values := map[string]string{}
	for _, name := range []string{"UBAG_HELPER_CA_FILE", "UBAG_HELPER_CLIENT_CERT_FILE", "UBAG_HELPER_CLIENT_KEY_FILE", "UBAG_HELPER_WORKLOAD_VERSION"} {
		if values[name] = strings.TrimSpace(getenv(name, "")); values[name] == "" {
			return nil, fmt.Errorf("UBAG_HELPER_DISPATCH=true requires %s", name)
		}
	}
	pool, err := helperauth.LoadCAPool(values["UBAG_HELPER_CA_FILE"])
	if err != nil {
		return nil, err
	}
	keyPair, err := helperauth.NewKeyPair(values["UBAG_HELPER_CLIENT_CERT_FILE"], values["UBAG_HELPER_CLIENT_KEY_FILE"])
	if err != nil {
		return nil, err
	}
	dialer, err := helperclient.NewFromKeyPair(pool, keyPair, nodeStore)
	if err != nil {
		return nil, err
	}
	digest, err := helper.RegistryDigest(getenv("UBAG_ADAPTERS_DIR", "adapters"))
	if err != nil {
		return nil, fmt.Errorf("UBAG_HELPER_DISPATCH=true: adapter registry digest: %w", err)
	}
	// The attempt's wall-clock budget is the local worker's (UBAG_WORKER_MAX_RUNTIME_MS).
	maxRuntime, err := durationFromMillisEnv("UBAG_WORKER_MAX_RUNTIME_MS", 30*time.Second)
	if err != nil {
		return nil, err
	}
	runner, err := executor.NewRemoteWorkerRunner(executor.RemoteConfig{
		Store: store, Picker: picker, Audit: auditStore,
		Dialer: executor.HelperDialFunc(func(ctx context.Context, p executor.HelperPlacement) (executor.HelperConn, error) {
			conn, err := dialer.Dial(ctx, p.NodeID, p.Endpoint)
			if err != nil {
				return nil, err
			}
			return conn, nil
		}),
		WorkloadVersion: values["UBAG_HELPER_WORKLOAD_VERSION"],
		RegistryDigest:  digest,
		MaxRuntime:      maxRuntime,
		Nodes:           nodeStore, // the reconciler dials a node by id at its last accepted endpoint
	})
	if err != nil {
		return nil, err
	}
	if _, none := picker.(executor.NoHelperPicker); none {
		slog.Warn("UBAG_HELPER_DISPATCH is on but no helper picker is configured: every job runs on this gateway until the placer is wired")
	}
	return runner, nil
}

// newHelperReconcilerFromEnv builds the ledger-first attempt reconciler (P4.18)
// for the dispatch runner; nil when dispatch is off (runner is nil). The consumer
// asks it about every leased job before the job is placed or run, so a job whose
// prompt may already have left is resumed or failed for reconciling and never run
// again. The manager is not consulted: it reads the node store's last accepted state.
//
// Env:
//
//	UBAG_HELPER_RECONCILE_WINDOW_SECONDS  how long a submitted attempt may stay unresolved with its helper
//	                                      unreachable, from the moment its lease lapsed (default 600; 60..86400)
func newHelperReconcilerFromEnv(jobs jobstore.Store, nodeStore nodes.Store, runner *executor.RemoteWorkerRunner) (*nodes.Reconciler, error) {
	if runner == nil {
		return nil, nil
	}
	secs, err := intFromEnv("UBAG_HELPER_RECONCILE_WINDOW_SECONDS", int(nodes.DefaultReconcileWindow/time.Second))
	if err != nil {
		return nil, err
	}
	window := time.Duration(secs) * time.Second
	if window < time.Minute || window > nodes.MaxReconcileWindow {
		return nil, fmt.Errorf("UBAG_HELPER_RECONCILE_WINDOW_SECONDS must be between 60 and %d", int(nodes.MaxReconcileWindow/time.Second))
	}
	ledger, ok := jobs.(nodes.Ledger)
	if !ok {
		return nil, fmt.Errorf("UBAG_HELPER_DISPATCH=true needs a job store with the attempt ledger (postgres, or memory for tests)")
	}
	return &nodes.Reconciler{Ledger: ledger, Inspector: runner, Registry: nodeStore, Config: nodes.ReconcileConfig{Window: window}}, nil
}
