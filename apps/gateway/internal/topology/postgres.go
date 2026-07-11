package topology

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PostgresStore reads the gateway_browser_* tables via database/sql (driver
// "pgx", github.com/jackc/pgx/v5/stdlib). Its schema is migration-driven
// (migrations/postgres/0004_browser_topology.sql); Ready verifies the tables
// exist rather than creating them.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore returns a Store over db.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (p *PostgresStore) Ready(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("topology: postgres store is not configured")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return err
	}
	for _, objectName := range []string{"gateway_browser_instances", "gateway_provider_contexts", "gateway_browser_tabs"} {
		if err := requirePostgresObject(ctx, p.db, objectName); err != nil {
			return err
		}
	}
	return nil
}

func (p *PostgresStore) ListInstances(ctx context.Context, filter InstanceFilter) ([]BrowserInstance, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("topology: postgres store is not configured")
	}
	query := `
SELECT instance_id, worker_id, tenant_id, engine, remote_endpoint, state,
	context_count, tab_count, rss_bytes, created_at, recycle_at
FROM gateway_browser_instances WHERE tenant_id = $1`
	args := []any{filter.TenantID}
	idx := 2
	if filter.State != "" {
		query += fmt.Sprintf(` AND state = $%d`, idx)
		args = append(args, filter.State)
		idx++
	}
	query += ` ORDER BY instance_id ASC`
	if filter.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, idx)
		args = append(args, filter.Limit)
	}
	rows, err := p.db.QueryContext(ctx, query, args...)
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
			createdAt      time.Time
			recycleAt      sql.NullTime
		)
		if err := rows.Scan(&instance.InstanceID, &instance.WorkerID, &instance.TenantID, &instance.Engine,
			&remoteEndpoint, &instance.State, &instance.ContextCount, &instance.TabCount,
			&rssBytes, &createdAt, &recycleAt); err != nil {
			return nil, err
		}
		instance.RemoteEndpoint = remoteEndpoint.String
		instance.RSSBytes = nullInt64(rssBytes)
		instance.CreatedAt = createdAt.UTC()
		instance.RecycleAt = nullTime(recycleAt)
		out = append(out, instance)
	}
	return out, rows.Err()
}

func (p *PostgresStore) ListContexts(ctx context.Context, filter ContextFilter) ([]ProviderContext, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("topology: postgres store is not configured")
	}
	// storage_state_uri is never selected raw; only its presence is exposed.
	query := `
SELECT context_id, instance_id, tenant_id, target_id, identity_ref, login_state,
	conversation_model, fingerprint_id, proxy_id,
	(storage_state_uri IS NOT NULL AND storage_state_uri <> '') AS has_storage_state,
	max_tabs, created_at, last_health_at, recycle_at
FROM gateway_provider_contexts WHERE tenant_id = $1`
	args := []any{filter.TenantID}
	idx := 2
	if filter.InstanceID != "" {
		query += fmt.Sprintf(` AND instance_id = $%d`, idx)
		args = append(args, filter.InstanceID)
		idx++
	}
	query += ` ORDER BY context_id ASC`
	if filter.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, idx)
		args = append(args, filter.Limit)
	}
	rows, err := p.db.QueryContext(ctx, query, args...)
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
			createdAt     time.Time
			lastHealthAt  sql.NullTime
			recycleAt     sql.NullTime
		)
		if err := rows.Scan(&pc.ContextID, &pc.InstanceID, &pc.TenantID, &pc.TargetID, &pc.IdentityRef,
			&pc.LoginState, &pc.ConversationModel, &fingerprintID, &proxyID, &hasStorage,
			&pc.MaxTabs, &createdAt, &lastHealthAt, &recycleAt); err != nil {
			return nil, err
		}
		pc.FingerprintID = fingerprintID.String
		pc.ProxyID = proxyID.String
		pc.HasStorageState = hasStorage
		pc.CreatedAt = createdAt.UTC()
		pc.LastHealthAt = nullTime(lastHealthAt)
		pc.RecycleAt = nullTime(recycleAt)
		out = append(out, pc)
	}
	return out, rows.Err()
}

