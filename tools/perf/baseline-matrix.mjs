#!/usr/bin/env node
/**
 * perf-fleet P0.13: baseline matrix runner + memory-headroom budget (zero dependencies).
 *
 *   run     node tools/perf/baseline-matrix.mjs run --i-understand-this-is-load [--containers gateway=a,browser=b,worker=c]
 *                [--isolated-lab-host] [--steps 1,2,5,10,20] [--dry-run] [--out-dir DIR]
 *   budget  node tools/perf/baseline-matrix.mjs budget --baseline <baseline.json> --role <role> --slot-cost-mib N
 *                [--current-slots 1] [--headroom-pct 20] [--vps-snapshot file.json]
 *
 * `run` drives tests/load/acceptance.mjs (mock target, throwaway local stack) through: the concurrency ladder
 * (clients-100 with --clients N), clients-100, queue-1000, events-latency and a short steady-state, then writes
 * baseline.json + baseline.md. Needs UBAG_LOAD_BASE_URL / UBAG_LOAD_API_KEY like the harness.
 *
 * Guards: loopback/private target only (UBAG_LOAD_ALLOWED_HOSTS is stripped from the child env, so the harness host
 * guard cannot be widened through this tool). Anything not run with --isolated-lab-host on Linux is labelled
 * NON-AUTHORITATIVE (D5). Never aim this at the shared VPS.
 *
 * `budget` turns a baseline's cgroup peak + a measured per-slot cost (tests/../measure_slots.py or
 * tools/perf/helper-footprint.sh) + an optional operator-transcribed VPS snapshot into the pool-size cap P3.6 needs.
 * Missing inputs yield null (fail closed), never a guess.
 */
import { spawnSync } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { ACK_FLAG, isLoopbackOrPrivateHost } from '../../tests/load/acceptance.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const harness = join(root, 'tests/load/acceptance.mjs');
const DEFAULT_STEPS = [1, 2, 5, 10, 20]; // ladder_steps of tests/load/workloads/mixed.json
const r2 = (x) => (x == null || !Number.isFinite(x) ? null : Math.round(x * 100) / 100);

/** Ordered run plan: [{name, args}]. `jobs` sizes queue-1000 (lower it for smoke runs). */
export function buildPlan({ steps = DEFAULT_STEPS, jobs = 1000, steadySeconds = 30 } = {}) {
  return [
    ...steps.map((n) => ({ name: `ladder-${n}`, args: ['--scenario', 'clients-100', '--clients', String(n), '--iterations', '3'] })),
    { name: 'clients-100', args: ['--scenario', 'clients-100', '--clients', '100'] },
    { name: 'queue-1000', args: ['--scenario', 'queue-1000', '--jobs', String(jobs)] },
    { name: 'events-latency', args: ['--scenario', 'events-latency'] },
    { name: 'steady-state', args: ['--scenario', 'steady-state', '--steady-seconds', String(steadySeconds)] },
  ];
}

const okOf = (report, suffix) => Object.entries(report.ops ?? {}).filter(([k]) => k.endsWith(suffix)).map(([, v]) => v.ok?.p95).find((v) => v != null) ?? null;

/** Flatten one harness report.json into a baseline row. */
export function stepRow(name, report) {
  const s = report.summary ?? {};
  const roles = Object.fromEntries(Object.entries(report.resources?.containers ?? {}).map(([role, c]) => [role, {
    memory_peak_mb: c.memory_peak_mb ?? null, memory_limit_mb: c.memory_limit_mb ?? null, memory_headroom_pct: c.memory_headroom_pct ?? null,
    cpu_throttled_pct: c.cpu_throttled_pct ?? null, oom_kills: c.oom_kills ?? null,
  }]));
  return {
    step: name, passed: report.thresholds?.passed ?? null, create_ok_p95_ms: okOf(report, '/create'), completion_ok_p95_ms: okOf(report, '/completion'),
    steady_create_p95_ms: s.steady_create_p95_ms ?? null, steady_read_p95_ms: s.steady_read_p95_ms ?? null, event_latency_p95_ms: s.event_latency_p95_ms ?? null,
    failed_jobs: s.failed_jobs ?? null, lost_jobs: s.lost_jobs ?? null, containers: roles, host_pressure: report.resources?.host_pressure ?? null,
    gateway_commit: report.meta?.provenance?.gateway?.commit ?? null, harness_sha: report.meta?.provenance?.harness?.sha ?? null,
  };
}

