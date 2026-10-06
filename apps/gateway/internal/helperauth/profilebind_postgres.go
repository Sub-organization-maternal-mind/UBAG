package helperauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/storekit"
)

// PostgresProfileStore is the shared ProfileStore. Schema comes from
// migrations/postgres/0024_helper_profiles.sql; Ready only asserts it exists
// and the store never runs DDL. Bind serialises on one advisory lock (it is an
// operator action, so this is cheap) so the per-provider bound and the
// one-active-profile-per-tuple rule hold across gateway replicas.
type PostgresProfileStore struct {
	db *sql.DB
}

var _ ProfileStore = (*PostgresProfileStore)(nil)

func NewPostgresProfileStore(db *sql.DB) *PostgresProfileStore { return &PostgresProfileStore{db: db} }

func (s *PostgresProfileStore) configured() error {
	if s == nil || s.db == nil {
		return ErrNotConfigured
	}
	return nil
}

func (s *PostgresProfileStore) Ready(ctx context.Context) error {
	if err := s.configured(); err != nil {
		return err
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	if err := storekit.RequirePostgresObject(ctx, s.db, "gateway_helper_profiles"); err != nil {
		return fmt.Errorf("helperauth: %w; apply migrations/postgres/0024_helper_profiles.sql", err)
	}
	return nil
}

const profileColumns = `profile_ref, tenant_id, provider, identity_ref, node_id, state, created_at, revoked_at`

func scanBinding(row interface{ Scan(...any) error }) (Binding, error) {
	var b Binding
	var revoked sql.NullTime
	if err := row.Scan(&b.ProfileRef, &b.TenantID, &b.Provider, &b.IdentityRef, &b.NodeID, &b.State, &b.CreatedAt, &revoked); err != nil {
		return Binding{}, err
	}
	b.CreatedAt = b.CreatedAt.UTC()
	if revoked.Valid {
		b.RevokedAt = revoked.Time.UTC()
	}
	return b, nil
}

func (s *PostgresProfileStore) Bind(ctx context.Context, tenantID, provider, identityRef, nodeID string, now time.Time) (Binding, error) {
	if err := validateBindArgs(tenantID, provider, identityRef, nodeID); err != nil {
		return Binding{}, err
	}
	if err := s.configured(); err != nil {
		return Binding{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Binding{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('ubag-helper-profiles'))`); err != nil {
		return Binding{}, err
	}
	existing, err := scanBinding(tx.QueryRowContext(ctx, `SELECT `+profileColumns+` FROM gateway_helper_profiles
WHERE tenant_id = $1 AND provider = $2 AND identity_ref = $3 AND node_id = $4 AND state = 'active'`,
		tenantID, provider, identityRef, nodeID))
	switch {
	case err == nil:
		return existing, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return Binding{}, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM gateway_helper_profiles
WHERE tenant_id = $1 AND provider = $2 AND state = 'active'`, tenantID, provider).Scan(&active); err != nil {
		return Binding{}, err
	}
	if active >= MaxProfilesPerProvider {
		return Binding{}, ErrTooManyProfiles
	}
	b := Binding{
		ProfileRef: MintProfileRef(), TenantID: tenantID, Provider: provider, IdentityRef: identityRef,
		NodeID: nodeID, State: ProfileActive, CreatedAt: now.UTC().Truncate(time.Microsecond),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_helper_profiles (`+profileColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, NULL)`,
		b.ProfileRef, b.TenantID, b.Provider, b.IdentityRef, b.NodeID, b.State, b.CreatedAt); err != nil {
		return Binding{}, err
	}
	return b, tx.Commit()
}

func (s *PostgresProfileStore) Resolve(ctx context.Context, tenantID, profileRef string) (Binding, bool, error) {
	if err := s.configured(); err != nil {
		return Binding{}, false, err
	}
	b, err := scanBinding(s.db.QueryRowContext(ctx, `SELECT `+profileColumns+` FROM gateway_helper_profiles
WHERE tenant_id = $1 AND profile_ref = $2 AND state = 'active'`, tenantID, profileRef))
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, false, nil
	}
	return b, err == nil, err
}

func (s *PostgresProfileStore) List(ctx context.Context, tenantID, provider string) ([]Binding, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	// COLLATE "C" keeps the order identical to the memory store's byte order.
	rows, err := s.db.QueryContext(ctx, `SELECT `+profileColumns+` FROM gateway_helper_profiles
WHERE tenant_id = $1 AND provider = $2 AND state = 'active'
ORDER BY node_id COLLATE "C", profile_ref COLLATE "C" LIMIT $3`, tenantID, provider, MaxProfilesPerProvider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Binding{}
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *PostgresProfileStore) Revoke(ctx context.Context, tenantID, profileRef string, now time.Time) (bool, error) {
	if err := s.configured(); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE gateway_helper_profiles SET state = 'revoked', revoked_at = $3
WHERE tenant_id = $1 AND profile_ref = $2 AND state = 'active'`, tenantID, profileRef, now.UTC().Truncate(time.Microsecond))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
