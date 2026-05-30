package sso

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var _ ConfigStore = (*PostgresStore)(nil)

// PostgresStore is a ConfigStore backed by Postgres (github.com/jackc/pgx/v5/stdlib,
// driver name "pgx"). Like the SQLite store it persists only public / non-secret
// configuration: the full config document is stored as JSONB and, for OIDC, the
// client secret *reference id* is projected into its own column. Plaintext client
// secrets, private keys, and passwords are never written. The schema is
// migration-driven (migrations/postgres/0005_enterprise_stores.sql).
type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewPostgresStore returns a ConfigStore over db.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, now: time.Now}
}

// Ready pings the database and verifies the SSO config tables exist.
func (p *PostgresStore) Ready(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("sso: postgres store is not configured")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return err
	}
	for _, objectName := range []string{"gateway_sso_oidc_config", "gateway_sso_saml_config"} {
		if err := requirePostgresObject(ctx, p.db, objectName); err != nil {
			return err
		}
	}
	return nil
}

func (p *PostgresStore) SetOIDC(ctx context.Context, tenantID string, cfg OIDCConfig) error {
	if err := validateTenant(tenantID); err != nil {
		return err
	}
	blob, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `
INSERT INTO gateway_sso_oidc_config (tenant_id, issuer, client_id, client_secret_ref, config_json, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (tenant_id) DO UPDATE SET
	issuer = excluded.issuer,
	client_id = excluded.client_id,
	client_secret_ref = excluded.client_secret_ref,
	config_json = excluded.config_json,
	updated_at = excluded.updated_at`,
		tenantID, cfg.Issuer, cfg.ClientID, cfg.ClientSecretRef, json.RawMessage(blob), p.now().UTC())
	if err != nil {
		return fmt.Errorf("sso set oidc: %w", err)
	}
	return nil
}

func (p *PostgresStore) GetOIDC(ctx context.Context, tenantID string) (OIDCConfig, bool, error) {
	var blob []byte
	err := p.db.QueryRowContext(ctx, `SELECT config_json FROM gateway_sso_oidc_config WHERE tenant_id = $1`, tenantID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return OIDCConfig{}, false, nil
	}
	if err != nil {
		return OIDCConfig{}, false, err
	}
	var cfg OIDCConfig
	if err := json.Unmarshal(blob, &cfg); err != nil {
		return OIDCConfig{}, false, err
	}
	return cfg, true, nil
}

func (p *PostgresStore) ListOIDC(ctx context.Context) ([]StoredOIDC, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT tenant_id, config_json, updated_at FROM gateway_sso_oidc_config ORDER BY tenant_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredOIDC{}
	for rows.Next() {
		var (
			tenantID  string
			blob      []byte
			updatedAt time.Time
		)
		if err := rows.Scan(&tenantID, &blob, &updatedAt); err != nil {
			return nil, err
		}
		var cfg OIDCConfig
		if err := json.Unmarshal(blob, &cfg); err != nil {
			return nil, err
		}
		out = append(out, StoredOIDC{TenantID: tenantID, Config: cfg, UpdatedAt: updatedAt.UTC()})
	}
	return out, rows.Err()
}

func (p *PostgresStore) SetSAML(ctx context.Context, tenantID string, cfg SAMLConfig) error {
	if err := validateTenant(tenantID); err != nil {
		return err
	}
	blob, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `
INSERT INTO gateway_sso_saml_config (tenant_id, entity_id, idp_sso_url, config_json, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id) DO UPDATE SET
	entity_id = excluded.entity_id,
	idp_sso_url = excluded.idp_sso_url,
	config_json = excluded.config_json,
	updated_at = excluded.updated_at`,
		tenantID, cfg.EntityID, cfg.IdPSSOURL, json.RawMessage(blob), p.now().UTC())
	if err != nil {
		return fmt.Errorf("sso set saml: %w", err)
	}
	return nil
}

func (p *PostgresStore) GetSAML(ctx context.Context, tenantID string) (SAMLConfig, bool, error) {
	var blob []byte
	err := p.db.QueryRowContext(ctx, `SELECT config_json FROM gateway_sso_saml_config WHERE tenant_id = $1`, tenantID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return SAMLConfig{}, false, nil
	}
	if err != nil {
		return SAMLConfig{}, false, err
	}
	var cfg SAMLConfig
	if err := json.Unmarshal(blob, &cfg); err != nil {
		return SAMLConfig{}, false, err
	}
	return cfg, true, nil
}

func (p *PostgresStore) ListSAML(ctx context.Context) ([]StoredSAML, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT tenant_id, config_json, updated_at FROM gateway_sso_saml_config ORDER BY tenant_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredSAML{}
	for rows.Next() {
		var (
			tenantID  string
			blob      []byte
			updatedAt time.Time
		)
		if err := rows.Scan(&tenantID, &blob, &updatedAt); err != nil {
			return nil, err
		}
		var cfg SAMLConfig
		if err := json.Unmarshal(blob, &cfg); err != nil {
			return nil, err
		}
		out = append(out, StoredSAML{TenantID: tenantID, Config: cfg, UpdatedAt: updatedAt.UTC()})
	}
	return out, rows.Err()
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
