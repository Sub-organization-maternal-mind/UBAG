// Seeded corpus for the voice-relay A/B harness (perf-fleet P7.7). Pure: no network, no child processes.
// The same (seed, profile) always yields byte-identical scenarios, so the Python relay and any candidate see identical input.
// Packets are Opus-shaped but not speech: a TOC byte (all durations 2.5..120 ms) plus seeded random payload. libopus decodes
// any such payload deterministically, so decoded PCM can be compared byte for byte between two implementations that link the
// SAME libopus. Invalid and edge packets are included on purpose (they must be dropped identically).
import { createHash, createHmac } from 'node:crypto';
import { readFileSync } from 'node:fs';

export const CORPUS_VERSION = 1;
export const FRAME_BYTES = 1920; // 20 ms of 48 kHz mono s16le, what parec hands the relay per read
const FIXTURE_URL = new URL('../../packages/conformance/fixtures/voice-relay/v2.json', import.meta.url);

export const sha256Hex = (data) => createHash('sha256').update(data).digest('hex');

/** The shared relay protocol v2 fixture (also asserted by the Python and Go tests). */
export function loadFixture() {
  const text = readFileSync(FIXTURE_URL);
  return { ...JSON.parse(text.toString('utf8')), sha256: sha256Hex(text) };
}

// ---------------------------------------------------------------- PRNG (mulberry32), integer helpers

export function rng(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6D2B79F5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
const int = (r, lo, hi) => lo + Math.floor(r() * (hi - lo + 1));
const bytes = (r, n) => Buffer.from(Array.from({ length: n }, () => int(r, 0, 255)));
const rngFor = (seed, id) => rng(seed ^ fnv1a(id));
function fnv1a(text) {
  let h = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) h = Math.imul(h ^ text.charCodeAt(i), 0x01000193) >>> 0;
  return h;
}

// ---------------------------------------------------------------- Opus-shaped packets

// RFC 6716 TOC config numbers that carry a given frame duration (SILK 0-11, hybrid 12-15, CELT 16-31).
const CONFIGS = {
  2.5: [16, 20, 24, 28], 5: [17, 21, 25, 29], 10: [0, 4, 8, 12, 14, 18, 22, 26, 30],
  20: [1, 5, 9, 13, 15, 19, 23, 27, 31], 40: [2, 6, 10], 60: [3, 7, 11],
};
export const DURATIONS_MS = [2.5, 5, 10, 20, 40, 60, 120];

/** One well-formed packet of `ms` (120 ms = two 60 ms frames, code 3) with `payloadLen` random payload bytes. */
export function opusPacket(r, ms, payloadLen) {
  const config = CONFIGS[ms === 120 ? 60 : ms];
  const cfg = config[int(r, 0, config.length - 1)];
  const stereo = r() < 0.1 ? 1 : 0;
  if (ms === 120) {
    const per = Math.max(1, Math.ceil(payloadLen / 2));
    return Buffer.concat([Buffer.from([(cfg << 3) | (stereo << 2) | 3, 2]), bytes(r, per * 2)]);
  }
  return Buffer.concat([Buffer.from([(cfg << 3) | (stereo << 2)]), bytes(r, payloadLen)]);
}

/** Structurally invalid or empty packets: libopus (or the relay) must reject each one and the relay must carry on. */
function corruptPacket(r, i) {
  const toc20 = 19 << 3;
  switch (i % 6) {
    case 0: return Buffer.from([toc20 | 3, 0]); // code 3, zero frames
    case 1: return Buffer.from([(3 << 3) | 3, 3, ...bytes(r, 6)]); // code 3, 3 x 60 ms = 180 ms > 120 ms
    case 2: return Buffer.from([toc20 | 2, 0xff]); // code 2, truncated two-byte length
    case 3: return Buffer.alloc(0); // empty payload
    case 4: return Buffer.from([toc20 | 1, ...bytes(r, 3)]); // code 1 needs an even payload
    default: return bytes(r, int(r, 2, 40)); // garbage
  }
}

// ---------------------------------------------------------------- PCM for the parec shim (integer math only)

const tri = (n, period, amp) => {
  const half = period >> 1;
  const phase = n % period;
  return Math.floor(((phase < half ? phase : period - phase) * 2 * amp) / half) - amp;
};

