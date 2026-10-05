package voice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore is a Store backed by SQLite via database/sql (driver "sqlite",
// modernc.org/sqlite). Like the conversations SQLite store it owns its schema
// with CREATE ... IF NOT EXISTS; SQLite serialises writers, so a
// BEGIN IMMEDIATE transaction makes each Reserve's candidate claim atomic
// process-wide, and the partial UNIQUE indexes make it atomic across every
// replica sharing the database file.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore constructs a SQLiteStore over an opened database handle.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

const sqliteCreateVoiceSessionsTable = `
CREATE TABLE IF NOT EXISTS gateway_voice_sessions (
	session_id       TEXT PRIMARY KEY,
	tenant_id        TEXT NOT NULL,
	app_id           TEXT NOT NULL DEFAULT '',
	target           TEXT NOT NULL,
	status           TEXT NOT NULL DEFAULT 'queued',
	muted            INTEGER NOT NULL DEFAULT 0,
	identity_ref     TEXT NOT NULL DEFAULT '',
	instance_ref     TEXT NOT NULL DEFAULT '',
	last_error       TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL,
	lease_expires_at TEXT NOT NULL DEFAULT '',
	terminated_at    TEXT NOT NULL DEFAULT ''
)`

// Exclusive provider-account lease: at most one live session per
// (tenant, target, identity_ref). Queued rows carry no identity and are
// excluded by the predicate.
const sqliteCreateVoiceAccountIndex = `
CREATE UNIQUE INDEX IF NOT EXISTS uq_voice_active_account
	ON gateway_voice_sessions (tenant_id, target, identity_ref)
	WHERE status IN ('queued','connecting','connected') AND identity_ref <> ''`

// Exclusive browser/audio environment lease: at most one live session per
// browser instance (the virtual microphone and speaker monitor are
// instance-wide devices).
const sqliteCreateVoiceInstanceIndex = `
CREATE UNIQUE INDEX IF NOT EXISTS uq_voice_active_instance
	ON gateway_voice_sessions (tenant_id, instance_ref)
	WHERE status IN ('queued','connecting','connected') AND instance_ref <> ''`

const sqliteCreateVoiceTenantIndex = `
CREATE INDEX IF NOT EXISTS idx_voice_tenant_created
	ON gateway_voice_sessions (tenant_id, created_at DESC)`

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("voice: sqlite store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	for _, stmt := range []string{
		sqliteCreateVoiceSessionsTable,
		sqliteCreateVoiceAccountIndex,
		sqliteCreateVoiceInstanceIndex,
		sqliteCreateVoiceTenantIndex,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

const sqliteVoiceColumns = `session_id, tenant_id, app_id, target, status, muted,
identity_ref, instance_ref, last_error, created_at, updated_at, lease_expires_at, terminated_at`

func scanSQLiteVoiceSession(row interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var muted int
	var createdAt, updatedAt string
	var leaseExpires, terminatedAt string
	if err := row.Scan(&s.ID, &s.TenantID, &s.AppID, &s.Target, &s.Status, &muted,
		&s.IdentityRef, &s.InstanceRef, &s.LastError, &createdAt, &updatedAt,
		&leaseExpires, &terminatedAt); err != nil {
		return Session{}, err
	}
	s.Muted = muted != 0
	s.Status = Status(s.Status)
	_ = s.CreatedAt.UnmarshalText([]byte(createdAt))
	_ = s.UpdatedAt.UnmarshalText([]byte(updatedAt))
	if leaseExpires != "" {
		_ = s.LeaseExpires.UnmarshalText([]byte(leaseExpires))
	}
	if terminatedAt != "" {
		_ = s.TerminatedAt.UnmarshalText([]byte(terminatedAt))
	}
	return s, nil
}

func formatSQLiteTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *SQLiteStore) Reserve(ctx context.Context, req ReserveRequest) (Session, error) {
	if req.SessionID == "" || req.TenantID == "" || req.Target == "" {
		return Session{}, errors.New("voice: session_id, tenant_id and target are required")
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize admission: every candidate check-and-insert happens under the
	// database write lock, so two replicas cannot both believe the same
	// account is free.
	if _, err := tx.ExecContext(ctx, "BEGIN IMMEDIATE"); err == nil {
		// modernc sqlite honours implicit write locks; the explicit no-op
		// keeps the transaction's write intent unambiguous.
	}
	var exists string
	if err := tx.QueryRowContext(ctx,
		"SELECT session_id FROM gateway_voice_sessions WHERE session_id = ?", req.SessionID,
	).Scan(&exists); err == nil {
		return Session{}, ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Session{}, err
	}
	status := StatusQueued
	identity, instance := "", ""
	for _, candID := range req.IdentityCandidates {
		candID = strings.TrimSpace(candID)
		if candID == "" {
			continue
		}
		var taken int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM gateway_voice_sessions
WHERE tenant_id = ? AND target = ? AND identity_ref = ?
  AND status IN ('connecting','connected')`,
			req.TenantID, req.Target, candID).Scan(&taken); err != nil {
			return Session{}, err
		}
		if taken > 0 {
			continue
		}
		claimedInstance := ""
		for _, candInst := range req.InstanceCandidates {
			candInst = strings.TrimSpace(candInst)
			if candInst == "" {
				continue
			}
			var instanceTaken int
			if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM gateway_voice_sessions
WHERE tenant_id = ? AND instance_ref = ? AND status IN ('connecting','connected')`,
				req.TenantID, candInst).Scan(&instanceTaken); err != nil {
				return Session{}, err
			}
			if instanceTaken == 0 {
				claimedInstance = candInst
				break
			}
		}
		if claimedInstance == "" {
			continue // never hold an account without its exclusive environment
		}
		identity, instance = candID, claimedInstance
		status = StatusConnecting
		break
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO gateway_voice_sessions (`+sqliteVoiceColumns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.SessionID, req.TenantID, req.AppID, req.Target, string(status), 0,
		identity, instance, "",
		formatSQLiteTime(now), formatSQLiteTime(now),
		formatSQLiteTime(now.Add(req.LeaseTTL)), "",
	)
	if err != nil {
		// The partial unique indexes are the shared authority; a constraint
		// violation means another replica won the same lease between the
		// check and the insert — surface it as a conflict, never overwrite.
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(err.Error(), "constraint") {
			return Session{}, ErrConflict
		}
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return Session{
		ID: req.SessionID, TenantID: req.TenantID, AppID: req.AppID, Target: req.Target,
		Status: status, IdentityRef: identity, InstanceRef: instance,
		CreatedAt: now, UpdatedAt: now, LeaseExpires: now.Add(req.LeaseTTL),
	}, nil
}

func (s *SQLiteStore) Claim(ctx context.Context, tenantID, sessionID string, identityCandidates, instanceCandidates []string, leaseTTL time.Duration, now time.Time) (Session, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var target string
	if err := s.db.QueryRowContext(ctx,
		"SELECT target FROM gateway_voice_sessions WHERE session_id = ? AND tenant_id = ?",
		sessionID, tenantID).Scan(&target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, err
	}
	// Try each candidate pair; the NOT EXISTS guards inside the UPDATE are
	// the shared atomic check (plus the partial unique indexes behind them),
	// so two replicas racing on the same account cannot both win.
	for _, candID := range identityCandidates {
		candID = strings.TrimSpace(candID)
		if candID == "" {
			continue
		}
		for _, candInst := range instanceCandidates {
			candInst = strings.TrimSpace(candInst)
			if candInst == "" {
				continue
			}
			res, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions
SET status = 'connecting', identity_ref = ?, instance_ref = ?, lease_expires_at = ?, updated_at = ?
WHERE session_id = ? AND tenant_id = ? AND status = 'queued'
  AND NOT EXISTS (
    SELECT 1 FROM gateway_voice_sessions other
    WHERE other.tenant_id = gateway_voice_sessions.tenant_id
      AND other.target = gateway_voice_sessions.target
      AND other.identity_ref = ?
      AND other.session_id <> gateway_voice_sessions.session_id
      AND other.status IN ('connecting','connected')
  )
  AND NOT EXISTS (
    SELECT 1 FROM gateway_voice_sessions other2
    WHERE other2.tenant_id = gateway_voice_sessions.tenant_id
      AND other2.instance_ref = ?
      AND other2.session_id <> gateway_voice_sessions.session_id
      AND other2.status IN ('connecting','connected')
  )`,
				candID, candInst, formatSQLiteTime(now.Add(leaseTTL)), formatSQLiteTime(now),
				sessionID, tenantID, candID, candInst)
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "constraint") {
					continue // lease lost to a concurrent replica; try next pair
				}
				return Session{}, err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return Session{}, err
			}
			if n == 0 {
				continue // queued row moved or the pair was taken
			}
			return Session{
				ID: sessionID, TenantID: tenantID, Target: target, Status: StatusConnecting,
				IdentityRef: candID, InstanceRef: candInst,
				LeaseExpires: now.Add(leaseTTL), UpdatedAt: now,
			}, nil
		}
	}
	return Session{}, ErrConflict
}

