-- Migration 0020: browser/audio environment exclusivity is physical, not
-- per tenant. The virtual microphone and speaker monitor belong to one
-- browser instance, so at most ONE live voice session may hold an instance
-- across ALL tenants. Replaces the tenant-scoped index from 0019. Apply after
-- 0019_voice_sessions.sql.

DROP INDEX IF EXISTS uq_voice_active_instance;

CREATE UNIQUE INDEX IF NOT EXISTS uq_voice_active_instance_global
	ON gateway_voice_sessions (instance_ref)
	WHERE status IN ('queued', 'connecting', 'connected') AND instance_ref <> '';

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0020', 'voice_instance_global', '', now())
ON CONFLICT (version) DO NOTHING;
