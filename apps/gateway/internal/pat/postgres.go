package pat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresStore is a Store backed by Postgres (github.com/jackc/pgx/v5/stdlib,
// driver "pgx"). Its schema is migration-driven
// (migrations/postgres/0011_personal_access_tokens.sql); Ready asserts the
// table exists and never creates it. Only the SHA-256 hash of each token is
// persisted.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore returns a Store over db.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrNotConfigured
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	return requirePostgresObject(ctx, s.db, "gateway_pats")
}

func (s *PostgresStore) Save(ctx context.Context, token Token) error {
	if s == nil || s.db == nil {
		return ErrNotConfigured
	}
	if token.ID == "" {
		return errors.New("pat: token ID is required")
	}
	var expiresAt any
	if !token.ExpiresAt.IsZero() {
		expiresAt = token.ExpiresAt.UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO gateway_pats (token_hash, tenant_id, app_id, role, issued_at, expires_at, revoked)
VALUES ($1, $2, $3, $4, $5, $6, false)
ON CONFLICT (token_hash) DO UPDATE SET
	tenant_id  = excluded.tenant_id,
	app_id     = excluded.app_id,
	role       = excluded.role,
	issued_at  = excluded.issued_at,
	expires_at = excluded.expires_at,
	revoked    = false`,
		hashToken(token.ID), token.TenantID, token.AppID, token.Role,
		token.IssuedAt.UTC(), expiresAt)
	return err
}

func (s *PostgresStore) Resolve(ctx context.Context, raw string, now time.Time) (Token, bool, error) {
	if s == nil || s.db == nil {
		return Token{}, false, ErrNotConfigured
	}
	if raw == "" {
		return Token{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
SELECT tenant_id, app_id, role, issued_at, expires_at, revoked
FROM gateway_pats WHERE token_hash = $1`, hashToken(raw))
	var tenantID, appID, role string
	var issuedAt time.Time
	var expiresAt sql.NullTime
	var revoked bool
	if err := row.Scan(&tenantID, &appID, &role, &issuedAt, &expiresAt, &revoked); err != nil {
		if err == sql.ErrNoRows {
			return Token{}, false, nil
		}
		return Token{}, false, err
	}
	if revoked {
		return Token{}, false, nil
	}
	token := Token{ID: raw, TenantID: tenantID, AppID: appID, Role: role, IssuedAt: issuedAt.UTC()}
	if expiresAt.Valid {
		token.ExpiresAt = expiresAt.Time.UTC()
	}
	if token.IsExpired(now) {
		return Token{}, false, nil
	}
	return token, true, nil
}

func (s *PostgresStore) Revoke(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return ErrNotConfigured
	}
	if id == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE gateway_pats SET revoked = true WHERE token_hash = $1`, hashToken(id))
	return err
}

func requirePostgresObject(ctx context.Context, db *sql.DB, objectName string) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, objectName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s is missing", objectName)
	}
	return nil
}
