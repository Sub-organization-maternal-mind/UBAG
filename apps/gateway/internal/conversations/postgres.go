package conversations

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/ubag/ubag/apps/gateway/internal/storekit"
	"strings"
	"time"
)

// PostgresStore is a Store backed by Postgres (github.com/jackc/pgx/v5/stdlib,
// driver "pgx"). Its schema is migration-driven
// (migrations/postgres/0010_conversations.sql plus the node_id/profile_ref
// columns from 0024_helper_profiles.sql); Ready asserts the table and those
// columns exist and never creates them. Bind upserts by the full conversation key via
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
created_at, last_used_at, last_job_id, node_id, profile_ref`

func (s *PostgresStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("conversations: postgres store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	if err := storekit.RequirePostgresObject(ctx, s.db, "gateway_conversations"); err != nil {
		return err
	}
	var columns int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'gateway_conversations'
  AND column_name IN ('node_id', 'profile_ref')`).Scan(&columns); err != nil {
		return err
	}
	if columns != 2 {
		return fmt.Errorf("conversations: gateway_conversations.node_id/profile_ref are missing; apply migrations/postgres/0024_helper_profiles.sql")
	}
	return nil
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
	if err := prepareBind(&conv); err != nil {
		return Conversation{}, err
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO gateway_conversations (`+pgConversationColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (tenant_id, app_id, target, conversation_key) DO UPDATE SET
	provider_thread_ref = excluded.provider_thread_ref,
	state = excluded.state,
	last_used_at = excluded.last_used_at,
	last_job_id = excluded.last_job_id,
	node_id = excluded.node_id,
	profile_ref = excluded.profile_ref`,
		conv.TenantID, conv.AppID, conv.Target, conv.ConversationKey,
		conv.ProviderThreadRef, conv.State, conv.CreatedAt, nullableTime(conv.LastUsedAt), conv.LastJobID,
		conv.NodeID, conv.ProfileRef); err != nil {
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

func (s *PostgresStore) MarkNodeBroken(ctx context.Context, nodeID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("conversations: postgres store is not configured")
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE gateway_conversations SET state = $1 WHERE node_id = $2 AND state = $3`,
		StateBroken, nodeID, StateActive)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
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
	// Postgres permits OFFSET without LIMIT, so each clause is emitted only
	// when actually requested (a negative LIMIT would error here).
	if filter.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, idx)
		args = append(args, filter.Limit)
		idx++
	}
	if filter.Offset > 0 {
		query += fmt.Sprintf(` OFFSET $%d`, idx)
		args = append(args, filter.Offset)
		idx++
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
			&conv.ProviderThreadRef, &conv.State, &conv.CreatedAt, &lastUsedAt, &conv.LastJobID,
			&conv.NodeID, &conv.ProfileRef); err != nil {
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
