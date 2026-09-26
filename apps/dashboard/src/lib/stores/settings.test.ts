import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';

vi.mock('$app/environment', () => ({ browser: true }));

describe('dashboard gateway URL', () => {
  beforeEach(() => {
    vi.resetModules();
    localStorage.removeItem('ubag_gateway_url');
  });

  afterEach(() => {
    localStorage.removeItem('ubag_gateway_url');
  });

  it('uses the running local gateway instead of the Vite origin for a fresh session', async () => {
    const { settings } = await import('./settings');

    expect(get(settings).gatewayUrl).toBe('http://127.0.0.1:58080');
  });

  it('keeps an explicitly saved gateway URL', async () => {
    localStorage.setItem('ubag_gateway_url', 'http://localhost:8081');
    const { settings } = await import('./settings');

    expect(get(settings).gatewayUrl).toBe('http://localhost:8081');
  });
});