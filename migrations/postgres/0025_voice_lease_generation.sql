-- Migration 0025: voice session lease generation, hosting node, media lease and
-- terminating hold (UBAG_HELPER_VOICE, default off). Strictly additive and
-- idempotent: four columns with inert defaults. Nothing reads or writes them
-- unless a session is bound to a Helper Node (voice.Store.BindNode) or a
-- termination asks for a hold (voice.Store.BeginTerminate), both of which stay
-- unused while the flag is off, so applying this migration changes no runtime
-- behaviour. None of the columns is ever serialized: voice.Session marks them
-- json:"-", so node ids never reach a client.
--
--   node_id                 Helper Node that currently hosts the session's media
--                           ('' = primary-hosted).
--   lease_generation        grows on every node bind; a node's writes are fenced
--                           by (node_id, lease_generation), so a replaced node
--                           cannot commit, renew or release (0 = never bound).
--   media_lease_expires_at  the node's own lease, renewed by that node alone and
--                           swept like lease_expires_at (NULL while unbound).
--   terminating_until       a terminated session keeps its account and browser
--                           environment (identity_ref / instance_ref stay set)
--                           until the provider deactivation is acked or this
--                           instant (at most 45 s later) passes. Admission
--                           counts such a row as taken; the live-status UNIQUE
--                           indexes from 0019/0020 are unchanged.
--
-- Apply after 0020_voice_instance_global.sql (0019 owns the table); 0022-0024
-- belong to the attempt, node and profile tables and are independent.

ALTER TABLE gateway_voice_sessions
	ADD COLUMN IF NOT EXISTS node_id TEXT NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS lease_generation BIGINT NOT NULL DEFAULT 0,
	ADD COLUMN IF NOT EXISTS media_lease_expires_at TIMESTAMPTZ,
	ADD COLUMN IF NOT EXISTS terminating_until TIMESTAMPTZ;

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0025', 'voice_lease_generation', '', now())
ON CONFLICT (version) DO NOTHING;
