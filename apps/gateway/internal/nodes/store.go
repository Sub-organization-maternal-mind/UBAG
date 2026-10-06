package nodes

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound           = errors.New("nodes: not found")
	ErrInvalid            = errors.New("nodes: invalid record")
	ErrStaleGeneration    = errors.New("nodes: stale allocation generation")
	ErrGenerationConflict = errors.New("nodes: allocation changed without a generation bump")
	ErrNodeRevoked        = errors.New("nodes: node is revoked")
	ErrTooManyNodes       = errors.New("nodes: node limit reached")
	ErrNotConfigured      = errors.New("nodes: store is not configured")
)

// Store is the single store for helper nodes (UBAG_HELPER_NODES, default off):
// accepted manager allocations, per-node runtime state and the pinned-SPKI
// registry. The job-attempt ledger is NOT here (P4.2 owns it). Memory and
// Postgres implement it; SQLite deliberately does not (the edge profile refuses
// helper mode). Every write is bounded to MaxNodes nodes and validated; every
// lookup is by node id. Callers pass the clock in; nothing here reads it.
type Store interface {
	// Ready fails closed when the backing schema is not usable.
	Ready(ctx context.Context) error

	// ApplyAllocation accepts a manager grant. A lower generation than the
	// accepted one is ErrStaleGeneration; the same generation is accepted only
	// when identical or differing in valid_until alone, else
	// ErrGenerationConflict. An accepted state=revoked also revokes the node's
	// registry entry in the same step, and the registry refuses new pins for a
	// node whose accepted allocation is revoked.
	ApplyAllocation(ctx context.Context, a Allocation, now time.Time) error
	GetAllocation(ctx context.Context, nodeID string) (Allocation, error)
	// ListAllocations returns every allocation ordered by node id.
	ListAllocations(ctx context.Context) ([]Allocation, error)

	// PutState upserts runtime state for a node that has an allocation
	// (ErrNotFound otherwise). A state older than the stored LastHeartbeat is
	// ignored so out-of-order writes never rewind liveness.
	PutState(ctx context.Context, s HelperState) error
	GetState(ctx context.Context, nodeID string) (HelperState, error)

	// PutRegistry upserts the pins for a node (ErrNodeRevoked when revoked).
	PutRegistry(ctx context.Context, e RegistryEntry, now time.Time) error
	GetRegistry(ctx context.Context, nodeID string) (RegistryEntry, error)
	// PromoteSPKI makes the next pin current and clears next (end of a
	// certificate rotation). ErrInvalid when there is no next pin.
	PromoteSPKI(ctx context.Context, nodeID string, now time.Time) error
	// RevokeNode is sticky and idempotent (the first revocation time wins).
	RevokeNode(ctx context.Context, nodeID string, now time.Time) error
}
