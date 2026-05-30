package alerts

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PostgresStore is a Store backed by Postgres (github.com/jackc/pgx/v5/stdlib,
// driver "pgx"). Its schema is migration-driven
// (migrations/postgres/0007_alerts.sql). Active-alert dedupe runs under a
// transaction-scoped advisory lock keyed by (tenant, job, kind).
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore returns a Store over db.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

const pgAlertColumns = `
alert_id, tenant_id, app_id, job_id, session_id, target_id, kind, message, status,
created_at, notified_at, acked_at, resolved_at, attributes`

func (s *PostgresStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("alerts: postgres store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	return requireAlertsObject(ctx, s.db, "gateway_alerts")
}

func (s *PostgresStore) Raise(ctx context.Context, alert Alert) (Alert, bool, error) {
	if s == nil || s.db == nil {
		return Alert{}, false, fmt.Errorf("alerts: postgres store is not configured")
	}
	prepare(&alert)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Alert{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	// Serialise raises for this (tenant, job, kind) so the dedupe check and the
	// insert are atomic against concurrent workers.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, alert.TenantID+"|"+alert.JobID+"|"+alert.Kind); err != nil {
		return Alert{}, false, err
	}

	rows, err := tx.QueryContext(ctx, `
SELECT `+pgAlertColumns+`
FROM gateway_alerts
WHERE tenant_id = $1 AND job_id = $2 AND kind = $3 AND status IN ('open', 'notified', 'acknowledged')
ORDER BY created_at DESC LIMIT 1`, alert.TenantID, alert.JobID, alert.Kind)
	if err != nil {
		return Alert{}, false, err
	}
	existing, err := scanPostgresAlerts(rows)
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
INSERT INTO gateway_alerts (`+pgAlertColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL, NULL, NULL, $11)`,
		alert.AlertID, alert.TenantID, alert.AppID, alert.JobID, alert.SessionID, alert.TargetID,
		alert.Kind, alert.Message, alert.Status, alert.CreatedAt, attributesJSON); err != nil {
		return Alert{}, false, fmt.Errorf("alerts: insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Alert{}, false, err
	}
	return alert, true, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, alertID string) (Alert, bool, error) {
	if s == nil || s.db == nil {
		return Alert{}, false, fmt.Errorf("alerts: postgres store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+pgAlertColumns+`
FROM gateway_alerts WHERE tenant_id = $1 AND alert_id = $2 LIMIT 1`, tenantID, alertID)
	if err != nil {
		return Alert{}, false, err
	}
	out, err := scanPostgresAlerts(rows)
	if err != nil {
		return Alert{}, false, err
	}
	if len(out) == 0 {
		return Alert{}, false, nil
	}
	return out[0], true, nil
}

func (s *PostgresStore) UpdateStatus(ctx context.Context, tenantID, alertID, status string, at time.Time) (Alert, bool, error) {
	if s == nil || s.db == nil {
		return Alert{}, false, fmt.Errorf("alerts: postgres store is not configured")
	}
	column := statusTimestampColumn(status)
	query := `UPDATE gateway_alerts SET status = $1`
	args := []any{status}
	idx := 2
	if column != "" {
		query += fmt.Sprintf(`, %s = $%d`, column, idx)
		args = append(args, at.UTC())
		idx++
	}
	query += fmt.Sprintf(` WHERE tenant_id = $%d AND alert_id = $%d`, idx, idx+1)
	args = append(args, tenantID, alertID)
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return Alert{}, false, err
	}
	return s.Get(ctx, tenantID, alertID)
}

func (s *PostgresStore) List(ctx context.Context, filter Filter) ([]Alert, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("alerts: postgres store is not configured")
	}
	query := `SELECT ` + pgAlertColumns + ` FROM gateway_alerts WHERE tenant_id = $1`
	args := []any{filter.TenantID}
	idx := 2
	if filter.Status != "" {
		query += fmt.Sprintf(` AND status = $%d`, idx)
		args = append(args, filter.Status)
		idx++
	}
	query += ` ORDER BY created_at DESC`
	if filter.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, idx)
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanPostgresAlerts(rows)
}

func scanPostgresAlerts(rows *sql.Rows) ([]Alert, error) {
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var alert Alert
		var notifiedAt, ackedAt, resolvedAt sql.NullTime
		var attributes string
		if err := rows.Scan(&alert.AlertID, &alert.TenantID, &alert.AppID, &alert.JobID,
			&alert.SessionID, &alert.TargetID, &alert.Kind, &alert.Message, &alert.Status,
			&alert.CreatedAt, &notifiedAt, &ackedAt, &resolvedAt, &attributes); err != nil {
			return nil, err
		}
		alert.CreatedAt = alert.CreatedAt.UTC()
		if notifiedAt.Valid {
			alert.NotifiedAt = notifiedAt.Time.UTC()
		}
		if ackedAt.Valid {
			alert.AckedAt = ackedAt.Time.UTC()
		}
		if resolvedAt.Valid {
			alert.ResolvedAt = resolvedAt.Time.UTC()
		}
		alert.Attributes = decodeAttributes(attributes)
		out = append(out, alert)
	}
	return out, rows.Err()
}

func requireAlertsObject(ctx context.Context, db *sql.DB, objectName string) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, objectName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s is missing", objectName)
	}
	return nil
}
