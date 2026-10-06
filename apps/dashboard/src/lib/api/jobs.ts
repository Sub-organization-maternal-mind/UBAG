import type { Job, JobsSummary } from './types';
import { FAILED_STATES, isFailedStatus } from './statuses';

type UnknownRecord = Record<string, unknown>;

function record(value: unknown): UnknownRecord {
  return value != null && typeof value === 'object' && !Array.isArray(value)
    ? value as UnknownRecord
    : {};
}

function text(value: unknown, fallback = ''): string {
  return typeof value === 'string' ? value : fallback;
}

/**
 * Normalize the gateway's canonical job-summary shape for dashboard views.
 *
 * GET /v1/jobs returns `job_id` and nests request fields such as
 * `command_type` under `metadata`; older dashboard fixtures used `id` and
 * top-level request fields. Supporting both shapes keeps list rendering
 * resilient across gateway versions.
 */
export function normalizeJob(value: unknown): Job | null {
  const raw = record(value);
  const metadata = record(raw.metadata);
  const id = text(raw.job_id, text(raw.id));
  if (!id) return null;

  return {
    ...raw,
    id,
    job_id: id,
    target: text(raw.target, text(metadata.target, 'unknown')),
    command_type: text(raw.command_type, text(metadata.command_type, 'unknown')),
    status: text(raw.status, 'unknown'),
    created_at: text(raw.created_at),
    updated_at: text(raw.updated_at, text(raw.created_at)),
    input: record(raw.input ?? metadata.input),
    metadata,
  } as Job;
}

export function normalizeJobs(value: unknown): Job[] {
  return Array.isArray(value)
    ? value.map(normalizeJob).filter((job): job is Job => job !== null)
    : [];
}

/**
 * Validate a GET /v1/jobs/summary body. Returns null for anything that is not
 * the contract shape (older gateway, error body) so callers can fall back.
 */
export function parseJobsSummary(value: unknown): JobsSummary | null {
  const raw = record(value);
  if (typeof raw.total !== 'number') return null;
  const counts = (r: unknown): Record<string, number> =>
    Object.fromEntries(Object.entries(record(r)).filter(([, n]) => typeof n === 'number')) as Record<string, number>;
  return {
    total: raw.total,
    counts_by_status: counts(raw.counts_by_status),
    queued_by_reason: counts(raw.queued_by_reason),
    oldest_queued_at: typeof raw.oldest_queued_at === 'string' ? raw.oldest_queued_at : null,
  };
}

/**
 * Failed statuses worth a `filter[status]` read: those the summary reports as
 * non-zero, or all of them when the summary is unavailable (older gateway).
 */
export function failedStatusesToFetch(summary: JobsSummary | null): string[] {
  const all = [...FAILED_STATES];
  return summary ? all.filter((s) => (summary.counts_by_status[s] ?? 0) > 0) : all;
}

/** Merge per-status pages into one newest-first list, deduped by id and capped. */
export function mergeNewestFirst(pages: Job[][], limit: number): Job[] {
  const seen = new Set<string>();
  return pages
    .flat()
    .filter((j) => (seen.has(j.id) ? false : (seen.add(j.id), true)))
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .slice(0, limit);
}

export function failedCount(summary: JobsSummary): number {
  return Object.entries(summary.counts_by_status)
    .filter(([status]) => isFailedStatus(status))
    .reduce((sum, [, n]) => sum + n, 0);
}
