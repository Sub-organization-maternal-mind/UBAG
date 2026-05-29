package idempotency

import (
	"context"
	"database/sql"
	"fmt"
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
	result, err := tx.ExecContext(ctx, `
INSERT INTO gateway_idempotency_records (
	tenant_id, app_id, operation, idempotency_key, request_hash, created_at, updated_at, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT DO NOTHING`,
		scope.TenantID, scope.AppID, scope.Operation, scope.Key, requestHash, now, now, expiresAt)
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

	record, found, err := p.loadForUpdate(ctx, tx, scope)
	if err != nil {
		return Decision{}, err
	}
	if !found || !record.ExpiresAt.After(now) {
		record = Record{Scope: scope, RequestHash: requestHash, CreatedAt: now, UpdatedAt: now, ExpiresAt: expiresAt}
		_, err := tx.ExecContext(ctx, `
INSERT INTO gateway_idempotency_records (
	tenant_id, app_id, operation, idempotency_key, request_hash, resource_id, http_status, created_at, updated_at, expires_at
) VALUES ($1, $2, $3, $4, $5, NULL, NULL, $6, $7, $8)
ON CONFLICT (tenant_id, app_id, operation, idempotency_key) DO UPDATE SET
	request_hash = EXCLUDED.request_hash,
	resource_id = NULL,
	http_status = NULL,
	created_at = EXCLUDED.created_at,
	updated_at = EXCLUDED.updated_at,
	expires_at = EXCLUDED.expires_at`,
			scope.TenantID, scope.AppID, scope.Operation, scope.Key, requestHash, now, now, expiresAt)
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

func (p *PostgresStore) Complete(ctx context.Context, scope Scope, resourceID string, httpStatus int) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("postgres idempotency store is not configured")
	}
	_, err := p.db.ExecContext(ctx, `
UPDATE gateway_idempotency_records
SET resource_id = $1, http_status = $2, updated_at = $3
WHERE tenant_id = $4 AND app_id = $5 AND operation = $6 AND idempotency_key = $7`,
		resourceID, httpStatus, p.now().UTC(), scope.TenantID, scope.AppID, scope.Operation, scope.Key)
	return err
}

func (p *PostgresStore) Release(ctx context.Context, scope Scope) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("postgres idempotency store is not configured")
	}
	_, err := p.db.ExecContext(ctx, `
DELETE FROM gateway_idempotency_records
WHERE tenant_id = $1 AND app_id = $2 AND operation = $3 AND idempotency_key = $4`,
		scope.TenantID, scope.AppID, scope.Operation, scope.Key)
	return err
}

func (p *PostgresStore) Ready(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("postgres idempotency store is not configured")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return err
	}
	var exists bool
	if err := p.db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, "gateway_idempotency_records").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("gateway_idempotency_records is missing")
	}
	return nil
}

func (p *PostgresStore) loadForUpdate(ctx context.Context, tx *sql.Tx, scope Scope) (Record, bool, error) {
	var record Record
	record.Scope = scope
	var resourceID sql.NullString
	var httpStatus sql.NullInt64
	err := tx.QueryRowContext(ctx, `
SELECT request_hash, resource_id, http_status, created_at, updated_at, expires_at
FROM gateway_idempotency_records
WHERE tenant_id = $1 AND app_id = $2 AND operation = $3 AND idempotency_key = $4
FOR UPDATE`, scope.TenantID, scope.AppID, scope.Operation, scope.Key).
		Scan(&record.RequestHash, &resourceID, &httpStatus, &record.CreatedAt, &record.UpdatedAt, &record.ExpiresAt)
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
	return record, true, nil
}
