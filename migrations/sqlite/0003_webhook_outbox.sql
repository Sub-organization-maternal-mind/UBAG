-- Migration 0003: webhook_outbox
-- Edge/local parity schema for signed webhook delivery state.

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  app_id TEXT NOT NULL,
  job_id TEXT,
  event_name TEXT NOT NULL,
  endpoint_id TEXT NOT NULL,
  endpoint_kind TEXT NOT NULL DEFAULT 'job_callback',
  url TEXT NOT NULL,
  secret_id TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  trace_id TEXT,
  status TEXT NOT NULL CHECK (
    status IN ('pending', 'leased', 'retry_scheduled', 'delivered', 'dead_lettered', 'cancelled')
  ),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  max_attempts INTEGER NOT NULL DEFAULT 8 CHECK (max_attempts >= 1),
  next_attempt_at TEXT,
  lease_id TEXT,
  leased_until TEXT,
  last_http_status INTEGER,
  last_error_class TEXT,
  last_error_message TEXT,
  replay_of TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  delivered_at TEXT,
  UNIQUE (tenant_id, app_id, dedupe_key)
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_due
  ON webhook_deliveries (status, next_attempt_at, created_at, id);

CREATE TABLE IF NOT EXISTS webhook_delivery_attempts (
  id TEXT PRIMARY KEY,
  delivery_id TEXT NOT NULL REFERENCES webhook_deliveries(id) ON DELETE CASCADE,
  attempt_number INTEGER NOT NULL CHECK (attempt_number >= 1),
  status_code INTEGER,
  error_class TEXT,
  error_message TEXT,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  UNIQUE (delivery_id, attempt_number)
);

INSERT OR IGNORE INTO edge_schema_migrations (version, name, checksum)
VALUES ('0003', 'webhook_outbox', 'manual-v0-sqlite-webhooks');
