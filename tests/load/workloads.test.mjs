// Offline checks for the load workload manifests: schema validation + deterministic synthetic fixtures.
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { describe, it } from 'node:test';
import { fileURLToPath } from 'node:url';
import { loadWorkload, multipartBody, sha256Hex, syntheticWav, validateSchema, validateWorkload, wavSize, workloadSha256 } from './workloads.mjs';

const dir = join(dirname(fileURLToPath(import.meta.url)), 'workloads');
const clone = (x) => JSON.parse(JSON.stringify(x));

describe('workload manifests', () => {
  it('every shipped manifest validates against workload.schema.json', () => {
    const manifests = readdirSync(dir).filter((f) => f.endsWith('.json') && f !== 'workload.schema.json');
    assert.ok(manifests.includes('audio-upload.json'));
    for (const f of manifests) assert.deepEqual(validateWorkload(JSON.parse(readFileSync(join(dir, f), 'utf8'))), [], f);
  });

  it('rejects structurally and semantically bad manifests', () => {
    const good = loadWorkload('audio-upload');
    const mutate = (fn) => { const m = clone(good); fn(m); return validateWorkload(m).join('\n'); };
    assert.match(mutate((m) => { delete m.fixture; }), /missing required "fixture"/);
    assert.match(mutate((m) => { m.extra = 1; }), /unknown property "extra"/);
    assert.match(mutate((m) => { m.synthetic = false; }), /synthetic/);
    assert.match(mutate((m) => { m.fixture.bits_per_sample = 24; }), /bits_per_sample/);
    assert.match(mutate((m) => { m.fixture.default_profile = 'nope'; }), /default_profile/);
    assert.match(mutate((m) => { m.fixture.profiles.large.duration_s = 3000; }), /exceeds max_bytes/);
    assert.match(mutate((m) => { m.fixture.content_type = 'audio/ogg'; }), /content_type is not listed/);
    assert.match(mutate((m) => { m.fixture.profiles.short.duration_s = 0; }), /below 1/);
    assert.throws(() => loadWorkload('../etc'), /invalid workload name/);
  });

  it('ships the five recorded workloads, named after their file and kind', () => {
    const names = ['attachment', 'audio-upload', 'mixed', 'text', 'voice'];
    assert.deepEqual(readdirSync(dir).filter((f) => f !== 'workload.schema.json').map((f) => f.replace(/\.json$/, '')).sort(), names);
    for (const n of names) { const m = loadWorkload(n); assert.equal(m.name, n); assert.equal(m.kind, n); }
    assert.equal(loadWorkload('voice').provider_scenario.requires_live_media, true);
  });

  it('rejects broken mix, ladder and kind rules', () => {
    const mutate = (name, fn) => { const m = clone(loadWorkload(name)); fn(m); return validateWorkload(m).join('\n'); };
    assert.match(mutate('mixed', (m) => { m.mix[0].weight = 60; }), /sum to 100/);
    assert.match(mutate('mixed', (m) => { m.mix[0].kind = 'video'; }), /one of/);
    assert.match(mutate('mixed', (m) => { delete m.ladder_steps; }), /missing required "ladder_steps"/);
    assert.match(mutate('mixed', (m) => { m.mix[0].payload_bytes = { min: 9, max: 1 }; }), /min > max/);
    assert.match(mutate('mixed', (m) => { m.think_time_ms = { min: 9, max: 1 }; }), /think_time_ms: min > max/);
    assert.match(mutate('mixed', (m) => { m.ladder_steps = [5, 5]; }), /ascending/);
    assert.match(mutate('mixed', (m) => { m.schema_version = 2; }), /must equal 1/);
    assert.match(mutate('mixed', (m) => { m.name = 'Bad Name'; }), /does not match/);
    assert.match(mutate('mixed', (m) => { m.mix = []; }), /fewer than 1/);
    assert.match(mutate('mixed', (m) => { m.fixture = clone(loadWorkload('audio-upload').fixture); }), /only valid for kind audio-upload/);
    assert.match(mutate('audio-upload', (m) => { delete m.prompt; }), /missing required "prompt"/);
    assert.match(mutate('voice', (m) => { m.harness_support = 'implemented'; }), /live-media/);
  });

  it('hashes manifests independent of line endings', () => {
    assert.match(workloadSha256('text'), /^[0-9a-f]{64}$/);
    assert.notEqual(workloadSha256('text'), workloadSha256('mixed'));
    assert.equal(workloadSha256('text'), sha256Hex(Buffer.from(readFileSync(join(dir, 'text.json'), 'utf8').replace(/\r\n/g, '\n'))));
  });

  it('the checker covers the keywords it claims to', () => {
    assert.deepEqual(validateSchema({ type: 'array', minItems: 1, items: { type: 'integer', maximum: 3 } }, [1, 4]), ['$[1]: above 3']);
    assert.deepEqual(validateSchema({ type: 'object', required: ['a'], additionalProperties: { type: 'string' } }, { a: 'x', b: 1 }), ['$.b: must be string']);
  });

  it('every audio profile stays under the facade transcription cap and the gateway per-file ceiling', () => {
    const { fixture } = loadWorkload('audio-upload');
    for (const name of Object.keys(fixture.profiles)) assert.ok(wavSize(fixture, name) <= 24 << 20, name); // 24 MiB: maxTranscriptionAudioBytes
    assert.ok(fixture.max_bytes <= 32 << 20); // maxArtifactBodyBytes
  });
});

describe('synthetic fixtures', () => {
  const { fixture } = loadWorkload('audio-upload');

  it('produces a deterministic, well-formed PCM WAV of the advertised size', () => {
    const a = syntheticWav(fixture, 'short'); const b = syntheticWav(fixture, 'short');
    assert.equal(a.length, wavSize(fixture, 'short'));
    assert.equal(sha256Hex(a), sha256Hex(b));
    assert.equal(a.toString('latin1', 0, 4), 'RIFF');
    assert.equal(a.toString('latin1', 8, 16), 'WAVEfmt ');
    assert.equal(a.readUInt32LE(4), a.length - 8);
    assert.equal(a.readUInt32LE(24), 16000);
    assert.equal(a.readUInt32LE(40), a.length - 44);
    assert.throws(() => syntheticWav(fixture, 'nope'), /unknown fixture profile/);
  });

  it('builds multipart bodies a standard parser round-trips', async () => {
    const wav = syntheticWav(fixture, 'short');
    const { body, contentType } = multipartBody([
      { name: 'job', data: '{"a":1}', contentType: 'application/json' },
      { name: 'audio-0.wav', filename: 'audio-0.wav', data: wav, contentType: 'audio/wav' },
    ]);
    const form = await new Response(body, { headers: { 'Content-Type': contentType } }).formData();
    assert.equal(form.get('job'), '{"a":1}');
    const file = form.get('audio-0.wav');
    assert.equal(file.type, 'audio/wav');
    assert.equal(sha256Hex(Buffer.from(await file.arrayBuffer())), sha256Hex(wav));
  });
});
