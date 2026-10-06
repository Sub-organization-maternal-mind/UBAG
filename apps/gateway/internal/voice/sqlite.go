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
	mode             TEXT NOT NULL DEFAULT 'live',
	job_id           TEXT NOT NULL DEFAULT '',
	status           TEXT NOT NULL DEFAULT 'queued',
	muted            INTEGER NOT NULL DEFAULT 0,
	identity_ref     TEXT NOT NULL DEFAULT '',
	instance_ref     TEXT NOT NULL DEFAULT '',
	last_error       TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL,
	lease_expires_at TEXT NOT NULL DEFAULT '',
	terminated_at    TEXT NOT NULL DEFAULT '',
	node_id          TEXT NOT NULL DEFAULT '',
	lease_generation INTEGER NOT NULL DEFAULT 0,
	media_lease_expires_at TEXT NOT NULL DEFAULT '',
	terminating_until TEXT NOT NULL DEFAULT ''
)`

// sqliteLeaseColumns are the 0025 columns (see the Postgres migration for their
// meaning). A database created before them gets them added by Ready, since
// SQLite has no ADD COLUMN IF NOT EXISTS.
var sqliteLeaseColumns = []struct{ name, ddl string }{
	{"node_id", "node_id TEXT NOT NULL DEFAULT ''"},
	{"lease_generation", "lease_generation INTEGER NOT NULL DEFAULT 0"},
	{"media_lease_expires_at", "media_lease_expires_at TEXT NOT NULL DEFAULT ''"},
	{"terminating_until", "terminating_until TEXT NOT NULL DEFAULT ''"},
}

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
CREATE UNIQUE INDEX IF NOT EXISTS uq_voice_active_instance_global
	ON gateway_voice_sessions (instance_ref)
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
		// The original index was tenant-scoped; the environment is a physical
		// resource, so exclusivity is now global.
		"DROP INDEX IF EXISTS uq_voice_active_instance",
		sqliteCreateVoiceSessionsTable,
		sqliteCreateVoiceAccountIndex,
		sqliteCreateVoiceInstanceIndex,
		sqliteCreateVoiceTenantIndex,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	have := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('gateway_voice_sessions')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()
	for _, col := range sqliteLeaseColumns {
		if have[col.name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, "ALTER TABLE gateway_voice_sessions ADD COLUMN "+col.ddl); err != nil {
			return err
		}
	}
	return nil
}

// sqliteVoiceColumns are the columns an INSERT writes (the lease columns keep
// their defaults); sqliteVoiceSelect adds the internal lease state.
const sqliteVoiceColumns = `session_id, tenant_id, app_id, target, mode, job_id,
status, muted, identity_ref, instance_ref, last_error, created_at, updated_at,
lease_expires_at, terminated_at`

const sqliteVoiceSelect = sqliteVoiceColumns + `,
node_id, lease_generation, media_lease_expires_at, terminating_until`

// sqliteLiveOrHeld is the predicate for a row that holds its account and
// environment at the instant bound to the one `?` it contains: a live session,
// or a terminated one still inside its termination hold.
func sqliteLiveOrHeld(alias string) string {
	return "(" + alias + "status IN ('connecting','connected') OR (" + alias + "terminating_until <> '' AND " + alias + "terminating_until > ?))"
}

func scanSQLiteVoiceSession(row interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var muted int
	var generation int64
	var createdAt, updatedAt string
	var leaseExpires, terminatedAt, mediaLease, hold string
	if err := row.Scan(&s.ID, &s.TenantID, &s.AppID, &s.Target, &s.Mode, &s.JobID,
		&s.Status, &muted, &s.IdentityRef, &s.InstanceRef, &s.LastError,
		&createdAt, &updatedAt, &leaseExpires, &terminatedAt,
		&s.NodeID, &generation, &mediaLease, &hold); err != nil {
		return Session{}, err
	}
	s.Muted = muted != 0
	s.Status = Status(s.Status)
	s.LeaseGeneration = uint64(generation)
	_ = s.CreatedAt.UnmarshalText([]byte(createdAt))
	_ = s.UpdatedAt.UnmarshalText([]byte(updatedAt))
	if leaseExpires != "" {
		_ = s.LeaseExpires.UnmarshalText([]byte(leaseExpires))
	}
	if terminatedAt != "" {
		_ = s.TerminatedAt.UnmarshalText([]byte(terminatedAt))
	}
	if mediaLease != "" {
		_ = s.MediaLeaseExpires.UnmarshalText([]byte(mediaLease))
	}
	if hold != "" {
		_ = s.TerminatingUntil.UnmarshalText([]byte(hold))
	}
	return visible(s), nil
}

func formatSQLiteTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	// Fixed width so lexicographic order equals chronological order (the
	// sweeper and renew guards compare these strings in SQL).
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
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
	mode := req.Mode
	if strings.TrimSpace(mode) == "" {
		mode = ModeLive
	}
	status := StatusQueued
	identity, instance := "", ""
	if mode == ModeLive {
		var leased, queued int
		if err := tx.QueryRowContext(ctx, sqliteCountsQuery, req.TenantID).Scan(&leased, &queued); err != nil {
			return Session{}, err
		}
		if req.MaxActive <= 0 || leased < req.MaxActive {
			for _, p := range req.Placements {
				p, ok := p.clean()
				if !ok {
					continue
				}
				var taken int
				if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM gateway_voice_sessions
WHERE `+sqliteLiveOrHeld("")+`
  AND ((tenant_id = ? AND target = ? AND identity_ref = ?) OR instance_ref = ?)`,
					formatSQLiteTime(now), req.TenantID, req.Target, p.Identity, p.Instance).Scan(&taken); err != nil {
					return Session{}, err
				}
				if taken == 0 {
					identity, instance, status = p.Identity, p.Instance, StatusConnecting
					break
				}
			}
		}
		if identity == "" && req.MaxQueued > 0 && queued >= req.MaxQueued {
			return Session{}, ErrQueueFull
		}
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO gateway_voice_sessions (`+sqliteVoiceColumns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.SessionID, req.TenantID, req.AppID, req.Target, mode, req.JobID,
		string(status), 0, identity, instance, "",
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
		Mode: mode, JobID: req.JobID, Status: status, IdentityRef: identity, InstanceRef: instance,
		CreatedAt: now, UpdatedAt: now, LeaseExpires: now.Add(req.LeaseTTL),
	}, nil
}

const sqliteCountsQuery = `
SELECT
  COALESCE(SUM(status IN ('connecting','connected')), 0),
  COALESCE(SUM(status = 'queued'), 0)
FROM gateway_voice_sessions WHERE tenant_id = ? AND mode <> 'utterance'`

func (s *SQLiteStore) Claim(ctx context.Context, req ClaimRequest) (Session, error) {
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var target string
	if err := s.db.QueryRowContext(ctx,
		"SELECT target FROM gateway_voice_sessions WHERE session_id = ? AND tenant_id = ?",
		req.SessionID, req.TenantID).Scan(&target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, err
	}
	// Each attempt is ONE statement: the NOT EXISTS / budget guards inside the
	// UPDATE and the partial unique indexes behind them are the shared atomic
	// check, so racing replicas cannot both win.
	for _, p := range req.Placements {
		p, ok := p.clean()
		if !ok {
			continue
		}
		res, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions
SET status = 'connecting', identity_ref = ?, instance_ref = ?, lease_expires_at = ?, updated_at = ?
WHERE session_id = ? AND tenant_id = ? AND status = 'queued'
  AND (? <= 0 OR (
    SELECT COUNT(1) FROM gateway_voice_sessions c
    WHERE c.tenant_id = ? AND c.mode <> 'utterance' AND c.status IN ('connecting','connected')) < ?)
  AND NOT EXISTS (
    SELECT 1 FROM gateway_voice_sessions other
    WHERE `+sqliteLiveOrHeld("other.")+`
      AND other.session_id <> gateway_voice_sessions.session_id
      AND ((other.tenant_id = gateway_voice_sessions.tenant_id
            AND other.target = gateway_voice_sessions.target AND other.identity_ref = ?)
           OR other.instance_ref = ?)
  )`,
			p.Identity, p.Instance, formatSQLiteTime(now.Add(req.LeaseTTL)), formatSQLiteTime(now),
			req.SessionID, req.TenantID, req.MaxActive, req.TenantID, req.MaxActive,
			formatSQLiteTime(now), p.Identity, p.Instance)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "constraint") {
				continue // lease lost to a concurrent replica; try next pair
			}
			return Session{}, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return Session{}, err
		} else if n == 0 {
			continue // queued row moved, budget spent, or the pair was taken
		}
		sess, _, err := s.Get(ctx, req.TenantID, req.SessionID)
		return sess, err
	}
	return Session{}, ErrConflict
}

