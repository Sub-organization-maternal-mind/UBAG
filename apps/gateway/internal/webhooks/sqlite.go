package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// sqliteTimeLayout stores timestamps as fixed-width millisecond RFC3339 UTC
// strings so lexical ordering matches chronological ordering in SQLite.
const sqliteTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// SQLiteStore implements the webhook OutboxStore backed by SQLite. It mirrors
// PostgresStore using the gateway_webhook_deliveries and gateway_webhook_attempts
// tables. Because SQLite lacks SELECT ... FOR UPDATE SKIP LOCKED, leasing is
// serialized inside a single write transaction (the gateway configures
// SetMaxOpenConns(1) for SQLite).
type SQLiteStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db, now: time.Now}
}

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite webhook outbox is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	for _, objectName := range []string{"gateway_webhook_deliveries", "gateway_webhook_attempts"} {
		if err := requireSQLiteObject(ctx, s.db, objectName); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) Enqueue(ctx context.Context, request EnqueueRequest) (Delivery, bool, error) {
	if err := validateEnqueue(request); err != nil {
		return Delivery{}, false, err
	}
	now := s.now().UTC()
	if request.NextAttemptAt.IsZero() {
		request.NextAttemptAt = now
	}
	id := StableID("whd", request.TenantID, request.AppID, request.DedupeKey)
	endpointID := firstNonEmpty(request.EndpointID, StableID("whe", request.URL, request.SecretID))
	endpointKind := firstNonEmpty(request.EndpointKind, "job_callback")
	result, err := s.db.ExecContext(ctx, `
INSERT INTO gateway_webhook_deliveries (
	id, tenant_id, app_id, job_id, event_name, endpoint_id, endpoint_kind,
	url, secret_id, dedupe_key, payload_json, trace_id, status, attempt_count,
	max_attempts, next_attempt_at, replay_of, created_at, updated_at
) VALUES (
	?, ?, ?, nullif(?, ''), ?, ?, ?,
	?, ?, ?, ?, nullif(?, ''), 'pending', 0,
	?, ?, nullif(?, ''), ?, ?
)
ON CONFLICT (tenant_id, app_id, dedupe_key) DO NOTHING`,
		id, request.TenantID, request.AppID, request.JobID, request.EventName, endpointID, endpointKind,
		request.URL, request.SecretID, request.DedupeKey, string(json.RawMessage(request.Payload)), request.TraceID,
		normalizeMaxAttempts(request.MaxAttempts), formatNullableSQLiteTime(request.NextAttemptAt), request.ReplayOf, formatSQLiteTime(now), formatSQLiteTime(now))
	if err != nil {
		return Delivery{}, false, err
	}
	if inserted, _ := result.RowsAffected(); inserted == 1 {
		delivery, found, err := s.Get(ctx, request.TenantID, request.AppID, id)
		if err != nil || !found {
			return Delivery{}, false, err
		}
		return delivery, true, nil
	}
	delivery, _, err := s.deliveryByDedupe(ctx, request.TenantID, request.AppID, request.DedupeKey)
	return delivery, false, err
}

func (s *SQLiteStore) Get(ctx context.Context, tenantID string, appID string, deliveryID string) (Delivery, bool, error) {
	return scanSQLiteDelivery(s.db.QueryRowContext(ctx, `SELECT `+selectSQLiteDeliveryColumns()+` FROM gateway_webhook_deliveries WHERE id = ? AND tenant_id = ? AND app_id = ?`, deliveryID, tenantID, appID))
}

