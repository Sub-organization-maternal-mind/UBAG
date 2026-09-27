-- Migration 0012: gateway_jobs list-scan indexes (SQLite edge-tier dialect)
--
-- Every existing gateway_jobs index that covers created_at sorts it DESC:
--   idx_gateway_jobs_tenant_app_created (tenant_id, app_id, created_at DESC, id)
--   idx_gateway_jobs_created            (created_at DESC, id)
-- but the jobs List queries in internal/jobs ORDER BY created_at ASC, id ASC
-- (and created_at DESC, id DESC for the descending list sort). A backward scan
-- over a (created_at DESC, id) index inverts the id tiebreak to DESC, so ASC
-- pagination over jobs sharing a created_at is not deterministic and the
-- tenant-scoped list cannot use the tenant index for its ordering at all.
--
-- These ascending mirrors serve the exact list shapes:
--   * /v1/jobs and the gRPC facade: WHERE tenant_id = ? [AND app_id/status/target]
--     ORDER BY created_at ASC/DESC, id ASC/DESC LIMIT ? (cursor-resolved
--     store-side via a (created_at, id) row-value tuple compare).
--   * Operator collections and the reaper: ORDER BY created_at with no tenant
--     predicate.
--
-- Apply after 0011_gateway_core_tables.sql, which creates gateway_jobs (this
-- file used to be numbered 0009 and assumed an already-bootstrapped database).
-- Idempotent: CREATE INDEX IF NOT EXISTS, so a database bootstrapped by the
-- gateway's embedded schema (internal/sqlitestore/schema.sql, which carries
-- the same indexes) skips them.

CREATE INDEX IF NOT EXISTS idx_gateway_jobs_tenant_app_created_asc
  ON gateway_jobs (tenant_id, app_id, created_at ASC, id ASC);

CREATE INDEX IF NOT EXISTS idx_gateway_jobs_created_asc
  ON gateway_jobs (created_at ASC, id ASC);

INSERT OR IGNORE INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0012', 'gateway_jobs_list_indexes', 'manual-v0-sqlite-jobs-list-indexes');
