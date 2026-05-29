package responsecache

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// sqliteTimeLayout stores timestamps as fixed-width millisecond RFC3339 UTC
// strings so lexical ordering matches chronological ordering in SQLite.
const sqliteTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// SQLiteStore implements Store backed by SQLite. Entries and per-scope hit/miss
// counters are persisted in two tables. Expired rows are filtered on read and
// removed lazily. All timestamps are stored and returned in UTC.
type SQLiteStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewSQLiteStore returns a store bound to db. Callers should invoke EnsureSchema
// (also called by the constructor helper below) before use.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db, now: time.Now}
}

// WithClock overrides the clock used for expiry checks. Returns the receiver.
func (s *SQLiteStore) WithClock(now func() time.Time) *SQLiteStore {
	if now != nil {
		s.now = now
	}
	return s
}

// EnsureSchema creates the cache tables if they do not already exist. It is
// idempotent and safe to call repeatedly.
func (s *SQLiteStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite response cache is not configured")
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS gateway_response_cache (
	cache_key TEXT NOT NULL,
	tenant_id TEXT NOT NULL,
	app_id TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	command TEXT NOT NULL DEFAULT '',
	input_hash TEXT NOT NULL DEFAULT '',
	value BLOB,
	created_at TEXT NOT NULL,
	expires_at TEXT,
	PRIMARY KEY (tenant_id, app_id, cache_key)
)`,
		`CREATE TABLE IF NOT EXISTS gateway_response_cache_stats (
	tenant_id TEXT NOT NULL,
	app_id TEXT NOT NULL,
	hits INTEGER NOT NULL DEFAULT 0,
	misses INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (tenant_id, app_id)
)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) Get(ctx context.Context, tenantID string, appID string, key string) (Entry, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT cache_key, tenant_id, app_id, target, command, input_hash, value, created_at, expires_at
FROM gateway_response_cache WHERE tenant_id = ? AND app_id = ? AND cache_key = ?`, tenantID, appID, key)
	entry, found, err := scanSQLiteEntry(row)
	if err != nil {
		return Entry{}, false, err
	}
	if found && s.isExpired(entry) {
		if _, delErr := s.db.ExecContext(ctx, `DELETE FROM gateway_response_cache WHERE tenant_id = ? AND app_id = ? AND cache_key = ?`, tenantID, appID, key); delErr != nil {
			return Entry{}, false, delErr
		}
		found = false
	}
	if err := s.recordAccess(ctx, tenantID, appID, found); err != nil {
		return Entry{}, false, err
	}
	if !found {
		return Entry{}, false, nil
	}
	return entry, true, nil
}

func (s *SQLiteStore) Set(ctx context.Context, entry Entry) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO gateway_response_cache (
	cache_key, tenant_id, app_id, target, command, input_hash, value, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, app_id, cache_key) DO UPDATE SET
	target = excluded.target,
	command = excluded.command,
	input_hash = excluded.input_hash,
	value = excluded.value,
	created_at = excluded.created_at,
	expires_at = excluded.expires_at`,
		entry.Key, entry.TenantID, entry.AppID, entry.Target, entry.Command, entry.InputHash,
		cloneBytes(entry.Value), formatSQLiteTime(entry.CreatedAt), formatNullableSQLiteTime(entry.ExpiresAt))
	return err
}

func (s *SQLiteStore) Delete(ctx context.Context, tenantID string, appID string, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM gateway_response_cache WHERE tenant_id = ? AND app_id = ? AND cache_key = ?`, tenantID, appID, key)
	return err
}

func (s *SQLiteStore) Purge(ctx context.Context, tenantID string, appID string) (int, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM gateway_response_cache WHERE tenant_id = ? AND app_id = ?`, tenantID, appID)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (s *SQLiteStore) List(ctx context.Context, tenantID string, appID string, limit int) ([]Entry, error) {
	limit = normalizeLimit(limit)
	rows, err := s.db.QueryContext(ctx, `SELECT cache_key, tenant_id, app_id, target, command, input_hash, value, created_at, expires_at
FROM gateway_response_cache WHERE tenant_id = ? AND app_id = ? ORDER BY created_at DESC, cache_key ASC LIMIT ?`, tenantID, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]Entry, 0)
	var expired []string
	for rows.Next() {
		entry, _, scanErr := scanSQLiteEntryFromRows(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if s.isExpired(entry) {
			expired = append(expired, entry.Key)
			continue
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, key := range expired {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM gateway_response_cache WHERE tenant_id = ? AND app_id = ? AND cache_key = ?`, tenantID, appID, key); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (s *SQLiteStore) Stats(ctx context.Context, tenantID string, appID string) (Stats, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_response_cache WHERE tenant_id = ? AND app_id = ? AND (expires_at IS NULL OR expires_at > ?)`,
		tenantID, appID, formatSQLiteTime(s.now().UTC())).Scan(&count); err != nil {
		return Stats{}, err
	}
	stats := Stats{Entries: count}
	row := s.db.QueryRowContext(ctx, `SELECT hits, misses FROM gateway_response_cache_stats WHERE tenant_id = ? AND app_id = ?`, tenantID, appID)
	switch err := row.Scan(&stats.Hits, &stats.Misses); err {
	case nil, sql.ErrNoRows:
		return stats, nil
	default:
		return Stats{}, err
	}
}

func (s *SQLiteStore) recordAccess(ctx context.Context, tenantID string, appID string, hit bool) error {
	column := "misses"
	if hit {
		column = "hits"
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO gateway_response_cache_stats (tenant_id, app_id, hits, misses)
VALUES (?, ?, %s, %s)
ON CONFLICT (tenant_id, app_id) DO UPDATE SET %s = %s + 1`,
		boolToCount(hit), boolToCount(!hit), column, column), tenantID, appID)
	return err
}

func boolToCount(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func (s *SQLiteStore) isExpired(entry Entry) bool {
	if entry.ExpiresAt.IsZero() {
		return false
	}
	return !entry.ExpiresAt.After(s.now().UTC())
}

func scanSQLiteEntry(row *sql.Row) (Entry, bool, error) {
	var (
		entry     Entry
		value     []byte
		createdAt string
		expiresAt sql.NullString
	)
	err := row.Scan(&entry.Key, &entry.TenantID, &entry.AppID, &entry.Target, &entry.Command, &entry.InputHash, &value, &createdAt, &expiresAt)
	if err == sql.ErrNoRows {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	entry.Value = value
	entry.CreatedAt = parseSQLiteTime(createdAt)
	entry.ExpiresAt = parseNullableSQLiteTime(expiresAt)
	return entry, true, nil
}

func scanSQLiteEntryFromRows(rows *sql.Rows) (Entry, bool, error) {
	var (
		entry     Entry
		value     []byte
		createdAt string
		expiresAt sql.NullString
	)
	if err := rows.Scan(&entry.Key, &entry.TenantID, &entry.AppID, &entry.Target, &entry.Command, &entry.InputHash, &value, &createdAt, &expiresAt); err != nil {
		return Entry{}, false, err
	}
	entry.Value = value
	entry.CreatedAt = parseSQLiteTime(createdAt)
	entry.ExpiresAt = parseNullableSQLiteTime(expiresAt)
	return entry, true, nil
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}

func formatNullableSQLiteTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatSQLiteTime(t)
}

func parseSQLiteTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{sqliteTimeLayout, time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func parseNullableSQLiteTime(value sql.NullString) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return parseSQLiteTime(value.String)
}