func (s *SQLiteStore) Replay(ctx context.Context, tenantID string, appID string, deliveryID string, idempotencyKey string, now time.Time) (Delivery, bool, error) {
	original, found, err := s.Get(ctx, tenantID, appID, deliveryID)
	if err != nil || !found {
		return Delivery{}, found, err
	}
	return s.Enqueue(ctx, EnqueueRequest{
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

func (s *SQLiteStore) LeaseDue(ctx context.Context, workerID string, limit int, leaseFor time.Duration) ([]Delivery, error) {
	if stringsTrim(workerID) == "" {
		return nil, fmt.Errorf("webhook worker id is required")
	}
	if limit <= 0 {
		limit = 10
	}
	if leaseFor <= 0 {
		leaseFor = 30 * time.Second
	}
	now := s.now().UTC()
	nowText := formatSQLiteTime(now)
	leaseID := StableID("whlease", workerID, now.Format(time.RFC3339Nano))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollbackUnlessCommitted(tx)

	rows, err := tx.QueryContext(ctx, `
SELECT id
FROM gateway_webhook_deliveries
WHERE (
	status IN ('pending', 'retry_scheduled') AND next_attempt_at IS NOT NULL AND next_attempt_at <= ?
) OR (
	status = 'leased' AND leased_until IS NOT NULL AND leased_until <= ?
)
ORDER BY next_attempt_at ASC, created_at ASC, id ASC
LIMIT ?`, nowText, nowText, limit)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	leasedUntil := formatSQLiteTime(now.Add(leaseFor))
	deliveries := []Delivery{}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `
UPDATE gateway_webhook_deliveries
SET status = 'leased', lease_id = ?, leased_until = ?, updated_at = ?
WHERE id = ?`, leaseID, leasedUntil, nowText, id); err != nil {
			return nil, err
		}
		delivery, found, err := scanSQLiteDeliveryTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if found {
			deliveries = append(deliveries, delivery)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deliveries, nil
}

func (s *SQLiteStore) MarkDelivered(ctx context.Context, deliveryID string, leaseID string, result AttemptResult) error {
	return s.mark(ctx, deliveryID, leaseID, StatusDelivered, time.Time{}, result)
}

func (s *SQLiteStore) MarkRetry(ctx context.Context, deliveryID string, leaseID string, nextAttemptAt time.Time, result AttemptResult) error {
	return s.mark(ctx, deliveryID, leaseID, StatusRetryScheduled, nextAttemptAt.UTC(), result)
}

func (s *SQLiteStore) MarkDeadLetter(ctx context.Context, deliveryID string, leaseID string, result AttemptResult) error {
	return s.mark(ctx, deliveryID, leaseID, StatusDeadLettered, time.Time{}, result)
}

func (s *SQLiteStore) Stats(ctx context.Context) (Stats, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT status, count(*), min(coalesce(next_attempt_at, updated_at))
FROM gateway_webhook_deliveries
GROUP BY status`)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	now := s.now().UTC()
	stats := Stats{DepthByState: map[string]int{}, OldestAgeByState: map[string]time.Duration{}}
	for rows.Next() {
		var state string
		var count int
		var oldest sql.NullString
		if err := rows.Scan(&state, &count, &oldest); err != nil {
			return Stats{}, err
		}
		stats.DepthByState[state] = count
		age := time.Duration(0)
		if oldest.Valid {
			if oldestTime := parseSQLiteTime(oldest.String); !oldestTime.IsZero() {
				if delta := now.Sub(oldestTime); delta > 0 {
					age = delta
				}
			}
		}
		stats.OldestAgeByState[state] = age
	}
	return stats, rows.Err()
}

func (s *SQLiteStore) mark(ctx context.Context, deliveryID string, leaseID string, status DeliveryStatus, nextAttemptAt time.Time, result AttemptResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackUnlessCommitted(tx)
	var attempt int
	err = tx.QueryRowContext(ctx, `SELECT attempt_count + 1 FROM gateway_webhook_deliveries WHERE id = ? AND lease_id = ?`, deliveryID, leaseID).Scan(&attempt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("webhook delivery %s lease mismatch", deliveryID)
	}
	if err != nil {
		return err
	}
	now := s.now().UTC()
	attemptID := StableID("wha", deliveryID, fmt.Sprintf("%d", attempt))
	if _, err := tx.ExecContext(ctx, `
INSERT INTO gateway_webhook_attempts (
	id, delivery_id, attempt_number, status_code, error_class, error_message, duration_ms, created_at
) VALUES (?, ?, ?, nullif(?, 0), nullif(?, ''), nullif(?, ''), ?, ?)
ON CONFLICT (delivery_id, attempt_number) DO NOTHING`,
		attemptID, deliveryID, attempt, result.StatusCode, result.ErrorClass, sanitizeErrorMessage(result.ErrorMessage), int64(result.Duration/time.Millisecond), formatSQLiteTime(now)); err != nil {
		return err
	}
	var deliveredAt any
	if status == StatusDelivered {
		deliveredAt = formatSQLiteTime(now)
	}
	_, err = tx.ExecContext(ctx, `
UPDATE gateway_webhook_deliveries
SET status = ?,
	attempt_count = ?,
	next_attempt_at = ?,
	lease_id = NULL,
	leased_until = NULL,
	last_http_status = nullif(?, 0),
	last_error_class = nullif(?, ''),
	last_error_message = nullif(?, ''),
	delivered_at = coalesce(?, delivered_at),
	updated_at = ?
WHERE id = ? AND lease_id = ?`,
		string(status), attempt, formatNullableSQLiteTime(nextAttemptAt), result.StatusCode, result.ErrorClass, sanitizeErrorMessage(result.ErrorMessage), deliveredAt, formatSQLiteTime(now), deliveryID, leaseID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) deliveryByDedupe(ctx context.Context, tenantID string, appID string, dedupeKey string) (Delivery, bool, error) {
	return scanSQLiteDelivery(s.db.QueryRowContext(ctx, `SELECT `+selectSQLiteDeliveryColumns()+` FROM gateway_webhook_deliveries WHERE tenant_id = ? AND app_id = ? AND dedupe_key = ?`, tenantID, appID, dedupeKey))
}

func scanSQLiteDeliveryTx(ctx context.Context, tx *sql.Tx, deliveryID string) (Delivery, bool, error) {
	return scanSQLiteDelivery(tx.QueryRowContext(ctx, `SELECT `+selectSQLiteDeliveryColumns()+` FROM gateway_webhook_deliveries WHERE id = ?`, deliveryID))
}

func selectSQLiteDeliveryColumns() string {
	return `id, tenant_id, app_id, coalesce(job_id, ''), event_name, endpoint_id, endpoint_kind,
url, secret_id, dedupe_key, payload_json, coalesce(trace_id, ''), status, attempt_count, max_attempts,
next_attempt_at, coalesce(lease_id, ''), leased_until,
coalesce(last_http_status, 0), coalesce(last_error_class, ''), coalesce(last_error_message, ''),
coalesce(replay_of, ''), created_at, updated_at, delivered_at`
}

func scanSQLiteDelivery(row deliveryScanner) (Delivery, bool, error) {
	delivery, err := scanSQLiteDeliveryValue(row)
	if err == sql.ErrNoRows {
		return Delivery{}, false, nil
	}
	if err != nil {
		return Delivery{}, false, err
	}
	return delivery, true, nil
}

func scanSQLiteDeliveryValue(row deliveryScanner) (Delivery, error) {
	var delivery Delivery
	var payload []byte
	var status string
	var nextAttemptAt, leasedUntil, deliveredAt sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&delivery.ID, &delivery.TenantID, &delivery.AppID, &delivery.JobID, &delivery.EventName, &delivery.EndpointID, &delivery.EndpointKind,
		&delivery.URL, &delivery.SecretID, &delivery.DedupeKey, &payload, &delivery.TraceID, &status, &delivery.AttemptCount, &delivery.MaxAttempts,
		&nextAttemptAt, &delivery.LeaseID, &leasedUntil, &delivery.LastHTTPStatus, &delivery.LastErrorClass, &delivery.LastErrorMessage,
		&delivery.ReplayOf, &createdAt, &updatedAt, &deliveredAt,
	); err != nil {
		return Delivery{}, err
	}
	delivery.Status = DeliveryStatus(status)
	delivery.Payload = cloneBytes(payload)
	delivery.NextAttemptAt = parseNullableSQLiteTime(nextAttemptAt)
	delivery.LeasedUntil = parseNullableSQLiteTime(leasedUntil)
	delivery.DeliveredAt = parseNullableSQLiteTime(deliveredAt)
	delivery.CreatedAt = parseSQLiteTime(createdAt)
	delivery.UpdatedAt = parseSQLiteTime(updatedAt)
	return delivery, nil
}

func requireSQLiteObject(ctx context.Context, db *sql.DB, objectName string) error {
	var name string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE name = ? LIMIT 1`, objectName).Scan(&name)
	if err == sql.ErrNoRows {
		return fmt.Errorf("%s is missing", objectName)
	}
	return err
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}

func formatNullableSQLiteTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatSQLiteTime(t)
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

func parseNullableSQLiteTime(value sql.NullString) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return parseSQLiteTime(value.String)
}
