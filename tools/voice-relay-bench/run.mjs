#!/usr/bin/env node
/**
 * Voice-relay A/B harness (perf-fleet P7.7). Zero dependencies. Drives ANY relay implementation as a black box over the framed TCP
 * protocol v2 and compares it with a baseline, on identical seeded input:
 *
 *   byte compare  every corpus scenario (handshake refusals, framing violations, all Opus durations, corrupt and empty packets,
 *                 mute interleavings, partial final PCM chunk, busy, stale lease generation) runs against A and against B; the
 *                 relay's control replies, speaker packets, bytes written to the mic FIFO and the way it ends the connection
 *                 must be identical.
 *   paired runs   interleaved A/B steady-state runs (one call, 20 ms frames in both directions): CPU per call-minute, peak RSS,
 *                 mic and speaker frame transit p50/p95/p99, drops, optionally a mixed-workload probe (--mixed-probe).
 *   leaks         N reconnects: fd, thread, child-process and RSS growth.
 *
 * Implementation contract: a relay under test is a command line (`{addr}` is replaced by 127.0.0.1:<port>). It must honour the
 * relay's env seams: UBAG_VOICE_RELAY_SECRET, UBAG_VOICE_RELAY_ADDR, UBAG_VOICE_MIC_PIPE (the mic FIFO it writes), UBAG_VOICE_PAREC
 * and UBAG_VOICE_PACTL (executables it spawns; the harness provides shims), UBAG_VOICE_RELAY_IDLE_S. Mock input only: no browser,
 * no PulseAudio, no provider, no login. It listens on 127.0.0.1 and never touches a remote host.
 *
 *   node tools/voice-relay-bench/run.mjs --self-test                      # Python vs Python, fake codec: proves the harness (offline)
 *   node tools/voice-relay-bench/run.mjs --a "python3 deploy/vps/browser/audio-relay.py --addr {addr}" \
 *        --b "<candidate> --addr {addr}" --lab-host --host-class "<where>" --out ab.json   # real run, dedicated lab helper only
 *
 * Numbers are NON-AUTHORITATIVE unless the run is on Linux, with --lab-host, a real libopus and a CPU meter (/proc).
 */
import { execFileSync, spawn } from 'node:child_process';
import { EventEmitter } from 'node:events';
import { chmodSync, closeSync, existsSync, mkdtempSync, openSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import os, { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { performance } from 'node:perf_hooks';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createHash } from 'node:crypto';
import { FRAME_BYTES, buildCorpus, buildHello, frame, measureInputs, synthPcm, verifyTokenVectors } from './corpus.mjs';
import { buildReport, compareRuns, leakVerdict, renderMarkdown } from './report.mjs';
import { Sink } from './sink.mjs';

export const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '..', '..');
const DEFAULT_GATE = join(REPO, 'tests', 'load', 'voice-relay-gate.json');
const READY = '{"op":"ready"}';
const STD_HELLO = { session_id: 'bench-measure', offset_s: 60 };

const hr = () => process.hrtime.bigint();
const msBetween = (a, b) => Number(b - a) / 1e6;
const sleep = (ms) => new Promise((resolveSleep) => setTimeout(resolveSleep, Math.max(0, ms)));
const sleepUntil = (t) => sleep(t - performance.now());
const r3 = (x) => (x == null || !Number.isFinite(x) ? null : Math.round(x * 1000) / 1000);
const sha = (data) => createHash('sha256').update(data).digest('hex');

export function pctl(sorted, q) {
  return sorted[Math.min(sorted.length - 1, Math.floor(q * sorted.length))];
}
export function latencyStats(values) {
  if (!values.length) return null;
  const s = [...values].sort((a, b) => a - b);
  return { n: s.length, p50_ms: r3(pctl(s, 0.5)), p95_ms: r3(pctl(s, 0.95)), p99_ms: r3(pctl(s, 0.99)), max_ms: r3(s[s.length - 1]) };
}

/** Split a command line on whitespace, honouring single and double quotes (no escapes: Windows paths stay intact). */
export function parseCommand(text) {
  const out = [];
  let cur = '';
  let quote = null;
  let has = false;
  for (const ch of text) {
    if (quote) { if (ch === quote) quote = null; else cur += ch; }
    else if (ch === '"' || ch === "'") { quote = ch; has = true; }
    else if (/\s/.test(ch)) { if (cur || has) out.push(cur); cur = ''; has = false; }
    else cur += ch;
  }
  if (quote) throw new Error(`unterminated quote in: ${text}`);
  if (cur || has) out.push(cur);
  return out;
}

