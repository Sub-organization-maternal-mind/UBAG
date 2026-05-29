package sso

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// sqliteTimeLayout stores timestamps as fixed-width millisecond RFC3339 UTC
// strings so lexical ordering matches chronological ordering in SQLite.
const sqliteTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// ErrTenantRequired indicates a write was attempted without a tenant id.
var ErrTenantRequired = errors.New("sso: tenant id is required")

func validateTenant(tenantID string) error {
	if strings.TrimSpace(tenantID) == "" {
		return ErrTenantRequired
	}
	return nil
}

// SQLiteStore is a ConfigStore backed by SQLite (modernc.org/sqlite driver,
// registered as "sqlite"). It persists only public / non-secret configuration:
// the full config document is stored as JSON and, for OIDC, the client secret
// *reference id* is additionally projected into its own column. Plaintext
// client secrets, private keys, and passwords are never written.
type SQLiteStore struct {
	db          *sql.DB
	now         func() time.Time
	migrateOnce sync.Once
	migrateErr  error
}

// NewSQLiteStore returns a SQLiteStore over db. The required tables are created
// lazily (CREATE TABLE IF NOT EXISTS) on first use; callers may also invoke
// Migrate explicitly.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db, now: time.Now}
}

// Migrate creates the SSO config tables if they do not already exist.
func (s *SQLiteStore) Migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sso: sqlite store is not configured")
	}
	s.migrateOnce.Do(func() {
		const schema = `
CREATE TABLE IF NOT EXISTS gateway_sso_oidc_config (
	tenant_id TEXT PRIMARY KEY,
	issuer TEXT NOT NULL,
	client_id TEXT NOT NULL,
	client_secret_ref TEXT NOT NULL DEFAULT '',
	config_json TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS gateway_sso_saml_config (
	tenant_id TEXT PRIMARY KEY,
	entity_id TEXT NOT NULL,
	idp_sso_url TEXT NOT NULL,
	config_json TEXT NOT NULL,
	updated_at TEXT NOT NULL
);`
		_, s.migrateErr = s.db.ExecContext(ctx, schema)
	})
	return s.migrateErr
}

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sso: sqlite store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	return s.Migrate(ctx)
}

func (s *SQLiteStore) SetOIDC(ctx context.Context, tenantID string, cfg OIDCConfig) error {
	if err := validateTenant(tenantID); err != nil {
		return err
	}
	if err := s.Migrate(ctx); err != nil {
		return err
	}
	blob, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO gateway_sso_oidc_config (tenant_id, issuer, client_id, client_secret_ref, config_json, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id) DO UPDATE SET
	issuer = excluded.issuer,
	client_id = excluded.client_id,
	client_secret_ref = excluded.client_secret_ref,
	config_json = excluded.config_json,
	updated_at = excluded.updated_at`,
		tenantID, cfg.Issuer, cfg.ClientID, cfg.ClientSecretRef, string(blob), formatSQLiteTime(s.now()))
	return err
}

func (s *SQLiteStore) GetOIDC(ctx context.Context, tenantID string) (OIDCConfig, bool, error) {
	if err := s.Migrate(ctx); err != nil {
		return OIDCConfig{}, false, err
	}
	var blob string
	err := s.db.QueryRowContext(ctx, `SELECT config_json FROM gateway_sso_oidc_config WHERE tenant_id = ?`, tenantID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return OIDCConfig{}, false, nil
	}
	if err != nil {
		return OIDCConfig{}, false, err
	}
	var cfg OIDCConfig
	if err := json.Unmarshal([]byte(blob), &cfg); err != nil {
		return OIDCConfig{}, false, err
	}
	return cfg, true, nil
}

func (s *SQLiteStore) ListOIDC(ctx context.Context) ([]StoredOIDC, error) {
	if err := s.Migrate(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id, config_json, updated_at FROM gateway_sso_oidc_config ORDER BY tenant_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredOIDC{}
	for rows.Next() {
		var tenantID, blob, updatedAt string
		if err := rows.Scan(&tenantID, &blob, &updatedAt); err != nil {
			return nil, err
		}
		var cfg OIDCConfig
		if err := json.Unmarshal([]byte(blob), &cfg); err != nil {
			return nil, err
		}
		out = append(out, StoredOIDC{TenantID: tenantID, Config: cfg, UpdatedAt: parseSQLiteTime(updatedAt)})
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SetSAML(ctx context.Context, tenantID string, cfg SAMLConfig) error {
	if err := validateTenant(tenantID); err != nil {
		return err
	}
	if err := s.Migrate(ctx); err != nil {
		return err
	}
	blob, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO gateway_sso_saml_config (tenant_id, entity_id, idp_sso_url, config_json, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (tenant_id) DO UPDATE SET
	entity_id = excluded.entity_id,
	idp_sso_url = excluded.idp_sso_url,
	config_json = excluded.config_json,
	updated_at = excluded.updated_at`,
		tenantID, cfg.EntityID, cfg.IdPSSOURL, string(blob), formatSQLiteTime(s.now()))
	return err
}

func (s *SQLiteStore) GetSAML(ctx context.Context, tenantID string) (SAMLConfig, bool, error) {
	if err := s.Migrate(ctx); err != nil {
		return SAMLConfig{}, false, err
	}
	var blob string
	err := s.db.QueryRowContext(ctx, `SELECT config_json FROM gateway_sso_saml_config WHERE tenant_id = ?`, tenantID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return SAMLConfig{}, false, nil
	}
	if err != nil {
		return SAMLConfig{}, false, err
	}
	var cfg SAMLConfig
	if err := json.Unmarshal([]byte(blob), &cfg); err != nil {
		return SAMLConfig{}, false, err
	}
	return cfg, true, nil
}

func (s *SQLiteStore) ListSAML(ctx context.Context) ([]StoredSAML, error) {
	if err := s.Migrate(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id, config_json, updated_at FROM gateway_sso_saml_config ORDER BY tenant_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredSAML{}
	for rows.Next() {
		var tenantID, blob, updatedAt string
		if err := rows.Scan(&tenantID, &blob, &updatedAt); err != nil {
			return nil, err
		}
		var cfg SAMLConfig
		if err := json.Unmarshal([]byte(blob), &cfg); err != nil {
			return nil, err
		}
		out = append(out, StoredSAML{TenantID: tenantID, Config: cfg, UpdatedAt: parseSQLiteTime(updatedAt)})
	}
	return out, rows.Err()
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
