import { describe, it, expect } from 'vitest';

// Group B wiring: the dashboard pages must call the REAL contract routes.
// These guards pin the exact paths + payload shapes the gateway serves, so a
// page cannot regress to an invented route (the group-A replay regression).

function extractPaths(source: string): string[] {
  return [...source.matchAll(/['"`](\/v1\/[^'"`]+)['"`]/g)].map((m) => m[1]);
}

function readPage(name: string): string {
  // Vitest resolves fs relative to the project root config; use import.meta.url.
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const { readFileSync } = require('node:fs') as typeof import('node:fs');
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const { dirname, join, resolve } = require('node:path') as typeof import('node:path');
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const { fileURLToPath } = require('node:url') as typeof import('node:url');
  const here = dirname(fileURLToPath(import.meta.url));
  return readFileSync(resolve(here, name), 'utf8');
}

describe('group B route wiring', () => {
  it('security page calls the contract PAT route', () => {
    const src = readPage('../../routes/security/+page.svelte');
    expect(extractPaths(src)).toContain('/v1/auth/pat');
    expect(extractPaths(src)).toContain('/v1/mfa/verify');
    expect(extractPaths(src)).toContain('/v1/sso/logout');
  });

  it('admin page calls the contract privacy/elevation/region routes', () => {
    const src = readPage('../../routes/admin/+page.svelte');
    const paths = extractPaths(src);
    expect(paths).toContain('/v1/privacy/export');
    expect(paths).toContain('/v1/privacy/erase');
    expect(paths).toContain('/v1/admin/elevation');
    expect(paths.some((p) => p.startsWith('/v1/admin/elevation/'))).toBe(true);
    expect(paths.some((p) => p.startsWith('/v1/admin/regions/'))).toBe(true);
  });

  it('settings page calls the contract SIEM config route', () => {
    const src = readPage('../../routes/settings/+page.svelte');
    expect(extractPaths(src)).toContain('/v1/siem/config');
  });

  it('browser page calls the contract concurrency route', () => {
    const src = readPage('../../routes/browser/+page.svelte');
    expect(extractPaths(src)).toContain('/v1/concurrency');
  });

  it('webhook replay uses the flat contract route, not an invented subresource', () => {
    const src = readPage('../../routes/webhooks/+page.svelte');
    const paths = extractPaths(src);
    expect(paths).toContain('/v1/webhooks/replay');
    // The invented path from the group-A regression must never come back.
    expect(paths.some((p) => p.includes('/deliveries/') && p.endsWith('/replay'))).toBe(false);
  });

  it('webhook replay body carries delivery_id + reason (openapi required fields)', () => {
    const src = readPage('../../routes/webhooks/+page.svelte');
    expect(src).toContain('delivery_id:');
    expect(src).toContain('reason:');
  });

  it('jobs page calls the contract batch route', () => {
    const src = readPage('../../routes/jobs/+page.svelte');
    expect(extractPaths(src)).toContain('/v1/jobs/batch');
  });
});
