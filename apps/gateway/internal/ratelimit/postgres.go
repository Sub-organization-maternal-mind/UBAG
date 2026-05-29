package ratelimit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// postgresCountersTable is the Postgres table holding fixed-window counters.
const postgresCountersTable = "gateway_rate_limit_counters"

// PostgresStore is a fixed-window counter store backed by Postgres. The atomic
// INSERT ... ON CONFLICT DO UPDATE ... RETURNING makes Increment safe under
// concurrency across multiple gateway nodes sharing one database.
type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewPostgresStore builds a store over db and ensures the counter table exists.
func NewPostgresStore(ctx context.Context, db *sql.DB) (*PostgresStore, error) {
	store := &PostgresStore{db: db, now: time.Now}
	if err := store.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// SetClock overrides the time source used for pruning. Intended for tests.
func (p *PostgresStore) SetClock(now func() time.Time) {
	if now != nil {
		p.now = now
	}
}

// EnsureSchema creates the counter table if it does not already exist.
func (p *PostgresStore) EnsureSchema(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("ratelimit: postgres store is not configured")
	}
	_, err := p.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS `+postgresCountersTable+` (
	rl_key       TEXT        NOT NULL,
	window_start TIMESTAMPTZ NOT NULL,
	counter      BIGINT      NOT NULL,
	updated_at   TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (rl_key, window_start)
)`)
	return err
}

// Increment adds cost to the (key, windowStart) counter and returns the new total.
func (p *PostgresStore) Increment(ctx context.Context, key string, windowStart time.Time, window time.Duration, cost int) (int, error) {
	if cost <= 0 {
		cost = 1
	}
	now := p.now().UTC()
	var total int64
	err := p.db.QueryRowContext(ctx, `
INSERT INTO `+postgresCountersTable+` (rl_key, window_start, counter, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (rl_key, window_start)
DO UPDATE SET counter = `+postgresCountersTable+`.counter + excluded.counter, updated_at = excluded.updated_at
RETURNING counter`,
		key, windowStart.UTC(), cost, now).Scan(&total)
	if err != nil {
		return 0, err
	}
	if window > 0 {
		p.prune(ctx, key, windowStart)
	}
	return int(total), nil
}

// Peek returns the current count for (key, windowStart) without mutating it.
func (p *PostgresStore) Peek(ctx context.Context, key string, windowStart time.Time) (int, error) {
	var total int64
	err := p.db.QueryRowContext(ctx, `SELECT counter FROM `+postgresCountersTable+` WHERE rl_key = $1 AND window_start = $2`,
		key, windowStart.UTC()).Scan(&total)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return int(total), nil
}

// prune deletes counters for key whose window started before the current one.
// It is best-effort; failures do not affect the increment result.
func (p *PostgresStore) prune(ctx context.Context, key string, currentWindowStart time.Time) {
	_, _ = p.db.ExecContext(ctx, `DELETE FROM `+postgresCountersTable+` WHERE rl_key = $1 AND window_start < $2`,
		key, currentWindowStart.UTC())
}
