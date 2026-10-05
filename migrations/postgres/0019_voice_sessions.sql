-- Migration 0019: gateway_voice_sessions
-- Shared, atomic voice-session admission for provider live voice (the
-- multimodal/voice release). A voice session holds two EXCLUSIVE resources
-- for its lifetime, and both exclusivities are enforced HERE — in the shared
-- database, not in any one gateway process — so multiple replicas admit
-- against the same authority:
--
--   * provider-account lease: uq_voice_active_account allows at most one
--     live session per (tenant_id, target, identity_ref);
--   * browser/audio environment lease: uq_voice_active_instance allows at
--     most one live session per tenant browser instance (the virtual
--     microphone and speaker monitor are instance-wide devices).
--
-- Both indexes are partial on the live statuses and non-empty refs, so
-- QUEUED sessions (which hold nothing) never collide, and termination clears
-- the refs, releasing the lease immediately. Lease expiry is a data column
-- swept by any replica (voice.Store.SweepExpired), which is how a crashed
-- gateway's sessions release their accounts. Apply after
-- 0018_audit_worm_revoke.sql.

CREATE TABLE IF NOT EXISTS gateway_voice_sessions (
	session_id       TEXT PRIMARY KEY,
	tenant_id        TEXT NOT NULL,
	app_id           TEXT NOT NULL DEFAULT '',
	target           TEXT NOT NULL,
	mode             TEXT NOT NULL DEFAULT 'live',
	job_id           TEXT NOT NULL DEFAULT '',
	status           TEXT NOT NULL DEFAULT 'queued',
	muted            BOOLEAN NOT NULL DEFAULT FALSE,
	identity_ref     TEXT NOT NULL DEFAULT '',
	instance_ref     TEXT NOT NULL DEFAULT '',
	last_error       TEXT NOT NULL DEFAULT '',
	created_at       TIMESTAMPTZ NOT NULL,
	updated_at       TIMESTAMPTZ NOT NULL,
	lease_expires_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	terminated_at    TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_voice_active_account
	ON gateway_voice_sessions (tenant_id, target, identity_ref)
	WHERE status IN ('queued', 'connecting', 'connected') AND identity_ref <> '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_voice_active_instance
	ON gateway_voice_sessions (tenant_id, instance_ref)
	WHERE status IN ('queued', 'connecting', 'connected') AND instance_ref <> '';

CREATE INDEX IF NOT EXISTS idx_voice_tenant_created
	ON gateway_voice_sessions (tenant_id, created_at DESC);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0019', 'voice_sessions', '', now())
ON CONFLICT (version) DO NOTHING;
