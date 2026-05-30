package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PostgresStore is a Store backed by Postgres (github.com/jackc/pgx/v5/stdlib,
// driver "pgx"). Its schema is migration-driven
// (migrations/postgres/0006_audit_sessions.sql). Per-tenant Seq and PrevHash
// are assigned under a transaction-scoped advisory lock keyed by tenant so the
// chain stays intact under concurrent appends.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore returns a Store over db.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("audit: postgres store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	return requirePostgresObject(ctx, s.db, "gateway_audit_log")
}

func (s *PostgresStore) Append(ctx context.Context, rec Record) (Record, error) {
	if s == nil || s.db == nil {
		return Record{}, fmt.Errorf("audit: postgres store is not configured")
	}
	occurredAt, err := prepare(&rec)
	if err != nil {
		return Record{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// Serialise appends for this tenant for the duration of the transaction.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, rec.TenantID); err != nil {
		return Record{}, err
	}

	var maxSeq sql.NullInt64
	var prevHash sql.NullString
	row := tx.QueryRowContext(ctx, `
SELECT seq, record_hash FROM gateway_audit_log
WHERE tenant_id = $1 ORDER BY seq DESC LIMIT 1`, rec.TenantID)
	if err := row.Scan(&maxSeq, &prevHash); err != nil && err != sql.ErrNoRows {
		return Record{}, err
	}

	rec.PrevHash = GenesisHash
	if prevHash.Valid {
		rec.PrevHash = prevHash.String
	}
	rec.Seq = 1
	if maxSeq.Valid {
		rec.Seq = maxSeq.Int64 + 1
	}
	rec.RecordHash = computeHash(rec.TenantID, rec.AppID, rec.Actor, rec.Action, rec.Resource, rec.Outcome, occurredAt, rec.attributesJSON, rec.PrevHash)
	rec.ID = stableID("audit", rec.TenantID, rec.Seq, rec.RecordHash)

	if _, err := tx.ExecContext(ctx, `
INSERT INTO gateway_audit_log (
	id, seq, tenant_id, app_id, actor, action, resource, outcome, occurred_at, attributes, prev_hash, record_hash
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		rec.ID, rec.Seq, rec.TenantID, rec.AppID, rec.Actor, rec.Action, rec.Resource, rec.Outcome,
		rec.OccurredAt, rec.attributesJSON, rec.PrevHash, rec.RecordHash); err != nil {
		return Record{}, fmt.Errorf("audit: insert record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func (s *PostgresStore) List(ctx context.Context, filter Filter) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("audit: postgres store is not configured")
	}
	query := `
SELECT id, seq, tenant_id, app_id, actor, action, resource, outcome, occurred_at, attributes, prev_hash, record_hash
FROM gateway_audit_log WHERE tenant_id = $1`
	args := []any{filter.TenantID}
	idx := 2
	if !filter.Since.IsZero() {
		query += fmt.Sprintf(` AND occurred_at >= $%d`, idx)
		args = append(args, filter.Since.UTC())
		idx++
	}
	if !filter.Until.IsZero() {
		query += fmt.Sprintf(` AND occurred_at < $%d`, idx)
		args = append(args, filter.Until.UTC())
		idx++
	}
	query += ` ORDER BY seq ASC`
	if filter.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, idx)
		args = append(args, filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPostgresRecords(rows)
}

func (s *PostgresStore) Head(ctx context.Context, tenantID string) (string, int64, error) {
	if s == nil || s.db == nil {
		return GenesisHash, 0, fmt.Errorf("audit: postgres store is not configured")
	}
	var seq sql.NullInt64
	var hash sql.NullString
	row := s.db.QueryRowContext(ctx, `
SELECT seq, record_hash FROM gateway_audit_log
WHERE tenant_id = $1 ORDER BY seq DESC LIMIT 1`, tenantID)
	if err := row.Scan(&seq, &hash); err != nil {
		if err == sql.ErrNoRows {
			return GenesisHash, 0, nil
		}
		return GenesisHash, 0, err
	}
	return hash.String, seq.Int64, nil
}

func scanPostgresRecords(rows *sql.Rows) ([]Record, error) {
	out := []Record{}
	for rows.Next() {
		var rec Record
		var occurredAt time.Time
		var attributes string
		if err := rows.Scan(&rec.ID, &rec.Seq, &rec.TenantID, &rec.AppID, &rec.Actor, &rec.Action,
			&rec.Resource, &rec.Outcome, &occurredAt, &attributes, &rec.PrevHash, &rec.RecordHash); err != nil {
			return nil, err
		}
		rec.OccurredAt = occurredAt.UTC()
		rec.attributesJSON = attributes
		if attributes != "" && attributes != "{}" {
			rec.Attributes = decodeAttributes(attributes)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func requirePostgresObject(ctx context.Context, db *sql.DB, objectName string) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, objectName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s is missing", objectName)
	}
	return nil
}
