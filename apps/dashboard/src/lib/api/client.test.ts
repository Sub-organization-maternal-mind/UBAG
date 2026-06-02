import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock svelte/store
vi.mock('svelte/store', () => ({
  get: vi.fn(() => ({
    gatewayUrl: 'http://localhost:8081',
    appSecret: 'test-secret',
    apiVersion: '2026-05-22',
  })),
  writable: vi.fn(() => ({ subscribe: vi.fn(), set: vi.fn(), update: vi.fn() })),
}));

// Mock $app/environment
vi.mock('$app/environment', () => ({ browser: true }));

// Mock $lib/stores/settings
vi.mock('$lib/stores/settings', () => ({
  settings: { subscribe: vi.fn(), set: vi.fn() },
}));

// Mock fetch globally
const mockFetch = vi.fn();
global.fetch = mockFetch;
Object.defineProperty(global, 'crypto', {
  value: { randomUUID: () => 'test-uuid-1234' },
  configurable: true,
  writable: true,
});

import { gw, api } from './client';

describe('gateway client', () => {
  beforeEach(() => {
    mockFetch.mockReset();
  });

  it('injects Ubag-Api-Version header on GET', async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      text: () => Promise.resolve(JSON.stringify({ status: 'ok' })),
    });

    await gw('GET', '/v1/health');

    const [, init] = mockFetch.mock.calls[0];
    expect(init.headers['Ubag-Api-Version']).toBe('2026-05-22');
  });

  it('injects Authorization header when appSecret is set', async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      text: () => Promise.resolve('{}'),
    });

    await gw('GET', '/v1/jobs');

    const [, init] = mockFetch.mock.calls[0];
    expect(init.headers['Authorization']).toBe('Bearer test-secret');
  });

  it('adds Idempotency-Key on POST (mutation)', async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      text: () => Promise.resolve('{}'),
    });

    await gw('POST', '/v1/jobs', { target: 'test' });

    const [, init] = mockFetch.mock.calls[0];
    expect(init.headers['Idempotency-Key']).toBe('test-uuid-1234');
  });

  it('does NOT add Idempotency-Key on GET', async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      text: () => Promise.resolve('{}'),
    });

    await gw('GET', '/v1/jobs');

    const [, init] = mockFetch.mock.calls[0];
    expect(init.headers['Idempotency-Key']).toBeUndefined();
  });

  it('returns denied:true on 403 response', async () => {
    mockFetch.mockResolvedValue({
      ok: false,
      status: 403,
      text: () => Promise.resolve(JSON.stringify({ error: 'forbidden' })),
    });

    const result = await gw('GET', '/v1/audit');

    expect(result.denied).toBe(true);
    expect(result.status).toBe(403);
  });

  it('returns error on network failure', async () => {
    mockFetch.mockRejectedValue(new Error('Network error'));

    const result = await gw('GET', '/v1/health');

    expect(result.status).toBe(-1);
    expect(result.error).toBe('Network error');
    expect(result.denied).toBe(false);
  });

  it('api.get convenience wrapper works', async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      text: () => Promise.resolve('{"items":[]}'),
    });

    const result = await api.get('/v1/targets');
    expect(result.status).toBe(200);
  });
});
