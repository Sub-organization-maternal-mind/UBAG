import test from 'node:test';
import assert from 'node:assert/strict';

import {
  RegistryError,
  buildRegistryEntry,
  buildRegistryIndex,
  detectDrift,
  formatChecksum,
  isChecksum,
} from '../src/index.ts';

const HEX = 'a'.repeat(64);

function manifest(overrides = {}) {
  return {
    schema_version: 'ubag.adapter.v0',
    id: 'mock',
    display_name: 'Mock',
    version: '0.1.0',
    status: 'mock',
    supported_command_types: ['chat.prompt'],
    capabilities: ['token_streaming'],
    selector_strategy: { type: 'none', drift_baseline_required: false },
    ...overrides,
  };
}

test('formatChecksum normalizes and validates hex digests', () => {
  assert.equal(formatChecksum(HEX.toUpperCase()), `sha256:${HEX}`);
  assert.ok(isChecksum(`sha256:${HEX}`));
  assert.ok(!isChecksum('sha256:zzz'));
  assert.throws(() => formatChecksum('not-hex'), RegistryError);
});

test('buildRegistryEntry derives drift metadata and rejects bad checksums', () => {
  const entry = buildRegistryEntry('mock/manifest.json', manifest(), `sha256:${HEX}`);
  assert.equal(entry.id, 'mock');
  assert.equal(entry.version, '0.1.0');
  assert.equal(entry.drift.baseline_required, false);
  assert.equal(entry.drift.selector_strategy_type, 'none');
  assert.throws(() => buildRegistryEntry('mock/manifest.json', manifest(), 'bad'), RegistryError);
});

test('drift baseline flag reflects selector strategy', () => {
  const entry = buildRegistryEntry(
    'chatgpt_web/manifest.json',
    manifest({ id: 'chatgpt_web', selector_strategy: { type: 'provider_specific', drift_baseline_required: true } }),
    `sha256:${'b'.repeat(64)}`,
  );
  assert.equal(entry.drift.baseline_required, true);
  assert.equal(entry.drift.selector_strategy_type, 'provider_specific');
});

test('buildRegistryIndex sorts entries and rejects duplicates', () => {
  const a = buildRegistryEntry('a/manifest.json', manifest({ id: 'aaa' }), `sha256:${HEX}`);
  const b = buildRegistryEntry('b/manifest.json', manifest({ id: 'bbb' }), `sha256:${'c'.repeat(64)}`);
  const index = buildRegistryIndex([b, a]);
  assert.deepEqual(index.adapters.map((e) => e.id), ['aaa', 'bbb']);
  assert.equal(index.schema_version, 'ubag.adapters.index.v1');
  assert.throws(() => buildRegistryIndex([a, a]), RegistryError);
});

test('detectDrift reports added, removed, and changed adapters', () => {
  const base = buildRegistryEntry('mock/manifest.json', manifest(), `sha256:${HEX}`);
  const expectedIndex = buildRegistryIndex([base]);

  const inSync = detectDrift(expectedIndex, [base]);
  assert.equal(inSync.inSync, true);

  const mutated = { ...base, checksum: `sha256:${'d'.repeat(64)}` };
  const added = buildRegistryEntry('extra/manifest.json', manifest({ id: 'extra' }), `sha256:${'e'.repeat(64)}`);
  const report = detectDrift(expectedIndex, [mutated, added]);
  assert.equal(report.inSync, false);
  assert.deepEqual(report.added, ['extra']);
  assert.equal(report.changed.length, 1);
  assert.equal(report.changed[0].id, 'mock');

  const removedReport = detectDrift(expectedIndex, []);
  assert.deepEqual(removedReport.removed, ['mock']);
});
