-- UBAG SQLite queue baseline.
-- This is the edge/local queue schema behind the common Queue interface.

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS edge_queue_jobs (
  id TEXT PRIMARY KEY,
  queue_name TEXT NOT NULL DEFAULT 'default',
  job_name TEXT NOT NULL,
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  payload_version INTEGER NOT NULL DEFAULT 1 CHECK (payload_version >= 1),
  status TEXT NOT NULL DEFAULT 'queued'
    CHECK (
      status IN (
        'queued',
        'leased',
        'retry_scheduled',
        'completed',
        'dead_lettered',
        'cancelled'
      )
    ),
  priority INTEGER NOT NULL DEFAULT 0,
  run_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts >= 1),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  dedupe_key TEXT,
  idempotency_scope TEXT,
  idempotency_key TEXT,
  idempotency_request_hash TEXT,
  lease_id TEXT,
  leased_by TEXT,
  lease_expires_at TEXT,
  result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
  last_error_json TEXT CHECK (last_error_json IS NULL OR json_valid(last_error_json)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  completed_at TEXT,
  cancelled_at TEXT,
  CHECK (
    (idempotency_scope IS NULL AND idempotency_key IS NULL AND idempotency_request_hash IS NULL)
    OR (idempotency_scope IS NOT NULL AND idempotency_key IS NOT NULL AND idempotency_request_hash IS NOT NULL)
  )
);

CREATE INDEX IF NOT EXISTS idx_edge_queue_jobs_ready
  ON edge_queue_jobs (queue_name, status, run_at, priority DESC, created_at);

CREATE INDEX IF NOT EXISTS idx_edge_queue_jobs_lease_expiry
  ON edge_queue_jobs (lease_expires_at)
  WHERE status = 'leased';

CREATE INDEX IF NOT EXISTS idx_edge_queue_jobs_status_updated
  ON edge_queue_jobs (status, updated_at);

CREATE UNIQUE INDEX IF NOT EXISTS ux_edge_queue_jobs_active_dedupe
  ON edge_queue_jobs (queue_name, dedupe_key)
  WHERE dedupe_key IS NOT NULL
    AND status IN ('queued', 'leased', 'retry_scheduled');

CREATE UNIQUE INDEX IF NOT EXISTS ux_edge_queue_jobs_idempotency
  ON edge_queue_jobs (idempotency_scope, idempotency_key)
  WHERE idempotency_scope IS NOT NULL
    AND idempotency_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS edge_queue_attempts (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES edge_queue_jobs(id) ON DELETE CASCADE,
  attempt_number INTEGER NOT NULL CHECK (attempt_number >= 1),
  lease_id TEXT NOT NULL,
  worker_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'leased'
    CHECK (status IN ('leased', 'completed', 'retry_scheduled', 'dead_lettered', 'cancelled')),
  started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  finished_at TEXT,
  error_json TEXT CHECK (error_json IS NULL OR json_valid(error_json)),
  UNIQUE (job_id, attempt_number)
);

CREATE INDEX IF NOT EXISTS idx_edge_queue_attempts_job
  ON edge_queue_attempts (job_id, attempt_number);

CREATE TABLE IF NOT EXISTS edge_queue_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id TEXT NOT NULL REFERENCES edge_queue_jobs(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  event_json TEXT CHECK (event_json IS NULL OR json_valid(event_json)),
  occurred_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_edge_queue_events_job
  ON edge_queue_events (job_id, occurred_at);

CREATE TABLE IF NOT EXISTS edge_queue_dead_letters (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL UNIQUE REFERENCES edge_queue_jobs(id) ON DELETE CASCADE,
  queue_name TEXT NOT NULL,
  job_name TEXT NOT NULL,
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  payload_version INTEGER NOT NULL CHECK (payload_version >= 1),
  attempts INTEGER NOT NULL CHECK (attempts >= 0),
  reason TEXT NOT NULL,
  last_error_json TEXT CHECK (last_error_json IS NULL OR json_valid(last_error_json)),
  moved_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_edge_queue_dead_letters_queue_moved
  ON edge_queue_dead_letters (queue_name, moved_at);

INSERT OR IGNORE INTO edge_schema_migrations (version, name, checksum)
VALUES ('0002', 'edge_queue', 'manual-v0');
