package topology

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SQLiteStore reads the gateway_browser_* tables via database/sql (driver
// "sqlite", modernc.org/sqlite). The tables are owned by the migration set /
// embedded sqlitestore schema; Ready creates them idempotently
// (CREATE TABLE IF NOT EXISTS) so the store also works standalone in tests.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore constructs a SQLiteStore over db.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

const sqliteCreateTopologyTables = `
CREATE TABLE IF NOT EXISTS gateway_browser_instances (
	instance_id     TEXT PRIMARY KEY,
	worker_id       TEXT NOT NULL,
	tenant_id       TEXT NOT NULL,
	engine          TEXT NOT NULL DEFAULT 'chromium',
	remote_endpoint TEXT,
	state           TEXT NOT NULL DEFAULT 'starting',
	context_count   INTEGER NOT NULL DEFAULT 0,
	tab_count       INTEGER NOT NULL DEFAULT 0,
	rss_bytes       INTEGER,
	created_at      TEXT NOT NULL,
	recycle_at      TEXT
);
CREATE TABLE IF NOT EXISTS gateway_provider_contexts (
	context_id         TEXT PRIMARY KEY,
	instance_id        TEXT NOT NULL,
	tenant_id          TEXT NOT NULL,
	target_id          TEXT NOT NULL,
	identity_ref       TEXT NOT NULL,
	login_state        TEXT NOT NULL DEFAULT 'unknown',
	conversation_model TEXT NOT NULL DEFAULT 'url',
	fingerprint_id     TEXT,
	proxy_id           TEXT,
	storage_state_uri  TEXT,
	max_tabs           INTEGER NOT NULL DEFAULT 2,
	created_at         TEXT NOT NULL,
	last_health_at     TEXT,
	recycle_at         TEXT
);
CREATE TABLE IF NOT EXISTS gateway_browser_tabs (
	tab_id          TEXT PRIMARY KEY,
	context_id      TEXT NOT NULL,
	state           TEXT NOT NULL DEFAULT 'warming',
	conversation_id TEXT,
	current_job_id  TEXT,
	jobs_completed  INTEGER NOT NULL DEFAULT 0,
	rss_bytes       INTEGER,
	last_health_at  TEXT,
	created_at      TEXT NOT NULL,
	recycle_at      TEXT
);`

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("topology: sqlite store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, sqliteCreateTopologyTables)
	return err
}

func (s *SQLiteStore) ListInstances(ctx context.Context, filter InstanceFilter) ([]BrowserInstance, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("topology: sqlite store is not configured")
	}
	query := `
SELECT instance_id, worker_id, tenant_id, engine, remote_endpoint, state,
	context_count, tab_count, rss_bytes, created_at, recycle_at
FROM gateway_browser_instances WHERE tenant_id = ?`
	args := []any{filter.TenantID}
	if filter.State != "" {
		query += ` AND state = ?`
		args = append(args, filter.State)
	}
	query += ` ORDER BY instance_id ASC`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]BrowserInstance, 0)
	for rows.Next() {
		var (
			instance       BrowserInstance
			remoteEndpoint sql.NullString
			rssBytes       sql.NullInt64
			createdAt      string
			recycleAt      sql.NullString
		)
		if err := rows.Scan(&instance.InstanceID, &instance.WorkerID, &instance.TenantID, &instance.Engine,
			&remoteEndpoint, &instance.State, &instance.ContextCount, &instance.TabCount,
			&rssBytes, &createdAt, &recycleAt); err != nil {
			return nil, err
		}
		instance.RemoteEndpoint = remoteEndpoint.String
		instance.RSSBytes = nullInt64(rssBytes)
		if instance.CreatedAt, err = parseSQLiteTime(createdAt); err != nil {
			return nil, err
		}
		if instance.RecycleAt, err = parseSQLiteTimePtr(recycleAt); err != nil {
			return nil, err
		}
		out = append(out, instance)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListContexts(ctx context.Context, filter ContextFilter) ([]ProviderContext, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("topology: sqlite store is not configured")
	}
	// storage_state_uri is never selected raw; only its presence is exposed.
	query := `
SELECT context_id, instance_id, tenant_id, target_id, identity_ref, login_state,
	conversation_model, fingerprint_id, proxy_id,
	(storage_state_uri IS NOT NULL AND storage_state_uri <> '') AS has_storage_state,
	max_tabs, created_at, last_health_at, recycle_at
FROM gateway_provider_contexts WHERE tenant_id = ?`
	args := []any{filter.TenantID}
	if filter.InstanceID != "" {
		query += ` AND instance_id = ?`
		args = append(args, filter.InstanceID)
	}
	query += ` ORDER BY context_id ASC`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ProviderContext, 0)
	for rows.Next() {
		var (
			pc            ProviderContext
			fingerprintID sql.NullString
			proxyID       sql.NullString
			hasStorage    bool
			createdAt     string
			lastHealthAt  sql.NullString
			recycleAt     sql.NullString
		)
		if err := rows.Scan(&pc.ContextID, &pc.InstanceID, &pc.TenantID, &pc.TargetID, &pc.IdentityRef,
			&pc.LoginState, &pc.ConversationModel, &fingerprintID, &proxyID, &hasStorage,
			&pc.MaxTabs, &createdAt, &lastHealthAt, &recycleAt); err != nil {
			return nil, err
		}
		pc.FingerprintID = fingerprintID.String
		pc.ProxyID = proxyID.String
		pc.HasStorageState = hasStorage
		if pc.CreatedAt, err = parseSQLiteTime(createdAt); err != nil {
			return nil, err
		}
		if pc.LastHealthAt, err = parseSQLiteTimePtr(lastHealthAt); err != nil {
			return nil, err
		}
		if pc.RecycleAt, err = parseSQLiteTimePtr(recycleAt); err != nil {
			return nil, err
		}
		out = append(out, pc)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListTabs(ctx context.Context, filter TabFilter) ([]BrowserTab, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("topology: sqlite store is not configured")
	}
	// Tabs have no tenant_id column; isolation is enforced by joining the parent
	// provider context and filtering on its tenant.
	query := `
SELECT t.tab_id, t.context_id, t.state, t.conversation_id, t.current_job_id,
	t.jobs_completed, t.rss_bytes, t.last_health_at, t.created_at, t.recycle_at
FROM gateway_browser_tabs t
JOIN gateway_provider_contexts c ON c.context_id = t.context_id
WHERE c.tenant_id = ?`
	args := []any{filter.TenantID}
	if filter.ContextID != "" {
		query += ` AND t.context_id = ?`
		args = append(args, filter.ContextID)
	}
	if filter.State != "" {
		query += ` AND t.state = ?`
		args = append(args, filter.State)
	}
	query += ` ORDER BY t.tab_id ASC`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]BrowserTab, 0)
	for rows.Next() {
		var (
			tab            BrowserTab
			conversationID sql.NullString
			currentJobID   sql.NullString
			rssBytes       sql.NullInt64
			lastHealthAt   sql.NullString
			createdAt      string
			recycleAt      sql.NullString
		)
		if err := rows.Scan(&tab.TabID, &tab.ContextID, &tab.State, &conversationID, &currentJobID,
			&tab.JobsCompleted, &rssBytes, &lastHealthAt, &createdAt, &recycleAt); err != nil {
			return nil, err
		}
		tab.ConversationID = conversationID.String
		tab.CurrentJobID = currentJobID.String
		tab.RSSBytes = nullInt64(rssBytes)
		if tab.CreatedAt, err = parseSQLiteTime(createdAt); err != nil {
			return nil, err
		}
		if tab.LastHealthAt, err = parseSQLiteTimePtr(lastHealthAt); err != nil {
			return nil, err
		}
		if tab.RecycleAt, err = parseSQLiteTimePtr(recycleAt); err != nil {
			return nil, err
		}
		out = append(out, tab)
	}
	return out, rows.Err()
}