export function findPython() {
  for (const cand of [process.env.UBAG_BENCH_PYTHON, 'python3', 'python'].filter(Boolean)) {
    try {
      const out = execFileSync(cand, ['--version'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 10000 });
      if (/Python 3\.(9|[1-9]\d)/.test(out)) return cand;
    } catch { /* not this one */ }
  }
  return null;
}

/** Per-platform executable that runs a Node script (the relay spawns parec and pactl by path, with its own arguments). */
export function makeWrapper(dir, name, script) {
  if (process.platform === 'win32') {
    const file = join(dir, `${name}.cmd`);
    writeFileSync(file, `@echo off\r\n"${process.execPath}" "${script}" %*\r\n`);
    return file;
  }
  const file = join(dir, name);
  writeFileSync(file, `#!/bin/sh\nexec "${process.execPath}" "${script}" "$@"\n`, { mode: 0o755 });
  chmodSync(file, 0o755);
  return file;
}

const freePort = () => new Promise((res, rej) => {
  const server = net.createServer();
  server.once('error', rej);
  server.listen(0, '127.0.0.1', () => { const { port } = server.address(); server.close(() => res(port)); });
});

// ---------------------------------------------------------------- framed client

export class Client extends EventEmitter {
  static async connect(port) {
    const sock = net.connect({ host: '127.0.0.1', port, noDelay: true });
    const client = new Client(sock);
    await new Promise((res, rej) => { sock.once('connect', res); sock.once('error', rej); });
    return client;
  }

  constructor(sock) {
    super();
    this.sock = sock;
    this.frames = [];
    this.closed = false;
    this.protocolError = false;
    this.buf = Buffer.alloc(0);
    sock.on('data', (d) => this.#data(d));
    sock.on('close', () => { this.closed = true; this.emit('change'); });
    sock.on('error', () => { this.closed = true; this.emit('change'); }); // a reset ends the session like a close
  }

  #data(chunk) {
    this.buf = this.buf.length ? Buffer.concat([this.buf, chunk]) : chunk;
    const t = hr();
    while (this.buf.length >= 4) {
      const len = this.buf.readUInt32LE(0);
      if (len === 0 || len > 65536) { this.protocolError = true; this.buf = Buffer.alloc(0); break; }
      if (this.buf.length < 4 + len) break;
      this.frames.push({ type: this.buf[4], payload: Buffer.from(this.buf.subarray(5, 4 + len)), t });
      this.buf = this.buf.subarray(4 + len);
    }
    this.emit('change');
  }

  audio() { return this.frames.filter((f) => f.type === 1); }
  control() { return this.frames.filter((f) => f.type === 2).map((f) => f.payload.toString('utf8')); }
  write(buf) { if (!this.closed) this.sock.write(buf); }
  destroy() { this.sock.destroy(); }

  waitFor(pred, timeoutMs) {
    return new Promise((res) => {
      if (pred()) { res(true); return; }
      const finish = (v) => { clearTimeout(timer); this.off('change', check); res(v); };
      const check = () => { if (pred()) finish(true); };
      const timer = setTimeout(() => finish(pred()), timeoutMs);
      this.on('change', check);
    });
  }
}

// ---------------------------------------------------------------- relay process under test