/**
 * Memory budget: keep `headroomPct` of the container limit (and of host RAM, when a snapshot is given) free.
 * extra_slots = floor((limit*(1-h) - peak) / slotCost); the host term uses MemAvailable - h*MemTotal.
 * pool_cap = currentSlots + min(extra_slots over the known terms). Any unknown input => null with a reason.
 */
export function headroomBudget({ limitMib, peakMib, slotCostMib, currentSlots = 1, headroomPct = 20, hostTotalMib, hostAvailableMib }) {
  const h = headroomPct / 100;
  const bad = (v) => v == null || !Number.isFinite(v);
  if (bad(limitMib) || bad(peakMib) || bad(slotCostMib) || slotCostMib <= 0) {
    return { pool_cap: null, reason: 'need container memory limit, measured peak and a positive per-slot cost (unlimited or unsampled container => unknown)' };
  }
  const cgroupFree = limitMib * (1 - h) - peakMib;
  const terms = { cgroup: Math.max(0, Math.floor(cgroupFree / slotCostMib)) };
  if (!bad(hostTotalMib) && !bad(hostAvailableMib)) terms.host = Math.max(0, Math.floor((hostAvailableMib - hostTotalMib * h) / slotCostMib));
  const extra = Math.min(...Object.values(terms));
  return {
    pool_cap: currentSlots + extra, extra_slots: extra, limiting_term: Object.entries(terms).sort((a, b) => a[1] - b[1])[0][0], terms,
    cgroup_free_mib: r2(cgroupFree), host_term_used: 'host' in terms, headroom_pct: headroomPct,
    note: 'peak was measured with currentSlots slots; the cap is only as good as slotCostMib (stub slots under-count a real Chrome-backed slot)',
  };
}

export function classify({ platform = process.platform, isolatedLabHost = false } = {}) {
  return platform === 'linux' && isolatedLabHost ? 'lab-host (operator asserts isolated Linux stack)' : 'NON-AUTHORITATIVE (not an isolated Linux lab host)';
}

function parse(argv) {
  const o = { _: [] };
  for (let i = 0; i < argv.length; i += 1) {
    const t = argv[i];
    if (!t.startsWith('--')) { o._.push(t); continue; }
    const [k, v] = t.slice(2).split(/=(.*)/s);
    o[k] = v !== undefined ? v : (argv[i + 1] === undefined || argv[i + 1].startsWith('--')) ? true : argv[++i];
  }
  return o;
}

export function runMatrix(opts, env = process.env, exec = spawnSync) {
  if (!opts[ACK_FLAG]) throw new Error(`pass --${ACK_FLAG}; this generates load (docs/load-testing.md)`);
  const url = new URL(env.UBAG_LOAD_BASE_URL ?? '');
  if (!isLoopbackOrPrivateHost(url.hostname)) throw new Error('refusing: baselines run only against a loopback/private throwaway stack, never the shared VPS');
  const steps = String(opts.steps ?? DEFAULT_STEPS.join(',')).split(',').map(Number);
  if (steps.some((n) => !Number.isInteger(n) || n < 1 || n > 1000)) throw new Error('--steps must be integers in [1,1000]');
  const plan = buildPlan({ steps, jobs: Number(opts.jobs ?? 1000), steadySeconds: Number(opts['steady-seconds'] ?? 30) });
  const outDir = resolve(opts['out-dir'] ?? join(root, 'tests/load/results', `baseline-${new Date().toISOString().replace(/[:.]/g, '-')}`));
  const childEnv = { ...env };
  delete childEnv.UBAG_LOAD_ALLOWED_HOSTS; // host guard stays on
  const rows = [];
  for (const step of plan) {
    const args = [harness, `--${ACK_FLAG}`, ...step.args, '--target', 'mock', '--out-dir', join(outDir, step.name)];
    if (opts.containers) args.push('--cgroup-containers', String(opts.containers));
    if (opts['dry-run']) { rows.push({ step: step.name, dry_run: ['node', ...args.map((a) => (a === harness ? 'tests/load/acceptance.mjs' : a))].join(' ') }); continue; }
    const r = exec(process.execPath, args, { env: childEnv, encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] });
    // The harness prints "[load] report written to <dir>"; read report.json from there. Exit 1 = threshold violated (still recorded).
    const dir = /\[load\] report written to (.+)/.exec(r.stdout ?? '')?.[1]?.trim();
    if (r.status === 2 || !dir) { rows.push({ step: step.name, error: `harness refused or failed (exit ${r.status})` }); continue; }
    rows.push(stepRow(step.name, JSON.parse(readFileSync(join(dir, 'report.json'), 'utf8'))));
  }
  const baseline = { generated_at: new Date().toISOString(), classification: classify({ isolatedLabHost: !!opts['isolated-lab-host'] }), harness_host: { platform: process.platform, node: process.version }, mock_bypasses_warm_daemon: true, rows };
  if (!opts['dry-run']) {
    mkdirSync(outDir, { recursive: true });
    writeFileSync(join(outDir, 'baseline.json'), `${JSON.stringify(baseline, null, 2)}\n`);
    writeFileSync(join(outDir, 'baseline.md'), `${renderMarkdown(baseline)}\n`);
  }
  return { baseline, outDir };
}

