import { describe, it, expect } from 'vitest';
import { UBAG_ENDPOINTS } from '../../../../../packages/sdk-typescript/src/generated/contract-manifest';

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

  it('users page calls the contract SCIM v2 routes, not invented /v1/scim/users', () => {
    const paths = extractPaths(readPage('../../routes/users/+page.svelte'));
    expect(paths).toContain('/v1/scim/v2/Users');
    expect(paths).toContain('/v1/scim/v2/Groups');
    expect(paths.some((p) => /^\/v1\/scim\/(users|groups)/.test(p))).toBe(false);
  });
});

// Every literal /v1 path a dashboard route calls must exist in the generated
// contract manifest (UBAG_ENDPOINTS, built from packages/openapi). Template
// segments ({id} in the contract, ${expr} in the page) match any one segment;
// a trailing-slash literal is a prefix of a dynamically built path.
function matchesContract(path: string): boolean {
  const clean = path.split('?')[0];
  const isPrefix = clean.endsWith('/');
  const parts = clean.split('/').filter(Boolean);
  return Object.values(UBAG_ENDPOINTS).some((ep) => {
    const tpl = ep.path.split('/').filter(Boolean);
    if (isPrefix ? tpl.length <= parts.length : tpl.length !== parts.length) return false;
    return parts.every(
      (seg, i) => seg.includes('${') || (tpl[i].startsWith('{') && tpl[i].endsWith('}')) || seg === tpl[i]
    );
  });
}

function routePages(): string[] {
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const { readdirSync } = require('node:fs') as typeof import('node:fs');
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const { dirname, join, resolve } = require('node:path') as typeof import('node:path');
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const { fileURLToPath } = require('node:url') as typeof import('node:url');
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../routes');
  const out: string[] = [];
  const walk = (dir: string) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      if (e.isDirectory()) walk(join(dir, e.name));
      else if (e.name === '+page.svelte') out.push(join(dir, e.name));
    }
  };
  walk(root);
  return out;
}

// Known pre-existing gaps, tracked as P2.1 follow-ups (shrink this list, never grow it):
// - secret:rotate IS in openapi.yaml and served by the gateway, but tools/make-sdks/generate-manifest.mjs
//   path regex (`[^s:]+`) drops ':' paths, so it is missing from UBAG_ENDPOINTS.
// - /v1/webhooks/{id}/deliveries is an invented route (not in openapi.yaml or the gateway); the webhooks
//   page deliveries panel needs a contract decision.
const KNOWN_DRIFT = new Set(['/v1/webhooks/secret:rotate', '/v1/webhooks/${webhookId}/deliveries']);

describe('dashboard paths vs UBAG_ENDPOINTS', () => {
  it('matcher accepts contract templates and rejects invented routes', () => {
    expect(matchesContract('/v1/scim/v2/Users')).toBe(true);
    expect(matchesContract('/v1/jobs/${id}/cancel')).toBe(true);
    expect(matchesContract('/v1/admin/elevation/')).toBe(true);
    expect(matchesContract('/v1/scim/users')).toBe(false);
  });

  it('every literal /v1 path in a route page exists in the contract', () => {
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const { readFileSync } = require('node:fs') as typeof import('node:fs');
    const unknown: string[] = [];
    for (const file of routePages()) {
      for (const p of extractPaths(readFileSync(file, 'utf8'))) {
        if (!matchesContract(p) && !KNOWN_DRIFT.has(p)) unknown.push(`${file.split('routes')[1]}: ${p}`);
      }
    }
    expect(unknown).toEqual([]);
  });
});
