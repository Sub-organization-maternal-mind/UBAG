-- Migration 0005: enterprise_stores
-- Native Postgres tables for the enterprise subsystems that previously fell back to
-- in-memory storage under UBAG_GATEWAY_STORE=postgres: response cache, workflow
-- definitions/runs, SSO config, SCIM users/groups, SIEM sink configs, and webhook
-- signing-secret rotations. Apply after 0004_browser_topology.sql.
--
-- Only non-secret material is persisted: SSO stores client-secret *references*,
-- SCIM never persists the password attribute, SIEM stores secret references, and
-- webhook secret rotations store opaque secret references.

-- Response cache ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS gateway_response_cache (
  cache_key   TEXT        NOT NULL,
  tenant_id   TEXT        NOT NULL,
  app_id      TEXT        NOT NULL,
  target      TEXT        NOT NULL DEFAULT '',
  command     TEXT        NOT NULL DEFAULT '',
  input_hash  TEXT        NOT NULL DEFAULT '',
  value       BYTEA,
  created_at  TIMESTAMPTZ NOT NULL,
  expires_at  TIMESTAMPTZ,
  PRIMARY KEY (tenant_id, app_id, cache_key)
);

CREATE INDEX IF NOT EXISTS idx_gateway_response_cache_scope
  ON gateway_response_cache (tenant_id, app_id, created_at DESC, cache_key);

CREATE TABLE IF NOT EXISTS gateway_response_cache_stats (
  tenant_id TEXT   NOT NULL,
  app_id    TEXT   NOT NULL,
  hits      BIGINT NOT NULL DEFAULT 0,
  misses    BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, app_id)
);

-- Workflow orchestration ----------------------------------------------------
CREATE TABLE IF NOT EXISTS gateway_workflow_definitions (
  id         TEXT        PRIMARY KEY,
  tenant_id  TEXT        NOT NULL,
  app_id     TEXT        NOT NULL,
  name       TEXT        NOT NULL,
  steps_json JSONB       NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_gateway_workflow_definitions_scope
  ON gateway_workflow_definitions (tenant_id, app_id, created_at, id);

CREATE TABLE IF NOT EXISTS gateway_workflow_runs (
  id              TEXT        PRIMARY KEY,
  definition_id   TEXT        NOT NULL,
  tenant_id       TEXT        NOT NULL,
  app_id          TEXT        NOT NULL,
  state           TEXT        NOT NULL,
  current_step    INTEGER     NOT NULL,
  steps_json      JSONB       NOT NULL,
  idempotency_key TEXT        NOT NULL DEFAULT '',
  created_at      TIMESTAMPTZ NOT NULL,
  updated_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_gateway_workflow_runs_scope
  ON gateway_workflow_runs (tenant_id, app_id, created_at, id);

CREATE UNIQUE INDEX IF NOT EXISTS uq_gateway_workflow_runs_idem
  ON gateway_workflow_runs (tenant_id, app_id, idempotency_key)
  WHERE idempotency_key <> '';

-- SSO configuration ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS gateway_sso_oidc_config (
  tenant_id         TEXT        PRIMARY KEY,
  issuer            TEXT        NOT NULL,
  client_id         TEXT        NOT NULL,
  client_secret_ref TEXT        NOT NULL DEFAULT '',
  config_json       JSONB       NOT NULL,
  updated_at        TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS gateway_sso_saml_config (
  tenant_id   TEXT        PRIMARY KEY,
  entity_id   TEXT        NOT NULL,
  idp_sso_url TEXT        NOT NULL,
  config_json JSONB       NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL
);

-- SCIM provisioning ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS gateway_scim_users (
  tenant_id    TEXT        NOT NULL,
  id           TEXT        NOT NULL,
  user_name    TEXT        NOT NULL,
  external_id  TEXT,
  display_name TEXT        NOT NULL DEFAULT '',
  active       INTEGER     NOT NULL DEFAULT 1,
  emails_json  TEXT        NOT NULL DEFAULT '[]',
  groups_json  TEXT        NOT NULL DEFAULT '[]',
  version      TEXT        NOT NULL DEFAULT '',
  created_at   TEXT        NOT NULL,
  updated_at   TEXT        NOT NULL,
  PRIMARY KEY (tenant_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS gateway_scim_users_username
  ON gateway_scim_users (tenant_id, user_name);

CREATE UNIQUE INDEX IF NOT EXISTS gateway_scim_users_externalid
  ON gateway_scim_users (tenant_id, external_id)
  WHERE external_id IS NOT NULL AND external_id <> '';

CREATE TABLE IF NOT EXISTS gateway_scim_groups (
  tenant_id    TEXT  NOT NULL,
  id           TEXT  NOT NULL,
  display_name TEXT  NOT NULL,
  external_id  TEXT,
  members_json TEXT  NOT NULL DEFAULT '[]',
  version      TEXT  NOT NULL DEFAULT '',
  created_at   TEXT  NOT NULL,
  updated_at   TEXT  NOT NULL,
  PRIMARY KEY (tenant_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS gateway_scim_groups_externalid
  ON gateway_scim_groups (tenant_id, external_id)
  WHERE external_id IS NOT NULL AND external_id <> '';

-- SIEM sink configuration ---------------------------------------------------
CREATE TABLE IF NOT EXISTS gateway_siem_sink_configs (
  id         TEXT        PRIMARY KEY,
  tenant_id  TEXT        NOT NULL,
  name       TEXT        NOT NULL DEFAULT '',
  kind       TEXT        NOT NULL CHECK (kind IN ('file', 'http', 'syslog')),
  target     TEXT        NOT NULL,
  network    TEXT        NOT NULL DEFAULT '',
  secret_ref TEXT        NOT NULL DEFAULT '',
  enabled    INTEGER     NOT NULL DEFAULT 1,
  created_at TEXT        NOT NULL,
  updated_at TEXT        NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_gateway_siem_sink_configs_tenant
  ON gateway_siem_sink_configs (tenant_id, id);

-- Webhook signing-secret rotations ------------------------------------------
CREATE TABLE IF NOT EXISTS webhook_secret_rotations (
  id                  TEXT        PRIMARY KEY,
  tenant_id           TEXT        NOT NULL,
  app_id              TEXT        NOT NULL,
  webhook_id          TEXT        NOT NULL,
  active_secret_ref   TEXT        NOT NULL,
  previous_secret_ref TEXT        NOT NULL DEFAULT '',
  overlap_until       TIMESTAMPTZ,
  created_at          TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_webhook_secret_scope
  ON webhook_secret_rotations (tenant_id, app_id, webhook_id, created_at DESC);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0005', 'enterprise_stores', '', now())
ON CONFLICT (version) DO NOTHING;
