import assert from 'node:assert/strict';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { closeSync, existsSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync, writeSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { DURATIONS_MS, FRAME_BYTES, buildCorpus, buildHello, framingWire, frame, loadFixture, measureInputs, relayToken, synthPcm, verifyTokenVectors } from './corpus.mjs';
import { compareRuns, compareTranscripts, leakVerdict, pairedStats, reductionPct, summarizePairs, tCritical, transcriptDigest } from './report.mjs';
import { Sink } from './sink.mjs';
import {
  findPython, latencyStats, makeWrapper, parseArgs, parseCommand, parseProcStat, parseProcStatus, pctl, procStatTicks, runAb, selfTestImpls,
} from './run.mjs';
import { evaluateGate } from '../benchmark/rust-gate.mjs';

const here = fileURLToPath(new URL('.', import.meta.url));
const gate = JSON.parse(readFileSync(new URL('../../tests/load/voice-relay-gate.json', import.meta.url), 'utf8'));
const SEED = gate.corpus.seed;
const python = findPython();
const tmp = () => mkdtempSync(join(tmpdir(), 'relay-bench-test-'));

// ---------------------------------------------------------------- corpus

test('corpus: the same seed is byte-identical, another seed is not, the manifest carries seed and libopus version', () => {
  const a = buildCorpus({ seed: SEED, libopusVersion: 'libopus 1.3.1' });
  const b = buildCorpus({ seed: SEED, libopusVersion: 'libopus 1.3.1' });
  assert.deepEqual(a.scenarios, b.scenarios);
  assert.equal(a.manifest.sha256, b.manifest.sha256);
  assert.notEqual(buildCorpus({ seed: SEED + 1 }).manifest.sha256, a.manifest.sha256);
  assert.equal(a.manifest.seed, SEED);
  assert.equal(a.manifest.libopus_version, 'libopus 1.3.1');
  assert.equal(a.manifest.schema, 'ubag.voice_relay_corpus/1');
  assert.equal(new Set(a.scenarios.map((s) => s.id)).size, a.scenarios.length);
  assert.throws(() => buildCorpus({}), /integer seed/);
  assert.throws(() => buildCorpus({ seed: 1, only: ['nope'] }), /unknown scenario id/);
});

test('corpus: covers every hello and framing case of the shared v2 fixture, and the profiles nest', () => {
  const fx = loadFixture();
  const full = buildCorpus({ seed: SEED });
  const ids = new Set(full.scenarios.map((s) => s.id));
  for (const c of fx.hello_validation) if (c.secret_configured !== false) assert.ok(ids.has(`hello.${c.id}`), c.id);
  for (const c of fx.framing) { assert.ok(ids.has(`rawfirst.${c.id}`)); assert.ok(ids.has(`post.${c.id}`)); }
  const smoke = buildCorpus({ seed: SEED, profile: 'smoke' });
  assert.ok(smoke.scenarios.length > 8 && smoke.scenarios.length < full.scenarios.length);
  for (const s of smoke.scenarios) assert.ok(ids.has(s.id));
  assert.ok(!ids.has('slow.hello_timeout'));
  assert.ok(buildCorpus({ seed: SEED, includeSlow: true }).scenarios.some((s) => s.id === 'slow.hello_timeout'));
  assert.equal(buildCorpus({ seed: SEED, only: ['mic.mute'] }).scenarios.length, 1);
});

test('corpus: the mic scenarios exercise every Opus duration, corrupt and empty packets, and max-size frames', () => {
  const sc = (id) => buildCorpus({ seed: SEED, only: [id] }).scenarios[0];
  const ms = (b) => {
    const toc = b[0];
    const config = toc >> 3;
    const dur = config < 12 ? [10, 20, 40, 60][config % 4] : config < 16 ? [10, 20][config % 2] : [2.5, 5, 10, 20][config % 4];
    return (toc & 3) === 3 ? dur * b[1] : dur; // code 3: frame count in byte 1 (CBR, no padding here)
  };
  const durations = new Set(sc('mic.durations').client.map((s) => ms(Buffer.from(s.b, 'hex'))));
  assert.deepEqual([...durations].sort((a, b) => a - b), DURATIONS_MS);
  const corrupt = sc('mic.corrupt').client.map((s) => Buffer.from(s.b, 'hex'));
  assert.ok(corrupt.some((b) => b.length === 0), 'an empty packet');
  assert.ok(corrupt.some((b) => b.length === 2 && b[1] === 0 && (b[0] & 3) === 3), 'a code-3 packet with zero frames');
  assert.equal(sc('mic.maxsize').client.at(-1).b.length / 2, 65535);
  const mute = sc('mic.mute').client.filter((s) => s.t === 'control').map((s) => JSON.parse(Buffer.from(s.b, 'hex')));
  assert.ok(mute.some((m) => m.muted === true) && mute.some((m) => m.muted === false));
});

test('corpus: PCM is deterministic, honours the partial tail, and the measure inputs are 100 valid 20 ms packets', () => {
  const p = synthPcm({ seed: 5, frames: 4, tail: 700 });
  assert.equal(p.length, 4 * FRAME_BYTES + 700);
  assert.ok(p.equals(synthPcm({ seed: 5, frames: 4, tail: 700 })));
  assert.ok(!p.equals(synthPcm({ seed: 6, frames: 4, tail: 700 })));
  assert.ok(synthPcm({ seed: 1, frames: 1, shape: 'silence' }).every((b) => b === 0));
  const { packets, pcm } = measureInputs(SEED);
  assert.equal(packets.length, 100);
  assert.equal(pcm.length, 100 * FRAME_BYTES);
  for (const pk of packets) assert.equal((pk[0] & 3), 0); // code 0, one 20 ms frame
});

test('corpus: the shared HMAC vectors reproduce and hellos are built exactly like the Python and Go tests build them', () => {
  const fx = loadFixture();
  assert.deepEqual(verifyTokenVectors(fx), { checked: 8, passed: true, mismatches: [] });
  const now = 1_800_000_000;
  const hello = (id) => JSON.parse(buildHello({ case: fx.hello_validation.find((c) => c.id === id) }, fx.secret, now).subarray(5));
  const v = hello('valid');
  assert.equal(v.exp, now + 60);
  assert.equal(v.token, relayToken(fx.secret, 's1', now + 60));
  assert.equal(hello('token_wrong').token, '0'.repeat(64));
  const bound = hello('valid_bound');
  assert.equal(bound.token, relayToken(fx.secret, 's1', now + 60, 'node-a', 3));
  assert.notEqual(bound.token, relayToken(fx.secret, 's1', now + 60));
  assert.equal(hello('bound_token_unbound_signature').token, relayToken(fx.secret, 's1', now + 60));
  assert.equal(hello('valid_session_id_128').session_id.length, 128);
  const audio = buildHello({ case: fx.hello_validation.find((c) => c.id === 'first_frame_audio') }, fx.secret, now);
  assert.equal(audio[4], 1);
  const wire = (id) => framingWire(fx.framing.find((c) => c.id === id));
  assert.equal(wire('length_zero').length, 4);
  assert.equal(wire('audio_max_length').length, 4 + 65536);
  assert.equal(wire('audio_min_payload').toString('hex'), '02000000' + '01' + 'ab');
  assert.equal(frame(2, '{"op":"ready"}').toString('hex'), '0f000000027b226f70223a227265616479227d');
});

// ---------------------------------------------------------------- report: compare and statistics

const transcript = (over = {}) => ({
  control: ['{"op":"ready"}'], speaker: { frames: 2, bytes: 10, sha256: 's', per: ['a', 'b'] }, fifo: { bytes: 4, sha256: 'f', per: ['x', 'y'] }, closed: true, ...over,
});

test('compare: identical transcripts match; each stream difference is reported at its first index', () => {
  assert.deepEqual(compareTranscripts(transcript(), transcript()), []);
  const fifo = compareTranscripts(transcript(), transcript({ fifo: { bytes: 4, sha256: 'g', per: ['x', 'z'] } }));
  assert.deepEqual(fifo.map((d) => [d.stream, d.index, d.a, d.b]), [['fifo', 1, 'y', 'z']]);
  const spk = compareTranscripts(transcript(), transcript({ speaker: { frames: 3, bytes: 15, sha256: 't', per: ['a', 'b', 'c'] } }));
  assert.deepEqual(spk.map((d) => [d.stream, d.index]), [['speaker', 2]]);
  const ctl = compareTranscripts(transcript(), transcript({ control: ['{"op":"ready"}', '{"op":"error","reason":"x"}'] }));
  assert.deepEqual(ctl.map((d) => [d.stream, d.index]), [['control', 1]]);
  assert.equal(compareTranscripts(transcript(), transcript({ closed: false }))[0].stream, 'closed');
  assert.equal(compareTranscripts(transcript({ second: { control: ['a'], closed: true } }), transcript({ second: { control: ['b'], closed: true } }))[0].stream, 'second');
  // a stream left out of `compare` does not count
  assert.deepEqual(compareTranscripts(transcript(), transcript({ speaker: { frames: 3, bytes: 15, sha256: 't', per: ['a', 'b', 'c'] } }), ['control', 'fifo', 'closed']), []);
});

test('compareRuns: coverage shows how much output the identical streams carried; a missing scenario is a mismatch', () => {
  const scenarios = [{ id: 'one' }, { id: 'two', compare: ['control'] }];
  const a = { one: transcript(), two: transcript() };
  const ok = compareRuns(scenarios, a, { one: transcript(), two: transcript({ closed: false }) });
  assert.equal(ok.passed, true);
  assert.deepEqual(ok.coverage, { scenarios_with_fifo: 2, fifo_bytes: 8, scenarios_with_speaker: 2, speaker_frames: 4, scenarios_with_error_reply: 0 });
  assert.equal(ok.digests.length, 2);
  assert.notEqual(transcriptDigest(transcript()), transcriptDigest(transcript({ closed: false })));
  const bad = compareRuns(scenarios, a, { one: transcript({ closed: false }) });
  assert.equal(bad.passed, false);
  assert.deepEqual(bad.failures.map((f) => f.id), ['one', 'two']);
});

test('paired statistics: mean, sample sd and the Student t 95 % interval', () => {
  const st = pairedStats([10, 12, 11, 13, 9]);
  assert.equal(st.n, 5);
  assert.equal(st.mean, 11);
  assert.equal(st.sd, 1.581);
  assert.equal(tCritical(4), 2.776);
  assert.equal(st.ci_lower, 9.037); // 11 - 2.776 * 1.5811 / sqrt(5)
  assert.equal(st.ci_upper, 12.963);
  assert.deepEqual(pairedStats([3, 3, 3]), { n: 3, mean: 3, sd: 0, ci_lower: 3, ci_upper: 3 });
  assert.deepEqual(pairedStats([4]), { n: 1, mean: 4, sd: null, ci_lower: null, ci_upper: null });
  assert.equal(pairedStats([null, NaN, 5, undefined]).n, 1);
  assert.equal(pairedStats([]).mean, null);
  assert.equal(reductionPct(100, 80), 20);
  assert.equal(reductionPct(100, 120), -20);
  assert.equal(reductionPct(0, 5), null);
  assert.equal(reductionPct(null, 5), null);
});

test('summarizePairs: reductions are positive when B is lower, regressions are their negation, identical runs are exactly 0 %', () => {
  const run = (cpu, rss, p95, extra = {}) => ({ cpu_ms_per_call_minute: cpu, rss_peak_kib: rss, mic: { p95_ms: p95, p99_ms: p95 }, speaker: { p95_ms: p95, p99_ms: p95 }, mic_blocks: 100, speaker_frames: 100, mic_drops: 0, speaker_drops: 0, ...extra });
  const better = summarizePairs([{ a: run(100, 100, 10), b: run(80, 50, 12, { mic_drops: 2 }) }, { a: run(200, 200, 10), b: run(160, 100, 12) }]);
  assert.equal(better.cpu_reduction_pct.mean, 20);
  assert.equal(better.rss_reduction_pct.mean, 50);
  assert.equal(better.mic_p95_regression_pct.mean, 20); // 10 -> 12 ms is a +20 % regression
  assert.deepEqual(better.drops, { a: 0, b: 2 });
  const same = summarizePairs([{ a: run(100, 100, 10), b: run(100, 100, 10) }, { a: run(100, 100, 10), b: run(100, 100, 10) }]);
  for (const k of ['cpu_reduction_pct', 'rss_reduction_pct', 'mic_p95_regression_pct', 'mic_blocks_delta_pct', 'speaker_frames_delta_pct']) {
    assert.deepEqual([same[k].mean, same[k].ci_lower, same[k].ci_upper], [0, 0, 0], k);
  }
});

test('leakVerdict counts fd, thread, child and RSS growth over the limit', () => {
  const base = { fds: 10, threads: 3, children: 0, rss_kib: 30000 };
  assert.equal(leakVerdict(base, { ...base }, 4096).leaks, 0);
  assert.equal(leakVerdict(base, { ...base, fds: 12, children: 1 }, 4096).leaks, 2);
  assert.equal(leakVerdict(base, { ...base, rss_kib: 30000 + 5000 }, 4096).leaks, 1);
  assert.equal(leakVerdict(base, { ...base, fds: 9, rss_kib: 29000 }, 4096).leaks, 0); // shrinking is not a leak
});

// ---------------------------------------------------------------- run.mjs helpers

test('helpers: command parsing keeps quoted paths, percentiles use the relay bench indexing, /proc parsers survive odd command names', () => {
  assert.deepEqual(parseCommand('python3 "C:\\Program Files\\relay.py" --addr {addr} \'a b\''), ['python3', 'C:\\Program Files\\relay.py', '--addr', '{addr}', 'a b']);
  assert.throws(() => parseCommand('x "oops'), /unterminated/);
  assert.equal(pctl([1, 2, 3, 4, 5, 6, 7, 8, 9, 10], 0.95), 10);
  assert.equal(pctl([1, 2, 3, 4, 5, 6, 7, 8, 9, 10], 0.5), 6);
  assert.equal(latencyStats([]), null);
  assert.equal(latencyStats([3, 1, 2]).p50_ms, 2);
  const stat = '4242 (python3 (x) y) S 1 4242 4242 0 -1 4194560 100 0 0 0 150 50 0 0 20 0 3 0 1000 1 2 3';
  assert.deepEqual(procStatTicks(stat), { ticks: 200, ppid: 1 });
  assert.equal(parseProcStat(stat)[0], 'S');
  assert.deepEqual(parseProcStatus('Name:\tpython3\nVmHWM:\t  31244 kB\nVmRSS:\t  29000 kB\nThreads:\t3\n'), { rss_peak_kib: 31244, rss_kib: 29000, threads: 3 });
  assert.deepEqual(parseProcStatus('Name:\tx\n'), { rss_peak_kib: null, rss_kib: null, threads: null });
});

test('helpers: the shim wrapper is a runnable per-platform executable that forwards its arguments', () => {
  const dir = tmp();
  try {
    writeFileSync(join(dir, 'echo.mjs'), 'process.stdout.write(process.argv.slice(2).join("|"));');
    const exe = makeWrapper(dir, 'echo', join(dir, 'echo.mjs'));
    assert.ok(exe.endsWith(process.platform === 'win32' ? 'echo.cmd' : 'echo'));
    // Node cannot exec a .cmd directly (the relay's Python can), so go through the shell on Windows
    const r = spawnSync(exe, ['--raw', '--format=s16le'], { encoding: 'utf8', shell: process.platform === 'win32' });
    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.stdout.trim(), '--raw|--format=s16le');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('cli: argument validation and help', () => {
  assert.throws(() => parseArgs([]), /--self-test or both --a and --b/);
  assert.throws(() => parseArgs(['--self-test', '--profile', 'huge']), /--profile/);
  assert.throws(() => parseArgs(['--self-test', '--bogus']), /unknown option/);
  assert.throws(() => parseArgs(['--self-test', '--selftest-mutate', 'x']), /--selftest-mutate/);
  assert.throws(() => parseArgs(['--a', 'x']), /both --a and --b/);
  const o = parseArgs(['--a', 'python3 relay.py --addr {addr}', '--b', 'cand --addr {addr}', '--label-b', 'rust', '--lab-host', '--pairs=7', '--only', 'mic.mute,spk.silence']);
  assert.deepEqual(o.a.argv, ['python3', 'relay.py', '--addr', '{addr}']);
  assert.equal(o.b.label, 'rust');
  assert.equal(o.pairs, 7);
  assert.deepEqual(o.only, ['mic.mute', 'spk.silence']);
  assert.equal(o.labHost, true);
  assert.equal(parseArgs(['--self-test', '--no-measure']).pairs, 0);
  const help = spawnSync(process.execPath, [join(here, 'run.mjs'), '--help'], { encoding: 'utf8' });
  assert.equal(help.status, 0);
  assert.match(help.stdout, /--self-test/);
  assert.equal(spawnSync(process.execPath, [join(here, 'run.mjs')], { encoding: 'utf8' }).status, 2);
});

// ---------------------------------------------------------------- shims and sink

/** Run the shim; the gate opens once `gateAtBytes` have arrived (event driven, so a slow start-up cannot truncate the stream). */
function runShim(dir, cfg, { gateAtBytes, gateAfterMs }) {
  const cfgPath = join(dir, 'shim.json');
  rmSync(join(dir, 'gate'), { force: true });
  writeFileSync(cfgPath, JSON.stringify({ gate: join(dir, 'gate'), log: join(dir, 'log'), ...cfg }));
  const child = spawn(process.execPath, [join(here, 'parec-shim.mjs'), '--raw'], { env: { ...process.env, UBAG_BENCH_SHIM_CONFIG: cfgPath }, stdio: ['ignore', 'pipe', 'inherit'] });
  const chunks = [];
  let total = 0;
  let aliveAtGate = null;
  const open = () => { aliveAtGate = child.exitCode === null; writeFileSync(join(dir, 'gate'), '1'); };
  child.stdout.on('data', (d) => { chunks.push(d); total += d.length; if (gateAtBytes && total >= gateAtBytes && aliveAtGate === null) open(); });
  if (gateAfterMs) setTimeout(open, gateAfterMs);
  return new Promise((res) => child.once('exit', (code) => res({ code, aliveAtGate, out: Buffer.concat(chunks), log: existsSync(join(dir, 'log')) ? readFileSync(join(dir, 'log'), 'utf8').split('\n').filter(Boolean) : [] })));
}

test('parec shim: emits the PCM in 1920 B chunks (partial tail last), holds the pipe open until the gate, then EOF', async () => {
  const dir = tmp();
  try {
    const pcm = synthPcm({ seed: 9, frames: 3, tail: 700 });
    writeFileSync(join(dir, 'in.pcm'), pcm);
    const r = await runShim(dir, { pcm: join(dir, 'in.pcm'), pace_ms: 5, loop: false }, { gateAtBytes: pcm.length });
    assert.equal(r.code, 0);
    assert.ok(r.out.equals(pcm));
    assert.equal(r.aliveAtGate, true, 'the pipe stays open after the last chunk until the gate opens');
    assert.equal(r.log.length, 4); // one hand-off stamp per chunk, three whole and the partial tail
    assert.ok(r.log.every((l) => /^\d+$/.test(l)));
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('parec shim: loop with max_chunks emits exactly that many chunks; an early gate stops a paced stream', async () => {
  const dir = tmp();
  try {
    writeFileSync(join(dir, 'in.pcm'), synthPcm({ seed: 9, frames: 3 }));
    const r = await runShim(dir, { pcm: join(dir, 'in.pcm'), pace_ms: 2, loop: true, max_chunks: 10 }, { gateAtBytes: 10 * FRAME_BYTES });
    assert.equal(r.out.length, 10 * FRAME_BYTES);
    assert.equal(r.log.length, 10);
    const early = await runShim(dir, { pcm: join(dir, 'in.pcm'), pace_ms: 200, loop: false }, { gateAfterMs: 1 });
    assert.ok(early.out.length < 3 * FRAME_BYTES);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('pactl shim: reports the virtual mic and provider sink so the relay health check passes', () => {
  const run = (...a) => spawnSync(process.execPath, [join(here, 'pactl-shim.mjs'), ...a], { encoding: 'utf8' });
  assert.equal(run('info').status, 0);
  assert.match(run('list', 'short', 'sources').stdout, /ubag_virtual_mic[\s\S]*ubag_provider_sink\.monitor/);
  assert.match(run('list', 'short', 'sinks').stdout, /\tubag_provider_sink\t/);
  assert.equal(run('set-default-sink', 'x').status, 0);
});

const settle = (ms) => new Promise((res) => setTimeout(res, ms));

test('sink: tails a regular file by offset and stamps every read', async () => {
  const dir = tmp();
  const file = join(dir, 'mic.pcm');
  writeFileSync(file, '');
  const sink = new Sink(file, { fifo: false }).start();
  try {
    const fd = openSync(file, 'a');
    writeSync(fd, Buffer.alloc(FRAME_BYTES, 1));
    await settle(100);
    writeSync(fd, Buffer.alloc(FRAME_BYTES, 2));
    await settle(100);
    closeSync(fd);
    assert.equal(await sink.quiet(50), true);
    const data = sink.take();
    assert.equal(data.length, 2 * FRAME_BYTES);
    assert.equal(data[0], 1);
    assert.equal(data[FRAME_BYTES], 2);
    assert.equal(sink.total, 2 * FRAME_BYTES);
    assert.equal(sink.timeOfByte(FRAME_BYTES) <= sink.timeOfByte(2 * FRAME_BYTES), true);
    assert.equal(sink.timeOfByte(99 * FRAME_BYTES), null);
  } finally {
    await sink.stop();
    rmSync(dir, { recursive: true, force: true });
  }
});

test('sink: the FIFO mode reads a real FIFO (POSIX only)', { skip: process.platform === 'win32' && 'no mkfifo on Windows' }, async () => {
  const dir = tmp();
  const fifo = join(dir, 'mic.fifo');
  execFileSync('mkfifo', ['-m', '600', fifo]);
  const sink = new Sink(fifo, { fifo: true }).start(); // reader first, as the relay's non-blocking open requires
  try {
    const w = openSync(fifo, 'w');
    writeSync(w, Buffer.alloc(FRAME_BYTES, 7));
    await settle(100);
    closeSync(w);
    await settle(100);
    assert.equal(sink.take().length, FRAME_BYTES);
  } finally {
    await sink.stop();
    rmSync(dir, { recursive: true, force: true });
  }
});

// ---------------------------------------------------------------- the offline self-test: Python relay as A and B

const baselineFixture = (version = 'libopus 1.3.1') => ({
  schema: 'ubag.relay_baseline/1', authoritative: true, host_class: 'lab', libopus_version: version,
  runs: [{ sessions: 1, window_s: 30, codec: 'libopus', fifo_kind: 'fifo', per_session: [{ process_cpu_s: 3, cpu_pct_of_core: 10, libopus_cpu_s: 0.6, syscall_cpu_s: 0.45 }] }],
  stop_rule: { A: { fired: false }, B: { container_cpu_pct_per_call: 40 } },
});

// off Linux there is no /proc: the leak check then reports supported: false
const fast = { profile: 'smoke', paceMs: 5, quietMs: 150, durationS: 1.2, warmupS: 0.3, pairs: 2, leakReconnects: 4, codec: 'fake', selfTest: true, libopusVersion: 'selftest-fake', hostClass: 'unit test' };

test('self-test: the Python relay against itself passes the byte compare and moves exactly the same frames (0 % delta)', { skip: !python && 'Python 3.9+ not on PATH', timeout: 240000 }, async () => {
  const report = await runAb({ ...fast, ...selfTestImpls(python) });
  assert.equal(report.schema, 'ubag.voice_relay_ab/1');
  assert.equal(report.compare.passed, true, JSON.stringify(report.compare.failures));
  assert.equal(report.compare.mismatches, 0);
  assert.deepEqual(report.compare.expectation_failures, { a: [], b: [] }); // the relay answers the way the shared fixture says
  assert.ok(report.compare.coverage.fifo_bytes > 100_000 && report.compare.coverage.speaker_frames > 50, JSON.stringify(report.compare.coverage));
  assert.ok(report.compare.coverage.scenarios_with_error_reply >= 10);
  assert.equal(report.golden.relay_token_vectors.passed, true);
  assert.equal(report.golden.opus_golden.recorded, false);
  assert.equal(report.pairs.length, 2);
  for (const key of ['mic_blocks_delta_pct', 'speaker_frames_delta_pct']) {
    assert.deepEqual([report.summary[key].mean, report.summary[key].ci_lower, report.summary[key].ci_upper], [0, 0, 0], key);
  }
  assert.deepEqual(report.summary.drops, { a: 0, b: 0 });
  for (const pair of report.pairs) for (const run of [pair.a, pair.b]) {
    assert.equal(run.mic_drops, 0);
    assert.equal(run.speaker_drops, 0);
    assert.ok(run.mic.n > 0 && run.speaker.n > 0);
    assert.deepEqual(run.errors, []);
  }
  assert.equal(report.pairs[0].order, 'ab');
  assert.equal(report.pairs[1].order, 'ba'); // interleaved, alternating
  assert.equal(report.self_test, true);
  assert.equal(report.authoritative, false);
  assert.ok(report.non_authoritative_reasons.some((r) => /self-test/.test(r)));
  if (process.platform === 'linux') {
    assert.equal(report.leaks.a.supported, true);
    assert.equal(typeof report.leaks.b.leaks, 'number');
    assert.ok(report.pairs[0].a.cpu_ms_per_call_minute > 0);
  } else {
    assert.equal(report.pairs[0].a.cpu_ms_per_call_minute, null); // no /proc: an unmeasured value is null, never invented
    assert.equal(report.leaks.a.supported, false);
  }
  // the evaluator refuses a self-test report outright, whatever the baseline says
  const verdict = evaluateGate({ gate, baseline: baselineFixture(), ab: report, rollbackDrill: 'pass' });
  assert.equal(verdict.verdict, 'invalid');
  assert.match(verdict.reason, /self-test/);
});

const only = ['mic.durations', 'mic.mute', 'spk.partial_tail', 'both.mixed'];
for (const [mutate, expected] of [['speaker', ['spk.partial_tail', 'both.mixed']], ['mic', ['mic.durations']], ['mute', ['mic.mute']]]) {
  test(`self-test negative control: a candidate that breaks ${mutate} output fails the byte compare in the right scenarios`, { skip: !python && 'Python 3.9+ not on PATH', timeout: 120000 }, async () => {
    const report = await runAb({ ...fast, pairs: 0, leakReconnects: 0, only, ...selfTestImpls(python, mutate) });
    assert.equal(report.compare.passed, false);
    for (const id of expected) assert.ok(report.compare.failures.some((f) => f.id === id), `${id} not flagged: ${JSON.stringify(report.compare.failures.map((f) => f.id))}`);
    assert.ok(report.compare.failures.every((f) => f.diffs.length > 0));
    assert.deepEqual(report.compare.expectation_failures.b, []); // still protocol-correct, just not byte-identical
  });
}

test('a relay command that dies at start-up is a harness error with its stderr, not a hang', { timeout: 60000 }, async () => {
  const dead = { label: 'dead', argv: [process.execPath, '-e', 'console.error("boom: no such relay"); process.exit(3)'], env: {} };
  await assert.rejects(runAb({ ...fast, pairs: 0, leakReconnects: 0, only: ['mic.mute'], a: dead, b: dead }), /relay exited before listening[\s\S]*boom: no such relay/);
});

test('the report records provenance a verdict needs: libopus version, host class, seed, corpus sha, scenario count', { skip: !python && 'Python 3.9+ not on PATH', timeout: 120000 }, async () => {
  const report = await runAb({ ...fast, pairs: 0, leakReconnects: 0, only: ['hello.valid', 'spk.silence'], ...selfTestImpls(python) });
  assert.equal(report.libopus_version, 'selftest-fake');
  assert.equal(report.host_class, 'unit test');
  assert.equal(report.corpus.seed, SEED);
  assert.match(report.corpus.sha256, /^[0-9a-f]{64}$/);
  assert.equal(report.corpus.scenarios, 2);
  assert.equal(report.corpus.fixture_sha256, loadFixture().sha256);
  assert.equal(report.impls.a.label, 'python-selftest');
});
