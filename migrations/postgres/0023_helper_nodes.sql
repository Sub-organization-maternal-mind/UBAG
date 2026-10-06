-- Migration 0023: helper node store (UBAG_HELPER_NODES, default off).
-- Three tables for the shared-fleet Helper Node plane. Nothing reads or writes
-- them unless the flag is on, so applying this migration changes no runtime
-- behaviour. Strictly additive and idempotent. The job-attempt ledger is NOT
-- here (0022_gateway_job_attempts owns it). SQLite has no counterpart on
-- purpose: the edge profile refuses helper mode.
--
--   gateway_helper_allocations  latest accepted manager grant per node
--                               (node-allocation.schema.json v1; no tenant or
--                               job ids). generation is monotonic per node.
--   gateway_helper_state        per-node runtime state between heartbeats
--                               (liveness, pressure hysteresis, ramp, host size).
--   gateway_helper_registry     pinned certificate identity per node: URI SAN
--                               plus SPKI SHA-256 pins (current and next, for
--                               rotation). Revocation is sticky.
--
-- The store bounds each table to 256 nodes (matching the schema's
-- allocation_list maxItems). Apply after 0021_admission_tokens.sql; 0022 is
-- reserved for gateway_job_attempts and the two are independent.

CREATE TABLE IF NOT EXISTS gateway_helper_allocations (
	node_id               TEXT PRIMARY KEY,
	region                TEXT NOT NULL,
	endpoint              TEXT NOT NULL,
	uri_san               TEXT NOT NULL,
	spki_sha256           TEXT NOT NULL DEFAULT '',
	cpu_millis            INTEGER NOT NULL CHECK (cpu_millis >= 0),
	memory_bytes          BIGINT NOT NULL CHECK (memory_bytes >= 0),
	reservation_state     TEXT NOT NULL CHECK (reservation_state IN ('known', 'unknown')),
	state                 TEXT NOT NULL CHECK (state IN ('active', 'draining', 'revoked')),
	max_browser_workloads INTEGER NOT NULL CHECK (max_browser_workloads BETWEEN 0 AND 64),
	voice_capable         BOOLEAN NOT NULL DEFAULT FALSE,
	udp_port_min          INTEGER NOT NULL DEFAULT 0,
	udp_port_max          INTEGER NOT NULL DEFAULT 0,
	nat_ip                TEXT NOT NULL DEFAULT '',
	valid_until           TIMESTAMPTZ NOT NULL,
	generation            BIGINT NOT NULL CHECK (generation >= 0),
	accepted_at           TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS gateway_helper_state (
	node_id             TEXT PRIMARY KEY REFERENCES gateway_helper_allocations (node_id),
	last_heartbeat_at   TIMESTAMPTZ NOT NULL,
	pressure_reduced    BOOLEAN NOT NULL DEFAULT FALSE,
	pressure_calm_since TIMESTAMPTZ,
	ramped_limit        INTEGER NOT NULL DEFAULT 0 CHECK (ramped_limit BETWEEN 0 AND 64),
	host_cores          INTEGER NOT NULL DEFAULT 0 CHECK (host_cores >= 0),
	host_memory_bytes   BIGINT NOT NULL DEFAULT 0 CHECK (host_memory_bytes >= 0)
);

CREATE TABLE IF NOT EXISTS gateway_helper_registry (
	node_id      TEXT PRIMARY KEY,
	uri_san      TEXT NOT NULL,
	spki_current TEXT NOT NULL DEFAULT '',
	spki_next    TEXT NOT NULL DEFAULT '',
	revoked_at   TIMESTAMPTZ,
	updated_at   TIMESTAMPTZ NOT NULL
);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0023', 'helper_nodes', '', now())
ON CONFLICT (version) DO NOTHING;
