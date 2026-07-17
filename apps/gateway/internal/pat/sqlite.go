package pat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// ErrNotConfigured is returned when a persistent store is used without a
// database handle.
var ErrNotConfigured = errors.New("pat: store is not configured")

// patTimeLayout is the RFC3339 layout used to persist times as TEXT in SQLite,
// matching the gateway's other SQLite stores.
const patTimeLayout = "2006-01-02T15:04:05Z07:00"

// hashToken returns the hex-encoded SHA-256 of a raw token. Persistent stores
// key rows by this hash and never store the raw token, so a store leak reveals
// no usable credential (matching the session store).
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// SQLiteStore is a Store backed by SQLite (driver "sqlite",
// modernc.org/sqlite). It owns its schema (CREATE TABLE/INDEX IF NOT EXISTS via
// Ready) and persists only the SHA-256 hash of each token.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore constructs a SQLiteStore over db.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

const sqliteCreatePATsTable = `
CREATE TABLE IF NOT EXISTS gateway_pats (
	token_hash TEXT PRIMARY KEY,
	tenant_id  TEXT NOT NULL,
	app_id     TEXT NOT NULL DEFAULT '',
	role       TEXT NOT NULL DEFAULT 'viewer',
	issued_at  TEXT NOT NULL,
	expires_at TEXT,
	revoked    INTEGER NOT NULL DEFAULT 0
)`

const sqlitePATsExpiryIndex = `
CREATE INDEX IF NOT EXISTS idx_gateway_pats_expires_at
	ON gateway_pats (expires_at)`

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrNotConfigured
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, sqliteCreatePATsTable); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, sqlitePATsExpiryIndex)
	return err
}

func (s *SQLiteStore) Save(ctx context.Context, token Token) error {
	if s == nil || s.db == nil {
		return ErrNotConfigured
	}
	if token.ID == "" {
		return errors.New("pat: token ID is required")
	}
	var expiresAt any
	if !token.ExpiresAt.IsZero() {
		expiresAt = token.ExpiresAt.UTC().Format(patTimeLayout)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO gateway_pats (token_hash, tenant_id, app_id, role, issued_at, expires_at, revoked)
VALUES (?, ?, ?, ?, ?, ?, 0)
ON CONFLICT(token_hash) DO UPDATE SET
	tenant_id  = excluded.tenant_id,
	app_id     = excluded.app_id,
	role       = excluded.role,
	issued_at  = excluded.issued_at,
	expires_at = excluded.expires_at,
	revoked    = 0`,
		hashToken(token.ID), token.TenantID, token.AppID, token.Role,
		token.IssuedAt.UTC().Format(patTimeLayout), expiresAt)
	return err
}

func (s *SQLiteStore) Resolve(ctx context.Context, raw string, now time.Time) (Token, bool, error) {
	if s == nil || s.db == nil {
		return Token{}, false, ErrNotConfigured
	}
	if raw == "" {
		return Token{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
SELECT tenant_id, app_id, role, issued_at, expires_at, revoked
FROM gateway_pats WHERE token_hash = ?`, hashToken(raw))
	var tenantID, appID, role, issuedAt string
	var expiresAt sql.NullString
	var revoked int
	if err := row.Scan(&tenantID, &appID, &role, &issuedAt, &expiresAt, &revoked); err != nil {
		if err == sql.ErrNoRows {
			return Token{}, false, nil
		}
		return Token{}, false, err
	}
	if revoked != 0 {
		return Token{}, false, nil
	}
	token := Token{ID: raw, TenantID: tenantID, AppID: appID, Role: role}
	if t, err := time.Parse(patTimeLayout, issuedAt); err == nil {
		token.IssuedAt = t.UTC()
	}
	if expiresAt.Valid && expiresAt.String != "" {
		if t, err := time.Parse(patTimeLayout, expiresAt.String); err == nil {
			token.ExpiresAt = t.UTC()
		}
	}
	if token.IsExpired(now) {
		return Token{}, false, nil
	}
	return token, true, nil
}

func (s *SQLiteStore) Revoke(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return ErrNotConfigured
	}
	if id == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE gateway_pats SET revoked = 1 WHERE token_hash = ?`, hashToken(id))
	return err
}
