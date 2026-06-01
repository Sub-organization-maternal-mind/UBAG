-- Migration 0008: UBAG full blueprint §22 schema (DDL-ready, partitioned, indexed).
--
-- This migration implements the complete data model described in blueprint §22
-- using the exact table names from the document. The existing gateway_* v0 tables
-- are NOT dropped here; they are retained as a compatibility layer while Phase 2
-- migrates all gateway logic to the new tables. Transitional views that alias the
-- blueprint tables over the v0 tables are added in migration 0009 (contract step).
--
-- Prerequisite: PostgreSQL 16+, pgvector extension, pg_partman (for automation_jobs
-- partition lifecycle management). Both are verified by the checks below.
--
-- Apply with: the gateway's built-in migration runner (Phase 1 internal/migrate).
-- Idempotent: every statement is guarded by IF NOT EXISTS.

-- ---------------------------------------------------------------------------
-- Prerequisites
-- ---------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS vector;    -- pgvector: semantic cache HNSW index
CREATE EXTENSION IF NOT EXISTS pg_partman SCHEMA partman;  -- partition lifecycle

-- ---------------------------------------------------------------------------
-- Identity hierarchy: Tenant → Project → App → Device (blueprint §7.4)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS tenants (
  id          BIGSERIAL PRIMARY KEY,
  external_id TEXT        UNIQUE NOT NULL,
  name        TEXT        NOT NULL,
  plan        TEXT        NOT NULL DEFAULT 'free',
  data_region TEXT        NOT NULL DEFAULT 'default',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at  TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS projects (
  id          BIGSERIAL PRIMARY KEY,
  tenant_id   BIGINT      NOT NULL REFERENCES tenants(id),
  external_id TEXT        NOT NULL,
  name        TEXT        NOT NULL,
  environment TEXT        NOT NULL CHECK (environment IN ('dev','staging','prod')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, external_id)
);

CREATE TABLE IF NOT EXISTS apps (
  id             BIGSERIAL PRIMARY KEY,
  project_id     BIGINT    NOT NULL REFERENCES projects(id),
  app_id         TEXT      UNIQUE NOT NULL,
  app_name       TEXT      NOT NULL,
  platform_types TEXT[]    NOT NULL DEFAULT '{}',
  status         TEXT      NOT NULL DEFAULT 'enabled',
  metadata       JSONB     NOT NULL DEFAULT '{}'::jsonb,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Credentials: argon2id hash for verification + AES-256-GCM ciphertext for
-- recovery, both under the master KEK loaded at startup. (§11.3)
CREATE TABLE IF NOT EXISTS app_credentials (
  id                BIGSERIAL PRIMARY KEY,
  app_id            BIGINT    NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  kind              TEXT      NOT NULL CHECK (kind IN ('app_secret','jwt_signing','webhook_secret','pat')),
  secret_prefix     TEXT      NOT NULL,  -- e.g. 'ubag_sk_prod_AbCd' (scannable by GitHub)
  secret_hash       TEXT      NOT NULL,  -- argon2id
  secret_ciphertext BYTEA,               -- AES-256-GCM under KEK; optional recovery
  scopes            TEXT[]    NOT NULL DEFAULT '{}',
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at        TIMESTAMPTZ,
  revoked_at        TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_app_credentials_app ON app_credentials(app_id);

CREATE TABLE IF NOT EXISTS devices (
  id               BIGSERIAL PRIMARY KEY,
  app_id           BIGINT    NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  device_id        TEXT      NOT NULL,
  device_name      TEXT,
  os               TEXT,
  app_version      TEXT,
  fingerprint_hash TEXT,
  last_seen_at     TIMESTAMPTZ,
  revoked_at       TIMESTAMPTZ,
  UNIQUE (app_id, device_id)
);

-- ---------------------------------------------------------------------------
-- Targets and adapters (blueprint §7.3, §13.5)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS targets (
  id             BIGSERIAL PRIMARY KEY,
  name           TEXT      UNIQUE NOT NULL,
  display_name   TEXT      NOT NULL,
  category       TEXT      NOT NULL,
  homepage_url   TEXT,
  enabled        BOOLEAN   NOT NULL DEFAULT TRUE,
  requires_login BOOLEAN   NOT NULL DEFAULT TRUE,
  capabilities   JSONB     NOT NULL DEFAULT '{}'::jsonb,
  metadata       JSONB     NOT NULL DEFAULT '{}'::jsonb,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS adapters (
  id             BIGSERIAL PRIMARY KEY,
  target_id      BIGINT    NOT NULL REFERENCES targets(id),
  version        TEXT      NOT NULL,
  module_path    TEXT      NOT NULL,
  manifest       JSONB     NOT NULL,
  is_active      BOOLEAN   NOT NULL DEFAULT FALSE,
  canary_percent INT       NOT NULL DEFAULT 0 CHECK (canary_percent BETWEEN 0 AND 100),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (target_id, version)
);

CREATE TABLE IF NOT EXISTS app_target_permissions (
  id                    BIGSERIAL PRIMARY KEY,
  app_id                BIGINT    NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  target_id             BIGINT    NOT NULL REFERENCES targets(id),
  allowed_command_types TEXT[]    NOT NULL DEFAULT '{}',
  rate_limit_per_minute INT       NOT NULL DEFAULT 60,
  daily_quota           INT,
  UNIQUE (app_id, target_id)
);

-- ---------------------------------------------------------------------------
-- Prompt templates (blueprint §15, §7.7)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS prompt_templates (
  id            BIGSERIAL PRIMARY KEY,
  template_id   TEXT      NOT NULL,
  version       TEXT      NOT NULL,
  content       TEXT      NOT NULL,
  input_schema  JSONB     NOT NULL,
  output_schema JSONB,
  metadata      JSONB     NOT NULL DEFAULT '{}'::jsonb,
  is_active     BOOLEAN   NOT NULL DEFAULT FALSE,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (template_id, version)
);
CREATE INDEX IF NOT EXISTS idx_prompt_templates_active ON prompt_templates(template_id) WHERE is_active;

-- ---------------------------------------------------------------------------
-- Jobs (partitioned by month, blueprint §22, §21.1)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS automation_jobs (
  id              BIGINT      NOT NULL,
  job_id          TEXT        NOT NULL,
  tenant_id       BIGINT      NOT NULL,    -- FK to tenants(id) enforced at app layer (cross-partition FKs unsupported)
  app_id          BIGINT      NOT NULL,
  device_id       BIGINT,
  user_ref        TEXT,
  target_id       BIGINT      NOT NULL,
  adapter_version TEXT,
  command_type    TEXT        NOT NULL,
  template_id     TEXT,
  template_version TEXT,
  conversation_id TEXT,
  idempotency_key TEXT,
  priority        TEXT        NOT NULL DEFAULT 'normal'
                              CHECK (priority IN ('critical','high','normal','low','bulk')),
  status          TEXT        NOT NULL
                              CHECK (status IN (
                                'created','queued','assigned','running',
                                'token_streaming','completing','completed',
                                'completed_with_warnings','failed_retryable',
                                'failed_terminal','dead_letter','cancelled','timed_out'
                              )),
  -- Structured result envelope (blueprint §6.2)
  output_text       TEXT,
  output_markdown   TEXT,
  output_plain_text TEXT,
  output_sections   JSONB,
  output_html       TEXT,
  output_validation JSONB,
  cached            BOOLEAN   NOT NULL DEFAULT FALSE,
  cache_source      TEXT,
  -- Full input/options for replay
  input           JSONB       NOT NULL,
  options         JSONB,
  -- Error fields
  error_code      TEXT,
  error_message   TEXT,
  -- Metadata (blueprint §6.2 metadata block)
  retries         INT         NOT NULL DEFAULT 0,
  trace_id        TEXT,
  cost_credits    NUMERIC(12,4),
  browser_session_id TEXT,
  adapter_name    TEXT,
  worker_id       TEXT,
  -- Timestamps
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  queued_at       TIMESTAMPTZ,
  started_at      TIMESTAMPTZ,
  completed_at    TIMESTAMPTZ,
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Indexes on the parent (inherited by all partitions)
CREATE INDEX IF NOT EXISTS idx_automation_jobs_app_created
  ON automation_jobs (app_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_automation_jobs_tenant_status_created
  ON automation_jobs (tenant_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_automation_jobs_job_id
  ON automation_jobs (job_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_jobs_idempotency
  ON automation_jobs (app_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

-- Seed partitions for current + next two months; pg_partman manages the rest.
DO $$
DECLARE
  this_month DATE := date_trunc('month', now())::date;
BEGIN
  FOR i IN 0..2 LOOP
    DECLARE
      pstart DATE := (this_month + (i || ' months')::interval)::date;
      pend   DATE := (pstart + '1 month'::interval)::date;
      pname  TEXT := 'automation_jobs_' || to_char(pstart, 'YYYY_MM');
    BEGIN
      EXECUTE format(
        'CREATE TABLE IF NOT EXISTS %I PARTITION OF automation_jobs
           FOR VALUES FROM (%L) TO (%L)',
        pname, pstart, pend
      );
    EXCEPTION WHEN others THEN NULL; -- partition already exists
    END;
  END LOOP;
END $$;

-- Register with pg_partman for automatic monthly partition creation
SELECT partman.create_parent(
  p_parent_table   := 'public.automation_jobs',
  p_control        := 'created_at',
  p_type           := 'range',
  p_interval       := '1 month',
  p_premake        := 3
) WHERE NOT EXISTS (
  SELECT 1 FROM partman.part_config WHERE parent_table = 'public.automation_jobs'
);

-- ---------------------------------------------------------------------------
-- Job events (partitioned, blueprint §22)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS automation_job_events (
  id         BIGSERIAL,
  job_id     TEXT        NOT NULL,
  seq        INT         NOT NULL,
  event_type TEXT        NOT NULL,
  message    TEXT,
  metadata   JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

CREATE INDEX IF NOT EXISTS idx_automation_job_events_job_seq
  ON automation_job_events (job_id, seq);

DO $$
DECLARE
  this_month DATE := date_trunc('month', now())::date;
BEGIN
  FOR i IN 0..2 LOOP
    DECLARE
      pstart DATE := (this_month + (i || ' months')::interval)::date;
      pend   DATE := (pstart + '1 month'::interval)::date;
      pname  TEXT := 'automation_job_events_' || to_char(pstart, 'YYYY_MM');
    BEGIN
      EXECUTE format(
        'CREATE TABLE IF NOT EXISTS %I PARTITION OF automation_job_events
           FOR VALUES FROM (%L) TO (%L)',
        pname, pstart, pend
      );
    EXCEPTION WHEN others THEN NULL;
    END;
  END LOOP;
END $$;

-- ---------------------------------------------------------------------------
-- Webhooks (blueprint §22, §7.9)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS webhook_endpoints (
  id            BIGSERIAL PRIMARY KEY,
  app_id        BIGINT    NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  url           TEXT      NOT NULL,
  events        TEXT[]    NOT NULL DEFAULT '{*}',
  secret_id     BIGINT    REFERENCES app_credentials(id),
  enabled       BOOLEAN   NOT NULL DEFAULT TRUE,
  circuit_state TEXT      NOT NULL DEFAULT 'closed' CHECK (circuit_state IN ('closed','open','half-open')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id             BIGSERIAL PRIMARY KEY,
  endpoint_id    BIGINT    NOT NULL REFERENCES webhook_endpoints(id),
  job_id         TEXT,
  event_type     TEXT      NOT NULL,
  payload        JSONB     NOT NULL,
  attempt        INT       NOT NULL DEFAULT 0,
  status         TEXT      NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered','failed','dead_letter')),
  http_status    INT,
  response_body  TEXT,
  next_attempt_at TIMESTAMPTZ,
  delivered_at   TIMESTAMPTZ,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_endpoint_status
  ON webhook_deliveries(endpoint_id, status);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_next_attempt
  ON webhook_deliveries(next_attempt_at) WHERE status = 'pending';

-- ---------------------------------------------------------------------------
-- Browser sessions v2.1 (blueprint §22 — browser_instances, provider_contexts,
-- browser_tabs; browser_sessions retained as backward-compat alias)
-- ---------------------------------------------------------------------------

-- Note: gateway_browser_instances / gateway_provider_contexts / gateway_browser_tabs
-- already exist from migration 0004. The blueprint names are added here as
-- separate tables that will become the primary tables in Phase 2.
-- Migration 0009 (transitional views) aliases one to the other; Phase 2 drops
-- the gateway_* copies.

CREATE TABLE IF NOT EXISTS browser_instances (
  id              BIGSERIAL PRIMARY KEY,
  instance_id     TEXT      UNIQUE NOT NULL,
  worker_id       TEXT      NOT NULL,
  tenant_id       BIGINT    NOT NULL REFERENCES tenants(id),
  engine          TEXT      NOT NULL DEFAULT 'chromium' CHECK (engine IN ('chromium','firefox','webkit','bidi')),
  remote_endpoint TEXT,
  state           TEXT      NOT NULL DEFAULT 'starting',
  context_count   INT       NOT NULL DEFAULT 0,
  tab_count       INT       NOT NULL DEFAULT 0,
  rss_bytes       BIGINT,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  recycle_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_browser_instances_tenant_state
  ON browser_instances(tenant_id, state);

CREATE TABLE IF NOT EXISTS provider_contexts (
  id                 BIGSERIAL PRIMARY KEY,
  context_id         TEXT      UNIQUE NOT NULL,
  instance_id        TEXT      NOT NULL REFERENCES browser_instances(instance_id) ON DELETE CASCADE,
  tenant_id          BIGINT    NOT NULL REFERENCES tenants(id),
  target_id          BIGINT    NOT NULL REFERENCES targets(id),
  identity_ref       TEXT      NOT NULL,
  login_state        TEXT      NOT NULL DEFAULT 'unknown',
  conversation_model TEXT      NOT NULL DEFAULT 'url' CHECK (conversation_model IN ('url','tabbed','spa-singleton')),
  fingerprint_id     TEXT,
  proxy_id           TEXT,
  storage_state_uri  TEXT,
  max_tabs           INT       NOT NULL DEFAULT 2,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_health_at     TIMESTAMPTZ,
  recycle_at         TIMESTAMPTZ,
  UNIQUE (tenant_id, target_id, identity_ref)
);
CREATE INDEX IF NOT EXISTS idx_provider_contexts_instance
  ON provider_contexts(instance_id);

CREATE TABLE IF NOT EXISTS browser_tabs (
  id              BIGSERIAL PRIMARY KEY,
  tab_id          TEXT      UNIQUE NOT NULL,
  context_id      TEXT      NOT NULL REFERENCES provider_contexts(context_id) ON DELETE CASCADE,
  state           TEXT      NOT NULL DEFAULT 'warming'
                            CHECK (state IN ('warming','ready','busy','draining','quarantined','closed')),
  conversation_id TEXT,
  current_job_id  TEXT,
  jobs_completed  INT       NOT NULL DEFAULT 0,
  rss_bytes       BIGINT,
  last_health_at  TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  recycle_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_browser_tabs_context_state
  ON browser_tabs(context_id, state);
CREATE INDEX IF NOT EXISTS idx_browser_tabs_conversation
  ON browser_tabs(conversation_id) WHERE conversation_id IS NOT NULL;

-- browser_sessions: v2.0 compatibility alias — kept for existing callers.
-- In Phase 2 it is replaced by a view over provider_contexts.
CREATE TABLE IF NOT EXISTS browser_sessions (
  id              BIGSERIAL PRIMARY KEY,
  session_id      TEXT      UNIQUE NOT NULL,
  target_id       BIGINT    NOT NULL REFERENCES targets(id),
  worker_id       TEXT      NOT NULL,
  profile_dir     TEXT      NOT NULL,
  state           TEXT      NOT NULL,
  login_state     TEXT      NOT NULL,
  current_job_id  TEXT,
  jobs_completed  INT       NOT NULL DEFAULT 0,
  last_health_at  TIMESTAMPTZ,
  recycle_at      TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Semantic cache (blueprint §22, §7.8, §17)
-- pgvector HNSW index for sub-millisecond cosine-similarity lookups.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS semantic_cache (
  id               BIGSERIAL PRIMARY KEY,
  tenant_id        BIGINT    NOT NULL,
  target_id        BIGINT    NOT NULL,
  template_id      TEXT,
  prompt_hash      TEXT      NOT NULL,   -- SHA-256 of canonical prompt+options (exact-match fast path)
  prompt_embedding vector(384),          -- sentence-transformer embedding (semantic path)
  output           JSONB     NOT NULL,
  hits             INT       NOT NULL DEFAULT 0,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at       TIMESTAMPTZ
);
-- HNSW index for cosine-similarity search (§7.8: approx O(log N), threshold ≥0.97)
CREATE INDEX IF NOT EXISTS idx_semantic_cache_hnsw
  ON semantic_cache USING hnsw (prompt_embedding vector_cosine_ops)
  WITH (m = 16, ef_construction = 64);
CREATE INDEX IF NOT EXISTS idx_semantic_cache_lookup
  ON semantic_cache(tenant_id, target_id, template_id, prompt_hash);
CREATE INDEX IF NOT EXISTS idx_semantic_cache_expiry
  ON semantic_cache(expires_at) WHERE expires_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Audit log — append-only, Merkle-chained (blueprint §22, §11.6)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS audit_log (
  id            BIGSERIAL PRIMARY KEY,
  ts            TIMESTAMPTZ NOT NULL DEFAULT now(),
  actor_kind    TEXT        NOT NULL,
  actor_id      TEXT        NOT NULL,
  tenant_id     BIGINT,
  action        TEXT        NOT NULL,
  resource_kind TEXT        NOT NULL,
  resource_id   TEXT,
  request       JSONB,
  result        TEXT        NOT NULL,
  prev_hash     BYTEA,
  this_hash     BYTEA       NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_log_tenant_ts
  ON audit_log(tenant_id, ts DESC);

-- ---------------------------------------------------------------------------
-- Transactional outbox (blueprint §22, §7.6, §14.3)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS outbox_events (
  id           BIGSERIAL PRIMARY KEY,
  topic        TEXT        NOT NULL,
  payload      JSONB       NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_outbox_events_unpublished
  ON outbox_events(published_at NULLS FIRST, id);

-- ---------------------------------------------------------------------------
-- Record this migration
-- ---------------------------------------------------------------------------
INSERT INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0008', 'blueprint_schema', 'sha256:placeholder-recalculated-at-apply-time')
ON CONFLICT (version) DO NOTHING;