export function renderMarkdown(b) {
  const lines = [`# UBAG baseline matrix (${b.classification})`, '', `- generated: ${b.generated_at}`, '- target: mock adapter (bypasses the warm daemon and any browser/provider cost)', ''];
  lines.push('| step | pass | create ok p95 ms | completion p95 ms | failed | lost | min headroom % | max throttle % | OOM |', '| --- | --- | --- | --- | --- | --- | --- | --- | --- |');
  for (const r of b.rows) {
    if (!r.containers) { lines.push(`| ${r.step} | ${r.error ?? r.dry_run ?? '-'} | | | | | | | |`); continue; }
    const cs = Object.values(r.containers);
    const agg = (f, fn) => { const v = cs.map(f).filter((x) => x != null); return v.length ? r2(fn(...v)) : '-'; };
    lines.push(`| ${r.step} | ${r.passed} | ${r.create_ok_p95_ms ?? '-'} | ${r.completion_ok_p95_ms ?? '-'} | ${r.failed_jobs ?? '-'} | ${r.lost_jobs ?? '-'} | ${agg((c) => c.memory_headroom_pct, Math.min)} | ${agg((c) => c.cpu_throttled_pct, Math.max)} | ${agg((c) => c.oom_kills, (...x) => x.reduce((a, c) => a + c, 0))} |`);
  }
  return lines.join('\n');
}

/** Budget from a baseline.json: peak = max memory_peak_mb of `role` across all steps. */
export function budgetFromBaseline(baseline, { role, slotCostMib, currentSlots, headroomPct, snapshot }) {
  const cs = baseline.rows.map((r) => r.containers?.[role]).filter(Boolean);
  const peaks = cs.map((c) => c.memory_peak_mb).filter((x) => x != null);
  const limits = cs.map((c) => c.memory_limit_mb).filter((x) => x != null);
  return headroomBudget({
    limitMib: limits.length ? Math.max(...limits) : null, peakMib: peaks.length ? Math.max(...peaks) : null, slotCostMib, currentSlots, headroomPct,
    hostTotalMib: snapshot?.mem_total_mib, hostAvailableMib: snapshot?.mem_available_mib,
  });
}

function main(argv) {
  const [cmd, ...rest] = argv; const o = parse(rest);
  try {
    if (cmd === 'run') {
      const { baseline, outDir } = runMatrix(o);
      console.log(renderMarkdown(baseline));
      if (!o['dry-run']) console.log(`[baseline] written to ${outDir} (git-ignored)`);
      return baseline.rows.some((r) => r.error) ? 1 : 0;
    }
    if (cmd === 'budget') {
      const baseline = JSON.parse(readFileSync(String(o.baseline), 'utf8'));
      const snapshot = o['vps-snapshot'] ? JSON.parse(readFileSync(String(o['vps-snapshot']), 'utf8')) : undefined;
      const out = budgetFromBaseline(baseline, { role: String(o.role), slotCostMib: Number(o['slot-cost-mib']), currentSlots: Number(o['current-slots'] ?? 1), headroomPct: Number(o['headroom-pct'] ?? 20), snapshot });
      console.log(JSON.stringify({ classification: baseline.classification, ...out }, null, 2));
      return out.pool_cap == null ? 1 : 0;
    }
    console.error('usage: baseline-matrix.mjs run|budget ... (see file header)');
    return 2;
  } catch (e) { console.error(`[baseline] refused: ${e.message}`); return 2; }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) process.exit(main(process.argv.slice(2)));
