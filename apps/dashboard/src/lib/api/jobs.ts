import type { Job } from './types';

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
