-- Migration 0022: gateway_job_attempts
-- Attempt ledger for lease-generation fenced commits (ADR-0007, perf-fleet
-- P4.2). One row per leased execution attempt of a job. `generation` is a
-- per-job counter that only ever grows; every write made for an attempt
-- (renew, submission mark, event commit) must present the generation it was
-- leased under, and the store rejects it once the attempt is no longer the
-- job's active one. See jobs.AttemptStore.
--
-- Additive and inert: nothing reads or writes this table unless
-- UBAG_EXECUTOR_ATTEMPTS is on (default off). This is the only attempts table;
-- the helper node registry (0023) must reference it, never define a second one.
-- Apply after 0021_admission_tokens.sql.

CREATE TABLE IF NOT EXISTS gateway_job_attempts (
	job_id            TEXT        NOT NULL REFERENCES gateway_jobs(id) ON DELETE CASCADE,
	attempt_id        TEXT        NOT NULL,
	generation        BIGINT      NOT NULL CHECK (generation >= 1),
	node_id           TEXT        NOT NULL DEFAULT '',
	state             TEXT        NOT NULL CHECK (state IN ('active', 'finished', 'expired')),
	lease_expires_at  TIMESTAMPTZ NOT NULL,
	input_fingerprint TEXT        NOT NULL DEFAULT '',
	workload_version  TEXT        NOT NULL DEFAULT '',
	submitted_at      TIMESTAMPTZ,
	created_at        TIMESTAMPTZ NOT NULL,
	updated_at        TIMESTAMPTZ NOT NULL,
	ended_at          TIMESTAMPTZ,
	PRIMARY KEY (job_id, attempt_id),
	UNIQUE (job_id, generation)
);

-- At most one live lease per job, enforced by the database as a backstop to
-- the BeginAttempt transaction.
CREATE UNIQUE INDEX IF NOT EXISTS uq_job_attempts_active
	ON gateway_job_attempts (job_id)
	WHERE state = 'active';

-- Drives the ExpireAttempts sweep.
CREATE INDEX IF NOT EXISTS idx_job_attempts_lease_expiry
	ON gateway_job_attempts (lease_expires_at)
	WHERE state = 'active';

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0022', 'gateway_job_attempts', '', now())
ON CONFLICT (version) DO NOTHING;
