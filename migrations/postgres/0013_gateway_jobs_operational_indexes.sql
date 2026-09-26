-- Migration 0013: gateway_jobs operational-scan indexes
--
-- Every existing gateway_jobs index leads with tenant_id:
--   idx_gateway_jobs_tenant_app_created (tenant_id, app_id, created_at DESC, id)
--   idx_gateway_jobs_tenant_app_status  (tenant_id, app_id, status)
--   idx_gateway_jobs_tenant_app_target  (tenant_id, app_id, target)
-- so none of them can serve a bare `WHERE status = ?` predicate. That left the
-- background loops and the metrics endpoint doing sequential scans:
--
--   * executor.StaleJobReaper.SweepOnce issues `WHERE status = ?` once per
--     non-terminal status, with no tenant filter, every 60 seconds - eight
--     unbounded full-table reads per minute, each selecting all seven JSON
--     columns.
--   * executor.CountsByStatus (GROUP BY status) runs on every /v1/metrics
--     scrape. That endpoint is unauthenticated, so the cost is anonymously
--     triggerable.
--   * The attachment gate's created-job count does `WHERE status = 'created'`
--     on the same scrape path.
--
-- There was also no index on updated_at at all, so the reaper's
-- `updated_at < cutoff` predicate could not be served either.
--
-- Applied on an existing database this is a blocking index build, so schedule
-- it in a maintenance window on a large gateway_jobs table; on an empty or
-- small table it is effectively instant. Apply after
-- 0012_gateway_jobs_not_before.sql.
-- These indexes are plain, not partial. A partial index restricted to
-- non-terminal statuses would be smaller, but a planner only uses a partial
-- index when the query's WHERE clause implies its predicate, and neither
-- Postgres nor SQLite infers that `status = 'queued'` implies
-- `status NOT IN ('completed', ...)`. Verified with EXPLAIN: the partial form
-- still produced a sequential scan for the reaper's exact query. Note that the
-- reaper already narrows its own loop to non-terminal statuses, so a plain
-- index on (status, updated_at) is what actually gets used.
CREATE INDEX IF NOT EXISTS idx_gateway_jobs_status_updated
  ON gateway_jobs (status, updated_at);

CREATE INDEX IF NOT EXISTS idx_gateway_jobs_created
  ON gateway_jobs (created_at DESC, id);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0013', 'gateway_jobs_operational_indexes', '', now())
ON CONFLICT (version) DO NOTHING;
