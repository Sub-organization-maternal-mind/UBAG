import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { parsePluginManifest, parsePluginManifestJson } from '../src/manifest.ts';
import { ManifestValidationError } from '../src/errors.ts';

const here = dirname(fileURLToPath(import.meta.url));
const examplesDir = resolve(here, '..', 'examples');

function baseManifest() {
  return {
    schema_version: 'ubag.plugin.v0',
    id: 'response-normalizer',
    display_name: 'Response Normalizer',
    version: '0.1.0',
    entrypoint: {
      type: 'wasi-component',
      module: 'build/plugin.wasm',
      exports: { transform: 'transform' },
    },
    capabilities: ['transform.response'],
    permissions: { host_functions: ['log', 'clock'] },
    engine: { runtime: 'wasi-preview2' },
  };
}

test('parses a valid manifest and applies permission defaults', () => {
  const manifest = parsePluginManifest(baseManifest());
  assert.equal(manifest.id, 'response-normalizer');
  assert.deepEqual(manifest.capabilities, ['transform.response']);
  assert.equal(manifest.permissions.network.allowed, false);
  assert.equal(manifest.permissions.filesystem.allowed, false);
  assert.equal(manifest.permissions.env.allowed, false);
  assert.equal(manifest.permissions.max_memory_bytes, 16_777_216);
  assert.equal(manifest.permissions.max_execution_ms, 1_000);
});

test('rejects an invalid plugin id', () => {
  const bad = baseManifest();
  bad.id = 'Invalid ID!';
  assert.throws(() => parsePluginManifest(bad), (error) => {
    assert.ok(error instanceof ManifestValidationError);
    assert.ok(error.issues.some((issue) => issue.includes('id must match')));
    return true;
  });
});

test('rejects unknown capability and host function values', () => {
  const bad = baseManifest();
  bad.capabilities = ['transform.response', 'transform.unknown'];
  bad.permissions = { host_functions: ['log', 'telepathy'] };
  assert.throws(() => parsePluginManifest(bad), (error) => {
    assert.ok(error instanceof ManifestValidationError);
    assert.ok(error.issues.some((issue) => issue.includes('unknown capability')));
    assert.ok(error.issues.some((issue) => issue.includes('unknown host function')));
    return true;
  });
});

test('network permission requires the fetch host function', () => {
  const bad = baseManifest();
  bad.permissions = {
    host_functions: ['log'],
    network: { allowed: true, allowed_hosts: ['api.example.com'] },
  };
  assert.throws(() => parsePluginManifest(bad), (error) => {
    assert.ok(error instanceof ManifestValidationError);
    assert.ok(error.issues.some((issue) => issue.includes('requires host function "fetch"')));
    return true;
  });
});

test('allowlist entries without an allowed flag are rejected', () => {
  const bad = baseManifest();
  bad.permissions = {
    host_functions: ['read_file'],
    filesystem: { allowed: false, allowed_paths: ['/tmp/data'] },
  };
  assert.throws(() => parsePluginManifest(bad), (error) => {
    assert.ok(error instanceof ManifestValidationError);
    assert.ok(error.issues.some((issue) => issue.includes('allowed is false')));
    return true;
  });
});

test('entrypoint module must end in .wasm', () => {
  const bad = baseManifest();
  bad.entrypoint = { type: 'wasi-component', module: 'plugin.js', exports: { transform: 'transform' } };
  assert.throws(() => parsePluginManifest(bad), ManifestValidationError);
});

test('bundled example manifests are valid', () => {
  for (const id of ['response-normalizer', 'prompt-template']) {
    const text = readFileSync(join(examplesDir, id, 'plugin.manifest.json'), 'utf8');
    const manifest = parsePluginManifestJson(text);
    assert.equal(manifest.id, id);
  }
});
