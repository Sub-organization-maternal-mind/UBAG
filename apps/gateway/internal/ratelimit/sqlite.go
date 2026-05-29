package ratelimit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// sqliteTimeLayout stores window-start timestamps as fixed-width millisecond
// RFC3339 UTC strings so lexical ordering matches chronological ordering, the
// same convention used by the webhooks SQLite store.
const sqliteTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// sqliteCountersTable is the SQLite table holding fixed-window counters.
const sqliteCountersTable = "gateway_rate_limit_counters"

// SQLiteStore is a fixed-window counter store backed by SQLite. Counters are
// keyed by (rl_key, window_start); a new window naturally yields a new row, so
// counts reset across windows. Expired rows for a key are pruned on each write.
type SQLiteStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewSQLiteStore builds a store over db and ensures the counter table exists.
func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLiteStore, error) {
	store := &SQLiteStore{db: db, now: time.Now}
	if err := store.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// SetClock overrides the time source used for pruning. Intended for tests.
func (s *SQLiteStore) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// EnsureSchema creates the counter table if it does not already exist.
func (s *SQLiteStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("ratelimit: sqlite store is not configured")
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS `+sqliteCountersTable+` (
	rl_key       TEXT    NOT NULL,
	window_start TEXT    NOT NULL,
	counter      INTEGER NOT NULL,
	updated_at   TEXT    NOT NULL,
	PRIMARY KEY (rl_key, window_start)
)`)
	return err
}

// Increment adds cost to the (key, windowStart) counter and returns the new total.
func (s *SQLiteStore) Increment(ctx context.Context, key string, windowStart time.Time, window time.Duration, cost int) (int, error) {
	if cost <= 0 {
		cost = 1
	}
	now := s.now().UTC()
	windowText := formatSQLiteTime(windowStart)
	var total int
	err := s.db.QueryRowContext(ctx, `
INSERT INTO `+sqliteCountersTable+` (rl_key, window_start, counter, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (rl_key, window_start)
DO UPDATE SET counter = `+sqliteCountersTable+`.counter + excluded.counter, updated_at = excluded.updated_at
RETURNING counter`,
		key, windowText, cost, formatSQLiteTime(now)).Scan(&total)
	if err != nil {
		return 0, err
	}
	if window > 0 {
		s.prune(ctx, key, windowStart)
	}
	return total, nil
}

// Peek returns the current count for (key, windowStart) without mutating it.
func (s *SQLiteStore) Peek(ctx context.Context, key string, windowStart time.Time) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, `SELECT counter FROM `+sqliteCountersTable+` WHERE rl_key = ? AND window_start = ?`,
		key, formatSQLiteTime(windowStart)).Scan(&total)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return total, nil
}

// prune deletes counters for key whose window started before the current one.
// It is best-effort; failures do not affect the increment result.
func (s *SQLiteStore) prune(ctx context.Context, key string, currentWindowStart time.Time) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM `+sqliteCountersTable+` WHERE rl_key = ? AND window_start < ?`,
		key, formatSQLiteTime(currentWindowStart))
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}
