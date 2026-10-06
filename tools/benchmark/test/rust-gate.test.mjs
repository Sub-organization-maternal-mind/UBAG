import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { checkGate, evaluateBaseline, evaluateGate, renderVerdict } from '../rust-gate.mjs';

const cli = fileURLToPath(new URL('../rust-gate.mjs', import.meta.url));
const gate = JSON.parse(readFileSync(new URL('../../../tests/load/voice-relay-gate.json', import.meta.url), 'utf8'));
const bench = readFileSync(new URL('../../../deploy/vps/browser/tests/bench_relay.py', import.meta.url), 'utf8');

// ---- fixtures: a P7.5 baseline and a voice-relay A/B report -------------------------------------------------------------

/** One N=1 run of 30 s: the relay burns `cpuPct` % of a core, of which `libopus` % and `syscall` % of its own CPU. */
function baseline({ libopus = 20, syscall = 15, cpuPct = 10, container = 40, authoritative = true, codec = 'libopus', fifo = 'fifo', version = 'libopus 1.3.1', embeddedA } = {}) {
  const cpu = (cpuPct / 100) * 30;
  const a = ((libopus + syscall) / 100) >= gate.stop_libopus_syscall_share_pct / 100;
  return {
    schema: 'ubag.relay_baseline/1', authoritative, host_class: '2c4g-lab', libopus_version: version,
    runs: [{ sessions: 1, window_s: 30, codec, fifo_kind: fifo, per_session: [{ process_cpu_s: cpu, cpu_pct_of_core: cpuPct, libopus_cpu_s: (cpu * libopus) / 100, syscall_cpu_s: (cpu * syscall) / 100 }] }],
    stop_rule: { A: { fired: embeddedA ?? a }, B: container == null ? { container_cpu_pct_per_call: null } : { container_cpu_pct_per_call: container } },
  };
}
// libopus 20% of 10% of a core over 30 s = 0.6 s => 1200 ms per call-minute

const wobble = (i) => 1 + 0.002 * (((i * 37) % 7) - 3); // deterministic +-0.6 % noise per pair
/** One side of a pair. cpu in ms per call-minute, rss in KiB, latencies in ms. */
const side = (i, { cpu = 6000, rss = 40000, lat = 2, drops = 0, mixed } = {}) => ({
  cpu_ms_per_call_minute: cpu * wobble(i), rss_peak_kib: rss * wobble(i + 1),
  mic: { p95_ms: lat * wobble(i + 2), p99_ms: lat * 1.2 * wobble(i + 3) }, speaker: { p95_ms: lat * wobble(i + 4), p99_ms: lat * 1.2 * wobble(i + 5) },
  mic_drops: drops, speaker_drops: 0, ...(mixed ? { mixed } : {}),
});
function ab({ n = 6, a = {}, b = {}, mixedA, mixedB, mismatches = 0, leaks = 0, leaksSupported = true, selfTest = false, authoritative = true, seed = gate.corpus.seed,
  version = 'libopus 1.3.1', codec = 'libopus', coverage = { fifo_bytes: 1000, speaker_frames: 50 }, tokensOk = true, opusRecorded = true } = {}) {
  return {
    schema: 'ubag.voice_relay_ab/1', self_test: selfTest, authoritative, non_authoritative_reasons: authoritative ? [] : ['no --lab-host attestation'],
    host_class: '2c4g-lab', libopus_version: version, codec, corpus: { seed, sha256: 'x' },
    golden: { relay_token_vectors: { checked: 8, passed: tokensOk }, opus_golden: { recorded: opusRecorded, libopus_version: version, matches_run_version: opusRecorded } },
    compare: { passed: mismatches === 0, mismatches, scenarios: 66, coverage },
    pairs: Array.from({ length: n }, (_, i) => ({ pair: i + 1, a: side(i, { ...a, ...(mixedA ? { mixed: mixedA(i) } : {}) }), b: side(i + 11, { ...b, ...(mixedB ? { mixed: mixedB(i) } : {}) }) })),
    leaks: { a: { supported: true, leaks: 0 }, b: leaksSupported ? { supported: true, leaks } : { supported: false } },
  };
}
// a candidate that cuts CPU 40% (overhead (6000-1200) -> (3600-1200): -50%), RSS 50%, latency better (a regression must be RULED OUT), mixed run better
const winner = () => ab({
  a: { cpu: 6000, rss: 40000, lat: 2 }, b: { cpu: 3600, rss: 20000, lat: 1.6 },
  mixedA: () => ({ container_cpu_pct: 80, job_p95_ms: 900 }), mixedB: () => ({ container_cpu_pct: 70, job_p95_ms: 880 }),
});
const evalAll = (over = {}) => evaluateGate({ gate, baseline: baseline(), ab: winner(), rollbackDrill: 'pass', ...over });
const check = (v, id) => (v.ab?.checks ?? []).find((c) => c.id === id) ?? v.baseline.checks.find((c) => c.id === id);

