package voice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgresStore is a Store backed by PostgreSQL. Schema objects come from
// migrations/postgres/0019_voice_sessions.sql — Ready() only asserts they
// exist and fails closed otherwise (the store never runs DDL at runtime).
// Exclusivity rides the same partial UNIQUE indexes the migration declares,
// so every replica sharing the database enforces one live session per
// provider account and per browser/audio environment.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore constructs a PostgresStore over an opened handle.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("voice: postgres store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	for _, object := range []string{"gateway_voice_sessions", "uq_voice_active_account", "uq_voice_active_instance"} {
		var exists bool
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)`, object,
		).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("voice: postgres schema object %q is missing; apply migrations/postgres/0019_voice_sessions.sql", object)
		}
	}
	return nil
}

const postgresVoiceColumns = `session_id, tenant_id, app_id, target, mode, job_id,
status, muted, identity_ref, instance_ref, last_error, created_at, updated_at,
lease_expires_at, terminated_at`

func scanPostgresVoiceSession(row interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var muted bool
	if err := row.Scan(&s.ID, &s.TenantID, &s.AppID, &s.Target, &s.Mode, &s.JobID,
		&s.Status, &muted, &s.IdentityRef, &s.InstanceRef, &s.LastError,
		&s.CreatedAt, &s.UpdatedAt, &s.LeaseExpires, &s.TerminatedAt); err != nil {
		return Session{}, err
	}
	s.Muted = muted
	s.Status = Status(s.Status)
	return s, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

func (s *PostgresStore) Reserve(ctx context.Context, req ReserveRequest) (Session, error) {
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
	var exists string
	if err := tx.QueryRowContext(ctx,
		`SELECT session_id FROM gateway_voice_sessions WHERE session_id = $1 FOR UPDATE`,
		req.SessionID).Scan(&exists); err == nil {
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
WHERE tenant_id = $1 AND target = $2 AND identity_ref = $3
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
WHERE tenant_id = $1 AND instance_ref = $2 AND status IN ('connecting','connected')`,
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
INSERT INTO gateway_voice_sessions (`+postgresVoiceColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, FALSE, $8, $9, '', $10, $10, $11, $11)`,
		req.SessionID, req.TenantID, req.AppID, req.Target,
		req.Mode, req.JobID, string(status),
		identity, instance, now,
		now.Add(req.LeaseTTL),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Session{}, ErrConflict
		}
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, ErrConflict
	}
	return Session{
		ID: req.SessionID, TenantID: req.TenantID, AppID: req.AppID, Target: req.Target,
		Status: status, IdentityRef: identity, InstanceRef: instance,
		CreatedAt: now, UpdatedAt: now, LeaseExpires: now.Add(req.LeaseTTL),
	}, nil
}

func (s *PostgresStore) Claim(ctx context.Context, tenantID, sessionID string, identityCandidates, instanceCandidates []string, leaseTTL time.Duration, now time.Time) (Session, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var target string
	if err := s.db.QueryRowContext(ctx,
		`SELECT target FROM gateway_voice_sessions WHERE session_id = $1 AND tenant_id = $2`,
		sessionID, tenantID).Scan(&target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, err
	}
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
SET status = 'connecting', identity_ref = $3, instance_ref = $4, lease_expires_at = $5, updated_at = $6
WHERE session_id = $1 AND tenant_id = $2 AND status = 'queued'
  AND NOT EXISTS (
    SELECT 1 FROM gateway_voice_sessions other
    WHERE other.tenant_id = gateway_voice_sessions.tenant_id
      AND other.target = gateway_voice_sessions.target
      AND other.identity_ref = $3
      AND other.session_id <> gateway_voice_sessions.session_id
      AND other.status IN ('connecting','connected')
  )
  AND NOT EXISTS (
    SELECT 1 FROM gateway_voice_sessions other2
    WHERE other2.tenant_id = gateway_voice_sessions.tenant_id
      AND other2.instance_ref = $4
      AND other2.session_id <> gateway_voice_sessions.session_id
      AND other2.status IN ('connecting','connected')
  )`,
				sessionID, tenantID, candID, candInst, now.Add(leaseTTL), now)
			if err != nil {
				if isUniqueViolation(err) {
					continue
				}
				return Session{}, err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return Session{}, err
			}
			if n == 0 {
				continue
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

func (s *PostgresStore) Get(ctx context.Context, tenantID, sessionID string) (Session, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT `+postgresVoiceColumns+` FROM gateway_voice_sessions
WHERE session_id = $1 AND tenant_id = $2`, sessionID, tenantID)
	session, err := scanPostgresVoiceSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return session, true, nil
}

