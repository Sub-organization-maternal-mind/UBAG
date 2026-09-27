-- Migration 0017: gateway_outbox_events
-- Moves the outbox table's DDL out of the store's runtime Ready() path into the
-- migration chain, where every other schema object lives. Previously
-- internal/outbox/postgres.go ran CREATE TABLE IF NOT EXISTS on every Ready()
-- call, so the table existed in no migration and a fresh database migrated by
-- the container entrypoint relied on the gateway process to create it (and a
-- mismatch between this DDL and the store's assertion would only surface at
-- boot). Apply after 0016_webhook_delivery_indexes.sql.
--
-- The store now asserts the table exists (storekit.RequirePostgresObject) and
-- fails closed if the migration has not been applied.

CREATE TABLE IF NOT EXISTS gateway_outbox_events (
	id           TEXT PRIMARY KEY,
	topic        TEXT NOT NULL,
	payload      BYTEA NOT NULL,
	created_at   TIMESTAMPTZ NOT NULL
);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0017', 'gateway_outbox_events', '', now())
ON CONFLICT (version) DO NOTHING;