// ---- the pre-registered gate ---------------------------------------------------------------------------------------------

test('the gate file is complete and its stop rule matches the P7.5 bench constants', () => {
  checkGate(gate);
  const py = (name) => Number(new RegExp(`^${name}\\s*=\\s*([0-9.]+)`, 'm').exec(bench)[1]) * 100;
  assert.equal(py('STOP_LIBOPUS_SYSCALL_SHARE'), gate.stop_libopus_syscall_share_pct);
  assert.equal(py('STOP_RELAY_OF_CONTAINER'), gate.min_relay_share_of_container_pct);
  assert.equal(py('PLAN_OVERHEAD_SHARE'), gate.min_python_overhead_share_pct);
  assert.equal(py('PLAN_CPU_SAVING'), gate.min_cpu_reduction_pct);
  assert.equal(gate.min_cpu_overhead_reduction_pct, 20);
  assert.equal(gate.min_rss_reduction_pct, 25);
  assert.equal(gate.max_p95_regression_pct, 0);
  assert.equal(gate.max_leaks, 0);
});

test('a renamed or missing gate key throws instead of silently dropping a limit', () => {
  const { max_leaks: _drop, ...rest } = gate;
  assert.throws(() => checkGate(rest), /max_leaks/);
  assert.throws(() => checkGate({ ...gate, ci_level: 0.99 }), /ci_level/);
  assert.throws(() => checkGate({ ...gate, schema: 'other' }), /schema/);
});

// ---- stage 0: the P7.5 baseline ------------------------------------------------------------------------------------------

test('stop rule A: libopus plus syscalls at 70 % or more of relay CPU closes the experiment', () => {
  const v = evaluateGate({ gate, baseline: baseline({ libopus: 60, syscall: 12 }) });
  assert.equal(v.verdict, 'stop');
  assert.match(v.reason, /rule A/);
  assert.equal(v.prototype_allowed, false);
  assert.equal(v.adoption_allowed, false);
});

test('stop rule B: a relay under 5 % of the container is not material', () => {
  const v = evaluateGate({ gate, baseline: baseline({ container: 400 }) }); // 10 / 400 = 2.5 %
  assert.equal(v.verdict, 'stop');
  assert.match(v.reason, /rule B/);
});

test('without the live-call container CPU the verdict is inconclusive, never a guess', () => {
  const v = evaluateGate({ gate, baseline: baseline({ container: null }) });
  assert.equal(v.verdict, 'inconclusive');
  assert.equal(check(v, 'stop_rule_B').status, 'unevaluated');
});

test('a stop rule that did not fire and a high enough Python overhead authorise a prototype, not adoption', () => {
  const v = evaluateGate({ gate, baseline: baseline() });
  assert.equal(v.verdict, 'prototype');
  assert.equal(v.prototype_allowed, true);
  assert.equal(v.adoption_allowed, false);
  assert.equal(check(v, 'stop_rule_A').value, 35);
  assert.equal(check(v, 'stop_rule_B').value, 25);
  assert.equal(v.baseline.libopus_ms_per_call_minute, 1200);
});

test('the overhead ceiling stops a baseline whose Python overhead is below 20 % (when rule A is looser)', () => {
  const loose = { ...gate, stop_libopus_syscall_share_pct: 95 };
  const v = evaluateGate({ gate: loose, baseline: baseline({ libopus: 70, syscall: 15, embeddedA: false }) });
  assert.equal(v.verdict, 'stop');
  assert.match(v.reason, /overhead is below the ceiling/);
});

test('an unusable baseline is invalid: fake codec, no FIFO, laptop, or a stop_rule that contradicts its own runs', () => {
  assert.equal(evaluateGate({ gate, baseline: baseline({ codec: 'fake' }) }).verdict, 'invalid');
  assert.equal(evaluateGate({ gate, baseline: baseline({ fifo: 'file' }) }).verdict, 'invalid');
  assert.equal(evaluateGate({ gate, baseline: baseline({ version: null }) }).verdict, 'invalid');
  assert.equal(evaluateGate({ gate, baseline: baseline({ authoritative: false }) }).verdict, 'invalid');
  assert.equal(evaluateGate({ gate, baseline: baseline({ authoritative: false }), allowNonAuthoritative: true }).verdict, 'prototype');
  assert.equal(evaluateGate({ gate, baseline: baseline({ embeddedA: true }) }).verdict, 'invalid');
  assert.equal(evaluateGate({ gate, baseline: null }).verdict, 'invalid');
  assert.equal(evaluateGate({ gate, baseline: { schema: 'nope' } }).verdict, 'invalid');
});

// ---- stage 1: the paired A/B --------------------------------------------------------------------------------------------

