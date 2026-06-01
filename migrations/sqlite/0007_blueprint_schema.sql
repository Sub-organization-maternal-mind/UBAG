-- Migration 0007: UBAG full blueprint §22 schema — SQLite edge-tier dialect.
--
-- SQLite limitations vs PostgreSQL:
--   • No PARTITION BY RANGE — automation_jobs is a single table (edge tier has
--     low volume; partitioning is not needed).
--   • No pgvector / HNSW — semantic_cache stores the embedding as a JSON array
--     TEXT column; similarity search uses brute-force cosine in the application
--     layer (acceptable at edge scale, ~1k cache entries). sqlite-vss is an
--     optional future upgrade path.
--   • No pg_partman, no BIGSERIAL (use INTEGER PRIMARY KEY AUTOINCREMENT).
--   • No TIMESTAMPTZ — use TEXT in ISO-8601 format (CURRENT_TIMESTAMP).
--   • No CHECK constraints on TEXT columns in SQLite < 3.37; inline where
--     supported but not relied on for correctness.
--
-- All tables use IF NOT EXISTS for idempotent application.

-- ---------------------------------------------------------------------------
-- Identity hierarchy
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS tenants (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  external_id TEXT    UNIQUE NOT NULL,
  name        TEXT    NOT NULL,
  plan        TEXT    NOT NULL DEFAULT 'free',
  data_region TEXT    NOT NULL DEFAULT 'default',
  created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  deleted_at  TEXT
);

CREATE TABLE IF NOT EXISTS projects (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant_id   INTEGER NOT NULL REFERENCES tenants(id),
  external_id TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  environment TEXT    NOT NULL,
  created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  UNIQUE (tenant_id, external_id)
);

