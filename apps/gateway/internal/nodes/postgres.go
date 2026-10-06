package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgresStore is the shared Store. Schema comes from
// migrations/postgres/0023_helper_nodes.sql; Ready only asserts it exists and
// the store never runs DDL. Writes serialise on one advisory lock (the node
// set is at most MaxNodes and writes are heartbeat-rate, so this is cheap) so
// the node-count bound and the generation compare are race-free across
// gateway replicas.
type PostgresStore struct {
	db *sql.DB
}

var _ Store = (*PostgresStore)(nil)

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) configured() error {
	if s == nil || s.db == nil {
		return ErrNotConfigured
	}
	return nil
}

func (s *PostgresStore) Ready(ctx context.Context) error {
	if err := s.configured(); err != nil {
		return err
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	for _, object := range []string{"gateway_helper_allocations", "gateway_helper_state", "gateway_helper_registry"} {
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)`, object).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("nodes: postgres schema object %q is missing; apply migrations/postgres/0023_helper_nodes.sql", object)
		}
	}
	return nil
}

// inTx runs fn in a transaction holding the helper-nodes advisory lock.
func (s *PostgresStore) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	if err := s.configured(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('ubag-helper-nodes'))`); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

const allocationColumns = `node_id, region, endpoint, uri_san, spki_sha256, cpu_millis, memory_bytes,
reservation_state, state, max_browser_workloads, voice_capable, udp_port_min, udp_port_max, nat_ip,
valid_until, generation, accepted_at`

func scanAllocation(row interface{ Scan(...any) error }) (Allocation, error) {
	var a Allocation
	err := row.Scan(&a.NodeID, &a.Region, &a.Endpoint, &a.URISAN, &a.SPKISHA256, &a.CPUMillis, &a.MemoryBytes,
		&a.ReservationState, &a.State, &a.MaxBrowserWorkloads, &a.VoiceCapable, &a.UDPPortMin, &a.UDPPortMax, &a.NATIP,
		&a.ValidUntil, &a.Generation, &a.AcceptedAt)
	a.ValidUntil, a.AcceptedAt = a.ValidUntil.UTC(), a.AcceptedAt.UTC()
	return a, err
}

func (s *PostgresStore) ApplyAllocation(ctx context.Context, a Allocation, now time.Time) error {
	if err := a.Validate(); err != nil {
		return err
	}
	a.ValidUntil, a.AcceptedAt = a.ValidUntil.UTC(), now.UTC()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		old, err := scanAllocation(tx.QueryRowContext(ctx,
			`SELECT `+allocationColumns+` FROM gateway_helper_allocations WHERE node_id = $1`, a.NodeID))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := checkRoom(ctx, tx, "gateway_helper_allocations"); err != nil {
				return err
			}
		case err != nil:
			return err
		case a.Generation < old.Generation:
			return ErrStaleGeneration
		case a.Generation == old.Generation && !a.sameExceptValidity(old):
			return ErrGenerationConflict
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO gateway_helper_allocations (`+allocationColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
ON CONFLICT (node_id) DO UPDATE SET
	region = EXCLUDED.region, endpoint = EXCLUDED.endpoint, uri_san = EXCLUDED.uri_san,
	spki_sha256 = EXCLUDED.spki_sha256, cpu_millis = EXCLUDED.cpu_millis, memory_bytes = EXCLUDED.memory_bytes,
	reservation_state = EXCLUDED.reservation_state, state = EXCLUDED.state,
	max_browser_workloads = EXCLUDED.max_browser_workloads, voice_capable = EXCLUDED.voice_capable,
	udp_port_min = EXCLUDED.udp_port_min, udp_port_max = EXCLUDED.udp_port_max, nat_ip = EXCLUDED.nat_ip,
	valid_until = EXCLUDED.valid_until, generation = EXCLUDED.generation, accepted_at = EXCLUDED.accepted_at`,
			a.NodeID, a.Region, a.Endpoint, a.URISAN, a.SPKISHA256, a.CPUMillis, a.MemoryBytes,
			a.ReservationState, a.State, a.MaxBrowserWorkloads, a.VoiceCapable, a.UDPPortMin, a.UDPPortMax, a.NATIP,
			a.ValidUntil, a.Generation, a.AcceptedAt); err != nil {
			return err
		}
		if a.State != StateRevoked {
			return nil
		}
		// Fence: the registry is the trust plane's single authority, so a
		// revoked grant revokes the pinned identity in the same transaction.
		_, err = tx.ExecContext(ctx, `
UPDATE gateway_helper_registry SET revoked_at = $2::timestamptz, updated_at = $2::timestamptz
WHERE node_id = $1 AND revoked_at IS NULL`, a.NodeID, a.AcceptedAt)
		return err
	})
}

