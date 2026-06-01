package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const sqliteOutboxTimeLayout = "2006-01-02T15:04:05.000Z07:00"

const sqliteCreateOutboxTable = `
CREATE TABLE IF NOT EXISTS gateway_outbox_events (
	id           TEXT PRIMARY KEY,
	topic        TEXT NOT NULL,
	payload      BLOB NOT NULL,
	created_at   TEXT NOT NULL,
	published_at TEXT
)`

const sqliteOutboxIndex = `
CREATE INDEX IF NOT EXISTS idx_gateway_outbox_pending
	ON gateway_outbox_events (created_at) WHERE published_at IS NULL`

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
	if _, err := s.db.ExecContext(ctx, sqliteOutboxIndex); err != nil {
		return fmt.Errorf("outbox: create index: %w", err)
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

func (s *SQLiteStore) MarkPublished(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("outbox: sqlite store is not configured")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE gateway_outbox_events SET published_at = ? WHERE id = ?`,
		s.now().UTC().Format(sqliteOutboxTimeLayout), id)
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

func (s *SQLiteStore) Pending(ctx context.Context, limit int) ([]Event, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("outbox: sqlite store is not configured")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, topic, payload, created_at
		 FROM gateway_outbox_events
		 WHERE published_at IS NULL
		 ORDER BY created_at ASC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("outbox: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var createdAt string
		if err := rows.Scan(&e.ID, &e.Topic, &e.Payload, &createdAt); err != nil {
			return nil, fmt.Errorf("outbox: scan: %w", err)
		}
		e.CreatedAt = parseSQLiteOutboxTime(createdAt)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox: rows: %w", err)
	}
	return events, nil
}

func parseSQLiteOutboxTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{sqliteOutboxTimeLayout, time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
