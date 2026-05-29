import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { parsePluginManifestJson } from '../src/manifest.ts';
import { MockWasmExecutor, PluginHost } from '../src/host.ts';
import { CapabilityUnsupportedError, PluginTimeoutError } from '../src/errors.ts';
import responseNormalizer from '../examples/response-normalizer/src/plugin.ts';
import promptTemplate from '../examples/prompt-template/src/plugin.ts';

const here = dirname(fileURLToPath(import.meta.url));
const examplesDir = resolve(here, '..', 'examples');

function loadExampleManifest(id) {
  return parsePluginManifestJson(readFileSync(join(examplesDir, id, 'plugin.manifest.json'), 'utf8'));
}

const moduleById = {
  'response-normalizer': responseNormalizer,
  'prompt-template': promptTemplate,
};

function buildHost(now = () => 0) {
  const executor = new MockWasmExecutor((manifest) => moduleById[manifest.id], now);
  const logs = [];
  const host = new PluginHost({
    executor,
    bindings: { log: (level, message) => logs.push(`${level}:${message}`), clock: () => 1717000000 },
    now,
  });
  return { host, logs };
}

test('response transform chain normalizes output and stamps timestamp', async () => {
  const { host } = buildHost();
  await host.register(loadExampleManifest('response-normalizer'));

  const result = await host.transform('response', {
    text: '  hello\n\n\n\nworld   \n',
    model: 'mock',
  });

  assert.equal(result.text, 'hello\n\nworld');
  assert.equal(result.finish_reason, 'stop');
  assert.equal(result.normalized_at, 1717000000);
  assert.equal(result.model, 'mock');
});

test('prompt transform renders template variables', async () => {
  const { host } = buildHost();
  await host.register(loadExampleManifest('prompt-template'));

  const result = await host.transform('prompt', {
    template: 'Hello {{name}}, region {{region}}.',
    variables: { name: 'Ada', region: 'eu' },
  });

  assert.equal(result.prompt, 'Hello Ada, region eu.');
});

test('job.pre hook rejects an empty prompt and continues otherwise', async () => {
  const { host } = buildHost();
  await host.register(loadExampleManifest('prompt-template'));

  const rejected = await host.runHooks('job.pre', { prompt: '   ' });
  assert.equal(rejected.action, 'reject');
  assert.equal(rejected.reason, 'prompt is empty');

  const accepted = await host.runHooks('job.pre', { prompt: 'do the thing' });
  assert.equal(accepted.action, 'continue');
});

test('plugins run in registration order across a multi-plugin host', async () => {
  const { host } = buildHost();
  await host.register(loadExampleManifest('prompt-template'));
  await host.register(loadExampleManifest('response-normalizer'));

  assert.deepEqual(host.listPlugins().map((p) => p.id), ['prompt-template', 'response-normalizer']);

  // prompt-template has no transform.response capability, so the response
  // transform only runs the normalizer.
  const out = await host.transform('response', { text: 'a\n\n\n\nb' });
  assert.equal(out.text, 'a\n\nb');
});

test('registering a plugin twice fails', async () => {
  const { host } = buildHost();
  await host.register(loadExampleManifest('response-normalizer'));
  await assert.rejects(() => host.register(loadExampleManifest('response-normalizer')));
});

test('declaring a capability without the matching export is rejected', async () => {
  const executor = new MockWasmExecutor(() => ({ /* no transform */ }));
  const host = new PluginHost({ executor });
  await assert.rejects(
    () => host.register(loadExampleManifest('response-normalizer')),
    CapabilityUnsupportedError,
  );
});

test('execution budget overrun raises a timeout error', async () => {
  let t = 0;
  // Advance the clock by 10s between start and end of the invocation, which
  // blows the response-normalizer 500ms budget.
  const now = () => {
    const value = t;
    t += 10_000;
    return value;
  };
  const executor = new MockWasmExecutor((manifest) => moduleById[manifest.id], now);
  const host = new PluginHost({
    executor,
    bindings: { log: () => {}, clock: () => 0 },
    now,
  });
  await host.register(loadExampleManifest('response-normalizer'));
  await assert.rejects(() => host.transform('response', { text: 'x' }), PluginTimeoutError);
});