// checkRoom enforces MaxNodes before inserting a new node into table.
func checkRoom(ctx context.Context, tx *sql.Tx, table string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM `+table).Scan(&n); err != nil {
		return err
	}
	if n >= MaxNodes {
		return ErrTooManyNodes
	}
	return nil
}

func (s *PostgresStore) GetAllocation(ctx context.Context, nodeID string) (Allocation, error) {
	if err := s.configured(); err != nil {
		return Allocation{}, err
	}
	a, err := scanAllocation(s.db.QueryRowContext(ctx,
		`SELECT `+allocationColumns+` FROM gateway_helper_allocations WHERE node_id = $1`, nodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return Allocation{}, ErrNotFound
	}
	return a, err
}

func (s *PostgresStore) ListAllocations(ctx context.Context) ([]Allocation, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	// COLLATE "C" keeps the order identical to the memory store's byte order.
	rows, err := s.db.QueryContext(ctx, `SELECT `+allocationColumns+
		` FROM gateway_helper_allocations ORDER BY node_id COLLATE "C" LIMIT $1`, MaxNodes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Allocation
	for rows.Next() {
		a, err := scanAllocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PostgresStore) PutState(ctx context.Context, st HelperState) error {
	if err := st.validate(); err != nil {
		return err
	}
	if err := s.configured(); err != nil {
		return err
	}
	var calm sql.NullTime
	if !st.PressureCalmSince.IsZero() {
		calm = sql.NullTime{Time: st.PressureCalmSince.UTC(), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO gateway_helper_state (node_id, last_heartbeat_at, pressure_reduced, pressure_calm_since, ramped_limit, host_cores, host_memory_bytes)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (node_id) DO UPDATE SET
	last_heartbeat_at = EXCLUDED.last_heartbeat_at, pressure_reduced = EXCLUDED.pressure_reduced,
	pressure_calm_since = EXCLUDED.pressure_calm_since, ramped_limit = EXCLUDED.ramped_limit,
	host_cores = EXCLUDED.host_cores, host_memory_bytes = EXCLUDED.host_memory_bytes
WHERE gateway_helper_state.last_heartbeat_at <= EXCLUDED.last_heartbeat_at`,
		st.NodeID, st.LastHeartbeat.UTC(), st.PressureReduced, calm, st.RampedLimit, st.HostCores, st.HostMemoryBytes)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation: no allocation
		return ErrNotFound
	}
	return err
}

func (s *PostgresStore) GetState(ctx context.Context, nodeID string) (HelperState, error) {
	if err := s.configured(); err != nil {
		return HelperState{}, err
	}
	var st HelperState
	var calm sql.NullTime
	err := s.db.QueryRowContext(ctx, `
SELECT node_id, last_heartbeat_at, pressure_reduced, pressure_calm_since, ramped_limit, host_cores, host_memory_bytes
FROM gateway_helper_state WHERE node_id = $1`, nodeID).Scan(
		&st.NodeID, &st.LastHeartbeat, &st.PressureReduced, &calm, &st.RampedLimit, &st.HostCores, &st.HostMemoryBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperState{}, ErrNotFound
	}
	st.LastHeartbeat = st.LastHeartbeat.UTC()
	if calm.Valid {
		st.PressureCalmSince = calm.Time.UTC()
	}
	return st, err
}

func (s *PostgresStore) PutRegistry(ctx context.Context, e RegistryEntry, now time.Time) error {
	if err := e.validate(); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var regRevoked sql.NullBool // NULL: no registry row
		var allocState sql.NullString
		if err := tx.QueryRowContext(ctx, `
SELECT (SELECT revoked_at IS NOT NULL FROM gateway_helper_registry WHERE node_id = $1),
       (SELECT state FROM gateway_helper_allocations WHERE node_id = $1)`, e.NodeID).Scan(&regRevoked, &allocState); err != nil {
			return err
		}
		if regRevoked.Bool || allocState.String == StateRevoked {
			return ErrNodeRevoked
		}
		if !regRevoked.Valid {
			if err := checkRoom(ctx, tx, "gateway_helper_registry"); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO gateway_helper_registry (node_id, uri_san, spki_current, spki_next, revoked_at, updated_at)
VALUES ($1, $2, $3, $4, NULL, $5)
ON CONFLICT (node_id) DO UPDATE SET
	uri_san = EXCLUDED.uri_san, spki_current = EXCLUDED.spki_current, spki_next = EXCLUDED.spki_next,
	updated_at = EXCLUDED.updated_at`,
			e.NodeID, e.URISAN, e.SPKICurrent, e.SPKINext, now.UTC())
		return err
	})
}

func (s *PostgresStore) GetRegistry(ctx context.Context, nodeID string) (RegistryEntry, error) {
	if err := s.configured(); err != nil {
		return RegistryEntry{}, err
	}
	var e RegistryEntry
	var revoked sql.NullTime
	err := s.db.QueryRowContext(ctx, `
SELECT node_id, uri_san, spki_current, spki_next, revoked_at, updated_at
FROM gateway_helper_registry WHERE node_id = $1`, nodeID).Scan(
		&e.NodeID, &e.URISAN, &e.SPKICurrent, &e.SPKINext, &revoked, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RegistryEntry{}, ErrNotFound
	}
	e.UpdatedAt = e.UpdatedAt.UTC()
	if revoked.Valid {
		e.RevokedAt = revoked.Time.UTC()
	}
	return e, err
}

func (s *PostgresStore) PromoteSPKI(ctx context.Context, nodeID string, now time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var next string
		var revoked bool
		err := tx.QueryRowContext(ctx, `
SELECT spki_next, revoked_at IS NOT NULL FROM gateway_helper_registry WHERE node_id = $1 FOR UPDATE`,
			nodeID).Scan(&next, &revoked)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		case revoked:
			return ErrNodeRevoked
		case next == "":
			return ErrInvalid
		}
		_, err = tx.ExecContext(ctx, `
UPDATE gateway_helper_registry SET spki_current = spki_next, spki_next = '', updated_at = $2 WHERE node_id = $1`,
			nodeID, now.UTC())
		return err
	})
}

func (s *PostgresStore) RevokeNode(ctx context.Context, nodeID string, now time.Time) error {
	if err := s.configured(); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE gateway_helper_registry
SET updated_at = CASE WHEN revoked_at IS NULL THEN $2::timestamptz ELSE updated_at END,
    revoked_at = COALESCE(revoked_at, $2::timestamptz)
WHERE node_id = $1`, nodeID, now.UTC())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}
