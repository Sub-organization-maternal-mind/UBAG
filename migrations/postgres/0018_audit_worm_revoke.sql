-- Migration 0018: audit log WORM enforcement
-- The gateway audit log (gateway_audit_log, migrations/postgres/0006) is a
-- Merkle-chained, tamper-evident, write-once store: the audit.Store interface
-- deliberately exposes no Update or Delete, and VerifyChain detects any
-- mutation of persisted rows. This migration backs that contract at the
-- database level by revoking UPDATE and DELETE on the table.
--
-- ROLE CAVEAT. No dedicated application role is provisioned anywhere in the
-- migration chain: the gateway connects as the DSN user, which is normally the
-- table OWNER. Postgres grants owners the table's privileges irrevocably (a
-- REVOKE against the owner is a silent no-op), so when the DSN user owns the
-- table this migration cannot take effect and WORM enforcement remains the
-- interface contract plus VerifyChain. It becomes active the moment a
-- dedicated, non-owner application role is provisioned and the DSN points at
-- it: the DO block only revokes from a role that actually exists, so the
-- migration is idempotent and safe on every managed Postgres.
--
-- Run after 0017_gateway_outbox_events.sql.

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ubag_app') THEN
    EXECUTE 'REVOKE UPDATE, DELETE ON gateway_audit_log FROM ubag_app';
  END IF;
END
$$;

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0018', 'audit_worm_revoke', '', now())
ON CONFLICT (version) DO NOTHING;
