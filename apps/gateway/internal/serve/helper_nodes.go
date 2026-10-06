package serve

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// newHelperNodeStoreFromEnv builds the Helper Node store behind
// UBAG_HELPER_NODES (default off: returns nil and touches nothing). It is the
// first rung of the helper flag ladder (UBAG_HELPER_NODES < UBAG_HELPER_PLANE <
// UBAG_HELPER_DISPATCH < UBAG_HELPER_VOICE). Memory and Postgres are
// supported; the SQLite edge store refuses helper mode rather than run
// without shared, durable node state.
func newHelperNodeStoreFromEnv(ctx context.Context, storeKind string, db *sql.DB) (nodes.Store, error) {
	if !envBool("UBAG_HELPER_NODES") {
		return nil, nil
	}
	var store nodes.Store
	switch storeKind {
	case "memory", "":
		slog.Warn("UBAG_HELPER_NODES is on with the memory store: node allocations and the SPKI registry are per-process and lost on restart")
		store = nodes.NewMemoryStore()
	case "postgres":
		if db == nil {
			return nil, fmt.Errorf("UBAG_HELPER_NODES=true requires a Postgres handle")
		}
		store = nodes.NewPostgresStore(db)
	default:
		return nil, fmt.Errorf("UBAG_HELPER_NODES=true is not supported with UBAG_GATEWAY_STORE=%s; use postgres (or memory for development)", storeKind)
	}
	if err := store.Ready(ctx); err != nil {
		return nil, fmt.Errorf("helper node store not ready: %w", err)
	}
	return store, nil
}
