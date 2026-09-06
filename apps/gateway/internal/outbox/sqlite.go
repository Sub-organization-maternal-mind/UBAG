package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const sqliteOutboxTimeLayout = "2006-01-02T15:04:05.000Z07:00"

const sqliteCreateOutboxTable = `
CREATE TABLE IF NOT EXISTS gateway_outbox_events (
	id           TEXT PRIMARY KEY,
	topic        TEXT NOT NULL,
	payload      BLOB NOT NULL,
	created_at   TEXT NOT NULL
)`

type SQLiteStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db, now: time.Now}
}

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("outbox: sqlite store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, sqliteCreateOutboxTable); err != nil {
		return fmt.Errorf("outbox: create table: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Append(ctx context.Context, id, topic string, payload []byte) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("outbox: sqlite store is not configured")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO gateway_outbox_events (id, topic, payload, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (id) DO NOTHING`,
		id, topic, payload, s.now().UTC().Format(sqliteOutboxTimeLayout))
	if err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	return nil
}
