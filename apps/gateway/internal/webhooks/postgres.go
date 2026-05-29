package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, now: time.Now}
}

func (p *PostgresStore) Ready(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("postgres webhook outbox is not configured")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return err
	}
	for _, objectName := range []string{"gateway_webhook_deliveries", "gateway_webhook_attempts"} {
		if err := requirePostgresObject(ctx, p.db, objectName); err != nil {
			return err
		}
	}
	return nil
}

func (p *PostgresStore) Enqueue(ctx context.Context, request EnqueueRequest) (Delivery, bool, error) {
	if err := validateEnqueue(request); err != nil {
		return Delivery{}, false, err
	}
	now := p.now().UTC()
	if request.NextAttemptAt.IsZero() {
		request.NextAttemptAt = now
	}
	id := StableID("whd", request.TenantID, request.AppID, request.DedupeKey)
	endpointID := firstNonEmpty(request.EndpointID, StableID("whe", request.URL, request.SecretID))
	endpointKind := firstNonEmpty(request.EndpointKind, "job_callback")
	row := p.db.QueryRowContext(ctx, `
INSERT INTO gateway_webhook_deliveries (
	id, tenant_id, app_id, job_id, event_name, endpoint_id, endpoint_kind,
	url, secret_id, dedupe_key, payload_json, trace_id, status, attempt_count,
	max_attempts, next_attempt_at, replay_of, created_at, updated_at
) VALUES (
	$1, $2, $3, nullif($4, ''), $5, $6, $7,
	$8, $9, $10, $11, nullif($12, ''), 'pending', 0,
	$13, $14, nullif($15, ''), $16, $17
)
ON CONFLICT (tenant_id, app_id, dedupe_key) DO NOTHING
RETURNING `+selectDeliveryColumns(),
		id, request.TenantID, request.AppID, request.JobID, request.EventName, endpointID, endpointKind,
		request.URL, request.SecretID, request.DedupeKey, json.RawMessage(request.Payload), request.TraceID,
		normalizeMaxAttempts(request.MaxAttempts), request.NextAttemptAt.UTC(), request.ReplayOf, now, now)
	delivery, found, err := scanDelivery(row)
	if err != nil {
		return Delivery{}, false, err
	}
	if found {
		return delivery, true, nil
	}
	delivery, found, err = p.deliveryByDedupe(ctx, request.TenantID, request.AppID, request.DedupeKey)
	return delivery, false, err
}

func (p *PostgresStore) Get(ctx context.Context, tenantID string, appID string, deliveryID string) (Delivery, bool, error) {
	return scanDelivery(p.db.QueryRowContext(ctx, `SELECT `+selectDeliveryColumns()+` FROM gateway_webhook_deliveries WHERE id = $1 AND tenant_id = $2 AND app_id = $3`, deliveryID, tenantID, appID))
}

func (p *PostgresStore) Replay(ctx context.Context, tenantID string, appID string, deliveryID string, idempotencyKey string, now time.Time) (Delivery, bool, error) {
	original, found, err := p.Get(ctx, tenantID, appID, deliveryID)
	if err != nil || !found {
		return Delivery{}, found, err
	}
	return p.Enqueue(ctx, EnqueueRequest{
		TenantID:      original.TenantID,
		AppID:         original.AppID,
		JobID:         original.JobID,
		EventName:     original.EventName,
		EndpointID:    original.EndpointID,
		EndpointKind:  original.EndpointKind,
		URL:           original.URL,
		SecretID:      original.SecretID,
		DedupeKey:     "replay:" + original.ID + ":" + idempotencyKey,
		Payload:       original.Payload,
		TraceID:       original.TraceID,
		MaxAttempts:   original.MaxAttempts,
		NextAttemptAt: now.UTC(),
		ReplayOf:      original.ID,
	})
}