export async function startRelay(impl, o) {
  const dir = mkdtempSync(join(tmpdir(), 'ubag-relay-ab-'));
  const fifoPath = join(dir, 'mic.pcm');
  const useFifo = process.platform !== 'win32' && !o.fileSink;
  if (useFifo) execFileSync('mkfifo', ['-m', '600', fifoPath]);
  else writeFileSync(fifoPath, '');
  const sink = new Sink(fifoPath, { fifo: useFifo, keep: o.keepSink !== false }).start(); // reader first: the relay's open is non-blocking
  const files = { pcm: join(dir, 'monitor.pcm'), cfg: join(dir, 'shim.json'), gate: join(dir, 'gate'), log: join(dir, 'parec.log') };
  const port = await freePort();
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('UBAG_VOICE_') && !k.startsWith('UBAG_SELFTEST_')));
  Object.assign(env, {
    UBAG_VOICE_RELAY_SECRET: o.secret, UBAG_VOICE_RELAY_ADDR: `127.0.0.1:${port}`, UBAG_VOICE_MIC_PIPE: fifoPath,
    UBAG_VOICE_PAREC: makeWrapper(dir, 'parec', join(HERE, 'parec-shim.mjs')), UBAG_VOICE_PACTL: makeWrapper(dir, 'pactl', join(HERE, 'pactl-shim.mjs')),
    UBAG_VOICE_RELAY_IDLE_S: String(o.idleS ?? 10), UBAG_BENCH_SHIM_CONFIG: files.cfg,
  }, impl.env ?? {});
  const [cmd, ...args] = impl.argv.map((a) => a.replaceAll('{addr}', `127.0.0.1:${port}`).replaceAll('{port}', String(port)));
  const out = openSync(join(dir, 'relay.out'), 'w');
  const err = openSync(join(dir, 'relay.err'), 'w');
  const child = spawn(cmd, args, { env, stdio: ['ignore', out, err], windowsHide: true });
  closeSync(out);
  closeSync(err);
  let exited = null;
  child.once('exit', (code, signal) => { exited = { code, signal }; });
  child.once('error', (e) => { exited = { error: e.message }; });
  const tail = () => { try { return readFileSync(join(dir, 'relay.err'), 'utf8').slice(-1500); } catch { return ''; } };

  const relay = {
    dir, port, pid: child.pid, sink, files, secret: o.secret, tail,
    configure({ pcm, paceMs, loop = false, maxChunks = 0 }) {
      writeFileSync(files.pcm, pcm);
      writeFileSync(files.cfg, JSON.stringify({ pcm: files.pcm, pace_ms: paceMs, loop, max_chunks: maxChunks, gate: files.gate, log: files.log }));
      rmSync(files.gate, { force: true });
      rmSync(files.log, { force: true });
    },
    openGate() { writeFileSync(files.gate, '1'); },
    /** True once the relay accepts a new session. A busy relay answers a bare connection with {"op":"error","reason":"busy"} at once. */
    async awaitFree(maxMs = 12000) {
      const end = Date.now() + maxMs;
      for (;;) {
        const probe = await Client.connect(port);
        await probe.waitFor(() => probe.frames.length > 0 || probe.closed, 150);
        const busy = probe.control().some((m) => m.includes('"busy"'));
        probe.destroy();
        if (!busy) return true;
        if (Date.now() > end) throw new Error(`relay never freed its session slot\n${tail()}`);
        await sleep(25);
      }
    },
    async stop() {
      try { writeFileSync(files.gate, '1'); } catch { /* dir already gone */ }
      if (!exited) {
        child.kill('SIGTERM');
        await Promise.race([new Promise((res) => child.once('exit', res)), sleep(3000)]);
        if (!exited) child.kill('SIGKILL');
      }
      await sink.stop();
      await sleep(100);
      try { rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }); } catch { /* a shim may still hold the dir on Windows */ }
    },
  };
  relay.configure({ pcm: Buffer.alloc(0), paceMs: 20 });
  const deadline = Date.now() + (o.startMs ?? 20000);
  for (;;) {
    if (exited) { await sink.stop(); throw new Error(`relay exited before listening: ${JSON.stringify(exited)}\n${tail()}`); }
    try { (await Client.connect(port)).destroy(); break; } catch { /* not listening yet */ }
    if (Date.now() > deadline) { await relay.stop(); throw new Error(`relay did not listen within ${o.startMs ?? 20000} ms\n${tail()}`); }
    await sleep(50);
  }
  return relay;
}

// ---------------------------------------------------------------- byte-compare scenarios

const stepBytes = (s) => (s.t === 'raw' ? Buffer.from(s.b, 'hex') : frame(s.t === 'audio' ? 1 : 2, Buffer.from(s.b, 'hex')));
const u32 = (n) => { const b = Buffer.alloc(4); b.writeUInt32LE(n); return b; };

function transcript(c, fifo, c2) {
  const speaker = c.audio().map((f) => f.payload);
  const blocks = [];
  for (let i = 0; i < fifo.length; i += FRAME_BYTES) blocks.push(sha(fifo.subarray(i, i + FRAME_BYTES)).slice(0, 12));
  const t = {
    control: c.control(),
    speaker: { frames: speaker.length, bytes: speaker.reduce((s, p) => s + p.length, 0), sha256: sha(Buffer.concat(speaker.flatMap((p) => [u32(p.length), p]))), per: speaker.map((p) => sha(p).slice(0, 12)) },
    fifo: { bytes: fifo.length, sha256: sha(fifo), per: blocks },
    closed: c.closed,
  };
  if (c2) t.second = { control: c2.control(), closed: c2.closed };
  return t;
}

