import test from 'node:test';
import assert from 'node:assert/strict';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { detectDrift } from '../src/index.ts';
import { loadAdapterRegistry } from '../src/node.mjs';

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
