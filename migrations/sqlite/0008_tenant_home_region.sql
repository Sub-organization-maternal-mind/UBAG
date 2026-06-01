-- Migration 0008: tenant home-region pin (SQLite edge-tier dialect)
--
-- Adds the gateway_tenants table which stores per-tenant configuration.
-- The home_region column is NULL for unpinned tenants (single-region or
-- auto-routed); a non-NULL value pins all traffic for that tenant to the
-- named region regardless of where the request arrives.
--
-- SQLite notes vs PostgreSQL:
--   • No TIMESTAMPTZ — use TEXT in ISO-8601 format (CURRENT_TIMESTAMP).
--
-- Apply after 0007_blueprint_schema.sql.

CREATE TABLE IF NOT EXISTS gateway_tenants (
  tenant_id   TEXT    PRIMARY KEY,
  home_region TEXT,
  created_at  TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at  TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Record this migration
INSERT OR IGNORE INTO edge_schema_migrations (version, name, applied_at)
VALUES ('0008', 'tenant_home_region', strftime('%Y-%m-%dT%H:%M:%SZ','now'));