func (s *SQLiteStore) Get(ctx context.Context, tenantID, sessionID string) (Session, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT `+sqliteVoiceSelect+` FROM gateway_voice_sessions
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
	query := `SELECT ` + sqliteVoiceSelect + ` FROM gateway_voice_sessions WHERE tenant_id = ?`
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
	res, err := s.db.ExecContext(ctx, sqliteTransitionSQL+` AND status = ?`,
		string(to), formatSQLiteTime(now), lastError,
		string(to), string(to), string(to), formatSQLiteTime(now),
		sessionID, tenantID, string(from),
	)
	if err != nil {
		return err
	}
	return requireSQLiteChange(res)
}

const sqliteTransitionSQL = `
UPDATE gateway_voice_sessions
SET status = ?, updated_at = ?, last_error = ?,
    identity_ref = CASE WHEN ? = 'terminated' THEN '' ELSE identity_ref END,
    instance_ref = CASE WHEN ? = 'terminated' THEN '' ELSE instance_ref END,
    terminated_at = CASE WHEN ? = 'terminated' THEN ? ELSE terminated_at END
WHERE session_id = ? AND tenant_id = ?`

// CommitTransition is Transition fenced by (node, generation): the single
// UPDATE only matches the current holder, so a stale commit changes nothing.
func (s *SQLiteStore) CommitTransition(ctx context.Context, tenantID, sessionID string, fence LeaseFence, from, to Status, now time.Time, lastError string) error {
	if !fence.valid() {
		return ErrBadBinding
	}
	res, err := s.db.ExecContext(ctx, sqliteTransitionSQL+` AND status = ? AND node_id = ? AND lease_generation = ?`,
		string(to), formatSQLiteTime(now), lastError,
		string(to), string(to), string(to), formatSQLiteTime(now),
		sessionID, tenantID, string(from), fence.NodeID, int64(fence.Generation),
	)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		return nil
	}
	current, found, err := s.Get(ctx, tenantID, sessionID)
	if err != nil {
		return err
	}
	return fenceMiss(current, found, fence)
}