/** What the fixture says the first reply must be; a mismatch means the harness or the relay broke the documented protocol. */
function expectationFailure(sc, t) {
  if (!sc.expect) return null;
  const got = sc.kind === 'busy' ? t.second?.control[0] : t.control[0];
  const want = sc.expect === 'ready' ? READY : `{"op":"error","reason":"${sc.expect}"}`;
  return got === want ? null : { expected: want, got: got ?? null };
}

export async function runScenario(relay, corpus, sc, o) {
  await relay.awaitFree();
  relay.configure({ pcm: sc.pcm ? synthPcm(sc.pcm) : Buffer.alloc(0), paceMs: o.paceMs });
  relay.sink.take();
  const c = await Client.connect(relay.port);
  let c2 = null;
  try {
    const now = Math.floor(Date.now() / 1000);
    const replied = () => c.frames.length > 0 || c.closed;
    if (sc.kind === 'slow') { // half a hello, then silence: the relay must time the handshake out
      c.write(buildHello({ session_id: 'bench-slow', offset_s: 60 }, corpus.secret, now).subarray(0, 6));
      await c.waitFor(replied, 9000);
    } else if (sc.kind === 'raw_first') {
      c.write(Buffer.from(sc.raw, 'hex'));
      await c.waitFor(replied, 9000);
    } else {
      c.write(buildHello(sc.hello, corpus.secret, now));
      await c.waitFor(replied, 15000);
      if (c.control()[0] === READY) {
        if (sc.kind === 'busy') {
          c2 = await Client.connect(relay.port);
          c2.write(buildHello({ ...sc.hello, session_id: 'bench-2' }, corpus.secret, now));
          await c2.waitFor(() => c2.frames.length > 0 || c2.closed, 9000);
        }
        const start = performance.now();
        let k = 0;
        for (const step of sc.client) {
          if (c.closed) break;
          await sleepUntil(start + k++ * o.paceMs);
          c.write(stepBytes(step));
        }
        await c.waitFor(() => c.audio().length >= (sc.pcm?.frames ?? 0) || c.closed, 20000);
        await relay.sink.quiet(o.quietMs);
        relay.openGate(); // parec EOF: the relay ends the session with monitor_exited at a point both sides agree on
      }
    }
    await c.waitFor(() => c.closed, 6000);
    await relay.sink.quiet(Math.min(o.quietMs, 100));
    return transcript(c, relay.sink.take(), c2);
  } finally {
    c.destroy();
    c2?.destroy();
  }
}

export async function runCorpus(impl, corpus, o) {
  const relay = await startRelay(impl, { secret: corpus.secret, idleS: o.idleS, fileSink: o.fileSink });
  const transcripts = {};
  const expectationFailures = [];
  try {
    for (const sc of corpus.scenarios) {
      transcripts[sc.id] = await runScenario(relay, corpus, sc, o);
      const bad = expectationFailure(sc, transcripts[sc.id]);
      if (bad) expectationFailures.push({ id: sc.id, ...bad });
      o.onProgress?.(`${impl.label} ${sc.id}`);
    }
  } catch (e) {
    throw new Error(`${impl.label}: ${e.message}\n--- relay stderr tail ---\n${relay.tail()}`);
  } finally {
    await relay.stop();
  }
  return { transcripts, expectation_failures: expectationFailures };
}

// ---------------------------------------------------------------- /proc meter (Linux)

let clkTck = null;
function clk() {
  if (clkTck == null) {
    try { clkTck = Number(execFileSync('getconf', ['CLK_TCK'], { encoding: 'utf8' }).trim()) || 100; } catch { clkTck = 100; }
  }
  return clkTck;
}
/** Fields of /proc/<pid>/stat after the parenthesised command name (which may itself contain spaces and parentheses). */
export const parseProcStat = (text) => text.slice(text.lastIndexOf(')') + 2).split(' ');
/** CPU clock ticks (utime + stime) and parent pid from /proc/<pid>/stat text. */
export function procStatTicks(text) { const f = parseProcStat(text); return { ticks: Number(f[11]) + Number(f[12]), ppid: Number(f[1]) }; }
export function parseProcStatus(text) {
  const grab = (key) => { const m = new RegExp(`^${key}:[ \\t]+(\\d+)`, 'm').exec(text); return m ? Number(m[1]) : null; };
  return { rss_peak_kib: grab('VmHWM'), rss_kib: grab('VmRSS'), threads: grab('Threads') };
}

