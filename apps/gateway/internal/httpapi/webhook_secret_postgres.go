package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

var _ WebhookSecretStore = (*PostgresWebhookSecretStore)(nil)

// PostgresWebhookSecretStore is a WebhookSecretStore backed by Postgres
// (github.com/jackc/pgx/v5/stdlib, driver name "pgx"). It persists webhook
// signing-secret rotations using opaque secret references only — plaintext
// secrets are never stored. The schema is migration-driven
// (migrations/postgres/0005_enterprise_stores.sql).
type PostgresWebhookSecretStore struct {
	db *sql.DB
}

// NewPostgresWebhookSecretStore builds a Postgres-backed store.
func NewPostgresWebhookSecretStore(db *sql.DB) *PostgresWebhookSecretStore {
	return &PostgresWebhookSecretStore{db: db}
}

// Ready verifies the connection is usable and the rotations table exists.
func (s *PostgresWebhookSecretStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("webhook secret postgres store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, "webhook_secret_rotations").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("webhook_secret_rotations is missing")
	}
	return nil
}

func (s *PostgresWebhookSecretStore) Rotate(ctx context.Context, rotation WebhookSecretRotation) (WebhookSecretRotation, error) {
	if rotation.CreatedAt.IsZero() {
		rotation.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO webhook_secret_rotations
    (id, tenant_id, app_id, webhook_id, active_secret_ref, previous_secret_ref, overlap_until, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		rotation.ID, rotation.TenantID, rotation.AppID, rotation.WebhookID,
		rotation.ActiveSecretRef, rotation.PreviousSecretRef, nullableRotationTime(rotation.OverlapUntil), rotation.CreatedAt.UTC())
	if err != nil {
		return WebhookSecretRotation{}, fmt.Errorf("webhook secret rotate: %w", err)
	}
	return rotation, nil
}

func (s *PostgresWebhookSecretStore) GetByID(ctx context.Context, id string) (WebhookSecretRotation, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, tenant_id, app_id, webhook_id, active_secret_ref, previous_secret_ref, overlap_until, created_at
FROM webhook_secret_rotations WHERE id = $1`, id)
	rotation, err := scanWebhookSecretRotation(row)
	if err == sql.ErrNoRows {
		return WebhookSecretRotation{}, false, nil
	}
	if err != nil {
		return WebhookSecretRotation{}, false, err
	}
	return rotation, true, nil
}

func (s *PostgresWebhookSecretStore) Latest(ctx context.Context, tenantID, appID, webhookID string) (WebhookSecretRotation, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, tenant_id, app_id, webhook_id, active_secret_ref, previous_secret_ref, overlap_until, created_at
FROM webhook_secret_rotations
WHERE tenant_id = $1 AND app_id = $2 AND webhook_id = $3
ORDER BY created_at DESC LIMIT 1`, tenantID, appID, webhookID)
	rotation, err := scanWebhookSecretRotation(row)
	if err == sql.ErrNoRows {
		return WebhookSecretRotation{}, false, nil
	}
	if err != nil {
		return WebhookSecretRotation{}, false, err
	}
	return rotation, true, nil
}

func nullableRotationTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
