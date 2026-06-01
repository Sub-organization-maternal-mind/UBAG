-- Migration 0009: tenant home-region pin
--
-- Adds the gateway_tenants table which stores per-tenant configuration.
-- The home_region column is NULL for unpinned tenants (single-region or
-- auto-routed); a non-NULL value pins all traffic for that tenant to the
-- named region regardless of where the request arrives.
--
-- Apply after 0008_blueprint_schema.sql.

CREATE TABLE IF NOT EXISTS gateway_tenants (
  tenant_id   TEXT        PRIMARY KEY,
  home_region TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0009', 'tenant_home_region', '', now())
ON CONFLICT (version) DO NOTHING;
