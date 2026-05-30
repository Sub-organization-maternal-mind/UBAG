-- Migration 0006: audit_sessions
-- Native Postgres tables for the gateway audit log (Merkle-chained, per-tenant)
-- and server-side SSO sessions. Apply after 0005_enterprise_stores.sql.
--
-- The audit log is append-only and tamper-evident: each record stores the hash
-- of the previous record for its tenant (prev_hash) plus its own record_hash,
-- forming a per-tenant hash chain that the gateway verifies on export. The
-- attributes column holds canonical JSON serialized by the gateway and is
-- stored as TEXT so the exact bytes used for hashing round-trip unchanged.
--
-- Sessions persist only the SHA-256 hash of the opaque bearer token (never the
-- token itself) along with the resolved principal. Revocation is a soft flag.

-- Audit log -----------------------------------------------------------------
CREATE TABLE IF NOT EXISTS gateway_audit_log (
  id          TEXT        NOT NULL,
  seq         BIGINT      NOT NULL,
  tenant_id   TEXT        NOT NULL,
  app_id      TEXT        NOT NULL DEFAULT '',
  actor       TEXT        NOT NULL DEFAULT '',
  action      TEXT        NOT NULL DEFAULT '',
  resource    TEXT        NOT NULL DEFAULT '',
  outcome     TEXT        NOT NULL DEFAULT '',
  occurred_at TIMESTAMPTZ NOT NULL,
  attributes  TEXT        NOT NULL DEFAULT '{}',
  prev_hash   TEXT        NOT NULL DEFAULT '',
  record_hash TEXT        NOT NULL,
  PRIMARY KEY (id),
  UNIQUE (tenant_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_gateway_audit_log_tenant
  ON gateway_audit_log (tenant_id, seq);

CREATE INDEX IF NOT EXISTS idx_gateway_audit_log_occurred
  ON gateway_audit_log (tenant_id, occurred_at);

-- SSO sessions --------------------------------------------------------------
CREATE TABLE IF NOT EXISTS gateway_sessions (
  token_hash TEXT        NOT NULL,
  id         TEXT        NOT NULL,
  tenant_id  TEXT        NOT NULL,
  app_id     TEXT        NOT NULL DEFAULT '',
  role       TEXT        NOT NULL DEFAULT 'viewer',
  subject    TEXT        NOT NULL DEFAULT '',
  email      TEXT        NOT NULL DEFAULT '',
  issued_at  TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  revoked    BOOLEAN     NOT NULL DEFAULT false,
  PRIMARY KEY (token_hash)
);

CREATE INDEX IF NOT EXISTS idx_gateway_sessions_expiry
  ON gateway_sessions (expires_at);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0006', 'audit_sessions', '', now())
ON CONFLICT (version) DO NOTHING;
