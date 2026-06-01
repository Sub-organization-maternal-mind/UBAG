package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const postgresCreateOutboxTable = `
CREATE TABLE IF NOT EXISTS gateway_outbox_events (
	id           TEXT PRIMARY KEY,
	topic        TEXT NOT NULL,
	payload      BYTEA NOT NULL,
	created_at   TIMESTAMPTZ NOT NULL,
	published_at TIMESTAMPTZ
)`

const postgresOutboxIndex = `
CREATE INDEX IF NOT EXISTS idx_gateway_outbox_pending
	ON gateway_outbox_events (created_at) WHERE published_at IS NULL`

type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, now: time.Now}
}

func (p *PostgresStore) Ready(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("outbox: postgres store is not configured")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	if _, err := p.db.ExecContext(ctx, postgresCreateOutboxTable); err != nil {
		return fmt.Errorf("outbox: create table: %w", err)
	}
	if _, err := p.db.ExecContext(ctx, postgresOutboxIndex); err != nil {
		return fmt.Errorf("outbox: create index: %w", err)
	}
	return nil
}

func (p *PostgresStore) Append(ctx context.Context, id, topic string, payload []byte) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("outbox: postgres store is not configured")
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO gateway_outbox_events (id, topic, payload, created_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (id) DO NOTHING`,
		id, topic, payload, p.now().UTC())
	if err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	return nil
}

func (p *PostgresStore) MarkPublished(ctx context.Context, id string) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("outbox: postgres store is not configured")
	}
	res, err := p.db.ExecContext(ctx,
		`UPDATE gateway_outbox_events SET published_at = $1 WHERE id = $2`,
		p.now().UTC(), id)
	if err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("outbox: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("outbox: event %q not found", id)
	}
	return nil
}

func (p *PostgresStore) Pending(ctx context.Context, limit int) ([]Event, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("outbox: postgres store is not configured")
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, topic, payload, created_at
		 FROM gateway_outbox_events
		 WHERE published_at IS NULL
		 ORDER BY created_at ASC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("outbox: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Topic, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("outbox: scan: %w", err)
		}
		e.CreatedAt = e.CreatedAt.UTC()
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox: rows: %w", err)
	}
	return events, nil
}
