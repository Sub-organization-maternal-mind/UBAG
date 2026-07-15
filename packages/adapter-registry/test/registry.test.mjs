import test from 'node:test';
import assert from 'node:assert/strict';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  detectDrift,
  validateAdapterManifest,
  buildRegistryEntry,
  RegistryError,
} from '../src/index.ts';
import { loadAdapterRegistry, adapterManifestSchema } from '../src/node.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, '..', '..', '..');
const adaptersDir = join(repoRoot, 'adapters');

test('loads and validates every adapter listed in registry.json', () => {
  const { registry, index } = loadAdapterRegistry({ adaptersDir });
  const ids = registry.list().map((entry) => entry.id);
  assert.ok(ids.includes('mock'));
  assert.ok(ids.includes('chatgpt_web'));
  assert.equal(index.schema_version, 'ubag.adapters.index.v1');
  assert.equal(index.adapters.length, registry.list().length);

  for (const entry of index.adapters) {
    assert.match(entry.checksum, /^sha256:[0-9a-f]{64}$/);
  }
});

test('get / getManifest resolve by id and alias', () => {
  const { registry } = loadAdapterRegistry({ adaptersDir });
  const mock = registry.get('mock');
  assert.ok(mock);
  assert.equal(mock.status, 'mock');

  const manifest = registry.getManifest('chatgpt_web');
  assert.ok(manifest);
  assert.ok(Array.isArray(manifest.aliases));
  // chatgpt_web declares the alias "chatgpt"
  const resolved = registry.resolve('chatgpt');
  assert.equal(resolved?.id, 'chatgpt_web');
});

test('capability, status, and command-type filters work', () => {
  const { registry } = loadAdapterRegistry({ adaptersDir });

  const streaming = registry.filterByCapability('token_streaming').map((e) => e.id);
  assert.ok(streaming.includes('mock'));

  const stubs = registry.filterByStatus('stub').map((e) => e.id);
  assert.ok(stubs.includes('chatgpt_web'));

  const promptable = registry.filterByCommandType('chat.prompt').map((e) => e.id);
  assert.ok(promptable.includes('chatgpt_web'));
});

test('drift metadata distinguishes selector-based adapters', () => {
  const { registry } = loadAdapterRegistry({ adaptersDir });
  assert.equal(registry.get('mock')?.drift.baseline_required, false);
  assert.equal(registry.get('chatgpt_web')?.drift.baseline_required, true);
});

test('a freshly generated index is in sync with disk', () => {
  const { index } = loadAdapterRegistry({ adaptersDir });
  const report = detectDrift(index, index.adapters);
  assert.equal(report.inSync, true);
  assert.deepEqual(report.added, []);
  assert.deepEqual(report.removed, []);
  assert.deepEqual(report.changed, []);
});

test('a tampered baseline checksum is detected as drift', () => {
  const { index } = loadAdapterRegistry({ adaptersDir });
  const tamperedBaseline = {
    ...index,
    adapters: index.adapters.map((entry, i) =>
      i === 0 ? { ...entry, checksum: `sha256:${'0'.repeat(64)}` } : entry,
    ),
  };
  const report = detectDrift(tamperedBaseline, index.adapters);
  assert.equal(report.inSync, false);
  assert.equal(report.changed.length, 1);
});

const ZERO_CHECKSUM = `sha256:${'0'.repeat(64)}`;

/** Minimal manifest carrying only the required fields, for catalog cases. */
function baseManifest() {
  return {
    schema_version: 'ubag.adapter.v0',
    id: 'sample',
    display_name: 'Sample',
    version: '0.1.0',
    status: 'mock',
    supported_command_types: ['chat.prompt'],
    capabilities: [],
    selector_strategy: { type: 'none', drift_baseline_required: false },
    safe_mode: { user_owned_sessions_only: true },
  };
}

test('rejects a model_catalog whose choice values contain a non-string', () => {
  const manifest = {
    ...baseManifest(),
    model_catalog: { settings: { model: { kind: 'choice', values: ['ok', 123] } } },
  };
  assert.throws(
    () => validateAdapterManifest(manifest, adapterManifestSchema),
    (err) => err instanceof RegistryError && err.code === 'manifest_invalid',
  );
});

test('rejects a model_catalog setting with an unknown kind', () => {
  const manifest = {
    ...baseManifest(),
    model_catalog: { settings: { model: { kind: 'slider', values: ['a'] } } },
  };
  assert.throws(
    () => validateAdapterManifest(manifest, adapterManifestSchema),
    (err) => err instanceof RegistryError && err.code === 'manifest_invalid',
  );
});

test('accepts a valid model_catalog and surfaces it on the registry entry', () => {
  const manifest = {
    ...baseManifest(),
    model_catalog: {
      settings: {
        model: { kind: 'choice', values: ['a', 'b'] },
        deepthink: { kind: 'toggle' },
      },
    },
  };
  // Must not throw.
  validateAdapterManifest(manifest, adapterManifestSchema);
  const entry = buildRegistryEntry('sample/manifest.json', manifest, ZERO_CHECKSUM);
  assert.deepEqual(entry.model_catalog, manifest.model_catalog);
});

test('a manifest without a model_catalog produces an entry without one', () => {
  const manifest = baseManifest();
  validateAdapterManifest(manifest, adapterManifestSchema);
  const entry = buildRegistryEntry('sample/manifest.json', manifest, ZERO_CHECKSUM);
  assert.equal(entry.model_catalog, undefined);
});

test('model_catalog is surfaced on the loaded mock registry entry', () => {
  const { registry } = loadAdapterRegistry({ adaptersDir });
  const mock = registry.get('mock');
  assert.ok(mock.model_catalog, 'mock entry should carry a model_catalog');
  assert.equal(mock.model_catalog.settings.model.kind, 'choice');
  assert.deepEqual(mock.model_catalog.settings.model.values, ['mock-fast', 'mock-deep']);
  assert.equal(mock.model_catalog.settings.deepthink.kind, 'toggle');
});
