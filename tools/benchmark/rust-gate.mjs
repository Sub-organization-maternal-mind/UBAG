#!/usr/bin/env node
/**
 * Rust-gate evaluator for the voice audio relay (perf-fleet P7.7). Zero dependencies. A pure function over recorded evidence:
 * no Rust is written before it says `prototype`, and no Rust is adopted before it says `go`. The rule is pre-registered in
 * tests/load/voice-relay-gate.json; this file only applies it.
 *
 *   node tools/benchmark/rust-gate.mjs --baseline relay-baseline.json                    # evidence from the P7.5 profile only
 *   node tools/benchmark/rust-gate.mjs --baseline relay-baseline.json --ab ab.json --rollback-drill pass --out verdict.json
 *
 * Inputs
 *   baseline  `ubag.relay_baseline/1` from deploy/vps/browser/tests/bench_relay.py (P7.5): stop rule A and B, libopus self-time.
 *   ab        `ubag.voice_relay_ab/1` from tools/voice-relay-bench/run.mjs (A = optimised Python, B = candidate): byte compare,
 *             interleaved pairs, leaks, golden token vectors. Absent until a candidate exists.
 *   rollback  --rollback-drill pass|fail, recorded by a human after the UBAG_VOICE_RELAY_IMPL switch drill (P7.8).
 *
 * Verdicts
 *   stop          the experiment is closed (stop rule fired, ceiling too low, or the candidate fails the rule). Python stays.
 *   prototype     stage 0 only: the baseline evidence permits writing a candidate. Never an adoption.
 *   go            the candidate clears every pre-registered check on CI bounds, authoritative host, byte-identical output.
 *   inconclusive  something needed to decide is unmeasured, or a CI straddles its limit: collect more evidence, do not guess.
 *   invalid       the evidence cannot decide anything (self-test, fake codec, laptop, different libopus, wrong seed, no baseline).
 */
