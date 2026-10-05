import { describe, expect, it } from 'vitest';
import { failedCount, normalizeJob, normalizeJobs, parseJobsSummary } from './jobs';

describe('job response normalization', () => {
  it('normalizes the production list summary shape', () => {
    expect(normalizeJob({
      job_id: 'job_000000000045',
      target: 'gemini_web',
      status: 'completed',
      created_at: '2026-07-23T20:53:50Z',
      updated_at: '2026-07-23T20:54:00Z',
      metadata: {
        command_type: 'submit',
        input: { prompt: 'redacted' },
      },
    })).toMatchObject({
      id: 'job_000000000045',
      job_id: 'job_000000000045',
      target: 'gemini_web',
      command_type: 'submit',
      status: 'completed',
      input: { prompt: 'redacted' },
    });
  });

  it('keeps the legacy dashboard fixture shape compatible', () => {
    expect(normalizeJob({
      id: 'job_legacy',
      target: 'mock',
      command_type: 'chat.send',
      status: 'queued',
      created_at: '2026-07-23T20:00:00Z',
      updated_at: '2026-07-23T20:00:00Z',
    })).toMatchObject({
      id: 'job_legacy',
      command_type: 'chat.send',
    });
  });

  it('drops malformed rows instead of crashing the jobs page', () => {
    expect(normalizeJobs([null, {}, { job_id: 'job_ok', status: 'queued' }]))
      .toHaveLength(1);
  });
});

describe('jobs summary parsing', () => {
  it('keeps uncapped counts and derives the failed total from the status vocabulary', () => {
    const summary = parseJobsSummary({
      kind: 'jobs_summary',
      total: 242,
      counts_by_status: { queued: 130, completed: 108, failed_terminal: 2, timed_out: 2 },
      queued_by_reason: {},
      oldest_queued_at: '2026-05-22T10:00:00Z',
    });
    expect(summary?.total).toBe(242);
    expect(summary?.counts_by_status.queued).toBe(130);
    expect(summary?.oldest_queued_at).toBe('2026-05-22T10:00:00Z');
    expect(failedCount(summary!)).toBe(4);
  });

  it('returns null for non-contract bodies so pages can fall back', () => {
    expect(parseJobsSummary(null)).toBeNull();
    expect(parseJobsSummary({ error: 'nope' })).toBeNull();
  });
});