func (s *SQLiteStore) Get(ctx context.Context, tenantID, sessionID string) (Session, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT `+sqliteVoiceColumns+` FROM gateway_voice_sessions
WHERE session_id = ? AND tenant_id = ?`, sessionID, tenantID)
	session, err := scanSQLiteVoiceSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return session, true, nil
}

func (s *SQLiteStore) List(ctx context.Context, tenantID, target string, limit int) ([]Session, error) {
	query := `SELECT ` + sqliteVoiceColumns + ` FROM gateway_voice_sessions WHERE tenant_id = ?`
	args := []any{tenantID}
	if target != "" {
		query += " AND target = ?"
		args = append(args, target)
	}
	query += " ORDER BY created_at DESC, session_id DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanSQLiteVoiceSessions(rows)
}

func scanSQLiteVoiceSessions(rows *sql.Rows) ([]Session, error) {
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		session, err := scanSQLiteVoiceSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Transition(ctx context.Context, tenantID, sessionID string, from, to Status, now time.Time, lastError string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions
SET status = ?, updated_at = ?, last_error = ?,
    identity_ref = CASE WHEN ? = 'terminated' THEN '' ELSE identity_ref END,
    instance_ref = CASE WHEN ? = 'terminated' THEN '' ELSE instance_ref END,
    terminated_at = CASE WHEN ? = 'terminated' THEN ? ELSE terminated_at END
WHERE session_id = ? AND tenant_id = ? AND status = ?`,
		string(to), formatSQLiteTime(now), lastError,
		string(to), string(to), string(to), formatSQLiteTime(now),
		sessionID, tenantID, string(from),
	)
	if err != nil {
		return err
	}
	return requireSQLiteChange(res)
}