func (s *SQLiteStore) BeginTerminate(ctx context.Context, tenantID, sessionID string, now time.Time, reason string, hold time.Duration) error {
	if hold <= 0 {
		return s.Terminate(ctx, tenantID, sessionID, now, reason)
	}
	hold = min(hold, TerminatingHoldMax)
	// The refs stay: the row leaves the live-status indexes but the admission
	// checks count it until terminating_until (or ReleaseHold).
	res, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions
SET status = 'terminated', updated_at = ?, terminated_at = ?, last_error = ?, terminating_until = ?
WHERE session_id = ? AND tenant_id = ? AND status IN ('connecting','connected')`,
		formatSQLiteTime(now), formatSQLiteTime(now), reason, formatSQLiteTime(now.Add(hold)), sessionID, tenantID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		return nil
	}
	return s.Terminate(ctx, tenantID, sessionID, now, reason) // queued: nothing to hold
}

func (s *SQLiteStore) ReleaseHold(ctx context.Context, tenantID, sessionID string, fence LeaseFence, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions
SET identity_ref = '', instance_ref = '', terminating_until = '', updated_at = ?
WHERE session_id = ? AND tenant_id = ? AND status = 'terminated' AND terminating_until <> ''
  AND node_id = ? AND lease_generation = ?`,
		formatSQLiteTime(now), sessionID, tenantID, fence.NodeID, int64(fence.Generation))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		return nil
	}
	current, found, err := s.Get(ctx, tenantID, sessionID)
	if err != nil {
		return err
	}
	return holdMiss(current, found, fence)
}

