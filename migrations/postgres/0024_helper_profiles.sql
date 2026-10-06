-- Migration 0024: helper profile bindings and conversation node affinity
-- (UBAG_HELPER_PLANE, default off). Strictly additive and idempotent. With the
-- flag off nothing reads or writes the new table, and the two new
-- gateway_conversations columns hold '' for every row, so applying this
-- migration changes no runtime behaviour.
--
--   gateway_helper_profiles  tenant-owned registry of opaque profile_ref values.
--                            A profile_ref names ONE logged-in browser profile
--                            on ONE helper node for a (tenant, provider,
--                            identity_ref). The primary mints it (random, never
--                            derived from or revealing the tenant or identity)
--                            and every lookup is tenant-scoped, so another
--                            tenant's profile_ref resolves to nothing. A
--                            revoked profile_ref is dead for good; binding the
--                            same tuple again mints a new one. provider is the
--                            job target id (for example chatgpt_web).
--   gateway_conversations    gains node_id and profile_ref: the node and profile
--                            a conversation's chat thread lives on. '' means
--                            "not bound to a helper" (the local worker path).
--                            A lost node marks its conversations broken in
--                            place (no migration of the thread to another node).
--
-- Apply after 0023_helper_nodes.sql (0022 is reserved for gateway_job_attempts;
-- the two are independent). SQLite has no counterpart for the registry (the
-- edge profile refuses helper mode); its conversations store adds the two
-- columns itself on startup.

CREATE TABLE IF NOT EXISTS gateway_helper_profiles (
	profile_ref  TEXT PRIMARY KEY,
	tenant_id    TEXT NOT NULL,
	provider     TEXT NOT NULL,
	identity_ref TEXT NOT NULL,
	node_id      TEXT NOT NULL,
	state        TEXT NOT NULL CHECK (state IN ('active', 'revoked')),
	created_at   TIMESTAMPTZ NOT NULL,
	revoked_at   TIMESTAMPTZ
);

-- One live profile per (tenant, provider, identity, node). Revoked rows are
-- history and do not block a fresh binding.
CREATE UNIQUE INDEX IF NOT EXISTS ux_gateway_helper_profiles_active
	ON gateway_helper_profiles (tenant_id, provider, identity_ref, node_id)
	WHERE state = 'active';

CREATE INDEX IF NOT EXISTS idx_gateway_helper_profiles_lookup
	ON gateway_helper_profiles (tenant_id, provider, state);

ALTER TABLE gateway_conversations ADD COLUMN IF NOT EXISTS node_id TEXT NOT NULL DEFAULT '';
ALTER TABLE gateway_conversations ADD COLUMN IF NOT EXISTS profile_ref TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_gateway_conversations_node
	ON gateway_conversations (node_id)
	WHERE node_id <> '';

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0024', 'helper_profiles', '', now())
ON CONFLICT (version) DO NOTHING;
