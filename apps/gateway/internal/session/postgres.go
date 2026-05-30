package session

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PostgresStore is a Store backed by Postgres (github.com/jackc/pgx/v5/stdlib,
// driver "pgx"). Its schema is migration-driven
// (migrations/postgres/0006_audit_sessions.sql). Only the SHA-256 hash of each
// token is persisted.
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
	return requirePostgresObject(ctx, s.db, "gateway_sessions")
}

func (s *PostgresStore) Create(ctx context.Context, sess Session) (Session, string, error) {
	if s == nil || s.db == nil {
		return Session{}, "", ErrNotConfigured
	}
	normalize(&sess)
	token, err := generateToken()
	if err != nil {
		return Session{}, "", err
	}
	hash := hashToken(token)
	sess.ID = newSessionID(hash)
	sess.Revoked = false

	if _, err := s.db.ExecContext(ctx, `
INSERT INTO gateway_sessions (
	token_hash, id, tenant_id, app_id, role, subject, email, issued_at, expires_at, revoked
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, false)`,
		hash, sess.ID, sess.TenantID, sess.AppID, sess.Role, sess.Subject, sess.Email,
		sess.IssuedAt, sess.ExpiresAt); err != nil {
		return Session{}, "", fmt.Errorf("session: insert: %w", err)
	}
	return sess, token, nil
}

func (s *PostgresStore) Resolve(ctx context.Context, token string, now time.Time) (Session, bool, error) {
	if s == nil || s.db == nil {
		return Session{}, false, ErrNotConfigured
	}
	if token == "" {
		return Session{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
SELECT id, tenant_id, app_id, role, subject, email, issued_at, expires_at, revoked
FROM gateway_sessions WHERE token_hash = $1`, hashToken(token))
	sess, err := scanPostgresSession(row)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	if !isLive(sess, now) {
		return Session{}, false, nil
	}
	return sess, true, nil
}

func (s *PostgresStore) Revoke(ctx context.Context, token string, now time.Time) (bool, error) {
	if s == nil || s.db == nil {
		return false, ErrNotConfigured
	}
	if token == "" {
		return false, nil
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE gateway_sessions SET revoked = true WHERE token_hash = $1 AND revoked = false`, hashToken(token))
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

func scanPostgresSession(row rowScanner) (Session, error) {
	var sess Session
	var issuedAt, expiresAt time.Time
	if err := row.Scan(&sess.ID, &sess.TenantID, &sess.AppID, &sess.Role, &sess.Subject, &sess.Email,
		&issuedAt, &expiresAt, &sess.Revoked); err != nil {
		return Session{}, err
	}
	sess.IssuedAt = issuedAt.UTC()
	sess.ExpiresAt = expiresAt.UTC()
	return sess, nil
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
