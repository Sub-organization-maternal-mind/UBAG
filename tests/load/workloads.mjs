// Workload manifests for tests/load: loader, schema check, deterministic synthetic fixtures.
// Zero dependencies. The schema checker implements only the JSON-schema keywords that
// workloads/workload.schema.json uses (type, const, enum, required, properties,
// additionalProperties, items, minItems, minimum, maximum, minLength, pattern).
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dir = join(dirname(fileURLToPath(import.meta.url)), 'workloads');
const readJson = (name) => JSON.parse(readFileSync(join(dir, name), 'utf8'));

const typeOk = (type, v) => ({
  object: v !== null && typeof v === 'object' && !Array.isArray(v), array: Array.isArray(v), string: typeof v === 'string',
  integer: Number.isInteger(v), number: typeof v === 'number' && Number.isFinite(v), boolean: typeof v === 'boolean',
})[type] ?? false;

/** Returns a list of error strings (empty = valid). */
export function validateSchema(schema, value, path = '$') {
  const errs = []; const bad = (m) => errs.push(`${path}: ${m}`);
  if ('const' in schema && value !== schema.const) bad(`must equal ${JSON.stringify(schema.const)}`);
  if (schema.enum && !schema.enum.includes(value)) bad(`must be one of ${JSON.stringify(schema.enum)}`);
  if (schema.type && !typeOk(schema.type, value)) { bad(`must be ${schema.type}`); return errs; }
  if (typeof value === 'string') {
    if (schema.minLength != null && value.length < schema.minLength) bad(`shorter than ${schema.minLength}`);
    if (schema.pattern && !new RegExp(schema.pattern).test(value)) bad(`does not match ${schema.pattern}`);
  }
  if (typeof value === 'number') {
    if (schema.minimum != null && value < schema.minimum) bad(`below ${schema.minimum}`);
    if (schema.maximum != null && value > schema.maximum) bad(`above ${schema.maximum}`);
  }
  if (Array.isArray(value)) {
    if (schema.minItems != null && value.length < schema.minItems) bad(`fewer than ${schema.minItems} items`);
    if (schema.items) value.forEach((v, i) => errs.push(...validateSchema(schema.items, v, `${path}[${i}]`)));
  }
  if (typeOk('object', value)) {
    for (const k of schema.required ?? []) if (!(k in value)) bad(`missing required "${k}"`);
    for (const [k, v] of Object.entries(value)) {
      const sub = schema.properties?.[k];
      if (sub) errs.push(...validateSchema(sub, v, `${path}.${k}`));
      else if (schema.additionalProperties === false) bad(`unknown property "${k}"`);
      else if (typeOk('object', schema.additionalProperties)) errs.push(...validateSchema(schema.additionalProperties, v, `${path}.${k}`));
    }
  }
  return errs;
}

/** Exact byte size of the synthetic WAV for a fixture profile (44-byte header + PCM). */
export function wavSize(fx, profile) {
  return 44 + fx.profiles[profile].duration_s * fx.sample_rate_hz * fx.channels * (fx.bits_per_sample / 8);
}

/** Schema check plus the cross-field rules a schema cannot express. */
export function validateWorkload(manifest, schema = readJson('workload.schema.json')) {
  const errs = validateSchema(schema, manifest);
  if (errs.length) return errs;
  const AUDIO_ONLY = ['synthetic', 'route', 'prompt', 'target_requirements', 'fixture'];
  const isAudio = manifest.kind === 'audio-upload';
  for (const k of AUDIO_ONLY) {
    if (isAudio && !(k in manifest)) errs.push(`$: missing required "${k}"`);
    if (!isAudio && k in manifest) errs.push(`$.${k}: only valid for kind audio-upload`);
  }
  const sum = manifest.mix.reduce((a, m) => a + m.weight, 0);
  if (sum !== 100) errs.push(`$.mix: weights must sum to 100 (got ${sum})`);
  manifest.mix.forEach((m, i) => { if (m.payload_bytes.min > m.payload_bytes.max) errs.push(`$.mix[${i}].payload_bytes: min > max`); });
  if (manifest.think_time_ms.min > manifest.think_time_ms.max) errs.push('$.think_time_ms: min > max');
  if (manifest.ladder_steps.some((v, i, a) => i > 0 && v <= a[i - 1])) errs.push('$.ladder_steps: must be strictly ascending');
  if (manifest.provider_scenario.requires_live_media && manifest.harness_support === 'implemented') errs.push('$.harness_support: a live-media workload cannot be "implemented"');
  const fx = manifest.fixture;
  if (errs.length || !fx) return errs;
  if (!fx.profiles[fx.default_profile]) errs.push(`$.fixture.default_profile "${fx.default_profile}" is not a defined profile`);
  for (const name of Object.keys(fx.profiles)) if (wavSize(fx, name) > fx.max_bytes) errs.push(`$.fixture.profiles.${name}: ${wavSize(fx, name)} bytes exceeds max_bytes ${fx.max_bytes}`);
  if (!manifest.target_requirements.attachment_kinds.includes(fx.attachment_kind)) errs.push('$.fixture.attachment_kind is not listed in target_requirements.attachment_kinds');
  if (!manifest.target_requirements.content_types.includes(fx.content_type)) errs.push('$.fixture.content_type is not listed in target_requirements.content_types');
  return errs;
}