export function procMeter(pid) {
  if (process.platform !== 'linux' || !existsSync(`/proc/${pid}/stat`)) {
    return { supported: false, cpuTicks: () => null, status: () => ({}), counts: () => ({}) };
  }
  return {
    supported: true,
    cpuTicks: () => procStatTicks(readFileSync(`/proc/${pid}/stat`, 'utf8')).ticks,
    status: () => parseProcStatus(readFileSync(`/proc/${pid}/status`, 'utf8')),
    counts() {
      let children = 0;
      for (const d of readdirSync('/proc')) {
        if (!/^\d+$/.test(d)) continue;
        try { if (procStatTicks(readFileSync(`/proc/${d}/stat`, 'utf8')).ppid === pid) children++; } catch { /* process gone */ }
      }
      return { fds: readdirSync(`/proc/${pid}/fd`).length, children };
    },
  };
}

// ---------------------------------------------------------------- steady-state measurement

function startProbe(commandText, label, seconds) {
  const [cmd, ...args] = parseCommand(commandText);
  const child = spawn(cmd, args, { env: { ...process.env, UBAG_BENCH_IMPL: label, UBAG_BENCH_WINDOW_S: String(seconds) }, stdio: ['ignore', 'pipe', 'inherit'] });
  let out = '';
  child.stdout.on('data', (d) => { out += d; });
  return new Promise((res) => {
    child.once('error', (e) => res({ error: e.message }));
    child.once('exit', () => { try { res(JSON.parse(out)); } catch { res({ error: 'probe printed no JSON' }); } });
  });
}

/** One paced call: client Opus at 20 ms into the relay, monitor PCM at 20 ms out of the parec shim, window after warmup. */
export async function measureRun(impl, corpus, o) {
  const { packets, pcm } = measureInputs(corpus.manifest.seed);
  const total = Math.round(((o.warmupS + o.durationS) * 1000) / o.paceMs);
  const warm = Math.round((o.warmupS * 1000) / o.paceMs);
  const relay = await startRelay(impl, { secret: corpus.secret, idleS: o.idleS, fileSink: o.fileSink, keepSink: false });
  try {
    relay.configure({ pcm, paceMs: o.paceMs, loop: true, maxChunks: total });
    const c = await Client.connect(relay.port);
    c.write(buildHello(STD_HELLO, corpus.secret, Math.floor(Date.now() / 1000)));
    await c.waitFor(() => c.frames.length > 0 || c.closed, 20000);
    if (c.control()[0] !== READY) throw new Error(`session refused: ${c.control()[0] ?? 'no reply'}\n${relay.tail()}`);
    const proc = procMeter(relay.pid);
    const sent = [];
    let w0 = null;
    let cpu0 = null;
    let probe = null;
    const start = performance.now();
    for (let k = 0; k < total; k++) {
      await sleepUntil(start + k * o.paceMs);
      if (c.closed) break;
      if (k === warm) { cpu0 = proc.cpuTicks(); w0 = hr(); probe = o.mixedProbe ? startProbe(o.mixedProbe, impl.label, o.durationS) : null; }
      sent.push(hr());
      c.write(frame(1, packets[k % packets.length]));
    }
    const w1 = hr();
    const cpu1 = proc.cpuTicks();
    const status = proc.status();
    await c.waitFor(() => c.audio().length >= total || c.closed, 5000);
    await relay.sink.quiet(150);
    relay.openGate();
    await c.waitFor(() => c.closed, 6000);
    const mixed = probe ? await probe : null;

    const windowS = w0 == null ? null : msBetween(w0, w1) / 1000;
    const blocks = Math.floor(relay.sink.total / FRAME_BYTES);
    const emit = existsSync(relay.files.log) ? readFileSync(relay.files.log, 'utf8').split('\n').filter(Boolean).map(BigInt) : [];
    const recv = c.audio().map((f) => f.t);
    const micLat = [];
    if (blocks === sent.length) for (let k = warm; k < sent.length; k++) { const t = relay.sink.timeOfByte((k + 1) * FRAME_BYTES); if (t != null) micLat.push(msBetween(sent[k], t)); }
    const spkLat = [];
    if (emit.length === recv.length) for (let k = warm; k < recv.length; k++) spkLat.push(msBetween(emit[k], recv[k]));
    return {
      window_s: r3(windowS),
      cpu_ms_per_call_minute: cpu0 == null || cpu1 == null || !windowS ? null : r3(((cpu1 - cpu0) * 1000) / clk() / (windowS / 60)),
      rss_peak_kib: status.rss_peak_kib ?? null, rss_kib: status.rss_kib ?? null, threads: status.threads ?? null,
      mic: latencyStats(micLat), speaker: latencyStats(spkLat),
      mic_sent: sent.length, mic_blocks: blocks, mic_drops: Math.max(0, sent.length - blocks),
      speaker_emitted: emit.length, speaker_frames: recv.length, speaker_drops: Math.max(0, emit.length - recv.length),
      errors: [...(c.protocolError ? ['protocol error from relay'] : []), ...c.control().slice(1).filter((m) => !m.includes('monitor_exited'))],
      ...(mixed ? { mixed } : {}),
    };
  } finally {
    await relay.stop();
  }
}