// UpdateContextLoginState persists a worker-detected login state onto the
// provider context(s) for a (tenant, target) pair and returns the number of rows
// updated (0 when the target is not registered — a benign no-op, not an error).
// The write is tenant-scoped so a worker event can never mutate another tenant's
// topology. Timestamps are stored as RFC3339Nano text to match parseSQLiteTime.
func (s *SQLiteStore) UpdateContextLoginState(ctx context.Context, tenantID, targetID, loginState string, at time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("topology: sqlite store is not configured")
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE gateway_provider_contexts SET login_state = ?, last_health_at = ? WHERE tenant_id = ? AND target_id = ?`,
		loginState, at.UTC().Format(time.RFC3339Nano), tenantID, targetID)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (s *SQLiteStore) Summary(ctx context.Context, tenantID string) (Summary, error) {
	if s == nil || s.db == nil {
		return Summary{}, fmt.Errorf("topology: sqlite store is not configured")
	}
	summary := newSummary(tenantID)
	if err := scanCounts(ctx, s.db,
		`SELECT state, count(*) FROM gateway_browser_instances WHERE tenant_id = ? GROUP BY state`,
		tenantID, summary.InstancesByState, &summary.TotalInstances); err != nil {
		return Summary{}, err
	}
	if err := scanCounts(ctx, s.db,
		`SELECT login_state, count(*) FROM gateway_provider_contexts WHERE tenant_id = ? GROUP BY login_state`,
		tenantID, summary.ContextsByLoginState, &summary.TotalContexts); err != nil {
		return Summary{}, err
	}
	if err := scanCounts(ctx, s.db,
		`SELECT t.state, count(*) FROM gateway_browser_tabs t
JOIN gateway_provider_contexts c ON c.context_id = t.context_id
WHERE c.tenant_id = ? GROUP BY t.state`,
		tenantID, summary.TabsByState, &summary.TotalTabs); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func scanCounts(ctx context.Context, db *sql.DB, query, tenantID string, into map[string]int, total *int) error {
	rows, err := db.QueryContext(ctx, query, tenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return err
		}
		into[state] = count
		*total += count
	}
	return rows.Err()
}

func nullInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}

func parseSQLiteTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("topology: parse time %q", value)
}

func parseSQLiteTimePtr(value sql.NullString) (*time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	parsed, err := parseSQLiteTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