/** SHA-256 of the manifest file with CRLF normalised, so Windows (autocrlf) and Linux checkouts agree. */
export const workloadSha256 = (name) => sha256Hex(Buffer.from(readFileSync(join(dir, `${name}.json`), 'utf8').replace(/\r\n/g, '\n')));

/** Load and validate a manifest by name; throws with every error so a bad manifest fails closed. */
export function loadWorkload(name) {
  if (!/^[a-z][a-z0-9-]*$/.test(name)) throw new Error(`invalid workload name "${name}"`);
  const manifest = readJson(`${name}.json`);
  const errs = validateWorkload(manifest);
  if (errs.length) throw new Error(`workload "${name}" is invalid:\n${errs.join('\n')}`);
  return manifest;
}

/** Deterministic mono/stereo 16-bit PCM WAV (440 Hz tone). Same inputs => identical bytes. */
export function syntheticWav(fx, profile) {
  if (!fx.profiles[profile]) throw new Error(`unknown fixture profile "${profile}" (have ${Object.keys(fx.profiles).join(', ')})`);
  const frames = fx.profiles[profile].duration_s * fx.sample_rate_hz;
  const blockAlign = fx.channels * (fx.bits_per_sample / 8); const dataBytes = frames * blockAlign;
  const buf = Buffer.alloc(44 + dataBytes);
  buf.write('RIFF', 0); buf.writeUInt32LE(36 + dataBytes, 4); buf.write('WAVEfmt ', 8); buf.writeUInt32LE(16, 16);
  buf.writeUInt16LE(1, 20); buf.writeUInt16LE(fx.channels, 22); buf.writeUInt32LE(fx.sample_rate_hz, 24);
  buf.writeUInt32LE(fx.sample_rate_hz * blockAlign, 28); buf.writeUInt16LE(blockAlign, 32); buf.writeUInt16LE(fx.bits_per_sample, 34);
  buf.write('data', 36); buf.writeUInt32LE(dataBytes, 40);
  for (let i = 0, off = 44; i < frames; i += 1) {
    const s = Math.round(8000 * Math.sin((2 * Math.PI * 440 * i) / fx.sample_rate_hz));
    for (let c = 0; c < fx.channels; c += 1, off += 2) buf.writeInt16LE(s, off);
  }
  return buf;
}

export const sha256Hex = (buf) => createHash('sha256').update(buf).digest('hex');

/** multipart/form-data body: parts = [{name, data, contentType, filename?}]. Returns {body, contentType}. */
export function multipartBody(parts, boundary = `ubag-load-${createHash('sha256').update(parts.map((p) => p.name).join()).digest('hex').slice(0, 16)}`) {
  const chunks = [];
  for (const p of parts) {
    const disp = `form-data; name="${p.name}"${p.filename ? `; filename="${p.filename}"` : ''}`;
    chunks.push(Buffer.from(`--${boundary}\r\nContent-Disposition: ${disp}\r\nContent-Type: ${p.contentType}\r\n\r\n`), Buffer.isBuffer(p.data) ? p.data : Buffer.from(p.data), Buffer.from('\r\n'));
  }
  chunks.push(Buffer.from(`--${boundary}--\r\n`));
  return { body: Buffer.concat(chunks), contentType: `multipart/form-data; boundary=${boundary}` };
}
