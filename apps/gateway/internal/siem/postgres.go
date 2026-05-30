package siem

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

var _ ConfigStore = (*PostgresStore)(nil)

// PostgresStore is a ConfigStore backed by Postgres (github.com/jackc/pgx/v5/stdlib,
// driver name "pgx"). It mirrors the SQLite store and persists only sink
// configuration metadata: secrets are represented by SecretRef, never written in
// plaintext. The schema is migration-driven
// (migrations/postgres/0005_enterprise_stores.sql).
type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewPostgresStore returns a ConfigStore over db.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, now: time.Now}
}

// Ready ensures the connection is usable and the schema exists.
func (s *PostgresStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("siem: postgres config store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	return requirePostgresObject(ctx, s.db, "gateway_siem_sink_configs")
}

// Put implements ConfigStore.
func (s *PostgresStore) Put(ctx context.Context, config SinkConfig) (SinkConfig, error) {
	if err := validateSinkConfig(config); err != nil {
		return SinkConfig{}, err
	}
	if s == nil || s.db == nil {
		return SinkConfig{}, fmt.Errorf("siem: postgres config store is not configured")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if config.ID == "" {
		config.ID = stableID("siemsink", strings.TrimSpace(config.TenantID), strings.ToLower(strings.TrimSpace(config.Kind)), strings.TrimSpace(config.Target), strings.TrimSpace(config.Name))
	}
	if existing, ok, err := s.Get(ctx, strings.TrimSpace(config.TenantID), config.ID); err != nil {
		return SinkConfig{}, err
	} else if ok {
		config.CreatedAt = existing.CreatedAt
	}
	config = normalizeSinkConfig(config, now)
	enabled := 0
	if config.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO gateway_siem_sink_configs (
	id, tenant_id, name, kind, target, network, secret_ref, enabled, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (id) DO UPDATE SET
	tenant_id = excluded.tenant_id,
	name = excluded.name,
	kind = excluded.kind,
	target = excluded.target,
	network = excluded.network,
	secret_ref = excluded.secret_ref,
	enabled = excluded.enabled,
	updated_at = excluded.updated_at`,
		config.ID, config.TenantID, config.Name, config.Kind, config.Target, config.Network,
		config.SecretRef, enabled, formatSinkConfigTime(config.CreatedAt), formatSinkConfigTime(config.UpdatedAt))
	if err != nil {
		return SinkConfig{}, fmt.Errorf("siem put sink config: %w", err)
	}
	return config, nil
}

// Get implements ConfigStore.
func (s *PostgresStore) Get(ctx context.Context, tenantID string, id string) (SinkConfig, bool, error) {
	if s == nil || s.db == nil {
		return SinkConfig{}, false, fmt.Errorf("siem: postgres config store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+selectSinkConfigColumns+` FROM gateway_siem_sink_configs WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	config, err := scanSinkConfig(row)
	if err == sql.ErrNoRows {
		return SinkConfig{}, false, nil
	}
	if err != nil {
		return SinkConfig{}, false, err
	}
	return config, true, nil
}

// List implements ConfigStore.
func (s *PostgresStore) List(ctx context.Context, tenantID string) ([]SinkConfig, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("siem: postgres config store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+selectSinkConfigColumns+` FROM gateway_siem_sink_configs WHERE tenant_id = $1 ORDER BY id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	output := []SinkConfig{}
	for rows.Next() {
		config, err := scanSinkConfig(rows)
		if err != nil {
			return nil, err
		}
		output = append(output, config)
	}
	return output, rows.Err()
}

// Delete implements ConfigStore.
func (s *PostgresStore) Delete(ctx context.Context, tenantID string, id string) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("siem: postgres config store is not configured")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM gateway_siem_sink_configs WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
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
