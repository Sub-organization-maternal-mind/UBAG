export const sqliteQueueStatements = {
  enqueueJob: `
INSERT INTO edge_queue_jobs (
  id,
  queue_name,
  job_name,
  payload_json,
  payload_version,
  status,
  priority,
  run_at,
  max_attempts,
  dedupe_key,
  idempotency_scope,
  idempotency_key,
  idempotency_request_hash,
  created_at,
  updated_at
) VALUES (
  :id,
  :queueName,
  :jobName,
  :payloadJson,
  :payloadVersion,
  'queued',
  :priority,
  :runAt,
  :maxAttempts,
  :dedupeKey,
  :idempotencyScope,
  :idempotencyKey,
  :idempotencyRequestHash,
  :now,
  :now
)
RETURNING *;
`,
  findActiveDedupeJob: `
SELECT *
FROM edge_queue_jobs
WHERE queue_name = :queueName
  AND dedupe_key = :dedupeKey
  AND status IN ('queued', 'leased', 'retry_scheduled')
ORDER BY created_at ASC
LIMIT 1;
`,
  findIdempotentJob: `
SELECT *
FROM edge_queue_jobs
WHERE idempotency_scope = :idempotencyScope
  AND idempotency_key = :idempotencyKey
LIMIT 1;
`,
  leaseNext: `
UPDATE edge_queue_jobs
SET
  status = 'leased',
  attempt_count = attempt_count + 1,
  lease_id = :leaseId,
  leased_by = :workerId,
  lease_expires_at = :leaseExpiresAt,
  updated_at = :now
WHERE id = (
  SELECT id
  FROM edge_queue_jobs
  WHERE queue_name = :queueName
    AND (
      status IN ('queued', 'retry_scheduled')
      OR (status = 'leased' AND lease_expires_at <= :now)
    )
    AND run_at <= :now
  ORDER BY priority DESC, run_at ASC, created_at ASC
  LIMIT 1
)
RETURNING *;
`,
  insertAttempt: `
INSERT INTO edge_queue_attempts (
  id,
  job_id,
  attempt_number,
  lease_id,
  worker_id,
  status,
  started_at
) VALUES (
  :id,
  :jobId,
  :attemptNumber,
  :leaseId,
  :workerId,
  'leased',
  :now
);
`,
  acknowledge: `
UPDATE edge_queue_jobs
SET
  status = 'completed',
  result_json = :resultJson,
  lease_id = NULL,
  leased_by = NULL,
  lease_expires_at = NULL,
  updated_at = :now,
  completed_at = :now
WHERE id = :jobId
  AND status = 'leased'
  AND lease_id = :leaseId
RETURNING *;
`,
  rejectForRetry: `
UPDATE edge_queue_jobs
SET
  status = 'retry_scheduled',
  run_at = :runAt,
  lease_id = NULL,
  leased_by = NULL,
  lease_expires_at = NULL,
  last_error_json = :errorJson,
  updated_at = :now
WHERE id = :jobId
  AND status = 'leased'
  AND lease_id = :leaseId
  AND attempt_count < max_attempts
RETURNING *;
`,
  rejectToDeadLetter: `
UPDATE edge_queue_jobs
SET
  status = 'dead_lettered',
  lease_id = NULL,
  leased_by = NULL,
  lease_expires_at = NULL,
  last_error_json = :errorJson,
  updated_at = :now,
  completed_at = :now
WHERE id = :jobId
  AND status = 'leased'
  AND lease_id = :leaseId
RETURNING *;
`,
  insertDeadLetter: `
INSERT INTO edge_queue_dead_letters (
  id,
  job_id,
  queue_name,
  job_name,
  payload_json,
  payload_version,
  attempts,
  reason,
  last_error_json,
  moved_at
) VALUES (
  :id,
  :jobId,
  :queueName,
  :jobName,
  :payloadJson,
  :payloadVersion,
  :attempts,
  :reason,
  :lastErrorJson,
  :now
)
ON CONFLICT(job_id) DO UPDATE SET
  attempts = excluded.attempts,
  reason = excluded.reason,
  last_error_json = excluded.last_error_json,
  moved_at = excluded.moved_at;
`,
  extendLease: `
UPDATE edge_queue_jobs
SET
  lease_expires_at = :leaseExpiresAt,
  updated_at = :now
WHERE id = :jobId
  AND status = 'leased'
  AND lease_id = :leaseId
RETURNING *;
`,
  cancel: `
UPDATE edge_queue_jobs
SET
  status = 'cancelled',
  lease_id = NULL,
  leased_by = NULL,
  lease_expires_at = NULL,
  last_error_json = :reasonJson,
  updated_at = :now,
  cancelled_at = :now
WHERE id = :jobId
  AND status NOT IN ('completed', 'dead_lettered', 'cancelled')
RETURNING *;
`,
  getStatus: `
SELECT *
FROM edge_queue_jobs
WHERE id = :jobId
LIMIT 1;
`,
  getStats: `
SELECT
  queue_name,
  SUM(CASE WHEN status = 'queued' THEN 1 ELSE 0 END) AS queued,
  SUM(CASE WHEN status = 'leased' THEN 1 ELSE 0 END) AS leased,
  SUM(CASE WHEN status = 'retry_scheduled' THEN 1 ELSE 0 END) AS retry_scheduled,
  SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) AS completed,
  SUM(CASE WHEN status = 'dead_lettered' THEN 1 ELSE 0 END) AS dead_lettered,
  SUM(CASE WHEN status = 'cancelled' THEN 1 ELSE 0 END) AS cancelled,
  MIN(CASE WHEN status IN ('queued', 'retry_scheduled') THEN run_at ELSE NULL END) AS oldest_ready_at
FROM edge_queue_jobs
WHERE queue_name = :queueName
GROUP BY queue_name;
`,
} as const;

export type SqliteQueueStatementName = keyof typeof sqliteQueueStatements;
