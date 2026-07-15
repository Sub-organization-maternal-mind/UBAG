package conversations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SQLiteStore is a Store backed by SQLite via database/sql (driver "sqlite",
// modernc.org/sqlite). It owns its schema (CREATE TABLE/INDEX IF NOT EXISTS)
// and upserts by the full conversation key so a re-bind overwrites rather than
// duplicates (SQLite serialises writers).
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore constructs a SQLiteStore over db.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

const sqliteCreateConversationsTable = `
CREATE TABLE IF NOT EXISTS gateway_conversations (
	tenant_id TEXT NOT NULL,
	app_id TEXT NOT NULL,
	target TEXT NOT NULL,
	conversation_key TEXT NOT NULL,
	provider_thread_ref TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL DEFAULT 'active',
	created_at TEXT NOT NULL,
	last_used_at TEXT NOT NULL DEFAULT '',
	last_job_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (tenant_id, app_id, target, conversation_key)
)`

const sqliteCreateConversationsTenantIndex = `
CREATE INDEX IF NOT EXISTS idx_gateway_conversations_tenant_used
	ON gateway_conversations (tenant_id, last_used_at)`

const conversationColumns = `
tenant_id, app_id, target, conversation_key, provider_thread_ref, state,
created_at, last_used_at, last_job_id`

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("conversations: sqlite store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	for _, stmt := range []string{sqliteCreateConversationsTable, sqliteCreateConversationsTenantIndex} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) Resolve(ctx context.Context, key Key) (Conversation, bool, error) {
	if s == nil || s.db == nil {
		return Conversation{}, false, fmt.Errorf("conversations: sqlite store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+conversationColumns+`
FROM gateway_conversations
WHERE tenant_id = ? AND app_id = ? AND target = ? AND conversation_key = ? LIMIT 1`,
		key.TenantID, key.AppID, key.Target, key.ConversationKey)
	if err != nil {
		return Conversation{}, false, err
	}
	out, err := scanConversations(rows)
	if err != nil {
		return Conversation{}, false, err
	}
	if len(out) == 0 {
		return Conversation{}, false, nil
	}
	return out[0], true, nil
}

// Bind upserts by the full conversation key. A re-bind overwrites the thread
// ref, state, last-used time, and last job while preserving the original
// created_at (ON CONFLICT does not touch created_at).
func (s *SQLiteStore) Bind(ctx context.Context, conv Conversation) (Conversation, error) {
	if s == nil || s.db == nil {
		return Conversation{}, fmt.Errorf("conversations: sqlite store is not configured")
	}
	prepareBind(&conv)
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO gateway_conversations (`+conversationColumns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, app_id, target, conversation_key) DO UPDATE SET
	provider_thread_ref = excluded.provider_thread_ref,
	state = excluded.state,
	last_used_at = excluded.last_used_at,
	last_job_id = excluded.last_job_id`,
		conv.TenantID, conv.AppID, conv.Target, conv.ConversationKey,
		conv.ProviderThreadRef, conv.State, canonicalTime(conv.CreatedAt),
		canonicalTime(conv.LastUsedAt), conv.LastJobID); err != nil {
		return Conversation{}, fmt.Errorf("conversations: bind: %w", err)
	}
	// Re-read so the returned row reflects the persisted state (including the
	// preserved original created_at on an upsert).
	got, found, err := s.Resolve(ctx, keyOf(conv))
	if err != nil {
		return Conversation{}, err
	}
	if !found {
		return conv, nil
	}
	return got, nil
}

func (s *SQLiteStore) MarkBroken(ctx context.Context, key Key, at time.Time) (Conversation, bool, error) {
	if s == nil || s.db == nil {
		return Conversation{}, false, fmt.Errorf("conversations: sqlite store is not configured")
	}
	query := `UPDATE gateway_conversations SET state = ?`
	args := []any{StateBroken}
	if stamp := canonicalTime(at); stamp != "" {
		query += `, last_used_at = ?`
		args = append(args, stamp)
	}
	query += ` WHERE tenant_id = ? AND app_id = ? AND target = ? AND conversation_key = ?`
	args = append(args, key.TenantID, key.AppID, key.Target, key.ConversationKey)
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return Conversation{}, false, err
	}
	return s.Resolve(ctx, key)
}

func (s *SQLiteStore) Touch(ctx context.Context, key Key, jobID string, at time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("conversations: sqlite store is not configured")
	}
	jobID = strings.TrimSpace(jobID)
	sets := []string{}
	args := []any{}
	if jobID != "" {
		sets = append(sets, "last_job_id = ?")
		args = append(args, jobID)
	}
	if stamp := canonicalTime(at); stamp != "" {
		sets = append(sets, "last_used_at = ?")
		args = append(args, stamp)
	}
	if len(sets) == 0 {
		return nil
	}
	query := `UPDATE gateway_conversations SET ` + strings.Join(sets, ", ") +
		` WHERE tenant_id = ? AND app_id = ? AND target = ? AND conversation_key = ?`
	args = append(args, key.TenantID, key.AppID, key.Target, key.ConversationKey)
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *SQLiteStore) List(ctx context.Context, filter Filter) ([]Conversation, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("conversations: sqlite store is not configured")
	}
	query := `SELECT ` + conversationColumns + ` FROM gateway_conversations WHERE tenant_id = ?`
	args := []any{filter.TenantID}
	if filter.AppID != "" {
		query += ` AND app_id = ?`
		args = append(args, filter.AppID)
	}
	if filter.Target != "" {
		query += ` AND target = ?`
		args = append(args, filter.Target)
	}
	query += ` ORDER BY last_used_at DESC`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanConversations(rows)
}

// scanConversations reads conversation rows whose timestamps are stored as
// canonical TEXT (SQLite). It closes rows before returning.
func scanConversations(rows *sql.Rows) ([]Conversation, error) {
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var conv Conversation
		var createdAt, lastUsedAt string
		if err := rows.Scan(&conv.TenantID, &conv.AppID, &conv.Target, &conv.ConversationKey,
			&conv.ProviderThreadRef, &conv.State, &createdAt, &lastUsedAt, &conv.LastJobID); err != nil {
			return nil, err
		}
		var err error
		if conv.CreatedAt, err = parseCanonicalTime(createdAt); err != nil {
			return nil, err
		}
		if conv.LastUsedAt, err = parseCanonicalTime(lastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, conv)
	}
	return out, rows.Err()
}

// canonicalTime renders t as a microsecond-precision UTC RFC3339 string so it
// round-trips identically through Postgres TIMESTAMPTZ and SQLite TEXT and
// sorts lexicographically in chronological order. A zero time renders empty.
func canonicalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z07:00")
}

func parseCanonicalTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse("2006-01-02T15:04:05.000000Z07:00", value); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("conversations: parse time %q: %w", value, err)
	}
	return parsed.UTC(), nil
}
