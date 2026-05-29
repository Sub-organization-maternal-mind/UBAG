-- Migration 0003: webhook_outbox
-- Durable signed webhook delivery outbox and attempt ledger.
-- Apply after 0001_gateway_stores.sql and before enabling webhook workers in Postgres mode.

CREATE TABLE IF NOT EXISTS gateway_webhook_deliveries (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  app_id TEXT NOT NULL,
  job_id TEXT REFERENCES gateway_jobs(id) ON DELETE SET NULL,
  event_name TEXT NOT NULL,
  endpoint_id TEXT NOT NULL,
  endpoint_kind TEXT NOT NULL DEFAULT 'job_callback',
  url TEXT NOT NULL,
  secret_id TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,
  payload_json JSONB NOT NULL,
  trace_id TEXT,
  status TEXT NOT NULL CHECK (
    status IN ('pending', 'leased', 'retry_scheduled', 'delivered', 'dead_lettered', 'cancelled')
  ),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  max_attempts INTEGER NOT NULL DEFAULT 8 CHECK (max_attempts >= 1),
  next_attempt_at TIMESTAMPTZ,
  lease_id TEXT,
  leased_until TIMESTAMPTZ,
  last_http_status INTEGER,
  last_error_class TEXT,
  last_error_message TEXT,
  replay_of TEXT REFERENCES gateway_webhook_deliveries(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  delivered_at TIMESTAMPTZ,
  UNIQUE (tenant_id, app_id, dedupe_key)
);

CREATE INDEX IF NOT EXISTS idx_gateway_webhook_deliveries_due
  ON gateway_webhook_deliveries (status, next_attempt_at, created_at, id)
  WHERE status IN ('pending', 'retry_scheduled', 'leased');

CREATE INDEX IF NOT EXISTS idx_gateway_webhook_deliveries_tenant_app
  ON gateway_webhook_deliveries (tenant_id, app_id, created_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_gateway_webhook_deliveries_job
  ON gateway_webhook_deliveries (job_id)
  WHERE job_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS gateway_webhook_attempts (
  id TEXT PRIMARY KEY,
  delivery_id TEXT NOT NULL REFERENCES gateway_webhook_deliveries(id) ON DELETE CASCADE,
  attempt_number INTEGER NOT NULL CHECK (attempt_number >= 1),
  status_code INTEGER,
  error_class TEXT,
  error_message TEXT,
  duration_ms BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (delivery_id, attempt_number)
);

CREATE INDEX IF NOT EXISTS idx_gateway_webhook_attempts_delivery
  ON gateway_webhook_attempts (delivery_id, attempt_number);

INSERT INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0003', 'webhook_outbox', 'manual-v0-postgres-webhooks')
ON CONFLICT (version) DO NOTHING;
