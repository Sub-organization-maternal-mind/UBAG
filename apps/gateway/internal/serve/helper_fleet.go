package serve

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/helper"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/helperclient"
	"github.com/ubag/ubag/apps/gateway/internal/helpermetrics"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// asyncPlacementHolds bounds the leased jobs that wait out a refused placement off
// the worker (ADR-0015). Past it a hold waits on the worker, as before.
const asyncPlacementHolds = 64

// helperFleet is the placement side of helper dispatch (P4.17, ADR-0015): the
// placer over the accepted grants and the picker the remote runner asks. It
// exists only with UBAG_HELPER_DISPATCH on.
type helperFleet struct {
	placer *nodes.Placer
	picker *executor.FleetPicker
}

// newFleetPollerFromEnv builds the manager grant poller behind
// UBAG_FLEET_MANAGER_URL (unset: nil, nothing polled and nothing is ever
// granted). It needs the node store (UBAG_HELPER_NODES). The caller starts Run.
func newFleetPollerFromEnv(store nodes.Store) (*nodes.Poller, error) {
	if store == nil {
		return nil, nil
	}
	rawURL, cfg, ok, err := nodes.PollerConfigFromEnv(os.LookupEnv)
	if err != nil || !ok {
		return nil, err
	}
	src, err := nodes.NewHTTPSource(rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", nodes.EnvFleetManagerURL, err)
	}
	return nodes.NewPoller(src, store, cfg, nil), nil
}

// newHelperFleetFromEnv builds the placer and its picker behind
// UBAG_HELPER_DISPATCH (default off, or no node store: nil, and the caller keeps
// NoHelperPicker; newHelperRemoteFromEnv reports the missing prerequisite). The
// tenant-owned profile registry follows the gateway store: Postgres, or memory for
// development. poller, when set, supplies the grants (stale or absent ones already
// draining); without it the stored grants are used as they are.
func newHelperFleetFromEnv(ctx context.Context, store nodes.Store, poller *nodes.Poller, storeKind string, db *sql.DB) (*helperFleet, error) {
	if !envBool("UBAG_HELPER_DISPATCH") || store == nil {
		return nil, nil
	}
	if strings.EqualFold(strings.TrimSpace(getenv("UBAG_EXECUTOR_MODE", "")), "nats") {
		slog.Warn("UBAG_HELPER_DISPATCH with UBAG_EXECUTOR_MODE=nats: every held placement consumes one delivery, so raise UBAG_NATS_WORKER_MAX_DELIVER (default 5) or held jobs are dropped from the stream")
	}
	var profiles helperauth.ProfileStore
	switch storeKind {
	case "memory", "":
		slog.Warn("UBAG_HELPER_DISPATCH is on with the memory store: tenant profile bindings are per-process and lost on restart")
		profiles = helperauth.NewMemoryProfileStore()
	case "postgres":
		if db == nil {
			return nil, fmt.Errorf("UBAG_HELPER_DISPATCH=true requires a Postgres handle")
		}
		profiles = helperauth.NewPostgresProfileStore(db)
	default:
		return nil, fmt.Errorf("UBAG_HELPER_DISPATCH=true is not supported with UBAG_GATEWAY_STORE=%s", storeKind)
	}
	if err := profiles.Ready(ctx); err != nil {
		return nil, fmt.Errorf("helper profile store not ready: %w", err)
	}
	allocations := func(ctx context.Context, _ time.Time) ([]nodes.Allocation, error) { return store.ListAllocations(ctx) }
	if poller != nil {
		allocations = poller.Current
	}
	placer, err := nodes.NewPlacer(nodes.PlacerConfig{Allocations: allocations, State: store.GetState})
	if err != nil {
		return nil, err
	}
	return &helperFleet{placer: placer, picker: &executor.FleetPicker{Placer: placer, Profiles: profiles}}, nil
}

// wire makes the consumer follow the fleet: holds wait off the worker and the
// worker count tracks the helper capacity. Both are inert on a nil fleet.
func (f *helperFleet) wire(c *executor.WorkerConsumer) {
	if f == nil || c == nil {
		return
	}
	c.AsyncHolds = asyncPlacementHolds
	c.HelperCapacity = func() int {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return f.placer.Capacity(ctx)
	}
}

// newHelperProberFromEnv builds the heartbeat prober (ADR-0009). The primary dials
// each granted helper with the same identity and registry as dispatch, so it is
// built only after newHelperRemoteFromEnv has validated that configuration.
func newHelperProberFromEnv(store nodes.Store) (*nodes.Prober, error) {
	var files [3]string
	for i, name := range []string{"UBAG_HELPER_CA_FILE", "UBAG_HELPER_CLIENT_CERT_FILE", "UBAG_HELPER_CLIENT_KEY_FILE"} {
		if files[i] = strings.TrimSpace(getenv(name, "")); files[i] == "" {
			return nil, fmt.Errorf("the helper prober requires %s", name)
		}
	}
	dialer, err := newHelperDialer(files[0], files[1], files[2], store)
	if err != nil {
		return nil, err
	}
	digest, err := helper.RegistryDigest(getenv("UBAG_ADAPTERS_DIR", "adapters"))
	if err != nil {
		return nil, fmt.Errorf("the helper prober: adapter registry digest: %w", err)
	}
	return nodes.NewProber(nodes.ProberConfig{
		Store: store,
		Reporter: helperReporter{dial: func(ctx context.Context, a nodes.Allocation) (capacityConn, error) {
			conn, err := dialer.Dial(ctx, a.NodeID, a.Endpoint)
			if err != nil {
				return nil, err
			}
			return conn, nil
		}},
		WorkloadVersion: strings.TrimSpace(getenv("UBAG_HELPER_WORKLOAD_VERSION", "")),
		RegistryDigest:  digest,
		OnProbe:         helpermetrics.RecordProbe,
	}), nil
}

// capacityConn is the slice of a dialed helper connection the prober uses.
type capacityConn interface {
	ReportCapacity(ctx context.Context, in *helperv1.ReportCapacityRequest, opts ...grpc.CallOption) (*helperv1.ReportCapacityResponse, error)
	Close() error
}

// helperReporter asks one helper for its capacity over a fresh mTLS connection
// (the dial verifies the helper's certificate for exactly this node). A report is
// every 15 s per node, so a connection per report costs nothing worth caching.
type helperReporter struct {
	dial func(ctx context.Context, a nodes.Allocation) (capacityConn, error)
}

func (r helperReporter) Report(ctx context.Context, a nodes.Allocation) (nodes.Report, error) {
	conn, err := r.dial(ctx, a)
	if err != nil {
		return nodes.Report{}, err
	}
	defer conn.Close()
	resp, err := conn.ReportCapacity(ctx, &helperv1.ReportCapacityRequest{NodeId: a.NodeID})
	if err != nil {
		return nodes.Report{}, err
	}
	if resp.GetNodeId() != a.NodeID {
		return nodes.Report{}, fmt.Errorf("helper answered for node %q, not %q", resp.GetNodeId(), a.NodeID)
	}
	return nodes.Report{
		CPUMillisTotal: resp.GetCpuMillisTotal(), CPUMillisUsed: resp.GetCpuMillisUsed(),
		MemoryBytesTotal: resp.GetMemoryBytesTotal(), MemoryBytesUsed: resp.GetMemoryBytesUsed(),
		BrowsersMax: resp.GetBrowsersMax(), BrowsersActive: resp.GetBrowsersActive(),
		WorkloadVersion: resp.GetWorkloadVersion(), RegistryDigest: resp.GetRegistryDigest(),
	}, nil
}

var _ capacityConn = (*helperclient.Conn)(nil)
