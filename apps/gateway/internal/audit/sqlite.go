package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SQLiteStore is a Store backed by SQLite via database/sql (driver "sqlite",
// modernc.org/sqlite). It owns its schema (CREATE TABLE IF NOT EXISTS) and
// assigns each record's per-tenant Seq and PrevHash inside a transaction so the
// chain stays intact under concurrent appends (SQLite serialises writers).
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore constructs a SQLiteStore over db.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

const sqliteCreateAuditTable = `
CREATE TABLE IF NOT EXISTS gateway_audit_log (
	id TEXT PRIMARY KEY,
	seq INTEGER NOT NULL,
	tenant_id TEXT NOT NULL,
	app_id TEXT NOT NULL DEFAULT '',
	actor TEXT NOT NULL DEFAULT '',
	action TEXT NOT NULL,
	resource TEXT NOT NULL DEFAULT '',
	outcome TEXT NOT NULL DEFAULT '',
	occurred_at TEXT NOT NULL,
	attributes TEXT NOT NULL DEFAULT '{}',
	prev_hash TEXT NOT NULL DEFAULT '',
	record_hash TEXT NOT NULL,
	UNIQUE (tenant_id, seq)
)`

const sqliteCreateAuditIndexes = `
CREATE INDEX IF NOT EXISTS idx_gateway_audit_log_tenant_occurred
	ON gateway_audit_log (tenant_id, occurred_at)`

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("audit: sqlite store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, sqliteCreateAuditTable); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, sqliteCreateAuditIndexes)
	return err
}

func (s *SQLiteStore) Append(ctx context.Context, rec Record) (Record, error) {
	if s == nil || s.db == nil {
		return Record{}, fmt.Errorf("audit: sqlite store is not configured")
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

	var maxSeq sql.NullInt64
	var prevHash sql.NullString
	row := tx.QueryRowContext(ctx, `
SELECT seq, record_hash FROM gateway_audit_log
WHERE tenant_id = ? ORDER BY seq DESC LIMIT 1`, rec.TenantID)
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
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Seq, rec.TenantID, rec.AppID, rec.Actor, rec.Action, rec.Resource, rec.Outcome,
		occurredAt, rec.attributesJSON, rec.PrevHash, rec.RecordHash); err != nil {
		return Record{}, fmt.Errorf("audit: insert record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func (s *SQLiteStore) List(ctx context.Context, filter Filter) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("audit: sqlite store is not configured")
	}
	query := `
SELECT id, seq, tenant_id, app_id, actor, action, resource, outcome, occurred_at, attributes, prev_hash, record_hash
FROM gateway_audit_log WHERE tenant_id = ?`
	args := []any{filter.TenantID}
	if !filter.Since.IsZero() {
		query += ` AND occurred_at >= ?`
		args = append(args, canonicalTime(filter.Since))
	}
	if !filter.Until.IsZero() {
		query += ` AND occurred_at < ?`
		args = append(args, canonicalTime(filter.Until))
	}
	query += ` ORDER BY seq ASC`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

func (s *SQLiteStore) Head(ctx context.Context, tenantID string) (string, int64, error) {
	if s == nil || s.db == nil {
		return GenesisHash, 0, fmt.Errorf("audit: sqlite store is not configured")
	}
	var seq sql.NullInt64
	var hash sql.NullString
	row := s.db.QueryRowContext(ctx, `
SELECT seq, record_hash FROM gateway_audit_log
WHERE tenant_id = ? ORDER BY seq DESC LIMIT 1`, tenantID)
	if err := row.Scan(&seq, &hash); err != nil {
		if err == sql.ErrNoRows {
			return GenesisHash, 0, nil
		}
		return GenesisHash, 0, err
	}
	return hash.String, seq.Int64, nil
}

// scanRecords reads audit rows, parsing occurred_at and recording the persisted
// attributes JSON so VerifyChain recomputes hashes from the exact stored bytes.
func scanRecords(rows *sql.Rows) ([]Record, error) {
	out := []Record{}
	for rows.Next() {
		var rec Record
		var occurredAt string
		var attributes string
		if err := rows.Scan(&rec.ID, &rec.Seq, &rec.TenantID, &rec.AppID, &rec.Actor, &rec.Action,
			&rec.Resource, &rec.Outcome, &occurredAt, &attributes, &rec.PrevHash, &rec.RecordHash); err != nil {
			return nil, err
		}
		parsed, err := time.Parse("2006-01-02T15:04:05.000000Z07:00", occurredAt)
		if err != nil {
			// Tolerate values written by other RFC3339 producers.
			if parsed, err = time.Parse(time.RFC3339Nano, occurredAt); err != nil {
				return nil, fmt.Errorf("audit: parse occurred_at %q: %w", occurredAt, err)
			}
		}
		rec.OccurredAt = parsed.UTC()
		rec.attributesJSON = attributes
		if attributes != "" && attributes != "{}" {
			rec.Attributes = decodeAttributes(attributes)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
