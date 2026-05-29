package idempotency

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// sqliteTimeLayout stores timestamps as fixed-width millisecond RFC3339 UTC
// strings so lexical ordering matches chronological ordering in SQLite.
const sqliteTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// SQLiteStore implements the idempotency Service backed by SQLite. It mirrors
// PostgresStore exactly using the gateway_idempotency_records table.
type SQLiteStore struct {
	db  *sql.DB
	ttl time.Duration
	now func() time.Time
}

func NewSQLiteStore(db *sql.DB, ttl time.Duration) *SQLiteStore {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &SQLiteStore{
		db:  db,
		ttl: ttl,
		now: time.Now,
	}
}

func (s *SQLiteStore) Reserve(ctx context.Context, scope Scope, requestHash string) (Decision, error) {
	if s == nil || s.db == nil {
		return Decision{}, fmt.Errorf("sqlite idempotency store is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Decision{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC()
	expiresAt := now.Add(s.ttl)
	result, err := tx.ExecContext(ctx, `
INSERT INTO gateway_idempotency_records (
	tenant_id, app_id, operation, idempotency_key, request_hash, created_at, updated_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING`,
		scope.TenantID, scope.AppID, scope.Operation, scope.Key, requestHash, formatSQLiteTime(now), formatSQLiteTime(now), formatSQLiteTime(expiresAt))
	if err != nil {
		return Decision{}, err
	}
	if inserted, _ := result.RowsAffected(); inserted == 1 {
		record := Record{Scope: scope, RequestHash: requestHash, CreatedAt: now, UpdatedAt: now, ExpiresAt: expiresAt}
		if err := tx.Commit(); err != nil {
			return Decision{}, err
		}
		return Decision{Kind: DecisionReserved, Record: record}, nil
	}

	record, found, err := s.loadForUpdate(ctx, tx, scope)
	if err != nil {
		return Decision{}, err
	}
	if !found || !record.ExpiresAt.After(now) {
		record = Record{Scope: scope, RequestHash: requestHash, CreatedAt: now, UpdatedAt: now, ExpiresAt: expiresAt}
		_, err := tx.ExecContext(ctx, `
INSERT INTO gateway_idempotency_records (
	tenant_id, app_id, operation, idempotency_key, request_hash, resource_id, http_status, created_at, updated_at, expires_at
) VALUES (?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?)
ON CONFLICT (tenant_id, app_id, operation, idempotency_key) DO UPDATE SET
	request_hash = excluded.request_hash,
	resource_id = NULL,
	http_status = NULL,
	created_at = excluded.created_at,
	updated_at = excluded.updated_at,
	expires_at = excluded.expires_at`,
			scope.TenantID, scope.AppID, scope.Operation, scope.Key, requestHash, formatSQLiteTime(now), formatSQLiteTime(now), formatSQLiteTime(expiresAt))
		if err != nil {
			return Decision{}, err
		}
		if err := tx.Commit(); err != nil {
			return Decision{}, err
		}
		return Decision{Kind: DecisionReserved, Record: record}, nil
	}

	kind := DecisionReplay
	if record.RequestHash != requestHash {
		kind = DecisionConflict
	}
	if err := tx.Commit(); err != nil {
		return Decision{}, err
	}
	return Decision{Kind: kind, Record: record}, nil
}

func (s *SQLiteStore) Complete(ctx context.Context, scope Scope, resourceID string, httpStatus int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite idempotency store is not configured")
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE gateway_idempotency_records
SET resource_id = ?, http_status = ?, updated_at = ?
WHERE tenant_id = ? AND app_id = ? AND operation = ? AND idempotency_key = ?`,
		resourceID, httpStatus, formatSQLiteTime(s.now().UTC()), scope.TenantID, scope.AppID, scope.Operation, scope.Key)
	return err
}

func (s *SQLiteStore) Release(ctx context.Context, scope Scope) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite idempotency store is not configured")
	}
	_, err := s.db.ExecContext(ctx, `
DELETE FROM gateway_idempotency_records
WHERE tenant_id = ? AND app_id = ? AND operation = ? AND idempotency_key = ?`,
		scope.TenantID, scope.AppID, scope.Operation, scope.Key)
	return err
}

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite idempotency store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE name = ? LIMIT 1`, "gateway_idempotency_records").Scan(&name)
	if err == sql.ErrNoRows {
		return fmt.Errorf("gateway_idempotency_records is missing")
	}
	return err
}

func (s *SQLiteStore) loadForUpdate(ctx context.Context, tx *sql.Tx, scope Scope) (Record, bool, error) {
	var record Record
	record.Scope = scope
	var resourceID sql.NullString
	var httpStatus sql.NullInt64
	var createdAt, updatedAt, expiresAt string
	err := tx.QueryRowContext(ctx, `
SELECT request_hash, resource_id, http_status, created_at, updated_at, expires_at
FROM gateway_idempotency_records
WHERE tenant_id = ? AND app_id = ? AND operation = ? AND idempotency_key = ?`, scope.TenantID, scope.AppID, scope.Operation, scope.Key).
		Scan(&record.RequestHash, &resourceID, &httpStatus, &createdAt, &updatedAt, &expiresAt)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if resourceID.Valid {
		record.ResourceID = resourceID.String
	}
	if httpStatus.Valid {
		record.HTTPStatus = int(httpStatus.Int64)
	}
	record.CreatedAt = parseSQLiteTime(createdAt)
	record.UpdatedAt = parseSQLiteTime(updatedAt)
	record.ExpiresAt = parseSQLiteTime(expiresAt)
	return record, true, nil
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}

func parseSQLiteTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{sqliteTimeLayout, time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}