func (p *PostgresStore) ListTabs(ctx context.Context, filter TabFilter) ([]BrowserTab, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("topology: postgres store is not configured")
	}
	// Tabs have no tenant_id column; isolation is enforced by joining the parent
	// provider context and filtering on its tenant.
	query := `
SELECT t.tab_id, t.context_id, t.state, t.conversation_id, t.current_job_id,
	t.jobs_completed, t.rss_bytes, t.last_health_at, t.created_at, t.recycle_at
FROM gateway_browser_tabs t
JOIN gateway_provider_contexts c ON c.context_id = t.context_id
WHERE c.tenant_id = $1`
	args := []any{filter.TenantID}
	idx := 2
	if filter.ContextID != "" {
		query += fmt.Sprintf(` AND t.context_id = $%d`, idx)
		args = append(args, filter.ContextID)
		idx++
	}
	if filter.State != "" {
		query += fmt.Sprintf(` AND t.state = $%d`, idx)
		args = append(args, filter.State)
		idx++
	}
	query += ` ORDER BY t.tab_id ASC`
	if filter.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, idx)
		args = append(args, filter.Limit)
	}
	rows, err := p.db.QueryContext(ctx, query, args...)
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
			lastHealthAt   sql.NullTime
			createdAt      time.Time
			recycleAt      sql.NullTime
		)
		if err := rows.Scan(&tab.TabID, &tab.ContextID, &tab.State, &conversationID, &currentJobID,
			&tab.JobsCompleted, &rssBytes, &lastHealthAt, &createdAt, &recycleAt); err != nil {
			return nil, err
		}
		tab.ConversationID = conversationID.String
		tab.CurrentJobID = currentJobID.String
		tab.RSSBytes = nullInt64(rssBytes)
		tab.CreatedAt = createdAt.UTC()
		tab.LastHealthAt = nullTime(lastHealthAt)
		tab.RecycleAt = nullTime(recycleAt)
		out = append(out, tab)
	}
	return out, rows.Err()
}

// UpdateContextLoginState persists a worker-detected login state onto the
// provider context(s) for a (tenant, target) pair and returns the number of rows
// updated (0 when the target is not registered — a benign no-op, not an error).
// The write is tenant-scoped so a worker event can never mutate another tenant's
// topology. login_state has no CHECK constraint, so the caller owns the
// vocabulary (authenticated / login_required / unknown).
func (p *PostgresStore) UpdateContextLoginState(ctx context.Context, tenantID, targetID, loginState string, at time.Time) (int, error) {
	if p == nil || p.db == nil {
		return 0, fmt.Errorf("topology: postgres store is not configured")
	}
	result, err := p.db.ExecContext(ctx,
		`UPDATE gateway_provider_contexts SET login_state = $3, last_health_at = $4 WHERE tenant_id = $1 AND target_id = $2`,
		tenantID, targetID, loginState, at.UTC())
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (p *PostgresStore) Summary(ctx context.Context, tenantID string) (Summary, error) {
	if p == nil || p.db == nil {
		return Summary{}, fmt.Errorf("topology: postgres store is not configured")
	}
	summary := newSummary(tenantID)
	if err := scanCountsPG(ctx, p.db,
		`SELECT state, count(*) FROM gateway_browser_instances WHERE tenant_id = $1 GROUP BY state`,
		tenantID, summary.InstancesByState, &summary.TotalInstances); err != nil {
		return Summary{}, err
	}
	if err := scanCountsPG(ctx, p.db,
		`SELECT login_state, count(*) FROM gateway_provider_contexts WHERE tenant_id = $1 GROUP BY login_state`,
		tenantID, summary.ContextsByLoginState, &summary.TotalContexts); err != nil {
		return Summary{}, err
	}
	if err := scanCountsPG(ctx, p.db,
		`SELECT t.state, count(*) FROM gateway_browser_tabs t
JOIN gateway_provider_contexts c ON c.context_id = t.context_id
WHERE c.tenant_id = $1 GROUP BY t.state`,
		tenantID, summary.TabsByState, &summary.TotalTabs); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func scanCountsPG(ctx context.Context, db *sql.DB, query, tenantID string, into map[string]int, total *int) error {
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

func nullTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	v := value.Time.UTC()
	return &v
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
