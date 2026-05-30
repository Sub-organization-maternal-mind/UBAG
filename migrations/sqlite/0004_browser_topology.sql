-- Migration 0004: browser_topology
-- v2.1 multi-tab browser topology: browser instances -> provider contexts -> channel tabs,
-- plus the browser_sessions compatibility table (v2.0 shape retained for backwards-compat).
-- SQLite parity of migrations/postgres/0004_browser_topology.sql. Timestamps are
-- RFC3339 UTC strings; integers use INTEGER.
-- Apply after 0003_webhook_outbox.sql.

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

-- v2.0 compatibility table (single-tab session shape) retained for backwards-compat.
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

INSERT OR IGNORE INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0004', 'browser_topology', 'manual-v0-sqlite-browser-topology');
