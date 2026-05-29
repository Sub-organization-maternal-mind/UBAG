import test from 'node:test';
import assert from 'node:assert/strict';

import { parsePluginManifest } from '../src/manifest.ts';
import { buildGuestContext } from '../src/permissions.ts';
import { PermissionDeniedError, PluginExecutionError } from '../src/errors.ts';

function manifestWith(permissions) {
  return parsePluginManifest({
    schema_version: 'ubag.plugin.v0',
    id: 'perm-test',
    display_name: 'Perm Test',
    version: '0.0.1',
    entrypoint: { type: 'wasi-component', module: 'p.wasm', exports: { transform: 'transform' } },
    capabilities: ['transform.response'],
    permissions,
    engine: { runtime: 'wasi-preview2' },
  });
}

const fullBindings = {
  log: () => {},
  clock: () => 123,
  random: () => 0.5,
  fetch: () => ({ status: 200, headers: {}, body: 'ok' }),
  read_file: () => 'file-contents',
  get_env: () => 'value',
};

test('granted host functions are callable', () => {
  const manifest = manifestWith({ host_functions: ['log', 'clock'] });
  const ctx = buildGuestContext(manifest, fullBindings, { capability: 'transform.response', now: () => 0 });
  assert.doesNotThrow(() => ctx.log('info', 'hi'));
  assert.equal(ctx.clock(), 123);
});

test('ungranted host function throws PermissionDeniedError', () => {
  const manifest = manifestWith({ host_functions: ['log'] });
  const ctx = buildGuestContext(manifest, fullBindings, { capability: 'transform.response', now: () => 0 });
  assert.throws(() => ctx.fetch({ url: 'https://api.example.com' }), PermissionDeniedError);
  assert.throws(() => ctx.readFile('/tmp/x'), PermissionDeniedError);
  assert.throws(() => ctx.getEnv('HOME'), PermissionDeniedError);
  assert.throws(() => ctx.random(), PermissionDeniedError);
});

test('network egress is restricted to the allowed host list', () => {
  const manifest = manifestWith({
    host_functions: ['fetch'],
    network: { allowed: true, allowed_hosts: ['api.example.com', '*.trusted.dev'] },
  });
  const ctx = buildGuestContext(manifest, fullBindings, { capability: 'transform.response', now: () => 0 });
  assert.deepEqual(ctx.fetch({ url: 'https://api.example.com/v1' }), { status: 200, headers: {}, body: 'ok' });
  assert.deepEqual(ctx.fetch({ url: 'https://edge.trusted.dev/x' }), { status: 200, headers: {}, body: 'ok' });
  assert.throws(() => ctx.fetch({ url: 'https://evil.test/steal' }), PermissionDeniedError);
});

test('filesystem reads are restricted to allowed paths and block traversal', () => {
  const manifest = manifestWith({
    host_functions: ['read_file'],
    filesystem: { allowed: true, allowed_paths: ['/data/plugins'] },
  });
  const ctx = buildGuestContext(manifest, fullBindings, { capability: 'transform.response', now: () => 0 });
  assert.equal(ctx.readFile('/data/plugins/config.json'), 'file-contents');
  assert.throws(() => ctx.readFile('/etc/passwd'), PermissionDeniedError);
  assert.throws(() => ctx.readFile('/data/plugins/../../etc/passwd'), PermissionDeniedError);
});

test('env reads are restricted to allowed keys', () => {
  const manifest = manifestWith({
    host_functions: ['get_env'],
    env: { allowed: true, allowed_keys: ['UBAG_REGION'] },
  });
  const ctx = buildGuestContext(manifest, fullBindings, { capability: 'transform.response', now: () => 0 });
  assert.equal(ctx.getEnv('UBAG_REGION'), 'value');
  assert.throws(() => ctx.getEnv('AWS_SECRET_ACCESS_KEY'), PermissionDeniedError);
});

test('granted host function with no binding throws an execution error', () => {
  const manifest = manifestWith({ host_functions: ['clock'] });
  const ctx = buildGuestContext(manifest, {}, { capability: 'transform.response', now: () => 0 });
  assert.throws(() => ctx.clock(), PluginExecutionError);
});
