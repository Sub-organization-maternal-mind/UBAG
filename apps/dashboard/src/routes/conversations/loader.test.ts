import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mirror the settings / environment mocking used by client.test.ts so the
// gateway client believes it is running in a browser with an app secret.
vi.mock('svelte/store', () => ({
  get: vi.fn(() => ({
    gatewayUrl: 'http://localhost:8081',
    appSecret: 'test-secret',
    apiVersion: '2026-05-22',
  })),
  writable: vi.fn(() => ({ subscribe: vi.fn(), set: vi.fn(), update: vi.fn() })),
}));
vi.mock('$app/environment', () => ({ browser: true }));
vi.mock('$lib/stores/settings', () => ({
  settings: { subscribe: vi.fn(), set: vi.fn() },
}));

const mockFetch = vi.fn();
global.fetch = mockFetch;
Object.defineProperty(global, 'crypto', {
  value: { randomUUID: () => 'test-uuid-1234' },
  configurable: true,
  writable: true,
});

import { loadConversations } from './loader';

describe('conversations loader', () => {
  beforeEach(() => {
    mockFetch.mockReset();
  });

  it('empty state: 200 with no conversations resolves to ok with an empty list', async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      text: () =>
        Promise.resolve(
          JSON.stringify({ api_version: '2026-05-22', conversations: [], next_cursor: null })
        ),
    });

    const view = await loadConversations();

    expect(view.kind).toBe('ok');
    if (view.kind === 'ok') {
      expect(view.conversations).toEqual([]);
      expect(view.nextCursor).toBeNull();
    }
  });

  it('populated state: 200 with rows maps the contract fields through', async () => {
    const conv = {
      tenant_id: 't1',
      app_id: 'a1',
      target: 'mock',
      conversation_key: 'c1',
      provider_thread_ref: 'https://example/chat/1',
      state: 'active',
      created_at: '2026-07-15T00:00:00Z',
      last_used_at: '2026-07-15T01:00:00Z',
      last_job_id: 'job-1',
    };
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      text: () => Promise.resolve(JSON.stringify({ conversations: [conv], next_cursor: null })),
    });

    const view = await loadConversations();

    expect(view.kind).toBe('ok');
    if (view.kind === 'ok') {
      expect(view.conversations).toHaveLength(1);
      const row = view.conversations[0];
      expect(row.conversation_key).toBe('c1');
      expect(row.target).toBe('mock');
      expect(row.state).toBe('active');
      expect(row.last_used_at).toBe('2026-07-15T01:00:00Z');
      expect(row.last_job_id).toBe('job-1');
    }
  });

  it('disabled state: 501 resolves to disabled and fabricates no rows', async () => {
    mockFetch.mockResolvedValue({
      ok: false,
      status: 501,
      text: () => Promise.resolve(JSON.stringify({ error: 'conversations not enabled' })),
    });

    const view = await loadConversations();

    expect(view.kind).toBe('disabled');
    // A disabled view carries no conversations array at all — nothing to render.
    expect('conversations' in view).toBe(false);
  });
});
