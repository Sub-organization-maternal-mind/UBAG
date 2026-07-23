import { describe, expect, it } from 'vitest';
import { normalizeJob, normalizeJobs } from './jobs';

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