// ---------------------------------------------------------------- reconnect leak check

export async function leakRun(impl, corpus, o) {
  const relay = await startRelay(impl, { secret: corpus.secret, idleS: o.idleS, fileSink: o.fileSink, keepSink: false });
  try {
    const proc = procMeter(relay.pid);
    if (!proc.supported) return { supported: false, reason: 'needs Linux /proc' };
    const { packets } = measureInputs(corpus.manifest.seed);
    const oneSession = async () => {
      await relay.awaitFree();
      relay.configure({ pcm: synthPcm({ seed: 7, frames: 3 }), paceMs: 5 });
      const c = await Client.connect(relay.port);
      c.write(buildHello({ session_id: 'bench-leak', offset_s: 60 }, corpus.secret, Math.floor(Date.now() / 1000)));
      await c.waitFor(() => c.frames.length > 0 || c.closed, 15000);
      for (let i = 0; i < 5; i++) c.write(frame(1, packets[i]));
      await c.waitFor(() => c.audio().length >= 2 || c.closed, 3000);
      c.destroy();
    };
    const sample = async () => { // minimum of several samples: a transient child (the health loop's pactl) is not a leak
      const best = { fds: Infinity, children: Infinity, threads: Infinity, rss_kib: Infinity };
      for (let i = 0; i < 6; i++) {
        const { fds, children } = proc.counts();
        const { threads, rss_kib: rss } = proc.status();
        Object.assign(best, { fds: Math.min(best.fds, fds), children: Math.min(best.children, children), threads: Math.min(best.threads, threads), rss_kib: Math.min(best.rss_kib, rss) });
        await sleep(120);
      }
      return best;
    };
    await oneSession();
    await relay.awaitFree();
    const before = await sample();
    for (let i = 0; i < o.leakReconnects; i++) { await oneSession(); o.onProgress?.(`${impl.label} reconnect ${i + 1}/${o.leakReconnects}`); }
    await relay.awaitFree();
    const after = await sample();
    return { ...leakVerdict(before, after, o.maxLeakRssGrowthKib), reconnects: o.leakReconnects };
  } finally {
    await relay.stop();
  }
}

// ---------------------------------------------------------------- orchestration

function opusGoldenState(libopusVersion) {
  try {
    const g = JSON.parse(readFileSync(join(REPO, 'deploy', 'vps', 'browser', 'tests', 'golden', 'opus_real_golden.json'), 'utf8'));
    const recorded = g.libopus_version != null && Array.isArray(g.packets_hex) && g.packets_hex.length > 0;
    return { recorded, libopus_version: g.libopus_version ?? null, matches_run_version: recorded && g.libopus_version === libopusVersion, note: 'the byte check itself is deploy/vps/browser/tests/test_opus_real.py' };
  } catch (e) {
    return { recorded: false, libopus_version: null, matches_run_version: false, note: `golden file unreadable: ${e.message}` };
  }
}

