-- Migration 0021: shared admission tokens.
-- Replaces the process-local in-flight counters with a database authority so
-- every gateway replica admits against the same ceilings. A token counts
-- against one or more LANES (per tenant/target/app, per app, per tenant,
-- global); it is admitted only when every lane has room, decided in one
-- transaction under per-lane advisory locks (see topology.SQLTokenBackend).
--
-- A token starts unassociated and expires (expires_at) so a gateway that dies
-- between admission and job creation cannot leak capacity; once a job id is
-- associated it is held until a terminal path or the stale-job reaper releases
-- it. gateway_admission_caps carries worker-reported adaptive (AIMD) lane
-- ceilings so they are visible to every replica. Apply after
-- 0020_voice_instance_global.sql.

CREATE TABLE IF NOT EXISTS gateway_admission_tokens (
	token_id   TEXT PRIMARY KEY,
	job_id     TEXT NOT NULL DEFAULT '',
	expires_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_admission_tokens_job
	ON gateway_admission_tokens (job_id) WHERE job_id <> '';

CREATE INDEX IF NOT EXISTS idx_admission_tokens_expiry
	ON gateway_admission_tokens (expires_at) WHERE job_id = '';

CREATE TABLE IF NOT EXISTS gateway_admission_token_lanes (
	token_id TEXT NOT NULL,
	lane_key TEXT NOT NULL,
	PRIMARY KEY (token_id, lane_key)
);

CREATE INDEX IF NOT EXISTS idx_admission_lanes_key
	ON gateway_admission_token_lanes (lane_key);

CREATE TABLE IF NOT EXISTS gateway_admission_caps (
	lane_key   TEXT PRIMARY KEY,
	cap        INTEGER NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL
);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0021', 'admission_tokens', '', now())
ON CONFLICT (version) DO NOTHING;
