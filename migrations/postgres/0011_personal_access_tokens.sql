-- Migration 0011: personal_access_tokens
-- Native Postgres table for gateway Personal Access Tokens (§11): opaque
-- ubag_pat_<base58> bearer tokens issued via POST /v1/auth/pat. Apply after
-- 0010_conversations.sql.
--
-- Only the SHA-256 hash of each token is persisted (token_hash), never the raw
-- token, so a store leak reveals no usable credential. expires_at is NULL for a
-- non-expiring token; revocation is a soft flag. The resolved principal
-- (tenant_id, app_id, role) is mapped from the row on every authenticated
-- request.

CREATE TABLE IF NOT EXISTS gateway_pats (
  token_hash TEXT        NOT NULL,
  tenant_id  TEXT        NOT NULL,
  app_id     TEXT        NOT NULL DEFAULT '',
  role       TEXT        NOT NULL DEFAULT 'viewer',
  issued_at  TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ,
  revoked    BOOLEAN     NOT NULL DEFAULT false,
  PRIMARY KEY (token_hash)
);

CREATE INDEX IF NOT EXISTS idx_gateway_pats_expiry
  ON gateway_pats (expires_at);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0011', 'personal_access_tokens', '', now())
ON CONFLICT (version) DO NOTHING;
