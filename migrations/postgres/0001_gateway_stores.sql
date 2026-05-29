-- UBAG small-profile Postgres gateway stores.
-- JSON values are JSONB; timestamps are UTC timestamptz.

CREATE TABLE IF NOT EXISTS gateway_schema_migrations (
  version TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  checksum TEXT NOT NULL,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE SEQUENCE IF NOT EXISTS gateway_job_id_seq;

CREATE TABLE IF NOT EXISTS gateway_jobs (
  id TEXT PRIMARY KEY,
  api_version TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  app_id TEXT NOT NULL,
  idempotency_key TEXT,
  target TEXT NOT NULL,
  command_type TEXT NOT NULL,
  client_json JSONB,
  conversation_id TEXT,
  template_id TEXT,
  input_json JSONB,
  options_json JSONB,
  callbacks_json JSONB,
  context_json JSONB,
  status TEXT NOT NULL CHECK (
    status IN (
      'created',
      'queued',
      'assigned',
      'running',
      'token_streaming',
      'completing',
      'completed',
      'completed_with_warnings',
      'failed_retryable',
      'failed_terminal',
      'dead_letter',
      'cancelled',
      'timed_out'
    )
  ),
  result_json JSONB,
  trace_id TEXT,
  retry_of TEXT,
  event_sequence INTEGER NOT NULL DEFAULT 0 CHECK (event_sequence >= 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_gateway_jobs_tenant_app_created
  ON gateway_jobs (tenant_id, app_id, created_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_gateway_jobs_tenant_app_status
  ON gateway_jobs (tenant_id, app_id, status);

CREATE INDEX IF NOT EXISTS idx_gateway_jobs_tenant_app_target
  ON gateway_jobs (tenant_id, app_id, target);

CREATE INDEX IF NOT EXISTS idx_gateway_jobs_retry_of
  ON gateway_jobs (retry_of)
  WHERE retry_of IS NOT NULL;

CREATE TABLE IF NOT EXISTS gateway_job_events (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES gateway_jobs(id) ON DELETE CASCADE,
  api_version TEXT NOT NULL,
  type TEXT NOT NULL,
  sequence INTEGER NOT NULL CHECK (sequence >= 1),
  data_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  trace_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (job_id, sequence)
);

CREATE INDEX IF NOT EXISTS idx_gateway_job_events_job_sequence
  ON gateway_job_events (job_id, sequence);

CREATE INDEX IF NOT EXISTS idx_gateway_job_events_created
  ON gateway_job_events (created_at);

CREATE TABLE IF NOT EXISTS gateway_job_worker_event_keys (
  job_id TEXT NOT NULL REFERENCES gateway_jobs(id) ON DELETE CASCADE,
  event_key TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (job_id, event_key)
);

CREATE TABLE IF NOT EXISTS gateway_idempotency_records (
  tenant_id TEXT NOT NULL,
  app_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  resource_id TEXT,
  http_status INTEGER,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (tenant_id, app_id, operation, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_gateway_idempotency_expires
  ON gateway_idempotency_records (expires_at);

CREATE INDEX IF NOT EXISTS idx_gateway_idempotency_resource
  ON gateway_idempotency_records (resource_id)
  WHERE resource_id IS NOT NULL;

INSERT INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0001', 'gateway_stores', 'manual-v0-postgres')
ON CONFLICT (version) DO NOTHING;
