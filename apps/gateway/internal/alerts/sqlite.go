package alerts

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SQLiteStore is a Store backed by SQLite via database/sql (driver "sqlite",
// modernc.org/sqlite). It owns its schema (CREATE TABLE IF NOT EXISTS) and
// dedupes active alerts inside a transaction (SQLite serialises writers).
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore constructs a SQLiteStore over db.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

const sqliteCreateAlertsTable = `
CREATE TABLE IF NOT EXISTS gateway_alerts (
	alert_id TEXT PRIMARY KEY,
	tenant_id TEXT NOT NULL,
	app_id TEXT NOT NULL DEFAULT '',
	job_id TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	target_id TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL,
	message TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'open',
	created_at TEXT NOT NULL,
	notified_at TEXT NOT NULL DEFAULT '',
	acked_at TEXT NOT NULL DEFAULT '',
	resolved_at TEXT NOT NULL DEFAULT '',
	attributes TEXT NOT NULL DEFAULT '{}'
)`

const sqliteCreateAlertsIndexes = `
CREATE INDEX IF NOT EXISTS idx_gateway_alerts_tenant_created
	ON gateway_alerts (tenant_id, created_at)`

const sqliteCreateAlertsActiveIndex = `
CREATE INDEX IF NOT EXISTS idx_gateway_alerts_active
	ON gateway_alerts (tenant_id, job_id, kind, status)`

const alertColumns = `
alert_id, tenant_id, app_id, job_id, session_id, target_id, kind, message, status,
created_at, notified_at, acked_at, resolved_at, attributes`

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("alerts: sqlite store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	for _, stmt := range []string{sqliteCreateAlertsTable, sqliteCreateAlertsIndexes, sqliteCreateAlertsActiveIndex} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) Raise(ctx context.Context, alert Alert) (Alert, bool, error) {
	if s == nil || s.db == nil {
		return Alert{}, false, fmt.Errorf("alerts: sqlite store is not configured")
	}
	prepare(&alert)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Alert{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
SELECT `+alertColumns+`
FROM gateway_alerts
WHERE tenant_id = ? AND job_id = ? AND kind = ? AND status IN ('open', 'notified', 'acknowledged')
ORDER BY created_at DESC LIMIT 1`, alert.TenantID, alert.JobID, alert.Kind)
	if err != nil {
		return Alert{}, false, err
	}
	existing, err := scanAlerts(rows)
	if err != nil {
		return Alert{}, false, err
	}
	if len(existing) > 0 {
		return existing[0], false, nil
	}

	attributesJSON, err := canonicalAttributes(alert.Attributes)
	if err != nil {
		return Alert{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO gateway_alerts (`+alertColumns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		alert.AlertID, alert.TenantID, alert.AppID, alert.JobID, alert.SessionID, alert.TargetID,
		alert.Kind, alert.Message, alert.Status, canonicalTime(alert.CreatedAt),
		"", "", "", attributesJSON); err != nil {
		return Alert{}, false, fmt.Errorf("alerts: insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Alert{}, false, err
	}
	return alert, true, nil
}

func (s *SQLiteStore) Get(ctx context.Context, tenantID, alertID string) (Alert, bool, error) {
	if s == nil || s.db == nil {
		return Alert{}, false, fmt.Errorf("alerts: sqlite store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+alertColumns+`
FROM gateway_alerts WHERE tenant_id = ? AND alert_id = ? LIMIT 1`, tenantID, alertID)
	if err != nil {
		return Alert{}, false, err
	}
	out, err := scanAlerts(rows)
	if err != nil {
		return Alert{}, false, err
	}
	if len(out) == 0 {
		return Alert{}, false, nil
	}
	return out[0], true, nil
}

func (s *SQLiteStore) UpdateStatus(ctx context.Context, tenantID, alertID, status string, at time.Time) (Alert, bool, error) {
	if s == nil || s.db == nil {
		return Alert{}, false, fmt.Errorf("alerts: sqlite store is not configured")
	}
	column := statusTimestampColumn(status)
	stamp := canonicalTime(at)
	query := `UPDATE gateway_alerts SET status = ?`
	args := []any{status}
	if column != "" {
		query += `, ` + column + ` = ?`
		args = append(args, stamp)
	}
	query += ` WHERE tenant_id = ? AND alert_id = ?`
	args = append(args, tenantID, alertID)
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return Alert{}, false, err
	}
	return s.Get(ctx, tenantID, alertID)
}

func (s *SQLiteStore) List(ctx context.Context, filter Filter) ([]Alert, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("alerts: sqlite store is not configured")
	}
	query := `SELECT ` + alertColumns + ` FROM gateway_alerts WHERE tenant_id = ?`
	args := []any{filter.TenantID}
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	query += ` ORDER BY created_at DESC`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanAlerts(rows)
}

func statusTimestampColumn(status string) string {
	switch status {
	case StatusNotified:
		return "notified_at"
	case StatusAcknowledged:
		return "acked_at"
	case StatusResolved, StatusExpired:
		return "resolved_at"
	}
	return ""
}

// scanAlerts reads alert rows whose timestamps are stored as canonical TEXT
// (SQLite). It closes rows before returning.
func scanAlerts(rows *sql.Rows) ([]Alert, error) {
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var alert Alert
		var createdAt, notifiedAt, ackedAt, resolvedAt, attributes string
		if err := rows.Scan(&alert.AlertID, &alert.TenantID, &alert.AppID, &alert.JobID,
			&alert.SessionID, &alert.TargetID, &alert.Kind, &alert.Message, &alert.Status,
			&createdAt, &notifiedAt, &ackedAt, &resolvedAt, &attributes); err != nil {
			return nil, err
		}
		var err error
		if alert.CreatedAt, err = parseCanonicalTime(createdAt); err != nil {
			return nil, err
		}
		if alert.NotifiedAt, err = parseCanonicalTime(notifiedAt); err != nil {
			return nil, err
		}
		if alert.AckedAt, err = parseCanonicalTime(ackedAt); err != nil {
			return nil, err
		}
		if alert.ResolvedAt, err = parseCanonicalTime(resolvedAt); err != nil {
			return nil, err
		}
		alert.Attributes = decodeAttributes(attributes)
		out = append(out, alert)
	}
	return out, rows.Err()
}
