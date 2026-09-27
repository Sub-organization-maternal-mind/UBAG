-- Migration 0016: webhook delivery leased-lease index
--
-- The webhook worker's expired-lease recovery predicate
-- (status = 'leased' AND leased_until <= ?) had no covering index: the partial
-- due index leads with status but is ordered for next_attempt_at scans, and
-- only a leased row carries a non-NULL leased_until, so the recovery branch
-- scanned the leased subset on every sweep.
--
-- Mirrors idx_gateway_webhook_deliveries_leased_until in
-- migrations/sqlite/0010_webhook_delivery_indexes.sql. Partial on
-- leased_until IS NOT NULL so terminal rows stay out of the index.
-- Apply after 0015_idempotency_lock_columns.sql.

CREATE INDEX IF NOT EXISTS idx_gateway_webhook_deliveries_leased_until
  ON gateway_webhook_deliveries (leased_until)
  WHERE leased_until IS NOT NULL;

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0016', 'webhook_delivery_indexes', '', now())
ON CONFLICT (version) DO NOTHING;