test('a candidate that wins on every axis with everything recorded is go', () => {
  const v = evalAll();
  assert.equal(v.verdict, 'go', JSON.stringify(v.ab.checks.filter((c) => !['pass'].includes(c.status))));
  assert.equal(v.adoption_allowed, true);
  assert.equal(v.libopus_version, 'libopus 1.3.1');
  assert.equal(v.host_class, '2c4g-lab');
  assert.ok(check(v, 'cpu_overhead_reduction').ci_lower >= 20);
  assert.match(renderVerdict(v), /GO/);
});

test('the overhead rule is CPU minus libopus self-time: -20 % CPU can still be a -25 % overhead win', () => {
  // A 6000, B 4800 ms: CPU -20 % (below 25) but overhead (6000-1200)->(4800-1200) = -25 % (above 20)
  const v = evalAll({ ab: ab({ a: { cpu: 6000, rss: 40000, lat: 2 }, b: { cpu: 4800, rss: 40000, lat: 1.6 }, mixedA: () => ({ container_cpu_pct: 80 }), mixedB: () => ({ container_cpu_pct: 70 }) }) });
  assert.equal(check(v, 'cpu_reduction').status, 'fail');
  assert.equal(check(v, 'cpu_overhead_reduction').status, 'pass');
  assert.equal(v.verdict, 'go');
});

test('Python against Python: zero delta everywhere is a stop (no win), not a go', () => {
  const same = ab({ a: { cpu: 6000 }, b: { cpu: 6000 }, mixedA: () => ({ container_cpu_pct: 80, job_p95_ms: 900 }), mixedB: () => ({ container_cpu_pct: 80, job_p95_ms: 900 }) });
  // identical measurements on both sides
  for (const p of same.pairs) p.b = JSON.parse(JSON.stringify(p.a));
  const v = evalAll({ ab: same });
  assert.equal(v.verdict, 'stop');
  assert.equal(check(v, 'win').status, 'fail');
  assert.equal(check(v, 'cpu_reduction').value, 0);
  assert.equal(check(v, 'material_in_mixed').status, 'fail'); // a CI of [0, 0] does not exceed 0
});

test('any byte-compare mismatch, token-vector failure, leak, extra drop or failed rollback drill is a stop', () => {
  assert.equal(evalAll({ ab: { ...winner(), compare: { ...winner().compare, mismatches: 1, passed: false } } }).verdict, 'stop');
  const w = winner();
  w.golden.relay_token_vectors.passed = false;
  assert.equal(evalAll({ ab: w }).verdict, 'stop');
  const leaky = winner();
  leaky.leaks.b = { supported: true, leaks: 1 };
  assert.equal(evalAll({ ab: leaky }).verdict, 'stop');
  const drops = winner();
  drops.pairs[0].b.mic_drops = 3;
  assert.equal(evalAll({ ab: drops }).verdict, 'stop');
  assert.equal(evalAll({ rollbackDrill: 'fail' }).verdict, 'stop');
});

test('a proven latency regression is a stop; a noisy one whose CI straddles the limit is inconclusive', () => {
  const slow = ab({ a: { cpu: 6000, rss: 40000, lat: 2 }, b: { cpu: 3600, rss: 20000, lat: 2.4 }, mixedA: () => ({ container_cpu_pct: 80 }), mixedB: () => ({ container_cpu_pct: 70 }) });
  const v = evalAll({ ab: slow });
  assert.equal(check(v, 'mic_p95_regression').status, 'fail');
  assert.equal(v.verdict, 'stop');

  const noisy = winner();
  noisy.pairs.forEach((p, i) => { p.b.mic.p95_ms = 2 * (i % 2 ? 0.9 : 1.1); }); // mean 0 %, CI straddles 0
  const v2 = evalAll({ ab: noisy });
  assert.equal(check(v2, 'mic_p95_regression').status, 'inconclusive');
  assert.equal(v2.verdict, 'inconclusive');
});

test('a win whose CI straddles its limit is inconclusive (more pairs), a win proven short of the limit is a stop', () => {
  const marginal = winner();
  marginal.pairs.forEach((p, i) => { p.b.cpu_ms_per_call_minute = 6000 * (i % 2 ? 0.55 : 1.05); p.b.rss_peak_kib = 40000 * (i % 2 ? 0.5 : 1.0); });
  const v = evalAll({ ab: marginal });
  assert.equal(check(v, 'win').status, 'inconclusive');
  assert.equal(v.verdict, 'inconclusive');

  const short = winner(); // a steady -10 % CPU, -5 % RSS: the whole CI sits below every limit
  short.pairs.forEach((p) => { p.b.cpu_ms_per_call_minute = p.a.cpu_ms_per_call_minute * 0.9; p.b.rss_peak_kib = p.a.rss_peak_kib * 0.95; });
  const v2 = evalAll({ ab: short });
  assert.equal(check(v2, 'win').status, 'fail');
  assert.equal(v2.verdict, 'stop');
});