func (s *SQLiteStore) BindNode(ctx context.Context, tenantID, sessionID, nodeID string, mediaTTL time.Duration, now time.Time) (Session, error) {
	if !validNodeID(nodeID) || mediaTTL <= 0 {
		return Session{}, ErrBadBinding
	}
	row := s.db.QueryRowContext(ctx, `
UPDATE gateway_voice_sessions
SET node_id = ?, lease_generation = lease_generation + 1, media_lease_expires_at = ?, updated_at = ?
WHERE session_id = ? AND tenant_id = ? AND status IN ('connecting','connected')
  AND lease_expires_at > ? AND lease_generation < ?
RETURNING `+sqliteVoiceSelect,
		nodeID, formatSQLiteTime(now.Add(mediaTTL)), formatSQLiteTime(now),
		sessionID, tenantID, formatSQLiteTime(now), int64(maxBoundGeneration))
	bound, err := scanSQLiteVoiceSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		current, found, getErr := s.Get(ctx, tenantID, sessionID)
		if getErr != nil {
			return Session{}, getErr
		}
		switch {
		case !found:
			return Session{}, ErrNotFound
		case current.LeaseGeneration >= maxBoundGeneration:
			return Session{}, ErrBadBinding
		}
		return Session{}, ErrConflict // not live, or its lease lapsed
	}
	return bound, err
}

func (s *SQLiteStore) RenewMediaLease(ctx context.Context, tenantID, sessionID string, fence LeaseFence, until time.Time, now time.Time) error {
	if !fence.valid() {
		return ErrBadBinding
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions SET media_lease_expires_at = ?, updated_at = ?
WHERE session_id = ? AND tenant_id = ? AND node_id = ? AND lease_generation = ?
  AND status IN ('connecting','connected') AND media_lease_expires_at > ?`,
		formatSQLiteTime(until), formatSQLiteTime(now), sessionID, tenantID,
		fence.NodeID, int64(fence.Generation), formatSQLiteTime(now))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		return nil
	}
	current, found, err := s.Get(ctx, tenantID, sessionID)
	if err != nil {
		return err
	}
	return fenceMiss(current, found, fence)
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
WHERE session_id = ? AND tenant_id = ? AND status IN ('connecting','connected')
  AND lease_expires_at > ?`,
		formatSQLiteTime(until), formatSQLiteTime(now), sessionID, tenantID, formatSQLiteTime(now))
	if err != nil {
		return err
	}
	return requireSQLiteChange(res)
}

func (s *SQLiteStore) SweepExpired(ctx context.Context, now time.Time) ([]string, error) {
	// One atomic statement: expiry and status are rechecked as the row is
	// terminated, so a concurrent renewal can never be swept. A node-bound
	// session also lapses when its media lease does (a dead node cannot renew);
	// the client lease reason wins when both lapsed.
	t := formatSQLiteTime(now)
	rows, err := s.db.QueryContext(ctx, `
UPDATE gateway_voice_sessions
SET status = 'terminated', updated_at = ?, terminated_at = ?,
    last_error = CASE WHEN lease_expires_at <> '' AND lease_expires_at <= ? THEN 'lease_expired' ELSE 'media_lease_expired' END,
    identity_ref = '', instance_ref = ''
WHERE status IN ('connecting','connected')
  AND ((lease_expires_at <> '' AND lease_expires_at <= ?) OR (node_id <> '' AND media_lease_expires_at <= ?))
RETURNING session_id`, t, t, t, t, t)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close() // release the connection before the next statement (MaxOpenConns may be 1)
	// Free the refs of terminations whose hold elapsed (admission already
	// ignores them; this just keeps the rows clean).
	_, err = s.db.ExecContext(ctx, `
UPDATE gateway_voice_sessions SET identity_ref = '', instance_ref = '', terminating_until = ''
WHERE status = 'terminated' AND terminating_until <> '' AND terminating_until <= ?`, t)
	return ids, err
}

func (s *SQLiteStore) ListLeaseHolders(ctx context.Context, now time.Time) ([]LeaseHolder, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT session_id, tenant_id, target, instance_ref FROM gateway_voice_sessions
WHERE mode <> 'utterance' AND instance_ref <> '' AND `+sqliteLiveOrHeld(""), formatSQLiteTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeaseHolder
	for rows.Next() {
		var h LeaseHolder
		if err := rows.Scan(&h.SessionID, &h.TenantID, &h.Target, &h.InstanceRef); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
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

func (s *SQLiteStore) TenantCounts(ctx context.Context, tenantID string) (int, int, error) {
	var leased, queued int
	err := s.db.QueryRowContext(ctx, sqliteCountsQuery, tenantID).Scan(&leased, &queued)
	return leased, queued, err
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
