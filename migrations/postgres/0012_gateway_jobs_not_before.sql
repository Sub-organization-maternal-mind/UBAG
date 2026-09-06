-- Migration 0012: gateway_jobs_not_before
-- Scheduled-job support on Postgres, parity with the memory store and the API
-- contract's not_before option (T4): persists the deferred-execution timestamp
-- per job and widens the status CHECK to the contract's 14-value set. Before
-- this migration the store dropped NotBefore and the CHECK rejected
-- 'scheduled', so scheduled jobs silently executed immediately. Apply after
-- 0011_personal_access_tokens.sql.

ALTER TABLE gateway_jobs ADD COLUMN IF NOT EXISTS not_before TIMESTAMPTZ;

ALTER TABLE gateway_jobs DROP CONSTRAINT IF EXISTS gateway_jobs_status_check;
ALTER TABLE gateway_jobs ADD CONSTRAINT gateway_jobs_status_check CHECK (
  status IN (
    'created',
    'scheduled',
    'queued',
    'assigned',
    'running',
    'token_streaming',
    'completing',
    'completed',
    'completed_with_warnings',
    'failed_retryable',
    'failed_terminal',
    'dead_letter',
    'cancelled',
    'timed_out'
  )
);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0012', 'gateway_jobs_not_before', '', now())
ON CONFLICT (version) DO NOTHING;
