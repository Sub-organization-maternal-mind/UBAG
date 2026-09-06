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
	created_at   TIMESTAMPTZ NOT NULL
)`

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