func (p *PostgresStore) LeaseDue(ctx context.Context, workerID string, limit int, leaseFor time.Duration) ([]Delivery, error) {
	if stringsTrim(workerID) == "" {
		return nil, fmt.Errorf("webhook worker id is required")
	}
	if limit <= 0 {
		limit = 10
	}
	if leaseFor <= 0 {
		leaseFor = 30 * time.Second
	}
	now := p.now().UTC()
	leaseID := StableID("whlease", workerID, now.Format(time.RFC3339Nano))
	rows, err := p.db.QueryContext(ctx, `
WITH due AS (
	SELECT id
	FROM gateway_webhook_deliveries
	WHERE (
		status IN ('pending', 'retry_scheduled') AND next_attempt_at <= $1
	) OR (
		status = 'leased' AND leased_until IS NOT NULL AND leased_until <= $1
	)
	ORDER BY next_attempt_at ASC, created_at ASC, id ASC
	LIMIT $2
	FOR UPDATE SKIP LOCKED
)
UPDATE gateway_webhook_deliveries d
SET status = 'leased',
	lease_id = $3,
	leased_until = $4,
	updated_at = $1
FROM due
WHERE d.id = due.id
RETURNING `+selectDeliveryColumns(), now, limit, leaseID, now.Add(leaseFor))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := []Delivery{}
	for rows.Next() {
		delivery, err := scanDeliveryRows(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (p *PostgresStore) MarkDelivered(ctx context.Context, deliveryID string, leaseID string, result AttemptResult) error {
	return p.mark(ctx, deliveryID, leaseID, StatusDelivered, time.Time{}, result)
}

func (p *PostgresStore) MarkRetry(ctx context.Context, deliveryID string, leaseID string, nextAttemptAt time.Time, result AttemptResult) error {
	return p.mark(ctx, deliveryID, leaseID, StatusRetryScheduled, nextAttemptAt.UTC(), result)
}

func (p *PostgresStore) MarkDeadLetter(ctx context.Context, deliveryID string, leaseID string, result AttemptResult) error {
	return p.mark(ctx, deliveryID, leaseID, StatusDeadLettered, time.Time{}, result)
}

func (p *PostgresStore) Stats(ctx context.Context) (Stats, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT status, count(*), greatest(extract(epoch FROM max(now() - coalesce(next_attempt_at, updated_at))), 0)
FROM gateway_webhook_deliveries
GROUP BY status`)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	stats := Stats{DepthByState: map[string]int{}, OldestAgeByState: map[string]time.Duration{}}
	for rows.Next() {
		var state string
		var count int
		var oldestSeconds float64
		if err := rows.Scan(&state, &count, &oldestSeconds); err != nil {
			return Stats{}, err
		}
		stats.DepthByState[state] = count
		stats.OldestAgeByState[state] = time.Duration(oldestSeconds * float64(time.Second))
	}
	return stats, rows.Err()
}

func (p *PostgresStore) mark(ctx context.Context, deliveryID string, leaseID string, status DeliveryStatus, nextAttemptAt time.Time, result AttemptResult) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackUnlessCommitted(tx)
	var attempt int
	err = tx.QueryRowContext(ctx, `SELECT attempt_count + 1 FROM gateway_webhook_deliveries WHERE id = $1 AND lease_id = $2 FOR UPDATE`, deliveryID, leaseID).Scan(&attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("webhook delivery %s lease mismatch", deliveryID)
	}
	if err != nil {
		return err
	}
	now := p.now().UTC()
	attemptID := StableID("wha", deliveryID, fmt.Sprintf("%d", attempt))
	if _, err := tx.ExecContext(ctx, `
INSERT INTO gateway_webhook_attempts (
	id, delivery_id, attempt_number, status_code, error_class, error_message, duration_ms, created_at
) VALUES ($1, $2, $3, nullif($4, 0), nullif($5, ''), nullif($6, ''), $7, $8)
ON CONFLICT (delivery_id, attempt_number) DO NOTHING`,
		attemptID, deliveryID, attempt, result.StatusCode, result.ErrorClass, sanitizeErrorMessage(result.ErrorMessage), int64(result.Duration/time.Millisecond), now); err != nil {
		return err
	}
	deliveredAt := any(nil)
	if status == StatusDelivered {
		deliveredAt = now
	}
	_, err = tx.ExecContext(ctx, `
UPDATE gateway_webhook_deliveries
SET status = $1,
	attempt_count = $2,
	next_attempt_at = $3,
	lease_id = NULL,
	leased_until = NULL,
	last_http_status = nullif($4, 0),
	last_error_class = nullif($5, ''),
	last_error_message = nullif($6, ''),
	delivered_at = coalesce($7, delivered_at),
	updated_at = $8
WHERE id = $9 AND lease_id = $10`,
		string(status), attempt, nullableTime(nextAttemptAt), result.StatusCode, result.ErrorClass, sanitizeErrorMessage(result.ErrorMessage), deliveredAt, now, deliveryID, leaseID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (p *PostgresStore) deliveryByDedupe(ctx context.Context, tenantID string, appID string, dedupeKey string) (Delivery, bool, error) {
	return scanDelivery(p.db.QueryRowContext(ctx, `SELECT `+selectDeliveryColumns()+` FROM gateway_webhook_deliveries WHERE tenant_id = $1 AND app_id = $2 AND dedupe_key = $3`, tenantID, appID, dedupeKey))
}

func selectDeliveryColumns() string {
	return `id, tenant_id, app_id, coalesce(job_id, ''), event_name, endpoint_id, endpoint_kind,
url, secret_id, dedupe_key, payload_json, coalesce(trace_id, ''), status, attempt_count, max_attempts,
coalesce(next_attempt_at, 'epoch'::timestamptz), coalesce(lease_id, ''), coalesce(leased_until, 'epoch'::timestamptz),
coalesce(last_http_status, 0), coalesce(last_error_class, ''), coalesce(last_error_message, ''),
coalesce(replay_of, ''), created_at, updated_at, coalesce(delivered_at, 'epoch'::timestamptz)`
}

type deliveryScanner interface {
	Scan(dest ...any) error
}

func scanDelivery(row deliveryScanner) (Delivery, bool, error) {
	delivery, err := scanDeliveryValue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, false, nil
	}
	if err != nil {
		return Delivery{}, false, err
	}
	return delivery, true, nil
}

func scanDeliveryRows(rows *sql.Rows) (Delivery, error) {
	return scanDeliveryValue(rows)
}

func scanDeliveryValue(row deliveryScanner) (Delivery, error) {
	var delivery Delivery
	var payload []byte
	var status string
	if err := row.Scan(
		&delivery.ID, &delivery.TenantID, &delivery.AppID, &delivery.JobID, &delivery.EventName, &delivery.EndpointID, &delivery.EndpointKind,
		&delivery.URL, &delivery.SecretID, &delivery.DedupeKey, &payload, &delivery.TraceID, &status, &delivery.AttemptCount, &delivery.MaxAttempts,
		&delivery.NextAttemptAt, &delivery.LeaseID, &delivery.LeasedUntil, &delivery.LastHTTPStatus, &delivery.LastErrorClass, &delivery.LastErrorMessage,
		&delivery.ReplayOf, &delivery.CreatedAt, &delivery.UpdatedAt, &delivery.DeliveredAt,
	); err != nil {
		return Delivery{}, err
	}
	delivery.Status = DeliveryStatus(status)
	delivery.Payload = cloneBytes(payload)
	if delivery.NextAttemptAt.Equal(time.Unix(0, 0).UTC()) {
		delivery.NextAttemptAt = time.Time{}
	}
	if delivery.LeasedUntil.Equal(time.Unix(0, 0).UTC()) {
		delivery.LeasedUntil = time.Time{}
	}
	if delivery.DeliveredAt.Equal(time.Unix(0, 0).UTC()) {
		delivery.DeliveredAt = time.Time{}
	}
	return delivery, nil
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

func rollbackUnlessCommitted(tx *sql.Tx) {
	_ = tx.Rollback()
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
