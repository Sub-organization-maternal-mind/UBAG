package session

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SQLiteStore is a Store backed by SQLite (driver "sqlite",
// modernc.org/sqlite). It owns its schema and persists only the SHA-256 hash of
// each token as the primary key.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore constructs a SQLiteStore over db.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

const sqliteCreateSessionsTable = `
CREATE TABLE IF NOT EXISTS gateway_sessions (
	token_hash TEXT PRIMARY KEY,
	id TEXT NOT NULL,
	tenant_id TEXT NOT NULL,
	app_id TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT 'viewer',
	subject TEXT NOT NULL DEFAULT '',
	email TEXT NOT NULL DEFAULT '',
	issued_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	revoked INTEGER NOT NULL DEFAULT 0
)`

const sqliteSessionsExpiryIndex = `
CREATE INDEX IF NOT EXISTS idx_gateway_sessions_expires_at
	ON gateway_sessions (expires_at)`

const sessionTimeLayout = "2006-01-02T15:04:05Z07:00"

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrNotConfigured
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, sqliteCreateSessionsTable); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, sqliteSessionsExpiryIndex)
	return err
}

func (s *SQLiteStore) Create(ctx context.Context, sess Session) (Session, string, error) {
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
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		hash, sess.ID, sess.TenantID, sess.AppID, sess.Role, sess.Subject, sess.Email,
		sess.IssuedAt.Format(sessionTimeLayout), sess.ExpiresAt.Format(sessionTimeLayout)); err != nil {
		return Session{}, "", fmt.Errorf("session: insert: %w", err)
	}
	return sess, token, nil
}

func (s *SQLiteStore) Resolve(ctx context.Context, token string, now time.Time) (Session, bool, error) {
	if s == nil || s.db == nil {
		return Session{}, false, ErrNotConfigured
	}
	if token == "" {
		return Session{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
SELECT id, tenant_id, app_id, role, subject, email, issued_at, expires_at, revoked
FROM gateway_sessions WHERE token_hash = ?`, hashToken(token))
	sess, err := scanSQLiteSession(row)
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

func (s *SQLiteStore) Revoke(ctx context.Context, token string, now time.Time) (bool, error) {
	if s == nil || s.db == nil {
		return false, ErrNotConfigured
	}
	if token == "" {
		return false, nil
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE gateway_sessions SET revoked = 1 WHERE token_hash = ? AND revoked = 0`, hashToken(token))
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSQLiteSession(row rowScanner) (Session, error) {
	var sess Session
	var issuedAt, expiresAt string
	var revoked int
	if err := row.Scan(&sess.ID, &sess.TenantID, &sess.AppID, &sess.Role, &sess.Subject, &sess.Email,
		&issuedAt, &expiresAt, &revoked); err != nil {
		return Session{}, err
	}
	if t, err := time.Parse(sessionTimeLayout, issuedAt); err == nil {
		sess.IssuedAt = t.UTC()
	}
	if t, err := time.Parse(sessionTimeLayout, expiresAt); err == nil {
		sess.ExpiresAt = t.UTC()
	}
	sess.Revoked = revoked != 0
	return sess, nil
}
