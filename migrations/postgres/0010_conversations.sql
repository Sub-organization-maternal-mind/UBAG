-- Migration 0010: conversations
-- Durable bindings from a caller-owned conversation key to a provider chat
-- thread. Apply after 0009_tenant_home_region.sql.
--
-- A conversation key is opaque and caller-owned, scoped to
-- (tenant_id, app_id, target). Reused keys resume the same provider chat so the
-- end user keeps their context; unseen keys open a new chat.
--
-- provider_thread_ref holds a provider chat URL ONLY. No cookies, storage
-- state, credentials, or noVNC URLs are ever stored here — resuming a chat is a
-- navigation inside an already user-authenticated session.

CREATE TABLE IF NOT EXISTS gateway_conversations (
  tenant_id           TEXT        NOT NULL,
  app_id              TEXT        NOT NULL,
  target              TEXT        NOT NULL,
  conversation_key    TEXT        NOT NULL,
  provider_thread_ref TEXT        NOT NULL DEFAULT '',
  state               TEXT        NOT NULL DEFAULT 'active',
  created_at          TIMESTAMPTZ NOT NULL,
  last_used_at        TIMESTAMPTZ,
  last_job_id         TEXT        NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, app_id, target, conversation_key)
);

CREATE INDEX IF NOT EXISTS idx_gateway_conversations_tenant_used
  ON gateway_conversations (tenant_id, last_used_at DESC);

CREATE INDEX IF NOT EXISTS idx_gateway_conversations_state
  ON gateway_conversations (tenant_id, state);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0010', 'conversations', '', now())
ON CONFLICT (version) DO NOTHING;
