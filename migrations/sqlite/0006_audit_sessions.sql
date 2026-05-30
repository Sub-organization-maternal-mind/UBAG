-- Migration 0006: audit_sessions
-- SQLite parity of migrations/postgres/0006_audit_sessions.sql for the
-- gateway's Merkle-chained audit log and server-side SSO sessions. Timestamps
-- are RFC3339 UTC strings; booleans are stored as INTEGER 0/1. The DDL here
-- mirrors the tables the gateway self-creates at runtime in
-- internal/audit/sqlite.go and internal/session/sqlite.go so a migrated
-- database and a runtime-bootstrapped database are identical. Apply after
-- 0005_alerts.sql.
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
  id          TEXT NOT NULL PRIMARY KEY,
  seq         INTEGER NOT NULL,
  tenant_id   TEXT NOT NULL,
  app_id      TEXT NOT NULL DEFAULT '',
  actor       TEXT NOT NULL DEFAULT '',
  action      TEXT NOT NULL,
  resource    TEXT NOT NULL DEFAULT '',
  outcome     TEXT NOT NULL DEFAULT '',
  occurred_at TEXT NOT NULL,
  attributes  TEXT NOT NULL DEFAULT '{}',
  prev_hash   TEXT NOT NULL DEFAULT '',
  record_hash TEXT NOT NULL,
  UNIQUE (tenant_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_gateway_audit_log_tenant_occurred
  ON gateway_audit_log (tenant_id, occurred_at);

-- SSO sessions --------------------------------------------------------------
CREATE TABLE IF NOT EXISTS gateway_sessions (
  token_hash TEXT NOT NULL PRIMARY KEY,
  id         TEXT NOT NULL,
  tenant_id  TEXT NOT NULL,
  app_id     TEXT NOT NULL DEFAULT '',
  role       TEXT NOT NULL DEFAULT 'viewer',
  subject    TEXT NOT NULL DEFAULT '',
  email      TEXT NOT NULL DEFAULT '',
  issued_at  TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_gateway_sessions_expires_at
  ON gateway_sessions (expires_at);

INSERT OR IGNORE INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0006', 'audit_sessions', 'manual-v0-sqlite-audit-sessions');
