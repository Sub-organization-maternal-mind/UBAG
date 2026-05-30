-- UBAG gateway SQLite schema.
--
-- This mirrors migrations/postgres/0001_gateway_stores.sql,
-- 0002_artifact_metadata.sql and 0003_webhook_outbox.sql using the SQLite
-- dialect. JSON values are stored as TEXT; timestamps are RFC3339 UTC strings
-- with fixed millisecond precision so they remain lexically sortable.

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS gateway_schema_migrations (
  version TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  checksum TEXT NOT NULL,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

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
  updated_at TEXT NOT NULL
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

CREATE INDEX IF NOT EXISTS idx_gateway_webhook_deliveries_due
  ON gateway_webhook_deliveries (status, next_attempt_at, created_at, id);

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

-- v2.1 multi-tab browser topology: browser instances -> provider contexts ->
-- channel tabs, plus a v2.0 compatibility session table. Mirrors
-- migrations/postgres/0004_browser_topology.sql in SQLite dialect.
CREATE TABLE IF NOT EXISTS gateway_browser_instances (
  instance_id     TEXT PRIMARY KEY,
  worker_id       TEXT NOT NULL,
  tenant_id       TEXT NOT NULL,
  engine          TEXT NOT NULL DEFAULT 'chromium' CHECK (
    engine IN ('chromium', 'firefox', 'webkit', 'bidi')
  ),
  remote_endpoint TEXT,
  state           TEXT NOT NULL DEFAULT 'starting',
  context_count   INTEGER NOT NULL DEFAULT 0,
  tab_count       INTEGER NOT NULL DEFAULT 0,
  rss_bytes       INTEGER,
  created_at      TEXT NOT NULL,
  recycle_at      TEXT
);

CREATE INDEX IF NOT EXISTS idx_gateway_browser_instances_tenant_state
  ON gateway_browser_instances (tenant_id, state);

CREATE TABLE IF NOT EXISTS gateway_provider_contexts (
  context_id         TEXT PRIMARY KEY,
  instance_id        TEXT NOT NULL REFERENCES gateway_browser_instances(instance_id) ON DELETE CASCADE,
  tenant_id          TEXT NOT NULL,
  target_id          TEXT NOT NULL,
  identity_ref       TEXT NOT NULL,
  login_state        TEXT NOT NULL DEFAULT 'unknown',
  conversation_model TEXT NOT NULL DEFAULT 'url' CHECK (
    conversation_model IN ('url', 'tabbed', 'spa-singleton')
  ),
  fingerprint_id     TEXT,
  proxy_id           TEXT,
  storage_state_uri  TEXT,
  max_tabs           INTEGER NOT NULL DEFAULT 2,
  created_at         TEXT NOT NULL,
  last_health_at     TEXT,
  recycle_at         TEXT,
  UNIQUE (tenant_id, target_id, identity_ref)
);

CREATE INDEX IF NOT EXISTS idx_gateway_provider_contexts_instance
  ON gateway_provider_contexts (instance_id);

CREATE TABLE IF NOT EXISTS gateway_browser_tabs (
  tab_id          TEXT PRIMARY KEY,
  context_id      TEXT NOT NULL REFERENCES gateway_provider_contexts(context_id) ON DELETE CASCADE,
  state           TEXT NOT NULL DEFAULT 'warming' CHECK (
    state IN ('warming', 'ready', 'busy', 'draining', 'quarantined', 'closed')
  ),
  conversation_id TEXT,
  current_job_id  TEXT,
  jobs_completed  INTEGER NOT NULL DEFAULT 0,
  rss_bytes       INTEGER,
  last_health_at  TEXT,
  created_at      TEXT NOT NULL,
  recycle_at      TEXT
);

CREATE INDEX IF NOT EXISTS idx_gateway_browser_tabs_context_state
  ON gateway_browser_tabs (context_id, state);

CREATE INDEX IF NOT EXISTS idx_gateway_browser_tabs_conversation
  ON gateway_browser_tabs (conversation_id)
  WHERE conversation_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS gateway_browser_sessions (
  session_id      TEXT PRIMARY KEY,
  tenant_id       TEXT NOT NULL,
  target_id       TEXT NOT NULL,
  worker_id       TEXT NOT NULL,
  profile_dir     TEXT NOT NULL,
  state           TEXT NOT NULL,
  login_state     TEXT NOT NULL,
  current_job_id  TEXT,
  jobs_completed  INTEGER NOT NULL DEFAULT 0,
  last_health_at  TEXT,
  recycle_at      TEXT,
  created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_gateway_browser_sessions_tenant_state
  ON gateway_browser_sessions (tenant_id, state);

CREATE TABLE IF NOT EXISTS gateway_alerts (
  alert_id    TEXT PRIMARY KEY,
  tenant_id   TEXT NOT NULL,
  app_id      TEXT NOT NULL DEFAULT '',
  job_id      TEXT NOT NULL DEFAULT '',
  session_id  TEXT NOT NULL DEFAULT '',
  target_id   TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL,
  message     TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'open',
  created_at  TEXT NOT NULL,
  notified_at TEXT NOT NULL DEFAULT '',
  acked_at    TEXT NOT NULL DEFAULT '',
  resolved_at TEXT NOT NULL DEFAULT '',
  attributes  TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_gateway_alerts_tenant_created
  ON gateway_alerts (tenant_id, created_at);

CREATE INDEX IF NOT EXISTS idx_gateway_alerts_active
  ON gateway_alerts (tenant_id, job_id, kind, status);

INSERT OR IGNORE INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0001', 'gateway_stores', 'manual-v0-sqlite');
INSERT OR IGNORE INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0002', 'artifact_metadata', 'manual-v0-sqlite-artifacts');
INSERT OR IGNORE INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0003', 'webhook_outbox', 'manual-v0-sqlite-webhooks');
INSERT OR IGNORE INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0004', 'browser_topology', 'manual-v0-sqlite-browser-topology');
INSERT OR IGNORE INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0005', 'alerts', 'manual-v0-sqlite-alerts');