function detectLibopus(py) {
  try {
    return execFileSync(py, ['-c', 'import sys;sys.path.insert(0,"deploy/vps/browser");import opus_bridge;print(opus_bridge.opus_version())'], { cwd: REPO, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'], timeout: 15000 }).trim();
  } catch { return null; }
}

export function selfTestImpls(py, mutate) {
  const argv = [py, join(HERE, 'selftest-relay.py'), '--addr', '{addr}'];
  return {
    a: { label: 'python-selftest', argv, env: {} },
    b: { label: mutate ? `python-selftest-mutated-${mutate}` : 'python-selftest', argv, env: mutate ? { UBAG_SELFTEST_MUTATE: mutate } : {} },
  };
}

export async function runAb(o) {
  const gate = JSON.parse(readFileSync(o.gate ?? DEFAULT_GATE, 'utf8'));
  const seed = o.seed ?? gate.corpus.seed;
  const run = { pairs: o.pairs, duration_s: o.durationS, warmup_s: o.warmupS, pace_ms: o.paceMs, quiet_ms: o.quietMs, profile: o.profile, leak_reconnects: o.leakReconnects };
  const opts = { ...o, maxLeakRssGrowthKib: gate.max_leak_rss_growth_kib };
  const corpus = buildCorpus({ seed, libopusVersion: o.libopusVersion, profile: o.profile, includeSlow: o.includeSlow, only: o.only });
  const ra = await runCorpus(o.a, corpus, opts);
  const rb = await runCorpus(o.b, corpus, opts);
  const compare = compareRuns(corpus.scenarios, ra.transcripts, rb.transcripts);
  compare.expectation_failures = { a: ra.expectation_failures, b: rb.expectation_failures };

  const pairs = [];
  for (let i = 0; i < o.pairs; i++) { // interleaved, order alternates, so drift and noisy neighbours hit both sides alike
    const order = i % 2 === 0 ? ['a', 'b'] : ['b', 'a'];
    const result = {};
    for (const side of order) result[side] = await measureRun(o[side], corpus, opts);
    pairs.push({ pair: i + 1, order: order.join(''), a: result.a, b: result.b });
  }
  const leaks = o.leakReconnects > 0 ? { a: await leakRun(o.a, corpus, opts), b: await leakRun(o.b, corpus, opts) } : null;

  const cpuMeter = process.platform === 'linux';
  const reasons = [
    !o.labHost && 'no --lab-host attestation', process.platform !== 'linux' && 'not Linux (no /proc CPU meter, no real FIFO)',
    o.selfTest && 'self-test: fake codec, both sides are the same Python relay', o.codec !== 'libopus' && `codec is ${o.codec}, not libopus`,
    !o.libopusVersion && 'libopus version unknown', o.pairs < 1 && 'no measured pairs',
  ].filter(Boolean);
  return buildReport({
    selfTest: o.selfTest, authoritative: reasons.length === 0 && cpuMeter, reasons, hostClass: o.hostClass,
    host: { platform: process.platform, arch: process.arch, release: os.release(), cpus: os.cpus().length, node: process.version, clk_tck: cpuMeter ? clk() : null },
    libopusVersion: o.libopusVersion, codec: o.codec,
    corpus: { seed, sha256: corpus.manifest.sha256, scenarios: corpus.scenarios.length, profile: o.profile, fixture_sha256: corpus.manifest.fixture_sha256 },
    impls: { a: { label: o.a.label, argv: o.a.argv }, b: { label: o.b.label, argv: o.b.argv } },
    golden: { relay_token_vectors: verifyTokenVectors(corpus.fixture), opus_golden: opusGoldenState(o.libopusVersion) },
    compare, run, pairs, leaks, mixedProbe: !!o.mixedProbe,
  });
}

// ---------------------------------------------------------------- CLI

const VALUE = new Set(['a', 'b', 'label-a', 'label-b', 'selftest-mutate', 'profile', 'only', 'seed', 'gate', 'pace-ms', 'quiet-ms', 'pairs', 'duration', 'warmup', 'leak-reconnects',
  'libopus-version', 'host-class', 'mixed-probe', 'out', 'markdown']);
const FLAG = new Set(['self-test', 'include-slow', 'lab-host', 'file-sink', 'no-measure', 'help']);
const HELP = `Usage: node tools/voice-relay-bench/run.mjs (--self-test | --a "<relay cmd>" --b "<relay cmd>") [options]
  --self-test                 Python relay (fake codec) as A and B: proves the harness offline. --selftest-mutate speaker|mic|mute breaks B on purpose.
  --a/--b "<cmd {addr}>"      relay command lines ({addr} -> 127.0.0.1:<port>); A is the baseline, B the candidate
  --label-a/--label-b <name>  labels in the report (default a / b)
  --profile full|smoke        corpus size (default full; smoke is for self-tests); --only id,id narrows it; --include-slow adds the 5 s hello timeout; --seed <int> overrides the gate's seed
  --pace-ms <n> (20)          frame pace for scenarios and measurement; --quiet-ms <n> (300) FIFO quiescence before a scenario ends
  --pairs <n> (5) --duration <s> (30) --warmup <s> (3)   interleaved paired runs; --leak-reconnects <n> (gate value, 100; 0 skips)
  --no-measure                byte compare only (no pairs, no leak check)
  --libopus-version <text>    default: asked of the Python opus_bridge; the operator attests B links the same libopus .so
  --host-class <text> --lab-host   provenance; without --lab-host (and Linux, real libopus) the report is NON-AUTHORITATIVE
  --mixed-probe "<cmd>"       runs beside each measured window, prints JSON {container_cpu_pct, job_p95_ms, host_headroom_pct}; env UBAG_BENCH_IMPL, UBAG_BENCH_WINDOW_S
  --file-sink                 force the regular-file mic stand-in (Windows always uses it)
  --out <report.json> --markdown <report.md> --gate <gate.json>
Exit: 0 byte compare passed, 1 failed, 2 usage or harness error.
`;

export function parseArgs(argv) {
  const raw = {};
  for (let i = 0; i < argv.length; i++) {
    const tok = argv[i];
    if (!tok.startsWith('--')) throw new Error(`unexpected argument: ${tok}`);
    const [name, inline] = tok.slice(2).split(/=(.*)/s);
    if (FLAG.has(name)) raw[name] = true;
    else if (VALUE.has(name)) raw[name] = inline ?? argv[++i];
    else throw new Error(`unknown option --${name}`);
    if (raw[name] === undefined) throw new Error(`--${name} needs a value`);
  }
  const int = (k, d) => { const v = raw[k] === undefined ? d : Number(raw[k]); if (!Number.isFinite(v) || v < 0) throw new Error(`--${k} must be a non-negative number`); return v; };
  const o = {
    selfTest: !!raw['self-test'], includeSlow: !!raw['include-slow'], labHost: !!raw['lab-host'], fileSink: !!raw['file-sink'], profile: raw.profile ?? 'full',
    only: raw.only ? raw.only.split(',') : null, seed: raw.seed === undefined ? undefined : int('seed'), gate: raw.gate, hostClass: raw['host-class'], libopusVersion: raw['libopus-version'], mixedProbe: raw['mixed-probe'],
    paceMs: int('pace-ms', 20), quietMs: int('quiet-ms', 300), durationS: int('duration', raw['self-test'] ? 2 : 30), warmupS: int('warmup', raw['self-test'] ? 0.5 : 3),
    pairs: raw['no-measure'] ? 0 : int('pairs', raw['self-test'] ? 2 : 5), leakReconnects: raw['no-measure'] ? 0 : raw['leak-reconnects'] === undefined ? undefined : int('leak-reconnects'),
    out: raw.out, markdown: raw.markdown, mutate: raw['selftest-mutate'], codec: 'libopus', help: !!raw.help,
  };
  if (!['full', 'smoke'].includes(o.profile)) throw new Error('--profile must be full or smoke');
  if (o.mutate && !['speaker', 'mic', 'mute'].includes(o.mutate)) throw new Error('--selftest-mutate must be speaker, mic or mute');
  if (o.paceMs < 1) throw new Error('--pace-ms must be >= 1');
  if (!o.help && !o.selfTest && !(raw.a && raw.b)) throw new Error('give --self-test or both --a and --b');
  if (raw.a) o.a = { label: raw['label-a'] ?? 'a', argv: parseCommand(raw.a), env: {} };
  if (raw.b) o.b = { label: raw['label-b'] ?? 'b', argv: parseCommand(raw.b), env: {} };
  return o;
}

async function main() {
  let o;
  try { o = parseArgs(process.argv.slice(2)); } catch (e) { process.stderr.write(`${e.message}\n\n${HELP}`); return 2; }
  if (o.help) { process.stdout.write(HELP); return 0; }
  try {
    const gate = JSON.parse(readFileSync(o.gate ?? DEFAULT_GATE, 'utf8'));
    o.leakReconnects ??= gate.leak_reconnects;
    const py = findPython();
    if (o.selfTest) {
      if (!py) throw new Error('--self-test needs Python 3.9+ (python3 or python on PATH, or UBAG_BENCH_PYTHON)');
      Object.assign(o, selfTestImpls(py, o.mutate), { codec: 'fake', libopusVersion: 'selftest-fake' });
    } else {
      o.libopusVersion ??= py ? detectLibopus(py) ?? undefined : undefined;
    }
    o.onProgress = (m) => process.stderr.write(`[voice-relay-bench] ${m}\n`);
    const report = await runAb(o);
    const text = `${JSON.stringify(report, null, 2)}\n`;
    if (o.out) writeFileSync(o.out, text); else process.stdout.write(text);
    if (o.markdown) writeFileSync(o.markdown, renderMarkdown(report));
    process.stderr.write(`[voice-relay-bench] byte compare ${report.compare.passed ? 'PASS' : 'FAIL'}; authoritative=${report.authoritative}\n`);
    return report.compare.passed ? 0 : 1;
  } catch (e) {
    process.stderr.write(`[voice-relay-bench] harness error: ${e.message}\n`);
    return 2;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) process.exit(await main());