/** `frames` whole 20 ms chunks plus `tail` extra bytes (the partial final read the relay zero-pads). */
export function synthPcm({ seed, frames, tail = 0, shape = 'speech' }) {
  const out = Buffer.alloc(frames * FRAME_BYTES + tail);
  let x = (seed >>> 0) || 1;
  for (let i = 0; i < Math.floor(out.length / 2); i++) {
    x = (Math.imul(x, 1103515245) + 12345) & 0x7fffffff;
    let s = 0;
    if (shape === 'square') s = (i >> 5) & 1 ? 32767 : -32768;
    else if (shape !== 'silence') s = tri(i, 96, 6000) + tri(i, 37, 2500) + ((x >>> 16) % 1001) - 500;
    out.writeInt16LE(s, i * 2);
  }
  return out;
}

// ---------------------------------------------------------------- relay protocol v2 wire helpers

export function frame(type, payload) {
  const body = Buffer.from(payload);
  const head = Buffer.alloc(5);
  head.writeUInt32LE(body.length + 1, 0);
  head[4] = type;
  return Buffer.concat([head, body]);
}

export function relayToken(secret, sessionId, exp, nodeId, generation) {
  let msg = `voice-relay|${sessionId}|${exp}`;
  if (nodeId !== undefined) msg += `|${nodeId}|${generation}`; // bound hello signs node and lease generation
  return createHmac('sha256', Buffer.from(secret, 'utf8')).update(msg, 'utf8').digest('hex');
}

/** Wire bytes of a fixture `framing` case (length, then type, then payload; length 0 has no body). */
export function framingWire(c) {
  const payload = c.payload_hex !== undefined ? Buffer.from(c.payload_hex, 'hex') : Buffer.alloc(c.payload_len ?? 0);
  const head = Buffer.alloc(4);
  head.writeUInt32LE(c.length, 0);
  return c.length > 0 ? Buffer.concat([head, Buffer.from([c.type]), payload]) : head;
}

/** The first frame for a symbolic hello: a fixture `hello_validation` case, or {session_id, node_id?, generation?, offset_s?}. */
export function buildHello(spec, secret, nowSec) {
  if (spec.case) return helloFromCase(spec.case, secret, nowSec);
  const h = { op: 'hello', session_id: spec.session_id, exp: nowSec + (spec.offset_s ?? 60) };
  const bound = spec.node_id !== undefined;
  h.token = bound ? relayToken(secret, h.session_id, h.exp, spec.node_id, spec.generation) : relayToken(secret, h.session_id, h.exp);
  if (bound) Object.assign(h, { node_id: spec.node_id, generation: spec.generation });
  return frame(2, JSON.stringify(h));
}

function helloFromCase(c, secret, nowSec) {
  if (c.frame_type === 'audio') return frame(1, Buffer.from([0x00, 0x01]));
  if (c.raw_payload !== undefined) return frame(2, c.raw_payload);
  const h = { ...c.hello };
  if (c.session_id_len !== undefined) h.session_id = 'a'.repeat(c.session_id_len);
  if (c.exp_offset_s !== undefined) h.exp = nowSec + c.exp_offset_s;
  const bound = h.node_id !== undefined && h.generation !== undefined;
  if (c.token_mode === 'valid') h.token = bound ? relayToken(secret, h.session_id, h.exp, h.node_id, h.generation) : relayToken(secret, h.session_id, h.exp);
  else if (c.token_mode === 'unbound') h.token = relayToken(secret, h.session_id, h.exp);
  else if (c.token_mode === 'other_node') h.token = relayToken(secret, h.session_id, h.exp, 'other-node', h.generation);
  else if (c.token_mode === 'wrong') h.token = '0'.repeat(64);
  return frame(2, JSON.stringify(h));
}

/** The shared token vectors reproduce with this harness's own HMAC (the "golden RelayToken vectors" check). */
export function verifyTokenVectors(fx = loadFixture()) {
  const bad = [];
  for (const v of fx.token_vectors) if (relayToken(v.secret, v.session_id, v.exp) !== v.token) bad.push({ session_id: v.session_id, bound: false });
  for (const v of fx.bound_token_vectors) if (relayToken(v.secret, v.session_id, v.exp, v.node_id, v.generation) !== v.token) bad.push({ session_id: v.session_id, bound: true });
  return { checked: fx.token_vectors.length + fx.bound_token_vectors.length, passed: bad.length === 0, mismatches: bad };
}

// ---------------------------------------------------------------- scenarios