CREATE TABLE IF NOT EXISTS apps (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id     INTEGER NOT NULL REFERENCES projects(id),
  app_id         TEXT    UNIQUE NOT NULL,
  app_name       TEXT    NOT NULL,
  platform_types TEXT    NOT NULL DEFAULT '[]',  -- JSON array as TEXT
  status         TEXT    NOT NULL DEFAULT 'enabled',
  metadata       TEXT    NOT NULL DEFAULT '{}',
  created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE TABLE IF NOT EXISTS app_credentials (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  app_id            INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  kind              TEXT    NOT NULL,
  secret_prefix     TEXT    NOT NULL,
  secret_hash       TEXT    NOT NULL,
  secret_ciphertext BLOB,
  scopes            TEXT    NOT NULL DEFAULT '[]',
  created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  expires_at        TEXT,
  revoked_at        TEXT
);
CREATE INDEX IF NOT EXISTS idx_app_credentials_app ON app_credentials(app_id);

CREATE TABLE IF NOT EXISTS devices (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  app_id           INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  device_id        TEXT    NOT NULL,
  device_name      TEXT,
  os               TEXT,
  app_version      TEXT,
  fingerprint_hash TEXT,
  last_seen_at     TEXT,
  revoked_at       TEXT,
  UNIQUE (app_id, device_id)
);

-- ---------------------------------------------------------------------------
-- Targets and adapters
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS targets (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  name           TEXT    UNIQUE NOT NULL,
  display_name   TEXT    NOT NULL,
  category       TEXT    NOT NULL,
  homepage_url   TEXT,
  enabled        INTEGER NOT NULL DEFAULT 1,
  requires_login INTEGER NOT NULL DEFAULT 1,
  capabilities   TEXT    NOT NULL DEFAULT '{}',
  metadata       TEXT    NOT NULL DEFAULT '{}',
  created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE TABLE IF NOT EXISTS adapters (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  target_id      INTEGER NOT NULL REFERENCES targets(id),
  version        TEXT    NOT NULL,
  module_path    TEXT    NOT NULL,
  manifest       TEXT    NOT NULL,
  is_active      INTEGER NOT NULL DEFAULT 0,
  canary_percent INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  UNIQUE (target_id, version)
);

CREATE TABLE IF NOT EXISTS app_target_permissions (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  app_id                INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  target_id             INTEGER NOT NULL REFERENCES targets(id),
  allowed_command_types TEXT    NOT NULL DEFAULT '[]',
  rate_limit_per_minute INTEGER NOT NULL DEFAULT 60,
  daily_quota           INTEGER,
  UNIQUE (app_id, target_id)
);

-- ---------------------------------------------------------------------------
-- Prompt templates
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS prompt_templates (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  template_id   TEXT    NOT NULL,
  version       TEXT    NOT NULL,
  content       TEXT    NOT NULL,
  input_schema  TEXT    NOT NULL,
  output_schema TEXT,
  metadata      TEXT    NOT NULL DEFAULT '{}',
  is_active     INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  UNIQUE (template_id, version)
);
CREATE INDEX IF NOT EXISTS idx_prompt_templates_active ON prompt_templates(template_id, is_active);

-- ---------------------------------------------------------------------------
-- Jobs — single table (no partitioning at edge scale)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS automation_jobs (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id            TEXT    UNIQUE NOT NULL,
  tenant_id         INTEGER NOT NULL,
  app_id            INTEGER NOT NULL,
  device_id         INTEGER,
  user_ref          TEXT,
  target_id         INTEGER NOT NULL,
  adapter_version   TEXT,
  command_type      TEXT    NOT NULL,
  template_id       TEXT,
  template_version  TEXT,
  conversation_id   TEXT,
  idempotency_key   TEXT,
  priority          TEXT    NOT NULL DEFAULT 'normal',
  status            TEXT    NOT NULL,
  -- Structured result envelope (blueprint §6.2)
  output_text       TEXT,
  output_markdown   TEXT,
  output_plain_text TEXT,
  output_sections   TEXT,   -- JSON
  output_html       TEXT,
  output_validation TEXT,   -- JSON
  cached            INTEGER NOT NULL DEFAULT 0,
  cache_source      TEXT,
  -- Full input/options for replay
  input             TEXT    NOT NULL,  -- JSON
  options           TEXT,              -- JSON
  -- Error fields
  error_code        TEXT,
  error_message     TEXT,
  -- Metadata
  retries           INTEGER NOT NULL DEFAULT 0,
  trace_id          TEXT,
  cost_credits      REAL,
  browser_session_id TEXT,
  adapter_name      TEXT,
  worker_id         TEXT,
  -- Timestamps
  created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  queued_at         TEXT,
  started_at        TEXT,
  completed_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_automation_jobs_app_created
  ON automation_jobs(app_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_automation_jobs_tenant_status
  ON automation_jobs(tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_automation_jobs_job_id
  ON automation_jobs(job_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_jobs_idempotency
  ON automation_jobs(app_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS automation_job_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id     TEXT    NOT NULL,
  seq        INTEGER NOT NULL,
  event_type TEXT    NOT NULL,
  message    TEXT,
  metadata   TEXT,   -- JSON
  created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_automation_job_events_job_seq
  ON automation_job_events(job_id, seq);

-- ---------------------------------------------------------------------------
-- Webhooks
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS webhook_endpoints (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  app_id        INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  url           TEXT    NOT NULL,
  events        TEXT    NOT NULL DEFAULT '["*"]',
  secret_id     INTEGER REFERENCES app_credentials(id),
  enabled       INTEGER NOT NULL DEFAULT 1,
  circuit_state TEXT    NOT NULL DEFAULT 'closed',
  created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  endpoint_id     INTEGER NOT NULL REFERENCES webhook_endpoints(id),
  job_id          TEXT,
  event_type      TEXT    NOT NULL,
  payload         TEXT    NOT NULL,  -- JSON
  attempt         INTEGER NOT NULL DEFAULT 0,
  status          TEXT    NOT NULL DEFAULT 'pending',
  http_status     INTEGER,
  response_body   TEXT,
  next_attempt_at TEXT,
  delivered_at    TEXT,
  created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_next
  ON webhook_deliveries(next_attempt_at) WHERE status = 'pending';

-- ---------------------------------------------------------------------------
-- Browser topology
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS browser_instances (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  instance_id     TEXT    UNIQUE NOT NULL,
  worker_id       TEXT    NOT NULL,
  tenant_id       INTEGER NOT NULL,
  engine          TEXT    NOT NULL DEFAULT 'chromium',
  remote_endpoint TEXT,
  state           TEXT    NOT NULL DEFAULT 'starting',
  context_count   INTEGER NOT NULL DEFAULT 0,
  tab_count       INTEGER NOT NULL DEFAULT 0,
  rss_bytes       INTEGER,
  created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  recycle_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_browser_instances_tenant ON browser_instances(tenant_id, state);

CREATE TABLE IF NOT EXISTS provider_contexts (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  context_id         TEXT    UNIQUE NOT NULL,
  instance_id        TEXT    NOT NULL REFERENCES browser_instances(instance_id) ON DELETE CASCADE,
  tenant_id          INTEGER NOT NULL,
  target_id          INTEGER NOT NULL,
  identity_ref       TEXT    NOT NULL,
  login_state        TEXT    NOT NULL DEFAULT 'unknown',
  conversation_model TEXT    NOT NULL DEFAULT 'url',
  fingerprint_id     TEXT,
  proxy_id           TEXT,
  storage_state_uri  TEXT,
  max_tabs           INTEGER NOT NULL DEFAULT 2,
  created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  last_health_at     TEXT,
  recycle_at         TEXT,
  UNIQUE (tenant_id, target_id, identity_ref)
);
CREATE INDEX IF NOT EXISTS idx_provider_contexts_instance ON provider_contexts(instance_id);

CREATE TABLE IF NOT EXISTS browser_tabs (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  tab_id          TEXT    UNIQUE NOT NULL,
  context_id      TEXT    NOT NULL REFERENCES provider_contexts(context_id) ON DELETE CASCADE,
  state           TEXT    NOT NULL DEFAULT 'warming',
  conversation_id TEXT,
  current_job_id  TEXT,
  jobs_completed  INTEGER NOT NULL DEFAULT 0,
  rss_bytes       INTEGER,
  last_health_at  TEXT,
  created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  recycle_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_browser_tabs_context_state ON browser_tabs(context_id, state);
CREATE INDEX IF NOT EXISTS idx_browser_tabs_conversation
  ON browser_tabs(conversation_id) WHERE conversation_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS browser_sessions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id      TEXT    UNIQUE NOT NULL,
  target_id       INTEGER NOT NULL,
  worker_id       TEXT    NOT NULL,
  profile_dir     TEXT    NOT NULL,
  state           TEXT    NOT NULL,
  login_state     TEXT    NOT NULL,
  current_job_id  TEXT,
  jobs_completed  INTEGER NOT NULL DEFAULT 0,
  last_health_at  TEXT,
  recycle_at      TEXT,
  created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- ---------------------------------------------------------------------------
-- Semantic cache — brute-force at edge scale; embedding stored as JSON array
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS semantic_cache (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant_id        INTEGER NOT NULL,
  target_id        INTEGER NOT NULL,
  template_id      TEXT,
  prompt_hash      TEXT    NOT NULL,
  prompt_embedding TEXT,   -- JSON array of 384 floats; cosine computed in app layer
  output           TEXT    NOT NULL,  -- JSON
  hits             INTEGER NOT NULL DEFAULT 0,
  created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  expires_at       TEXT
);
CREATE INDEX IF NOT EXISTS idx_semantic_cache_lookup
  ON semantic_cache(tenant_id, target_id, template_id, prompt_hash);
CREATE INDEX IF NOT EXISTS idx_semantic_cache_expiry
  ON semantic_cache(expires_at) WHERE expires_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Audit log (Merkle-chained)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS audit_log (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  ts            TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  actor_kind    TEXT    NOT NULL,
  actor_id      TEXT    NOT NULL,
  tenant_id     INTEGER,
  action        TEXT    NOT NULL,
  resource_kind TEXT    NOT NULL,
  resource_id   TEXT,
  request       TEXT,   -- JSON
  result        TEXT    NOT NULL,
  prev_hash     BLOB,
  this_hash     BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_log_tenant_ts ON audit_log(tenant_id, ts DESC);

-- ---------------------------------------------------------------------------
-- Transactional outbox
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS outbox_events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  topic        TEXT    NOT NULL,
  payload      TEXT    NOT NULL,  -- JSON
  created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  published_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_outbox_events_unpublished
  ON outbox_events(published_at, id);

-- ---------------------------------------------------------------------------
-- Record this migration
-- ---------------------------------------------------------------------------
INSERT OR IGNORE INTO edge_schema_migrations (version, name, applied_at)
VALUES ('0007', 'blueprint_schema', strftime('%Y-%m-%dT%H:%M:%SZ','now'));