func (s *SQLiteStore) SetMuted(ctx context.Context, tenantID, sessionID string, muted bool, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions SET muted = ?, updated_at = ?
WHERE session_id = ? AND tenant_id = ? AND status <> 'terminated'`,
		boolToInt(muted), formatSQLiteTime(now), sessionID, tenantID)
	if err != nil {
		return err
	}
	return requireSQLiteChange(res)
}

func (s *SQLiteStore) Terminate(ctx context.Context, tenantID, sessionID string, now time.Time, reason string) error {
	// Idempotent: already-terminated rows match the predicate and rewrite the
	// same values (reason is refreshed, which is harmless and keeps this one
	// statement CAS-free).
	_, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions
SET status = 'terminated', updated_at = ?, terminated_at = ?, last_error = ?,
    identity_ref = '', instance_ref = ''
WHERE session_id = ? AND tenant_id = ? AND status <> 'terminated'`,
		formatSQLiteTime(now), formatSQLiteTime(now), reason, sessionID, tenantID)
	return err
}

func (s *SQLiteStore) RenewLease(ctx context.Context, tenantID, sessionID string, until time.Time, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions SET lease_expires_at = ?, updated_at = ?
WHERE session_id = ? AND tenant_id = ? AND status IN ('connecting','connected')`,
		formatSQLiteTime(until), formatSQLiteTime(now), sessionID, tenantID)
	if err != nil {
		return err
	}
	return requireSQLiteChange(res)
}

func (s *SQLiteStore) SweepExpired(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT session_id FROM gateway_voice_sessions
WHERE status IN ('connecting','connected')
  AND lease_expires_at <> '' AND lease_expires_at <= ?`, formatSQLiteTime(now))
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions
SET status = 'terminated', updated_at = ?, terminated_at = ?, last_error = 'lease_expired',
    identity_ref = '', instance_ref = ''
WHERE session_id = ? AND status IN ('connecting','connected')`, formatSQLiteTime(now), formatSQLiteTime(now), id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func (s *SQLiteStore) GlobalSessionCounts(ctx context.Context) (int, int, error) {
	var active, queued int
	if err := s.db.QueryRowContext(ctx, `
SELECT
  COUNT(1) FILTER (WHERE status IN ('connecting','connected')),
  COUNT(1) FILTER (WHERE status = 'queued')
FROM gateway_voice_sessions`).Scan(&active, &queued); err != nil {
		return 0, 0, err
	}
	return active, queued, nil
}

func (s *SQLiteStore) ActiveCount(ctx context.Context, tenantID, target string) (int, error) {
	query := `
SELECT COUNT(1) FROM gateway_voice_sessions
WHERE tenant_id = ? AND status IN ('queued','connecting','connected')`
	args := []any{tenantID}
	if target != "" {
		query += " AND target = ?"
		args = append(args, target)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func requireSQLiteChange(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
