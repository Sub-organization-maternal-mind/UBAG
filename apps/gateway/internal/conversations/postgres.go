package conversations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// PostgresStore is a Store backed by Postgres (github.com/jackc/pgx/v5/stdlib,
// driver "pgx"). Its schema is migration-driven
// (migrations/postgres/0010_conversations.sql); Ready asserts the table exists
// and never creates it. Bind upserts by the full conversation key via
// ON CONFLICT DO UPDATE.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore returns a Store over db.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

const pgConversationColumns = `
tenant_id, app_id, target, conversation_key, provider_thread_ref, state,
created_at, last_used_at, last_job_id`

func (s *PostgresStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("conversations: postgres store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	return requireConversationsObject(ctx, s.db, "gateway_conversations")
}

func (s *PostgresStore) Resolve(ctx context.Context, key Key) (Conversation, bool, error) {
	if s == nil || s.db == nil {
		return Conversation{}, false, fmt.Errorf("conversations: postgres store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+pgConversationColumns+`
FROM gateway_conversations
WHERE tenant_id = $1 AND app_id = $2 AND target = $3 AND conversation_key = $4 LIMIT 1`,
		key.TenantID, key.AppID, key.Target, key.ConversationKey)
	if err != nil {
		return Conversation{}, false, err
	}
	out, err := scanPostgresConversations(rows)
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
func (s *PostgresStore) Bind(ctx context.Context, conv Conversation) (Conversation, error) {
	if s == nil || s.db == nil {
		return Conversation{}, fmt.Errorf("conversations: postgres store is not configured")
	}
	prepareBind(&conv)
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO gateway_conversations (`+pgConversationColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tenant_id, app_id, target, conversation_key) DO UPDATE SET
	provider_thread_ref = excluded.provider_thread_ref,
	state = excluded.state,
	last_used_at = excluded.last_used_at,
	last_job_id = excluded.last_job_id`,
		conv.TenantID, conv.AppID, conv.Target, conv.ConversationKey,
		conv.ProviderThreadRef, conv.State, conv.CreatedAt, nullableTime(conv.LastUsedAt), conv.LastJobID); err != nil {
		return Conversation{}, fmt.Errorf("conversations: bind: %w", err)
	}
	got, found, err := s.Resolve(ctx, keyOf(conv))
	if err != nil {
		return Conversation{}, err
	}
	if !found {
		return conv, nil
	}
	return got, nil
}

func (s *PostgresStore) MarkBroken(ctx context.Context, key Key, at time.Time) (Conversation, bool, error) {
	if s == nil || s.db == nil {
		return Conversation{}, false, fmt.Errorf("conversations: postgres store is not configured")
	}
	query := `UPDATE gateway_conversations SET state = $1`
	args := []any{StateBroken}
	idx := 2
	if !at.IsZero() {
		query += fmt.Sprintf(`, last_used_at = $%d`, idx)
		args = append(args, at.UTC())
		idx++
	}
	query += fmt.Sprintf(` WHERE tenant_id = $%d AND app_id = $%d AND target = $%d AND conversation_key = $%d`, idx, idx+1, idx+2, idx+3)
	args = append(args, key.TenantID, key.AppID, key.Target, key.ConversationKey)
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return Conversation{}, false, err
	}
	return s.Resolve(ctx, key)
}

func (s *PostgresStore) Touch(ctx context.Context, key Key, jobID string, at time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("conversations: postgres store is not configured")
	}
	jobID = strings.TrimSpace(jobID)
	sets := []string{}
	args := []any{}
	idx := 1
	if jobID != "" {
		sets = append(sets, fmt.Sprintf("last_job_id = $%d", idx))
		args = append(args, jobID)
		idx++
	}
	if !at.IsZero() {
		sets = append(sets, fmt.Sprintf("last_used_at = $%d", idx))
		args = append(args, at.UTC())
		idx++
	}
	if len(sets) == 0 {
		return nil
	}
	query := `UPDATE gateway_conversations SET ` + strings.Join(sets, ", ") +
		fmt.Sprintf(` WHERE tenant_id = $%d AND app_id = $%d AND target = $%d AND conversation_key = $%d`, idx, idx+1, idx+2, idx+3)
	args = append(args, key.TenantID, key.AppID, key.Target, key.ConversationKey)
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *PostgresStore) List(ctx context.Context, filter Filter) ([]Conversation, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("conversations: postgres store is not configured")
	}
	query := `SELECT ` + pgConversationColumns + ` FROM gateway_conversations WHERE tenant_id = $1`
	args := []any{filter.TenantID}
	idx := 2
	if filter.AppID != "" {
		query += fmt.Sprintf(` AND app_id = $%d`, idx)
		args = append(args, filter.AppID)
		idx++
	}
	if filter.Target != "" {
		query += fmt.Sprintf(` AND target = $%d`, idx)
		args = append(args, filter.Target)
		idx++
	}
	query += ` ORDER BY last_used_at DESC NULLS LAST`
	if filter.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, idx)
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanPostgresConversations(rows)
}

func scanPostgresConversations(rows *sql.Rows) ([]Conversation, error) {
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var conv Conversation
		var lastUsedAt sql.NullTime
		if err := rows.Scan(&conv.TenantID, &conv.AppID, &conv.Target, &conv.ConversationKey,
			&conv.ProviderThreadRef, &conv.State, &conv.CreatedAt, &lastUsedAt, &conv.LastJobID); err != nil {
			return nil, err
		}
		conv.CreatedAt = conv.CreatedAt.UTC()
		if lastUsedAt.Valid {
			conv.LastUsedAt = lastUsedAt.Time.UTC()
		}
		out = append(out, conv)
	}
	return out, rows.Err()
}

// nullableTime maps a zero time to a SQL NULL so nullable TIMESTAMPTZ columns
// stay NULL rather than the Go zero instant.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func requireConversationsObject(ctx context.Context, db *sql.DB, objectName string) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, objectName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s is missing", objectName)
	}
	return nil
}