func (s *PostgresStore) List(ctx context.Context, tenantID, target string, limit int) ([]Session, error) {
	query := `SELECT ` + postgresVoiceColumns + ` FROM gateway_voice_sessions WHERE tenant_id = $1`
	args := []any{tenantID}
	if target != "" {
		query += " AND target = $2"
		args = append(args, target)
	}
	query += " ORDER BY created_at DESC, session_id DESC"
	if limit > 0 {
		if target != "" {
			query += " LIMIT $3"
		} else {
			query += " LIMIT $2"
		}
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		session, err := scanPostgresVoiceSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

func (s *PostgresStore) Transition(ctx context.Context, tenantID, sessionID string, from, to Status, now time.Time, lastError string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions
SET status = $3, updated_at = $4, last_error = $5,
    identity_ref = CASE WHEN $3 = 'terminated' THEN '' ELSE identity_ref END,
    instance_ref = CASE WHEN $3 = 'terminated' THEN '' ELSE instance_ref END,
    terminated_at = CASE WHEN $3 = 'terminated' THEN $4 ELSE terminated_at END
WHERE session_id = $1 AND tenant_id = $2 AND status = $6`,
		sessionID, tenantID, string(to), now, lastError, string(from))
	if err != nil {
		return err
	}
	return requirePostgresChange(res)
}

func (s *PostgresStore) SetMuted(ctx context.Context, tenantID, sessionID string, muted bool, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions SET muted = $3, updated_at = $4
WHERE session_id = $1 AND tenant_id = $2 AND status <> 'terminated'`,
		sessionID, tenantID, muted, now)
	if err != nil {
		return err
	}
	return requirePostgresChange(res)
}

func (s *PostgresStore) Terminate(ctx context.Context, tenantID, sessionID string, now time.Time, reason string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions
SET status = 'terminated', updated_at = $3, terminated_at = $3, last_error = $4,
    identity_ref = '', instance_ref = ''
WHERE session_id = $1 AND tenant_id = $2 AND status <> 'terminated'`,
		sessionID, tenantID, now, reason)
	return err
}

func (s *PostgresStore) RenewLease(ctx context.Context, tenantID, sessionID string, until time.Time, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions SET lease_expires_at = $3, updated_at = $4
WHERE session_id = $1 AND tenant_id = $2 AND status IN ('connecting','connected')`,
		sessionID, tenantID, until, now)
	if err != nil {
		return err
	}
	return requirePostgresChange(res)
}

func (s *PostgresStore) SweepExpired(ctx context.Context, now time.Time) ([]string, error) {
	ids := []string{}
	rows, err := s.db.QueryContext(ctx, `
UPDATE gateway_voice_sessions
SET status = 'terminated', updated_at = $1, terminated_at = $1, last_error = 'lease_expired',
    identity_ref = '', instance_ref = ''
WHERE session_id IN (
    SELECT session_id FROM gateway_voice_sessions
    WHERE status IN ('connecting','connected') AND lease_expires_at <= $1
    FOR UPDATE SKIP LOCKED
)
RETURNING session_id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *PostgresStore) GlobalSessionCounts(ctx context.Context) (int, int, error) {
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

func (s *PostgresStore) ActiveCount(ctx context.Context, tenantID, target string) (int, error) {
	query := `
SELECT COUNT(1) FROM gateway_voice_sessions
WHERE tenant_id = $1 AND status IN ('queued','connecting','connected')`
	args := []any{tenantID}
	if target != "" {
		query += " AND target = $2"
		args = append(args, target)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func requirePostgresChange(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
