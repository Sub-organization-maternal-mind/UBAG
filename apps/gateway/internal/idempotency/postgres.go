package idempotency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/ubag/ubag/apps/gateway/internal/storekit"
	"time"
)

type PostgresStore struct {
	db  *sql.DB
	ttl time.Duration
	now func() time.Time
}

func NewPostgresStore(db *sql.DB, ttl time.Duration) *PostgresStore {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &PostgresStore{
		db:  db,
		ttl: ttl,
		now: time.Now,
	}
}

func (p *PostgresStore) Reserve(ctx context.Context, scope Scope, requestHash string) (Decision, error) {
	if p == nil || p.db == nil {
		return Decision{}, fmt.Errorf("postgres idempotency store is not configured")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Decision{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := p.now().UTC()
	expiresAt := now.Add(p.ttl)
	lockedUntil := now.Add(defaultInFlightLock)
	result, err := tx.ExecContext(ctx, `
INSERT INTO gateway_idempotency_records (
	tenant_id, app_id, operation, idempotency_key, request_hash, status, locked_until, created_at, updated_at, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT DO NOTHING`,
		scope.TenantID, scope.AppID, scope.Operation, scope.Key, requestHash, string(RecordInFlight), lockedUntil, now, now, expiresAt)
	if err != nil {
		return Decision{}, err
	}
	if inserted, _ := result.RowsAffected(); inserted == 1 {
		record := Record{Scope: scope, RequestHash: requestHash, Status: RecordInFlight, LockedUntil: lockedUntil, CreatedAt: now, UpdatedAt: now, ExpiresAt: expiresAt}
		if err := tx.Commit(); err != nil {
			return Decision{}, err
		}
		return Decision{Kind: DecisionReserved, Record: record}, nil
	}

	record, found, err := p.loadForUpdate(ctx, tx, scope)
	if err != nil {
		return Decision{}, err
	}
	// Take the key over when the previous reservation expired, or when it is
	// an in-flight lock whose deadline has passed (the reserving process died).
	if !found || !record.ExpiresAt.After(now) || staleInFlight(record, now) {
		record = Record{Scope: scope, RequestHash: requestHash, Status: RecordInFlight, LockedUntil: lockedUntil, CreatedAt: now, UpdatedAt: now, ExpiresAt: expiresAt}
		_, err := tx.ExecContext(ctx, `
INSERT INTO gateway_idempotency_records (
	tenant_id, app_id, operation, idempotency_key, request_hash, status, locked_until, resource_id, http_status, created_at, updated_at, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, NULL, $8, $9, $10)
ON CONFLICT (tenant_id, app_id, operation, idempotency_key) DO UPDATE SET
	request_hash = EXCLUDED.request_hash,
	status = EXCLUDED.status,
	locked_until = EXCLUDED.locked_until,
	resource_id = NULL,
	http_status = NULL,
	created_at = EXCLUDED.created_at,
	updated_at = EXCLUDED.updated_at,
	expires_at = EXCLUDED.expires_at`,
			scope.TenantID, scope.AppID, scope.Operation, scope.Key, requestHash, string(RecordInFlight), lockedUntil, now, now, expiresAt)
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

func (p *PostgresStore) Complete(ctx context.Context, scope Scope, requestHash string, resourceID string, httpStatus int) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("postgres idempotency store is not configured")
	}
	// Compare-and-set on the payload hash so a stale completion cannot be
	// attributed to a record a different payload now owns.
	_, err := p.db.ExecContext(ctx, `
UPDATE gateway_idempotency_records
SET resource_id = $1, http_status = $2, status = $3, locked_until = NULL, updated_at = $4
WHERE tenant_id = $5 AND app_id = $6 AND operation = $7 AND idempotency_key = $8 AND request_hash = $9`,
		resourceID, httpStatus, string(RecordCompleted), p.now().UTC(), scope.TenantID, scope.AppID, scope.Operation, scope.Key, requestHash)
	return err
}

func (p *PostgresStore) Release(ctx context.Context, scope Scope, requestHash string) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("postgres idempotency store is not configured")
	}
	_, err := p.db.ExecContext(ctx, `
DELETE FROM gateway_idempotency_records
WHERE tenant_id = $1 AND app_id = $2 AND operation = $3 AND idempotency_key = $4 AND request_hash = $5`,
		scope.TenantID, scope.AppID, scope.Operation, scope.Key, requestHash)
	return err
}

// Sweep deletes every record whose TTL has expired and returns how many rows
// were removed. The idx_gateway_idempotency_expires index serves this scan.
func (p *PostgresStore) Sweep(ctx context.Context) (int64, error) {
	if p == nil || p.db == nil {
		return 0, fmt.Errorf("postgres idempotency store is not configured")
	}
	result, err := p.db.ExecContext(ctx, `
DELETE FROM gateway_idempotency_records
WHERE expires_at <= $1`, p.now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (p *PostgresStore) Ready(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("postgres idempotency store is not configured")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return err
	}
	return storekit.RequirePostgresObject(ctx, p.db, "gateway_idempotency_records")
}

func (p *PostgresStore) loadForUpdate(ctx context.Context, tx *sql.Tx, scope Scope) (Record, bool, error) {
	var record Record
	record.Scope = scope
	var resourceID sql.NullString
	var httpStatus sql.NullInt64
	var status sql.NullString
	var lockedUntil sql.NullTime
	err := tx.QueryRowContext(ctx, `
SELECT request_hash, resource_id, http_status, status, locked_until, created_at, updated_at, expires_at
FROM gateway_idempotency_records
WHERE tenant_id = $1 AND app_id = $2 AND operation = $3 AND idempotency_key = $4
FOR UPDATE`, scope.TenantID, scope.AppID, scope.Operation, scope.Key).
		Scan(&record.RequestHash, &resourceID, &httpStatus, &status, &lockedUntil, &record.CreatedAt, &record.UpdatedAt, &record.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
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
	// Legacy rows predate the status/locked_until columns; a NULL status is
	// treated as the legacy "reserve until TTL" behavior, never as in-flight.
	if status.Valid && status.String != "" {
		record.Status = RecordStatus(status.String)
	}
	if lockedUntil.Valid {
		record.LockedUntil = lockedUntil.Time.UTC()
	}
	return record, true, nil
}