import { readFileSync, writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { pairedStats, reductionPct } from '../voice-relay-bench/report.mjs';

export const VERDICT_SCHEMA = 'ubag.rust_gate_verdict/1';
const BASELINE_SCHEMA = 'ubag.relay_baseline/1';
const AB_SCHEMA = 'ubag.voice_relay_ab/1';
const GATE_KEYS = ['min_pairs', 'min_cpu_overhead_reduction_pct', 'min_cpu_reduction_pct', 'min_rss_reduction_pct', 'max_p95_regression_pct', 'max_p99_regression_pct',
  'max_extra_drops', 'max_leaks', 'max_byte_mismatches', 'min_relay_share_of_container_pct', 'min_mixed_improvement_pct', 'stop_libopus_syscall_share_pct', 'min_python_overhead_share_pct'];
const r2 = (x) => (x == null || !Number.isFinite(x) ? null : Math.round(x * 100) / 100);
const mean = (xs) => xs.reduce((s, x) => s + x, 0) / xs.length;

/** A rename in the gate file throws here instead of silently dropping a limit. */
export function checkGate(gate) {
  if (gate?.schema !== 'ubag.voice_relay_gate/1') throw new Error('gate: schema must be ubag.voice_relay_gate/1');
  for (const k of GATE_KEYS) if (typeof gate[k] !== 'number') throw new Error(`gate: ${k} must be a number`);
  if (gate.ci_level !== 0.95) throw new Error('gate: only ci_level 0.95 is implemented');
  if (!Number.isInteger(gate.corpus?.seed)) throw new Error('gate: corpus.seed must be an integer');
  return gate;
}

const fakeCodec = (v) => v == null || /fake|selftest/i.test(String(v));

/** Stage 0: the P7.5 profile. Re-derives rule A and B from the raw runs; the embedded verdict is only cross-checked. */
export function evaluateBaseline(gate, baseline, { allowNonAuthoritative = false } = {}) {
  const base = { stage: 'baseline', checks: [], libopus_ms_per_call_minute: null };
  const invalid = (reason) => ({ ...base, verdict: 'invalid', reason });
  if (baseline?.schema !== BASELINE_SCHEMA || !Array.isArray(baseline.runs) || !baseline.runs.length) return invalid(`baseline must be a ${BASELINE_SCHEMA} report with runs`);
  if (fakeCodec(baseline.libopus_version) || baseline.runs.some((r) => r.codec !== 'libopus' || r.fifo_kind !== 'fifo')) return invalid('baseline needs real libopus and a real FIFO (Linux, browser image)');
  if (gate.require_authoritative && !baseline.authoritative && !allowNonAuthoritative) return invalid('baseline is NON-AUTHORITATIVE (not an isolated lab host)');
  const sessions = baseline.runs.flatMap((r) => r.per_session ?? []);
  const cpu = sessions.reduce((s, x) => s + x.process_cpu_s, 0);
  if (!(cpu > 0)) return invalid('baseline measured no relay CPU');
  const share = (100 * sessions.reduce((s, x) => s + x.libopus_cpu_s + x.syscall_cpu_s, 0)) / cpu;
  const ref = baseline.runs.reduce((m, r) => (r.sessions < m.sessions ? r : m)); // smallest N: the per-call reference
  const refPct = mean(ref.per_session.map((s) => s.cpu_pct_of_core));
  const libopusMs = (mean(ref.per_session.map((s) => s.libopus_cpu_s)) / ref.window_s) * 60 * 1000;
  const container = baseline.stop_rule?.B?.container_cpu_pct_per_call ?? null;
  const relayShare = container ? (100 * refPct) / container : null;
  const ruleA = share >= gate.stop_libopus_syscall_share_pct;
  const ruleB = relayShare == null ? null : relayShare < gate.min_relay_share_of_container_pct;
  const overheadShare = 100 - share;
  const embeddedA = baseline.stop_rule?.A?.fired;
  const checks = [
    { id: 'stop_rule_A', status: ruleA ? 'fail' : 'pass', value: r2(share), threshold: gate.stop_libopus_syscall_share_pct, rule: 'libopus + syscalls share of relay CPU (%) must stay below the threshold' },
    { id: 'stop_rule_B', status: ruleB == null ? 'unevaluated' : ruleB ? 'fail' : 'pass', value: r2(relayShare), threshold: gate.min_relay_share_of_container_pct, rule: 'relay share of one live call\'s container CPU (%) must reach the threshold' },
    { id: 'python_overhead_ceiling', status: overheadShare >= gate.min_python_overhead_share_pct ? 'pass' : 'fail', value: r2(overheadShare), threshold: gate.min_python_overhead_share_pct, rule: 'relay CPU outside libopus and syscalls (%) caps what a rewrite can remove' },
  ];
  const common = { ...base, checks, libopus_ms_per_call_minute: r2(libopusMs), relay_cpu_pct_of_core: r2(refPct), baseline_host_class: baseline.host_class ?? null, libopus_version: baseline.libopus_version, authoritative: !!baseline.authoritative };
  if (embeddedA !== undefined && embeddedA !== ruleA) return { ...common, verdict: 'invalid', reason: 'baseline stop_rule.A disagrees with its own runs' };
  if (ruleA || ruleB) return { ...common, verdict: 'stop', reason: ruleA ? 'stop rule A fired: libopus and syscalls dominate the relay' : 'stop rule B fired: the relay is not a material share of the container' };
  if (ruleB == null) return { ...common, verdict: 'inconclusive', reason: 'stop rule B needs container CPU from a real live call (bench_relay.py --container-cpu-pct)' };
  if (overheadShare < gate.min_python_overhead_share_pct) return { ...common, verdict: 'stop', reason: 'the Python-owned overhead is below the ceiling: even deleting all of it cannot reach the gate' };
  return { ...common, verdict: 'prototype', reason: 'the stop rule did not fire and the overhead ceiling permits a candidate; this authorises writing one, never adopting it' };
}

// ---------------------------------------------------------------- stage 1: paired A/B

// A claim is judged on the CI, never on the point estimate: pass = proven, fail = proven short, inconclusive = the CI straddles the limit.
// strict: the bound must EXCLUDE the threshold (an improvement that is merely not worse than 0 is not an improvement).
const win = (id, st, min, strict = false) => {
  const ok = (x) => (strict ? x > min : x >= min);
  const status = st.ci_lower == null ? 'unevaluated' : ok(st.ci_lower) ? 'pass' : !ok(st.ci_upper) ? 'fail' : 'inconclusive';
  return { id, status, value: st.mean, ci_lower: st.ci_lower, ci_upper: st.ci_upper, n: st.n, threshold: min, rule: `CI lower bound of the paired reduction (%) must ${strict ? 'exceed' : 'reach'} the threshold` };
};
const noRegression = (id, st, max) => {
  const status = st.ci_upper == null ? 'unevaluated' : st.ci_upper <= max ? 'pass' : st.ci_lower > max ? 'fail' : 'inconclusive';
  return { id, status, value: st.mean, ci_lower: st.ci_lower, ci_upper: st.ci_upper, n: st.n, threshold: max, rule: 'CI upper bound of the paired regression (%) must stay within the threshold: a regression has to be ruled out' };
};

export function evaluateAb(gate, ab, stage0, { rollbackDrill = null, allowNonAuthoritative = false } = {}) {
  const base = { stage: 'ab', checks: [] };
  const invalid = (reason) => ({ ...base, verdict: 'invalid', reason });
  if (ab?.schema !== AB_SCHEMA || !Array.isArray(ab.pairs)) return invalid(`ab must be a ${AB_SCHEMA} report`);
  if (ab.self_test) return invalid('self-test report: both sides are the same relay with a fake codec');
  if (gate.require_authoritative && !ab.authoritative && !allowNonAuthoritative) return invalid(`A/B run is NON-AUTHORITATIVE: ${(ab.non_authoritative_reasons ?? []).join('; ') || 'not a lab host'}`);
  if (ab.corpus?.seed !== gate.corpus.seed) return invalid(`corpus seed ${ab.corpus?.seed} is not the pre-registered ${gate.corpus.seed}`);
  if (fakeCodec(ab.libopus_version) || ab.codec !== 'libopus') return invalid('A/B needs a real libopus');
  if (ab.libopus_version !== stage0.libopus_version) return invalid(`libopus differs between the baseline profile (${stage0.libopus_version}) and the A/B (${ab.libopus_version})`);
  const cov = ab.compare?.coverage;
  if (!cov || !(cov.fifo_bytes > 0) || !(cov.speaker_frames > 0)) return invalid('the byte compare carried no FIFO or speaker output, so it proves nothing');

  const checks = [];
  const add = (c) => checks.push(c);
  add({ id: 'byte_compare', status: ab.compare.mismatches <= gate.max_byte_mismatches ? 'pass' : 'fail', value: ab.compare.mismatches, threshold: gate.max_byte_mismatches, rule: 'scenarios whose control replies, speaker packets, FIFO bytes or close behaviour differ' });
  add({ id: 'golden_relay_token_vectors', status: ab.golden?.relay_token_vectors?.passed ? 'pass' : 'fail', value: ab.golden?.relay_token_vectors?.checked ?? null, rule: 'shared v2 HMAC vectors reproduce' });
  const og = ab.golden?.opus_golden;
  add({ id: 'golden_opus', status: og?.recorded && og.matches_run_version ? 'pass' : 'unevaluated', rule: 'the recorded libopus golden (test_opus_real.py) exists for this libopus version', detail: og ? `recorded=${og.recorded} version=${og.libopus_version}` : 'missing' });

  const pairs = ab.pairs;
  add({ id: 'pairs', status: pairs.length >= gate.min_pairs ? 'pass' : 'inconclusive', value: pairs.length, threshold: gate.min_pairs, rule: 'interleaved A/B pairs measured' });
  const L = stage0.libopus_ms_per_call_minute ?? 0;
  const st = (fn) => pairedStats(pairs.map(fn));
  const net = (x) => (x == null ? null : x - L); // CPU minus libopus self-time (the same libopus .so runs in A and B)
  const ov = st((p) => reductionPct(net(p.a.cpu_ms_per_call_minute), net(p.b.cpu_ms_per_call_minute)));
  const wins = [
    win('cpu_overhead_reduction', ov, gate.min_cpu_overhead_reduction_pct),
    win('cpu_reduction', st((p) => reductionPct(p.a.cpu_ms_per_call_minute, p.b.cpu_ms_per_call_minute)), gate.min_cpu_reduction_pct),
    win('rss_reduction', st((p) => reductionPct(p.a.rss_peak_kib, p.b.rss_peak_kib)), gate.min_rss_reduction_pct),
  ];
  wins.forEach((w) => add({ ...w, any_of: 'win' })); // ANY ONE of the three is a win
  const winStatus = wins.some((w) => w.status === 'pass') ? 'pass' : wins.some((w) => w.status === 'inconclusive') ? 'inconclusive' : wins.every((w) => w.status === 'unevaluated') ? 'unevaluated' : 'fail';
  add({ id: 'win', status: winStatus, rule: 'at least one of cpu_overhead_reduction, cpu_reduction, rss_reduction passes on its CI lower bound' });

  for (const [dir, key] of [['mic', 'mic'], ['speaker', 'speaker']]) {
    for (const [q, max] of [['p95', gate.max_p95_regression_pct], ['p99', gate.max_p99_regression_pct]]) {
      const reg = st((p) => { const x = reductionPct(p.a[key]?.[`${q}_ms`], p.b[key]?.[`${q}_ms`]); return x == null ? null : -x; });
      add(noRegression(`${dir}_${q}_regression`, reg, max));
    }
  }
  const dropsA = pairs.reduce((s, p) => s + (p.a.mic_drops ?? 0) + (p.a.speaker_drops ?? 0), 0);
  const dropsB = pairs.reduce((s, p) => s + (p.b.mic_drops ?? 0) + (p.b.speaker_drops ?? 0), 0);
  add({ id: 'extra_drops', status: dropsB - dropsA <= gate.max_extra_drops ? 'pass' : 'fail', value: dropsB - dropsA, threshold: gate.max_extra_drops, rule: 'frames the candidate dropped beyond the baseline' });

  const lk = ab.leaks?.b;
  add({ id: 'leaks', status: !lk?.supported ? 'unevaluated' : lk.leaks <= gate.max_leaks ? 'pass' : 'fail', value: lk?.supported ? lk.leaks : null, threshold: gate.max_leaks, rule: 'fd, thread, child-process or RSS growth over the reconnect check' });

  const share = stage0.checks.find((c) => c.id === 'stop_rule_B');
  const mixedRows = [
    ['mixed_container_cpu', st((p) => reductionPct(p.a.mixed?.container_cpu_pct, p.b.mixed?.container_cpu_pct))],
    ['mixed_job_p95', st((p) => reductionPct(p.a.mixed?.job_p95_ms, p.b.mixed?.job_p95_ms))],
    ['mixed_host_headroom', st((p) => { const x = reductionPct(p.a.mixed?.host_headroom_pct, p.b.mixed?.host_headroom_pct); return x == null ? null : -x; })],
  ].map(([id, s]) => win(id, s, gate.min_mixed_improvement_pct, true));
  mixedRows.forEach((w) => add({ ...w, any_of: 'material' }));
  const matStatus = mixedRows.some((w) => w.status === 'pass') ? 'pass' : mixedRows.some((w) => w.status === 'inconclusive') ? 'inconclusive' : mixedRows.every((w) => w.status === 'unevaluated') ? 'unevaluated' : 'fail';
  add({ id: 'material_in_mixed', status: share?.status === 'pass' ? matStatus : 'unevaluated', rule: `relay >= ${gate.min_relay_share_of_container_pct}% of container CPU AND the mixed run improves container CPU, host headroom or job p95 (CI lower bound above ${gate.min_mixed_improvement_pct}%)` });

  add({ id: 'rollback_drill', status: rollbackDrill === 'pass' ? 'pass' : rollbackDrill === 'fail' ? 'fail' : 'unevaluated', rule: 'switching UBAG_VOICE_RELAY_IMPL back to python (or removing the candidate binary) restores the Python relay with no gateway change; recorded by a human after the P7.8 drill' });

  // `any_of` rows are components; their group row (win, material_in_mixed) carries the decision.
  const deciding = checks.filter((c) => !c.any_of);
  return { ...base, checks, pairs: pairs.length, deciding: deciding.map((c) => c.id), authoritative: !!ab.authoritative, libopus_version: ab.libopus_version, host_class: ab.host_class ?? null, corpus: ab.corpus };
}

function decide(stage) {
  const rows = (stage.checks ?? []).filter((c) => !c.any_of);
  if (rows.some((c) => c.status === 'fail')) return ['stop', `fails: ${rows.filter((c) => c.status === 'fail').map((c) => c.id).join(', ')}`];
  const open = rows.filter((c) => c.status === 'unevaluated' || c.status === 'inconclusive');
  if (open.length) return ['inconclusive', `not decided: ${open.map((c) => `${c.id} (${c.status})`).join(', ')}`];
  return ['go', 'every pre-registered check passed on CI bounds'];
}

export function evaluateGate({ gate, baseline, ab = null, rollbackDrill = null, allowNonAuthoritative = false }) {
  checkGate(gate);
  const stage0 = evaluateBaseline(gate, baseline, { allowNonAuthoritative });
  const out = {
    schema: VERDICT_SCHEMA, rule: gate._description ? 'tests/load/voice-relay-gate.json (pre-registered)' : 'gate', verdict: stage0.verdict, stage: stage0.stage, reason: stage0.reason,
    prototype_allowed: false, adoption_allowed: false, libopus_version: stage0.libopus_version ?? null, host_class: stage0.baseline_host_class ?? null,
    authoritative: !!stage0.authoritative, baseline: stage0, ab: null,
  };
  if (stage0.verdict === 'prototype') out.prototype_allowed = true;
  if (stage0.verdict !== 'prototype' || !ab) return out;
  const stage1 = evaluateAb(gate, ab, stage0, { rollbackDrill, allowNonAuthoritative });
  out.ab = stage1;
  out.stage = 'ab';
  if (stage1.verdict === 'invalid') return Object.assign(out, { verdict: 'invalid', reason: stage1.reason, prototype_allowed: true });
  const [verdict, reason] = decide(stage1);
  return Object.assign(out, { verdict, reason, prototype_allowed: true, adoption_allowed: verdict === 'go', host_class: stage1.host_class ?? out.host_class, authoritative: stage1.authoritative && stage0.authoritative });
}

export function renderVerdict(v) {
  const rows = [...v.baseline.checks, ...(v.ab?.checks ?? [])].map((c) => `| ${c.id} | ${c.status} | ${c.value ?? ''}${c.ci_lower != null ? ` [${c.ci_lower}, ${c.ci_upper}]` : ''} | ${c.threshold ?? ''} |`);
  return [`# Rust gate verdict: ${v.verdict.toUpperCase()}`, '', v.reason, '',
    `stage ${v.stage}; libopus ${v.libopus_version ?? 'unknown'}; host class ${v.host_class ?? 'unknown'}; authoritative ${v.authoritative}; prototype allowed ${v.prototype_allowed}; adoption allowed ${v.adoption_allowed}`, '',
    '| check | status | value [95% CI] | threshold |', '| --- | --- | --- | --- |', ...rows, ''].join('\n');
}

// ---------------------------------------------------------------- CLI

const HELP = `Usage: node tools/benchmark/rust-gate.mjs --baseline <relay-baseline.json> [--ab <ab.json>] [--rollback-drill pass|fail]
         [--gate tests/load/voice-relay-gate.json] [--allow-non-authoritative] [--out <verdict.json>] [--markdown <verdict.md>] [--require prototype|go]
Exit: 0 verdict computed (and --require, if given, satisfied), 1 --require not satisfied, 2 bad input.
`;
export function main(argv) {
  const opt = {};
  for (let i = 0; i < argv.length; i++) {
    const t = argv[i];
    if (t === '--help' || t === '-h') { process.stdout.write(HELP); return 0; }
    if (t === '--allow-non-authoritative') opt.allow = true;
    else if (['--baseline', '--ab', '--rollback-drill', '--gate', '--out', '--markdown', '--require'].includes(t)) opt[t.slice(2)] = argv[++i];
    else { process.stderr.write(`unknown argument ${t}\n${HELP}`); return 2; }
  }
  try {
    if (!opt.baseline) throw new Error('--baseline is required');
    if (opt['rollback-drill'] && !['pass', 'fail'].includes(opt['rollback-drill'])) throw new Error('--rollback-drill must be pass or fail');
    if (opt.require && !['prototype', 'go'].includes(opt.require)) throw new Error('--require must be prototype or go');
    const read = (p) => JSON.parse(readFileSync(p, 'utf8'));
    const gate = read(opt.gate ?? new URL('../../tests/load/voice-relay-gate.json', import.meta.url));
    const verdict = evaluateGate({ gate, baseline: read(opt.baseline), ab: opt.ab ? read(opt.ab) : null, rollbackDrill: opt['rollback-drill'] ?? null, allowNonAuthoritative: !!opt.allow });
    const text = `${JSON.stringify(verdict, null, 2)}\n`;
    if (opt.out) writeFileSync(opt.out, text); else process.stdout.write(text);
    if (opt.markdown) writeFileSync(opt.markdown, renderVerdict(verdict));
    process.stderr.write(`[rust-gate] ${verdict.verdict}: ${verdict.reason}\n`);
    if (opt.require === 'go' && verdict.verdict !== 'go') return 1;
    if (opt.require === 'prototype' && !verdict.prototype_allowed) return 1;
    return 0;
  } catch (e) {
    process.stderr.write(`[rust-gate] ${e.message}\n`);
    return 2;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) process.exit(main(process.argv.slice(2)));