test('missing evidence is inconclusive: no mixed run, no rollback drill, unrecorded Opus golden, no leak check, too few pairs', () => {
  const noMixed = ab({ a: { cpu: 6000, rss: 40000, lat: 2 }, b: { cpu: 3600, rss: 20000, lat: 1.6 } });
  assert.equal(evalAll({ ab: noMixed }).verdict, 'inconclusive', JSON.stringify(evalAll({ ab: noMixed }).ab.checks.filter((c) => c.status !== 'pass')));
  assert.equal(check(evalAll({ ab: noMixed }), 'material_in_mixed').status, 'unevaluated');
  assert.equal(evalAll({ rollbackDrill: null }).verdict, 'inconclusive');
  const unrecorded = winner();
  unrecorded.golden.opus_golden = { recorded: false, libopus_version: null, matches_run_version: false };
  assert.equal(evalAll({ ab: unrecorded }).verdict, 'inconclusive');
  const noLeaks = winner();
  noLeaks.leaks.b = { supported: false };
  assert.equal(evalAll({ ab: noLeaks }).verdict, 'inconclusive');
  const few = winner();
  few.pairs = few.pairs.slice(0, 3);
  assert.equal(check(evalAll({ ab: few }), 'pairs').status, 'inconclusive');
  assert.equal(evalAll({ ab: few }).verdict, 'inconclusive');
});

test('evidence that cannot decide anything is invalid: self-test, laptop, other seed, other libopus, fake codec, an empty compare', () => {
  const bad = (over) => evalAll({ ab: { ...winner(), ...over } });
  assert.equal(bad({ self_test: true }).verdict, 'invalid');
  assert.equal(bad({ authoritative: false, non_authoritative_reasons: ['laptop'] }).verdict, 'invalid');
  assert.equal(bad({ corpus: { seed: 1, sha256: 'x' } }).verdict, 'invalid');
  assert.equal(bad({ libopus_version: 'libopus 1.4' }).verdict, 'invalid');
  assert.equal(bad({ codec: 'fake', libopus_version: 'selftest-fake' }).verdict, 'invalid');
  const empty = winner();
  empty.compare.coverage = { fifo_bytes: 0, speaker_frames: 0 };
  assert.match(bad({ compare: empty.compare }).reason, /proves nothing/);
  assert.equal(evalAll({ ab: { schema: 'x' } }).verdict, 'invalid');
  assert.equal(evalAll({ ab: winner(), allowNonAuthoritative: true }).verdict, 'go'); // the flag only relaxes the host attestation
});

test('a stage-0 stop or inconclusive is final: an A/B report cannot overrule the baseline', () => {
  const v = evaluateGate({ gate, baseline: baseline({ libopus: 60, syscall: 12 }), ab: winner(), rollbackDrill: 'pass' });
  assert.equal(v.verdict, 'stop');
  assert.equal(v.ab, null);
});

test('evaluateBaseline exposes the libopus self-time the overhead rule subtracts', () => {
  assert.equal(evaluateBaseline(gate, baseline()).libopus_ms_per_call_minute, 1200);
});

// ---- the CLI --------------------------------------------------------------------------------------------------------------

test('CLI: writes the verdict, honours --require, rejects bad input', () => {
  const dir = mkdtempSync(join(tmpdir(), 'rust-gate-'));
  try {
    const b = join(dir, 'baseline.json');
    const a = join(dir, 'ab.json');
    writeFileSync(b, JSON.stringify(baseline()));
    writeFileSync(a, JSON.stringify(winner()));
    const run = (...args) => spawnSync(process.execPath, [cli, ...args], { encoding: 'utf8' });
    const out = join(dir, 'verdict.json');
    const proto = run('--baseline', b, '--out', out, '--require', 'prototype');
    assert.equal(proto.status, 0, proto.stderr);
    assert.equal(JSON.parse(readFileSync(out, 'utf8')).verdict, 'prototype');
    assert.equal(run('--baseline', b, '--require', 'go').status, 1);
    const go = run('--baseline', b, '--ab', a, '--rollback-drill', 'pass', '--require', 'go', '--out', out, '--markdown', join(dir, 'v.md'));
    assert.equal(go.status, 0, go.stderr);
    assert.equal(JSON.parse(readFileSync(out, 'utf8')).adoption_allowed, true);
    assert.match(readFileSync(join(dir, 'v.md'), 'utf8'), /GO/);
    assert.equal(run().status, 2);
    assert.equal(run('--baseline', b, '--rollback-drill', 'maybe').status, 2);
    assert.equal(run('--baseline', join(dir, 'missing.json')).status, 2);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
