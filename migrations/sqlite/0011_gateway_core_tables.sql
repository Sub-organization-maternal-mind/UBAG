-- Migration 0011: gateway core tables (SQLite edge-tier dialect)
--
-- The migration chain's core tables. Every table definition and index here is
-- copied verbatim from the gateway's embedded runtime schema
-- (internal/sqlitestore/schema.sql), which is the source of truth: before this
-- migration the chain never created gateway_jobs, gateway_job_events,
-- gateway_job_worker_event_keys, gateway_job_id_seq,
-- gateway_idempotency_records, gateway_webhook_deliveries,
-- gateway_webhook_attempts or artifact_metadata, so the chain only worked on
-- databases the gateway had already bootstrapped and `ubag migrate --store
-- sqlite` failed on a fresh database at 0012 (indexes on gateway_jobs).
--
-- Idempotent: CREATE TABLE/INDEX IF NOT EXISTS, so a database bootstrapped by
-- the gateway's embedded schema skips every statement. The job/idempotency
-- shape-repair migrations for pre-existing databases remain in
-- internal/sqlitestore (migrate.go), which runs at gateway boot.
--
-- Apply after 0008_tenant_home_region.sql.

-- Emulates the Postgres gateway_job_id_seq sequence. Rows are inserted and
-- immediately deleted; AUTOINCREMENT guarantees monotonic, non-reused ids via
-- the sqlite_sequence table.
CREATE TABLE IF NOT EXISTS gateway_job_id_seq (
  seq INTEGER PRIMARY KEY AUTOINCREMENT
);

CREATE TABLE IF NOT EXISTS gateway_jobs (
  id TEXT PRIMARY KEY,
  api_version TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  app_id TEXT NOT NULL,
  idempotency_key TEXT,
  target TEXT NOT NULL,
  command_type TEXT NOT NULL,
  client_json TEXT,
  conversation_id TEXT,
  template_id TEXT,
  input_json TEXT,
  options_json TEXT,
  callbacks_json TEXT,
  context_json TEXT,
  status TEXT NOT NULL CHECK (
    status IN (
      'created',
      'scheduled',
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
  result_json TEXT,
  trace_id TEXT,
  retry_of TEXT,
  event_sequence INTEGER NOT NULL DEFAULT 0 CHECK (event_sequence >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  not_before TEXT
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

-- Operational-scan indexes: the stale-job reaper, the metrics counters and the
-- bare-status jobs list. See internal/sqlitestore/schema.sql for the full
-- rationale and the partial-index caveat.
CREATE INDEX IF NOT EXISTS idx_gateway_jobs_status_updated
  ON gateway_jobs (status, updated_at);

CREATE INDEX IF NOT EXISTS idx_gateway_jobs_created
  ON gateway_jobs (created_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_gateway_jobs_tenant_app_created_asc
  ON gateway_jobs (tenant_id, app_id, created_at ASC, id ASC);

CREATE INDEX IF NOT EXISTS idx_gateway_jobs_created_asc
  ON gateway_jobs (created_at ASC, id ASC);

CREATE TABLE IF NOT EXISTS gateway_job_events (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES gateway_jobs(id) ON DELETE CASCADE,
  api_version TEXT NOT NULL,
  type TEXT NOT NULL,
  sequence INTEGER NOT NULL CHECK (sequence >= 1),
  data_json TEXT NOT NULL DEFAULT '{}',
  trace_id TEXT,
  created_at TEXT NOT NULL,
  UNIQUE (job_id, sequence)
);

CREATE INDEX IF NOT EXISTS idx_gateway_job_events_job_sequence
  ON gateway_job_events (job_id, sequence);

CREATE INDEX IF NOT EXISTS idx_gateway_job_events_created
  ON gateway_job_events (created_at);

CREATE TABLE IF NOT EXISTS gateway_job_worker_event_keys (
  job_id TEXT NOT NULL REFERENCES gateway_jobs(id) ON DELETE CASCADE,
  event_key TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (job_id, event_key)
);

CREATE TABLE IF NOT EXISTS gateway_idempotency_records (
  tenant_id TEXT NOT NULL,
  app_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  -- in_flight | completed. NULL marks a row written before the column
  -- existed; those rows keep the legacy reserve-until-TTL behavior.
  status TEXT,
  -- Bounds the in-flight lock: once it passes, a new Reserve may take the
  -- key over (crashed-gateway recovery). NULL means no deadline.
  locked_until TEXT,
  resource_id TEXT,
  http_status INTEGER,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  PRIMARY KEY (tenant_id, app_id, operation, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_gateway_idempotency_expires
  ON gateway_idempotency_records (expires_at);

CREATE INDEX IF NOT EXISTS idx_gateway_idempotency_resource
  ON gateway_idempotency_records (resource_id)
  WHERE resource_id IS NOT NULL;

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
  replay_of TEXT REFERENCES gateway_webhook_deliveries(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  delivered_at TEXT,
  UNIQUE (tenant_id, app_id, dedupe_key)
);

-- Partial, matching idx_gateway_webhook_deliveries_due in
-- migrations/postgres/0003_webhook_outbox.sql: only the three leaseable
-- statuses are ever scanned for due deliveries.
CREATE INDEX IF NOT EXISTS idx_gateway_webhook_deliveries_due
  ON gateway_webhook_deliveries (status, next_attempt_at, created_at, id)
  WHERE status IN ('pending', 'retry_scheduled', 'leased');

-- Serves the expired-lease recovery predicate in the webhook worker's lease
-- query (status = 'leased' AND leased_until <= ?).
CREATE INDEX IF NOT EXISTS idx_gateway_webhook_deliveries_leased_until
  ON gateway_webhook_deliveries (leased_until)
  WHERE leased_until IS NOT NULL;

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
  duration_ms INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  UNIQUE (delivery_id, attempt_number)
);

CREATE INDEX IF NOT EXISTS idx_gateway_webhook_attempts_delivery
  ON gateway_webhook_attempts (delivery_id, attempt_number);

CREATE TABLE IF NOT EXISTS artifact_metadata (
  job_id TEXT NOT NULL,
  artifact_key TEXT NOT NULL,
  bucket TEXT NOT NULL DEFAULT 'ubag-artifacts',
  object_key TEXT,
  content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
  size_bytes INTEGER NOT NULL DEFAULT 0,
  checksum TEXT,
  created_at TEXT NOT NULL,
  PRIMARY KEY (job_id, artifact_key)
);

CREATE INDEX IF NOT EXISTS artifact_metadata_job_created_at_idx
  ON artifact_metadata (job_id, created_at DESC, artifact_key ASC);

INSERT OR IGNORE INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0011', 'gateway_core_tables', 'manual-v0-sqlite-gateway-core');
