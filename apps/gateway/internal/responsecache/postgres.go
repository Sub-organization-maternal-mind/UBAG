package responsecache

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

var _ Store = (*PostgresStore)(nil)

// PostgresStore implements Store backed by Postgres (github.com/jackc/pgx/v5/stdlib,
// driver name "pgx"). Entries and per-scope hit/miss counters are persisted in two
// tables created by migrations/postgres/0005_enterprise_stores.sql. Expired rows
// are filtered on read and removed lazily. All timestamps are stored in UTC.
type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewPostgresStore returns a store bound to db. The schema is migration-driven;
// call Ready to verify the required tables exist before serving traffic.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, now: time.Now}
}

// WithClock overrides the clock used for expiry checks. Returns the receiver.
func (p *PostgresStore) WithClock(now func() time.Time) *PostgresStore {
	if now != nil {
		p.now = now
	}
	return p
}

// Ready pings the database and verifies the cache tables exist. It lets the
// gateway fail fast at boot when Postgres mode is selected but the migrations
// have not been applied.
func (p *PostgresStore) Ready(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("postgres response cache is not configured")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return err
	}
	for _, objectName := range []string{"gateway_response_cache", "gateway_response_cache_stats"} {
		if err := requirePostgresObject(ctx, p.db, objectName); err != nil {
			return err
		}
	}
	return nil
}

func (p *PostgresStore) Get(ctx context.Context, tenantID string, appID string, key string) (Entry, bool, error) {
	row := p.db.QueryRowContext(ctx, `SELECT cache_key, tenant_id, app_id, target, command, input_hash, value, created_at, expires_at
FROM gateway_response_cache WHERE tenant_id = $1 AND app_id = $2 AND cache_key = $3`, tenantID, appID, key)
	entry, found, err := scanPostgresEntry(row)
	if err != nil {
		return Entry{}, false, err
	}
	if found && p.isExpired(entry) {
		if _, delErr := p.db.ExecContext(ctx, `DELETE FROM gateway_response_cache WHERE tenant_id = $1 AND app_id = $2 AND cache_key = $3`, tenantID, appID, key); delErr != nil {
			return Entry{}, false, delErr
		}
		found = false
	}
	if err := p.recordAccess(ctx, tenantID, appID, found); err != nil {
		return Entry{}, false, err
	}
	if !found {
		return Entry{}, false, nil
	}
	return entry, true, nil
}

func (p *PostgresStore) Set(ctx context.Context, entry Entry) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO gateway_response_cache (
	cache_key, tenant_id, app_id, target, command, input_hash, value, created_at, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tenant_id, app_id, cache_key) DO UPDATE SET
	target = excluded.target,
	command = excluded.command,
	input_hash = excluded.input_hash,
	value = excluded.value,
	created_at = excluded.created_at,
	expires_at = excluded.expires_at`,
		entry.Key, entry.TenantID, entry.AppID, entry.Target, entry.Command, entry.InputHash,
		cloneBytes(entry.Value), entry.CreatedAt.UTC(), nullablePostgresTime(entry.ExpiresAt))
	if err != nil {
		return fmt.Errorf("response cache set: %w", err)
	}
	return nil
}

func (p *PostgresStore) Delete(ctx context.Context, tenantID string, appID string, key string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM gateway_response_cache WHERE tenant_id = $1 AND app_id = $2 AND cache_key = $3`, tenantID, appID, key)
	if err != nil {
		return fmt.Errorf("response cache delete: %w", err)
	}
	return nil
}

func (p *PostgresStore) Purge(ctx context.Context, tenantID string, appID string) (int, error) {
	result, err := p.db.ExecContext(ctx, `DELETE FROM gateway_response_cache WHERE tenant_id = $1 AND app_id = $2`, tenantID, appID)
	if err != nil {
		return 0, fmt.Errorf("response cache purge: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (p *PostgresStore) List(ctx context.Context, tenantID string, appID string, limit int) ([]Entry, error) {
	limit = normalizeLimit(limit)
	rows, err := p.db.QueryContext(ctx, `SELECT cache_key, tenant_id, app_id, target, command, input_hash, value, created_at, expires_at
FROM gateway_response_cache WHERE tenant_id = $1 AND app_id = $2 ORDER BY created_at DESC, cache_key ASC LIMIT $3`, tenantID, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]Entry, 0)
	var expired []string
	for rows.Next() {
		entry, _, scanErr := scanPostgresEntryFromRows(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if p.isExpired(entry) {
			expired = append(expired, entry.Key)
			continue
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, key := range expired {
		if _, err := p.db.ExecContext(ctx, `DELETE FROM gateway_response_cache WHERE tenant_id = $1 AND app_id = $2 AND cache_key = $3`, tenantID, appID, key); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (p *PostgresStore) Stats(ctx context.Context, tenantID string, appID string) (Stats, error) {
	var count int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_response_cache WHERE tenant_id = $1 AND app_id = $2 AND (expires_at IS NULL OR expires_at > $3)`,
		tenantID, appID, p.now().UTC()).Scan(&count); err != nil {
		return Stats{}, err
	}
	stats := Stats{Entries: count}
	row := p.db.QueryRowContext(ctx, `SELECT hits, misses FROM gateway_response_cache_stats WHERE tenant_id = $1 AND app_id = $2`, tenantID, appID)
	switch err := row.Scan(&stats.Hits, &stats.Misses); err {
	case nil, sql.ErrNoRows:
		return stats, nil
	default:
		return Stats{}, err
	}
}

func (p *PostgresStore) recordAccess(ctx context.Context, tenantID string, appID string, hit bool) error {
	column := "misses"
	if hit {
		column = "hits"
	}
	hits, misses := 0, 1
	if hit {
		hits, misses = 1, 0
	}
	_, err := p.db.ExecContext(ctx, `INSERT INTO gateway_response_cache_stats (tenant_id, app_id, hits, misses)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id, app_id) DO UPDATE SET `+column+` = gateway_response_cache_stats.`+column+` + 1`,
		tenantID, appID, hits, misses)
	return err
}

func (p *PostgresStore) isExpired(entry Entry) bool {
	if entry.ExpiresAt.IsZero() {
		return false
	}
	return !entry.ExpiresAt.After(p.now().UTC())
}

func scanPostgresEntry(row *sql.Row) (Entry, bool, error) {
	entry, err := scanPostgresEntryValue(row)
	if err == sql.ErrNoRows {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return entry, true, nil
}

func scanPostgresEntryFromRows(rows *sql.Rows) (Entry, bool, error) {
	entry, err := scanPostgresEntryValue(rows)
	if err != nil {
		return Entry{}, false, err
	}
	return entry, true, nil
}

type cacheRowScanner interface {
	Scan(dest ...any) error
}

func scanPostgresEntryValue(row cacheRowScanner) (Entry, error) {
	var (
		entry     Entry
		value     []byte
		expiresAt sql.NullTime
	)
	if err := row.Scan(&entry.Key, &entry.TenantID, &entry.AppID, &entry.Target, &entry.Command, &entry.InputHash, &value, &entry.CreatedAt, &expiresAt); err != nil {
		return Entry{}, err
	}
	entry.Value = value
	entry.CreatedAt = entry.CreatedAt.UTC()
	if expiresAt.Valid {
		entry.ExpiresAt = expiresAt.Time.UTC()
	}
	return entry, nil
}

func nullablePostgresTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
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