const STD_HELLO = { session_id: 'bench-1', offset_s: 60 };
const hex = (buf) => Buffer.from(buf).toString('hex');
const audio = (buf) => ({ t: 'audio', b: hex(buf) });
const control = (obj) => ({ t: 'control', b: hex(typeof obj === 'string' ? obj : JSON.stringify(obj)) });
const NO_SPEAKER_STREAM = ['control', 'fifo', 'closed']; // sessions the relay ends itself: how many speaker frames got out first is timing

/**
 * Build the scenario list. kind:
 *   session    hello, wait for ready (or the refusal), send `client` steps paced, end through the parec gate
 *   raw_first  raw bytes instead of a hello (framing violations before the handshake)
 *   busy       a second connection while a first one owns the environment
 *   slow       timing scenarios (hello timeout), only with includeSlow
 * `only` (ids) narrows the list (negative controls, debugging). `compare` lists the transcript streams that must match between A and B (default: all).
 */
export function buildCorpus({ seed, libopusVersion = null, profile = 'full', includeSlow = false, only = null } = {}) {
  if (!Number.isInteger(seed)) throw new Error('buildCorpus: an integer seed is required');
  const fx = loadFixture();
  const scenarios = [];
  const add = (id, rest) => scenarios.push({ id, ...rest });
  const rf = (id) => rngFor(seed, id);
  const pcmSpec = (id, frames, tail = 0, shape = 'speech') => ({ seed: (seed ^ fnv1a(`pcm.${id}`)) >>> 0, frames, tail, shape });

  for (const c of fx.hello_validation) {
    if (c.secret_configured === false) continue; // needs a relay started without a secret
    const ok = c.outcome === 'ok';
    add(`hello.${c.id}`, { kind: 'session', hello: { case: c }, expect: ok ? 'ready' : c.outcome, client: [], pcm: ok ? pcmSpec(c.id, 3) : null });
  }
  for (const c of fx.framing) {
    add(`rawfirst.${c.id}`, { kind: 'raw_first', raw: hex(framingWire(c)), expect: 'bad_hello' });
    const violation = c.outcome === 'violation';
    add(`post.${c.id}`, {
      kind: 'session', hello: STD_HELLO, expect: 'ready', client: [{ t: 'raw', b: hex(framingWire(c)) }],
      pcm: violation ? null : pcmSpec(c.id, 3),
    });
  }

  let r = rf('mic.durations');
  add('mic.durations', { kind: 'session', hello: STD_HELLO, expect: 'ready', pcm: null,
    client: DURATIONS_MS.flatMap((ms) => [0, 1, 2].map(() => audio(opusPacket(r, ms, int(r, 1, 160))))) });
  r = rf('mic.voice20');
  add('mic.voice20', { kind: 'session', hello: STD_HELLO, expect: 'ready', pcm: null,
    client: Array.from({ length: 60 }, () => audio(opusPacket(r, 20, int(r, 20, 90)))) });
  r = rf('mic.dtx');
  add('mic.dtx', { kind: 'session', hello: STD_HELLO, expect: 'ready', pcm: null,
    client: Array.from({ length: 30 }, (_, i) => audio(i % 3 === 0 ? opusPacket(r, 20, 40) : opusPacket(r, 20, i % 3 === 1 ? 0 : 1))) });
  r = rf('mic.corrupt');
  add('mic.corrupt', { kind: 'session', hello: STD_HELLO, expect: 'ready', pcm: null,
    client: Array.from({ length: 36 }, (_, i) => audio(i % 2 ? corruptPacket(r, i >> 1) : opusPacket(r, 20, int(r, 20, 80)))) });
  r = rf('mic.maxsize');
  add('mic.maxsize', { kind: 'session', hello: STD_HELLO, expect: 'ready', pcm: null,
    client: [1275, 1276, 5000, 61200, 61201, 65535].map((n) => audio(Buffer.concat([Buffer.from([19 << 3]), bytes(r, n - 1)]))) });
  r = rf('mic.mute');
  const muteSteps = [];
  for (let i = 0; i < 40; i++) {
    if (i % 7 === 3) muteSteps.push(control({ op: 'mute', muted: i % 14 === 3 }));
    muteSteps.push(audio(opusPacket(r, 20, int(r, 20, 80))));
  }
  add('mic.mute', { kind: 'session', hello: STD_HELLO, expect: 'ready', pcm: null, client: muteSteps });
  r = rf('mic.control_noise');
  add('mic.control_noise', { kind: 'session', hello: STD_HELLO, expect: 'ready', pcm: null, client: [
    control({ op: 'mute', muted: 'yes' }), control({ op: 'bogus' }), control('not json'), control('[]'), control(''), control({ op: 'mute' }),
    audio(opusPacket(r, 20, 50)), control({ op: 'mute', muted: true }), audio(opusPacket(r, 20, 50)), control({ op: 'mute', muted: false }), audio(opusPacket(r, 20, 50)),
  ] });

  add('spk.exact25', { kind: 'session', hello: STD_HELLO, expect: 'ready', client: [], pcm: pcmSpec('spk.exact25', 25) });
  add('spk.partial_tail', { kind: 'session', hello: STD_HELLO, expect: 'ready', client: [], pcm: pcmSpec('spk.partial_tail', 10, 700) });
  add('spk.silence', { kind: 'session', hello: STD_HELLO, expect: 'ready', client: [], pcm: pcmSpec('spk.silence', 20, 0, 'silence') });
  add('spk.square', { kind: 'session', hello: STD_HELLO, expect: 'ready', client: [], pcm: pcmSpec('spk.square', 20, 0, 'square') });
  r = rf('both.mixed');
  add('both.mixed', { kind: 'session', hello: STD_HELLO, expect: 'ready', pcm: pcmSpec('both.mixed', 40),
    client: Array.from({ length: 40 }, () => audio(opusPacket(r, 20, int(r, 20, 90)))) });

  add('busy.second', { kind: 'busy', hello: STD_HELLO, expect: 'busy', client: [], pcm: pcmSpec('busy.second', 2) });
  add('bound.gen5', { kind: 'session', hello: { session_id: 'stale-1', node_id: 'node-s', generation: 5 }, expect: 'ready', client: [], pcm: pcmSpec('bound.gen5', 2) });
  add('bound.gen4_stale', { kind: 'session', hello: { session_id: 'stale-1', node_id: 'node-s', generation: 4 }, expect: 'unauthorized', client: [], pcm: null });
  if (includeSlow) add('slow.hello_timeout', { kind: 'slow', expect: 'hello_timeout' });

  for (const sc of scenarios) if (sc.kind === 'session' && sc.client.some((s) => s.t === 'raw') && sc.pcm === null) sc.compare = NO_SPEAKER_STREAM;
  const known = new Set(scenarios.map((s) => s.id));
  for (const id of only ?? []) if (!known.has(id)) throw new Error(`corpus: unknown scenario id ${id}`);
  const chosen = scenarios.filter((s) => (only ? only.includes(s.id) : profile !== 'smoke' || SMOKE.has(s.id)));
  if (new Set(chosen.map((s) => s.id)).size !== chosen.length) throw new Error('corpus: duplicate scenario id');

  const manifest = {
    schema: 'ubag.voice_relay_corpus/1', corpus_version: CORPUS_VERSION, seed, profile: only ? 'custom' : profile, libopus_version: libopusVersion,
    fixture_sha256: fx.sha256,
    scenarios: chosen.map((s) => ({ id: s.id, kind: s.kind, sha256: sha256Hex(JSON.stringify(s)) })),
  };
  manifest.sha256 = sha256Hex(JSON.stringify(manifest.scenarios) + fx.sha256 + seed);
  return { manifest, scenarios: chosen, secret: fx.secret, fixture: fx };
}

// The smoke profile: one scenario per code path, fast enough for the offline self-test.
const SMOKE = new Set([
  'hello.valid', 'hello.token_wrong', 'hello.exp_in_past', 'hello.valid_bound', 'rawfirst.length_zero', 'post.unknown_type_3', 'post.audio_empty_payload',
  'mic.durations', 'mic.corrupt', 'mic.mute', 'spk.partial_tail', 'both.mixed', 'busy.second', 'bound.gen5', 'bound.gen4_stale',
]);

/** 100 well-formed 20 ms packets for the steady-state measurement (looped by the driver) and the matching monitor PCM. */
export function measureInputs(seed) {
  const r = rngFor(seed, 'measure');
  return {
    packets: Array.from({ length: 100 }, () => opusPacket(r, 20, int(r, 40, 80))),
    pcm: synthPcm({ seed: (seed ^ fnv1a('pcm.measure')) >>> 0, frames: 100 }),
  };
}
