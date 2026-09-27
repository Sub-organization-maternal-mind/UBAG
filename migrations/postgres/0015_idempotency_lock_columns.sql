-- Migration 0015: idempotency in-flight lock columns
--
-- Gives gateway_idempotency_records explicit in-flight semantics:
--   status       'in_flight' while the reserved operation is running,
--                'completed' once Complete lands. NULL marks rows written
--                before this migration; those keep the legacy
--                reserve-until-TTL behavior.
--   locked_until Bounds the in-flight lock (5 minutes from Reserve). Once it
--                passes, a new Reserve may take the key over even with a
--                different payload hash — the recovery path for a gateway
--                that crashed between Reserve and Complete/Release.
--
-- Complete and Release now compare-and-set on request_hash, so a stale
-- completion or release can no longer be attributed to a record a different
-- payload took over.
--
-- Additive only: existing rows are preserved and their new columns are NULL.
-- Apply after 0014_gateway_jobs_list_indexes.sql.

ALTER TABLE gateway_idempotency_records ADD COLUMN IF NOT EXISTS status TEXT;

ALTER TABLE gateway_idempotency_records ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0015', 'idempotency_lock_columns', '', now())
ON CONFLICT (version) DO NOTHING;
