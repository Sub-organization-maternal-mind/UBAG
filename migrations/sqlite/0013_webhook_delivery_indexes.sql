-- Migration 0013: webhook delivery lease indexes (SQLite edge-tier dialect)
--
-- Two repairs for databases migrated by 0003_webhook_outbox.sql:
--
-- 1. The due index was created WITHOUT the partial predicate the Postgres
--    dialect carries, so every delivered/dead-lettered row (the overwhelming
--    majority of a long-lived outbox) is indexed although only
--    pending/retry_scheduled/leased rows are ever scanned for due deliveries.
--    SQLite cannot ALTER an index, so the non-partial one is dropped and the
--    partial form is created under the same name.
-- 2. There was no index on leased_until, so the worker's expired-lease
--    recovery predicate (status = 'leased' AND leased_until <= ?) scanned the
--    leased rows on every sweep.
--
-- The gateway's embedded runtime schema (internal/sqlitestore/schema.sql)
-- carries the same definitions for freshly bootstrapped databases; this
-- migration repairs pre-existing ones. Apply after
-- 0012_gateway_jobs_list_indexes.sql (0011_gateway_core_tables.sql creates
-- gateway_webhook_deliveries on a fresh database).

DROP INDEX IF EXISTS idx_gateway_webhook_deliveries_due;

CREATE INDEX IF NOT EXISTS idx_gateway_webhook_deliveries_due
  ON gateway_webhook_deliveries (status, next_attempt_at, created_at, id)
  WHERE status IN ('pending', 'retry_scheduled', 'leased');

CREATE INDEX IF NOT EXISTS idx_gateway_webhook_deliveries_leased_until
  ON gateway_webhook_deliveries (leased_until)
  WHERE leased_until IS NOT NULL;

INSERT OR IGNORE INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0013', 'webhook_delivery_indexes', 'manual-v0-sqlite-webhook-indexes');
