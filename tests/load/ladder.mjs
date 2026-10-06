#!/usr/bin/env node
/**
 * UBAG workload ladder and capacity report (perf-fleet P7.2).
 * Zero dependencies; reuses the acceptance harness (tests/load/acceptance.mjs) for HTTP, retries, polling and verification.
 * DANGEROUS BY DESIGN: it generates real load. Read docs/load-testing.md ("Workload ladder") first.
 *
 *   UBAG_LOAD_BASE_URL=http://127.0.0.1:8080 UBAG_LOAD_API_KEY=... UBAG_LOAD_API_KEY_B=... \
 *     node tests/load/ladder.mjs --i-understand-this-is-load --workload mixed \
 *       --cgroup-containers gateway=<c>,browser=<c>,worker=<c> --host-class 2c4g --isolated-lab-host
 *
 * Modes
 *   ladder  (default) For each step N of the workload manifest (1/2/5/10/20) run N closed-loop virtual clients for --step-seconds:
 *           create (text | attachment | audio upload, weighted by the manifest mix) -> poll to terminal -> verify the result, the
 *           event log and the tenant boundary -> think -> repeat. Every step is judged against the goals; the report names the
 *           highest N that meets every goal (a contiguous prefix of steps, so a failing lower step caps it).
 *   paused  The worker consumer must be paused (UBAG_WORKER_CONSUMER_ENABLED=false): --clients (100) clients enqueue --jobs (1000)
 *           jobs, reads are timed against the deep queue, nothing may leave the queue; with --resume-wait-seconds the harness
 *           then waits for you to resume the consumer and drains and verifies every acked job.
 *
 * Live voice media cannot be driven by this harness (it needs a relay secret and a real browser, D7): voice frame age per step comes
 * from the in-process relay bench report passed with --voice-latency, and is labelled as such.
 * Numbers are NON-AUTHORITATIVE unless the run is on an isolated Linux lab host whose summed container limits match --host-class.
 */
import { randomBytes } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { performance } from 'node:perf_hooks';
import { fileURLToPath, pathToFileURL } from 'node:url';
import {
  ACK_FLAG, Recorder, callWithRetry, checkTarget, createJob, evaluateThresholds, histogramQuantile, idemKey, jobPayload, makeRequest, parseProm,
  pool, settleJobs, summarize, voiceLatencySummary,
} from './acceptance.mjs';
import { PRESSURE_RULES, parseTargets, startCgroupSampler } from './lib/cgroup.mjs';
import { containerLimits, gatewayInfoFrom, harnessGit, harnessHost, pickEnv, stackEnv } from './lib/provenance.mjs';
import { loadWorkload, multipartBody, sha256Hex, syntheticWav, wavSize, workloadSha256 } from './workloads.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const r2 = (x) => (x == null || !Number.isFinite(x) ? null : Math.round(x * 100) / 100);
const r3 = (x) => (x == null || !Number.isFinite(x) ? null : Math.round(x * 1000) / 1000);
const now = () => performance.now();
const range = (n) => Array.from({ length: n }, (_, i) => i);
const defaultSleep = (ms) => new Promise((res) => setTimeout(res, Math.max(0, ms)));

export const MODES = ['ladder', 'paused'];
// Targets that never touch a real provider. Anything else is a live web UI and needs --allow-live-provider.
const LOCAL_TARGETS = new Set(['mock', 'synthetic_chat']);
// Helper-node ceilings from the plan's table (2c/4G -> 1.5 CPU / 2.5 GiB, 4c/8G -> 3 CPU / 5 GiB), matched against the SUM of the sampled container limits.
export const HOST_CLASSES = { '2c4g': { cpu_cores: 1.5, memory_mb: 2560 }, '4c8g': { cpu_cores: 3, memory_mb: 5120 } };
// Counters a clean ladder must keep at zero. Their limits are read from thresholds.json (a rename there throws instead of dropping a gate).
const INTEGRITY_KEYS = [
  'max_server_errors_non_overload', 'max_overload_missing_retry_after', 'max_overload_missing_retry_after_ms_body', 'max_hangs', 'max_network_errors',
  'max_lost_jobs', 'max_unfinished_jobs', 'max_unaccepted_jobs', 'max_failed_jobs', 'max_queue_id_collisions', 'max_warning_jobs', 'max_result_mismatches',
  'max_terminal_event_violations', 'max_cross_tenant_leaks', 'max_tenant_probe_unexpected', 'max_scenario_errors',
];
const ECHO_VERIFIED = new Set(['mock', 'synthetic_chat']); // targets whose result text verifyJob can compare against the prompt
const TEXT_MIME = 'text/plain';
const PRE_RUN = new Set(['created', 'queued']);

// ----------------------------------------------------------------- arguments

const NUM = { // flag: [cfgKey, default, min, max]
  'step-seconds': ['stepSeconds', 60, 1, 3600], 'cooldown-seconds': ['cooldownSeconds', 5, 0, 600], 'min-samples': ['minSamples', 20, 1, 100_000],
  'poll-interval-ms': ['pollIntervalMs', 500, 5, 60_000], 'poll-concurrency': ['pollConcurrency', 25, 1, 500], 'deadline-ms': ['deadlineMs', 600_000, 100, 7_200_000],
  'request-timeout-ms': ['requestTimeoutMs', 30_000, 50, 600_000], 'upload-timeout-ms': ['uploadTimeoutMs', 90_000, 50, 600_000],
  'max-retries': ['maxRetries', 8, 0, 100], 'retry-cap-ms': ['retryCapMs', 60_000, 1, 600_000], 'think-scale': ['thinkScale', 100, 0, 1000],
  seed: ['seed', 1, 0, 2_147_483_647], 'tenant-probe-samples': ['tenantProbeSamples', 10, 0, 1000], 'truncation-probes': ['truncationProbes', 0, 0, 50],
  'docker-interval-ms': ['dockerIntervalMs', 5000, 10, 600_000], 'pressure-window-ms': ['pressureWindowMs', PRESSURE_RULES.recoveryWindowMs, 10, 3_600_000],
  clients: ['clients', 100, 1, 1000], jobs: ['jobs', 1000, 1, 100_000], 'hold-seconds': ['holdSeconds', 10, 0, 3600], 'read-rate': ['readRate', 20, 1, 1000],
  'resume-wait-seconds': ['resumeWaitSeconds', 0, 0, 7200],
};
const STR = {
  mode: 'mode', workload: 'workload', target: 'target', steps: 'steps', 'cgroup-containers': 'cgroupContainers', 'host-class': 'hostClass', 'voice-latency': 'voiceLatency',
  thresholds: 'thresholds', goals: 'goals', 'ladder-thresholds': 'ladderThresholds', 'out-dir': 'outDir',
};
const FLAGS = new Set([ACK_FLAG, 'require-goals', 'continue-after-fail', 'isolated-lab-host', 'allow-live-provider']);

export function parseArgs(argv, env = process.env) {
  const cfg = {
    mode: 'ladder', workload: 'mixed', jitter: 0.1, requireGoals: false, continueAfterFail: false, isolatedLabHost: false, allowLiveProvider: false,
    thresholds: join(here, 'thresholds.json'), goals: join(here, 'thresholds.goals.json'), ladderThresholds: join(here, 'thresholds.ladder.json'), outDir: join(here, 'results'),
  };
  for (const [, [key, def]] of Object.entries(NUM)) cfg[key] = def;
  let acknowledged = false;
  for (let i = 0; i < argv.length; i += 1) {
    const token = argv[i];
    if (!token.startsWith('--')) throw new Error(`unexpected argument "${token}"`);
    const eq = token.indexOf('=');
    const name = token.slice(2, eq === -1 ? undefined : eq);
    let value = eq === -1 ? undefined : token.slice(eq + 1);
    if (FLAGS.has(name)) {
      if (name === ACK_FLAG) acknowledged = true;
      else cfg[name.replace(/-(\w)/g, (_, c) => c.toUpperCase())] = true;
      continue;
    }
    if (!(name in NUM) && !(name in STR)) throw new Error(`unknown option --${name}`);
    if (value === undefined) {
      value = argv[++i];
      if (value === undefined || value.startsWith('--')) throw new Error(`--${name} requires a value`);
    }
    if (name in STR) cfg[STR[name]] = value;
    else {
      const [key, , min, max] = NUM[name];
      if (!/^\d+$/.test(value) || Number(value) < min || Number(value) > max) throw new Error(`--${name} must be an integer in [${min}, ${max}]`);
      cfg[key] = Number(value);
    }
  }
  // Order matters: the acknowledgement is checked before anything touches the target.
  if (!acknowledged) throw new Error(`this tool generates load against a live gateway; pass --${ACK_FLAG} to proceed (see docs/load-testing.md)`);
  if (!MODES.includes(cfg.mode)) throw new Error(`--mode must be one of ${MODES.join(' | ')}`);
  if (!env.UBAG_LOAD_BASE_URL) throw new Error('UBAG_LOAD_BASE_URL is required');
  if (!env.UBAG_LOAD_API_KEY) throw new Error('UBAG_LOAD_API_KEY is required');
  cfg.baseUrl = checkTarget(env.UBAG_LOAD_BASE_URL, env); // host guard stays on: loopback/private or an explicit UBAG_LOAD_ALLOWED_HOSTS entry
  cfg.apiKey = env.UBAG_LOAD_API_KEY;
  cfg.apiKeyB = env.UBAG_LOAD_API_KEY_B || undefined;
  if (cfg.apiKeyB && cfg.apiKeyB === cfg.apiKey) throw new Error('UBAG_LOAD_API_KEY_B must differ from UBAG_LOAD_API_KEY (it must belong to a second tenant)');
  try { cfg.cgroupTargets = parseTargets(cfg.cgroupContainers ?? ''); } catch (e) { throw new Error(`--cgroup-containers: ${e.message}`); }
  if (cfg.hostClass && !HOST_CLASSES[cfg.hostClass]) throw new Error(`--host-class must be one of ${Object.keys(HOST_CLASSES).join(' | ')}`);

  const wl = loadWorkload(cfg.workload);
  if (wl.provider_scenario.requires_live_media || wl.mix.some((m) => m.kind === 'voice_session')) {
    throw new Error(`workload "${cfg.workload}" needs live voice media, which this harness cannot drive (D7: media terminates on the helper). Run the in-process relay bench and pass its report with --voice-latency instead`);
  }
  cfg.target ??= wl.provider_scenario.target; cfg.commandType = wl.provider_scenario.command_type;
  if (!/^[a-z][a-z0-9_]{0,39}$/.test(cfg.target)) throw new Error('--target has an invalid name');
  if (!LOCAL_TARGETS.has(cfg.target) && !cfg.allowLiveProvider) {
    throw new Error(`target "${cfg.target}" is a live provider (safe-mode: user-owned sessions only, never at ladder load by default); pass --allow-live-provider only for a throwaway operator-owned session, or use ${[...LOCAL_TARGETS].join(' / ')}`);
  }
  if (cfg.truncationProbes > 0 && cfg.target !== 'synthetic_chat') throw new Error('--truncation-probes needs --target synthetic_chat (it sends the fixture\'s truncated scenario)');
  cfg.steps = cfg.steps !== undefined ? cfg.steps.split(',').map((s) => (/^\d+$/.test(s.trim()) ? Number(s) : NaN)) : [...wl.ladder_steps];
  if (!cfg.steps.length || cfg.steps.some((n, i) => !Number.isInteger(n) || n < 1 || n > 200 || (i > 0 && n <= cfg.steps[i - 1]))) throw new Error('--steps must be strictly ascending integers in [1, 200]');
  if (cfg.voiceLatency) voiceLatencySummary(JSON.parse(readFileSync(cfg.voiceLatency, 'utf8'))); // validate now: a bad report must not surface after an hour of load
  cfg.workloadInfo = { name: cfg.workload, sha256: workloadSha256(cfg.workload), manifest: wl };
  return cfg;
}

// ------------------------------------------------------------ workload mix

/** Small deterministic PRNG (mulberry32) seeded from (seed, ...salt): the same seed replays the same mix per client. */
export function makeRng(seed, ...salt) {
  let a = Number.parseInt(sha256Hex(Buffer.from([seed, ...salt].join(':'))).slice(0, 8), 16);
  return () => { a = (a + 0x6D2B79F5) | 0; let t = Math.imul(a ^ (a >>> 15), 1 | a); t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t; return ((t ^ (t >>> 14)) >>> 0) / 4294967296; };
}
/** Weighted pick over manifest `mix` entries (weights are validated to sum to 100). */
export function pickClass(mix, rand) {
  let x = rand() * mix.reduce((n, m) => n + m.weight, 0);
  for (const m of mix) { x -= m.weight; if (x < 0) return m; }
  return mix[mix.length - 1];
}
export const between = (rand, { min, max }) => min + Math.floor(rand() * (max - min + 1));
const padPrompt = (head, size) => (size <= head.length + 1 ? head : `${head} ${'x'.repeat(size - head.length - 1)}`);

// ---------------------------------------------------------- job generators

/** Native multipart create with declared attachments, then artifact size/sha256 verification. Same result shape as createJob(). */
async function multipartJob(ctx, key, { prompt, files, op }) {
  const { cfg, rec } = ctx; const fullPrompt = `${prompt} [${key}]`; const t0 = now();
  const envelope = jobPayload(cfg, key, fullPrompt);
  envelope.job.input.attachments = files.map((f) => ({ key: f.key, filename: f.key, content_type: f.contentType, kind: f.kind }));
  const { body, contentType } = multipartBody([
    { name: 'job', data: JSON.stringify(envelope), contentType: 'application/json' },
    ...files.map((f) => ({ name: f.key, filename: f.key, data: f.data, contentType: f.contentType })),
  ]);
  const { res } = await callWithRetry(ctx, op, () => ctx.request('POST', '/v1/jobs', { body, headers: { 'Content-Type': contentType, 'Idempotency-Key': key }, timeoutMs: cfg.uploadTimeoutMs }));
  if (!((res.status === 200 || res.status === 202) && typeof res.json?.job_id === 'string')) {
    if (res.status >= 400 && res.status < 500 && res.status !== 429) rec.violation('attachment_violations', `${op} rejected: HTTP ${res.status} ${res.json?.error?.code ?? ''} (target "${cfg.target}" must accept these attachments)`);
    return { ok: false, status: res.status };
  }
  const jobId = res.json.job_id; ctx.jobIds.add(jobId); rec.latency(`${op}_incl_retries`, now() - t0);
  const arts = await ctx.request('GET', `/v1/jobs/${encodeURIComponent(jobId)}/artifacts`); rec.observe('artifacts_fetch', arts);
  for (const f of files) {
    const stored = Array.isArray(arts.json?.data) ? arts.json.data.find((a) => a?.key === f.key) : undefined;
    if (!stored) rec.violation('attachment_violations', `${jobId}: artifact "${f.key}" is not listed`);
    else if (stored.size_bytes !== f.data.length) rec.violation('attachment_violations', `${jobId}: stored ${stored.size_bytes} bytes, uploaded ${f.data.length}`);
    else if (stored.checksum && stored.checksum !== (f.sha ??= sha256Hex(f.data))) rec.violation('attachment_violations', `${jobId}: stored checksum differs from the upload`);
    else rec.count('attachments_verified');
  }
  return { ok: true, status: res.status, jobId, key, prompt: fullPrompt, acceptedAt: now(), res };
}

/** One job of manifest class `cls` for virtual client c, iteration j of step n. */
async function issue(ctx, cls, n, c, j, rand) {
  const { cfg } = ctx; const tag = `l${n}c${c}`; const key = idemKey(ctx, tag, j); const size = between(rand, cls.payload_bytes);
  if (cls.kind === 'text') return createJob(ctx, key, { prompt: padPrompt(`ladder ${tag} job ${j}`, size), op: 'create' });
  if (cls.kind === 'attachment' && cfg.target === 'mock') { // mock declares no attachments policy: the manifest's "--with-upload path" (artifact PUT after create)
    const job = await createJob(ctx, key, { prompt: padPrompt(`ladder ${tag} job ${j}`, 256), op: 'create_attachment' });
    if (job.ok) ctx.rec.observe('upload', await ctx.request('PUT', `/v1/jobs/${job.jobId}/artifacts/ladder-${tag}-${j}.txt`, { body: Buffer.alloc(size, 'ladder-attachment\n'), headers: { 'Content-Type': TEXT_MIME } }));
    return job;
  }
  if (cls.kind === 'attachment') {
    return multipartJob(ctx, key, { op: 'create_attachment', prompt: padPrompt(`ladder ${tag} job ${j}`, 256), files: [{ key: `${tag}-${j}.txt`, kind: 'document', contentType: TEXT_MIME, data: Buffer.alloc(size, 'ladder-attachment\n') }] });
  }
  if (cls.kind === 'audio_upload') { // closest synthetic WAV profile that fits the class' size cap
    const fx = ctx.audio.fixture;
    const fits = Object.keys(fx.profiles).filter((p) => wavSize(fx, p) <= cls.payload_bytes.max);
    const profile = (fits.length ? fits : [fx.default_profile]).sort((a, b) => Math.abs(wavSize(fx, a) - size) - Math.abs(wavSize(fx, b) - size))[0];
    if (!ctx.audio.wavs.has(profile)) { const data = syntheticWav(fx, profile); ctx.audio.wavs.set(profile, { data, sha: sha256Hex(data) }); }
    const wav = ctx.audio.wavs.get(profile);
    return multipartJob(ctx, key, { op: 'create_audio', prompt: ctx.audio.prompt, files: [{ key: `${tag}-${j}.wav`, kind: fx.attachment_kind, contentType: fx.content_type, data: wav.data, sha: wav.sha }] });
  }
  throw new Error(`unsupported mix kind "${cls.kind}"`);
}

/** One closed-loop virtual client: at most one job in flight; new jobs only while more() holds. */
async function clientLoop(ctx, wl, n, c, more, stats) {
  const { cfg, rec } = ctx; const rand = makeRng(cfg.seed, 'step', n, c); let failures = 0;
  for (let j = 0; more(); j += 1) {
    const cls = pickClass(wl.mix, rand);
    stats.issued[cls.kind] = (stats.issued[cls.kind] ?? 0) + 1;
    const job = await issue(ctx, cls, n, c, j, rand);
    if (!job?.ok) {
      rec.count('unaccepted_jobs');
      if ((failures += 1) >= 5) { rec.violation('scenario_errors', `client ${c}: 5 consecutive jobs were not accepted, stopping this client`); return; }
      await ctx.sleep(250); // never a hot loop against a refusing gateway
      continue;
    }
    failures = 0; stats.accepted.push({ ...job, kind: cls.kind });
    await settleJobs(ctx, [job]);
    await ctx.sleep(Math.round((between(rand, wl.think_time_ms) * cfg.thinkScale) / 100));
  }
}

// ----------------------------------------------------- metrics (stage times)

/** Per-label-set quantiles (ms, bucket resolution) of one histogram family between two scrapes. Counter resets and empty series are skipped. */
export function histogramDelta(before, after, name) {
  const series = new Map();
  for (const [key, a] of after ?? []) {
    const m = /^([^{]+)(\{.*\})?$/.exec(key); const mm = m && /^(.*)_(bucket|sum|count)$/.exec(m[1]);
    if (!mm || mm[1] !== name) continue;
    const labels = m[2] ?? ''; const le = /(?:^\{|,)le="([^"]+)"/.exec(labels)?.[1];
    const rest = labels.replace(/,?le="[^"]+"/, '').replace('{,', '{').replace(/^\{\}$/, '');
    const e = series.get(rest) ?? { buckets: [], count: 0, sum: 0 }; const d = a - (before?.get(key) ?? 0);
    if (mm[2] === 'bucket') e.buckets.push({ le: le === '+Inf' ? Infinity : Number(le), count: d }); else e[mm[2]] = d;
    series.set(rest, e);
  }
  const out = [];
  for (const [labels, e] of series) {
    if (e.count <= 0 || e.buckets.some((b) => b.count < 0)) continue;
    e.buckets.sort((x, y) => x.le - y.le);
    const q = (p) => r3(histogramQuantile(p, e.buckets) * 1000);
    out.push({ labels: Object.fromEntries([...labels.matchAll(/(\w+)="((?:[^"\\]|\\.)*)"/g)].map((x) => [x[1], x[2]])), count: e.count, mean_ms: r3((e.sum / e.count) * 1000), p50_ms: q(0.5), p95_ms: q(0.95), p99_ms: q(0.99) });
  }
  return out;
}

/** Live queue depth (queued + assigned) from a scrape: the gateway's ubag_queue_depth_live series, else the queued+assigned states; undefined if neither exists. */
export function liveQueueDepth(metrics) {
  if (!metrics) return undefined;
  const keys = [...metrics.keys()]; const sum = (re) => keys.filter((k) => re.test(k)).reduce((n, k) => n + metrics.get(k), 0);
  if (keys.some((k) => /^ubag_queue_depth_live(\{|$)/.test(k))) return sum(/^ubag_queue_depth_live(\{|$)/);
  return keys.some((k) => k.startsWith('ubag_queue_depth{')) ? sum(/^ubag_queue_depth\{[^}]*state="(queued|assigned)"/) : undefined;
}

// -------------------------------------------------------------- voice / host

/** Voice frame age for a step: the in-process bench row with the smallest session count >= n (a ceiling, not an interpolation), or null. */
export function voiceForStep(report, n) {
  if (!report) return null;
  const sizes = [...new Set(report.rows.map((r) => r.sessions))].filter((s) => s >= n).sort((a, b) => a - b);
  if (!sizes.length) return null;
  const rows = report.rows.filter((r) => r.sessions === sizes[0]); const p95 = (d) => rows.find((r) => r.direction === d)?.p95_ms ?? null;
  return { bench_sessions: sizes[0], mic_p95_ms: p95('mic'), speaker_p95_ms: p95('speaker'), worst_p95_ms: r2(Math.max(...rows.map((r) => r.p95_ms))), source: 'in-process relay bench (gateway-side frame age; not live media)' };
}

/** Do the summed container limits match the declared helper class (within 5%)? Fails closed on any unread or unlimited container. */
export function checkHostClass(cls, containers) {
  if (!cls) return { class: null, verified: false, reason: 'no --host-class declared' };
  const want = HOST_CLASSES[cls]; const all = Object.values(containers ?? {});
  if (!all.length) return { class: cls, verified: false, reason: 'no container limits were read (--cgroup-containers)' };
  if (all.some((c) => c.skipped)) return { class: cls, verified: false, reason: 'a sampled container could not be read' };
  if (all.some((c) => c.cpu_limit_cores == null || c.memory_limit_mb == null)) return { class: cls, verified: false, reason: 'a sampled container has no CPU or memory limit' };
  const total = { cpu_cores: r2(all.reduce((s, c) => s + c.cpu_limit_cores, 0)), memory_mb: r2(all.reduce((s, c) => s + c.memory_limit_mb, 0)) };
  const close = (a, b) => Math.abs(a - b) <= b * 0.05;
  const verified = close(total.cpu_cores, want.cpu_cores) && close(total.memory_mb, want.memory_mb);
  return { class: cls, verified, expected: want, limits_total: total, reason: verified ? null : 'summed container limits differ from the class ceiling by more than 5%' };
}

// ----------------------------------------------------------------- goals

/** Merged threshold set: integrity zeros from thresholds.json + the plan goals + the ladder's own keys. */
export function ladderThresholds(cfg) {
  const read = (f) => JSON.parse(readFileSync(f, 'utf8')); const base = read(cfg.thresholds); const t = {};
  for (const k of INTEGRITY_KEYS) { if (typeof base[k] !== 'number') throw new Error(`${cfg.thresholds}: ladder needs "${k}"`); t[k] = base[k]; }
  return Object.assign(t, read(cfg.goals), read(cfg.ladderThresholds));
}

/** evaluateThresholds with a per-goal status: passed | failed | not_measured (not_measured fails only under --require-goals). */
function judge(summary, thresholds, strict) {
  const v = evaluateThresholds(summary, thresholds, { strict });
  const goals = v.results.map((r) => ({ ...r, status: r.actual == null ? 'not_measured' : r.ok ? 'passed' : 'failed' }));
  return { passed: v.passed, goals, failed: goals.filter((g) => g.status === 'failed').map((g) => g.name), not_measured: goals.filter((g) => g.status === 'not_measured').map((g) => g.name) };
}

/** Highest N meeting every goal: the longest passing prefix of the executed steps. */
export function capacityOf(steps) {
  let highest = null; let firstFailed = null;
  for (const s of steps) { if (s.skipped) break; if (s.passed) highest = s.n; else { firstFailed = s.n; break; } }
  const at = steps.find((s) => s.n === highest);
  return { highest_n_meeting_goals: highest, first_failed_step: firstFailed, goals_not_measured_at_highest: at ? at.not_measured : [], every_goal_measured_at_highest: at ? at.not_measured.length === 0 : false };
}

// ------------------------------------------------------------------ steps

const okOf = (rec, op) => rec.ops.get(`${rec.scenario}/${op}`)?.ok ?? [];
const statusesOf = (rec, op) => rec.ops.get(`${rec.scenario}/${op}`)?.statuses ?? {};
const p95Of = (values, min) => (values.length >= min ? summarize(values).p95 : undefined); // too few samples: unmeasured, not a lucky number

function newStepContext(ctx, label) {
  ctx.rec = new Recorder(); ctx.rec.scenario = label; ctx.tenantBudget = ctx.cfg.apiKeyB ? ctx.cfg.tenantProbeSamples : 0;
  return ctx.rec;
}

async function sampleResources(ctx) {
  const { cfg } = ctx;
  return cfg.cgroupTargets.length
    ? startCgroupSampler(cfg.cgroupTargets, cfg.dockerIntervalMs, { exec: ctx.deps.dockerExec, rules: { ...PRESSURE_RULES, recoveryWindowMs: cfg.pressureWindowMs } })
    : async () => ({ skipped: 'not requested (--cgroup-containers)' });
}

/** Containers of one step with the CPU headroom (informational: the plan states no CPU goal). */
const resourceRows = (resources) => Object.fromEntries(Object.entries(resources.containers ?? {}).map(([role, c]) => [role, c.skipped ? c : { ...c, cpu_headroom_pct: c.cpu_limit_cores && c.cpu_cores_avg != null ? r2(Math.max(0, (1 - c.cpu_cores_avg / c.cpu_limit_cores) * 100)) : null }]));

async function runStep(ctx, wl, n, thresholds) {
  const { cfg } = ctx; const rec = newStepContext(ctx, `step-${n}`);
  const stop = await sampleResources(ctx); const before = await ctx.scrape().catch(() => null);
  const stats = { issued: {}, accepted: [] }; const t0 = now(); const stopAt = t0 + cfg.stepSeconds * 1000; const hardStop = t0 + cfg.stepSeconds * 3000;
  // run at least --step-seconds, and (up to 3x that) until the two gated latencies each have --min-samples samples, so a slow step still yields a p95
  const gated = wl.mix.some((m) => m.kind === 'text') ? 'create' : null;
  const enough = () => (gated ? okOf(rec, gated).length : stats.accepted.length) >= cfg.minSamples && okOf(rec, 'poll').length >= cfg.minSamples;
  const more = () => now() < stopAt || (!enough() && now() < hardStop);
  await Promise.all(range(n).map((c) => clientLoop(ctx, wl, n, c, more, stats)));
  const wallMs = now() - t0;
  const ids = new Set(stats.accepted.map((a) => a.jobId));
  if (ids.size !== stats.accepted.length) rec.violation('queue_id_collisions', `${stats.accepted.length - ids.size} distinct keys mapped to an existing job id`);
  for (let k = 0; k < cfg.truncationProbes; k += 1) { // deadline-cut stream: anything other than a terminal non-completed outcome is a false success
    const job = await createJob(ctx, idemKey(ctx, `t${n}`, k), { prompt: `[[synthetic scenario=truncated]] ladder truncation probe ${k}`, op: 'create_probe' });
    if (!job.ok) { rec.count('unaccepted_jobs'); continue; }
    const { states } = await settleJobs(ctx, [job], { verify: false, count: false });
    if (!states[0].terminalAt) rec.violation('unfinished_jobs', `${job.jobId}: truncation probe never reached a terminal state`);
    else if (states[0].status === 'completed') rec.violation('false_successful_truncations', `${job.jobId}: a deadline-cut stream was reported completed`);
    else rec.count('truncation_probes_ok');
  }
  const after = before ? await ctx.scrape().catch(() => null) : null;
  const resources = await stop();

  const c = (name) => rec.counters[name] ?? 0;
  const createOps = [...rec.ops.entries()].filter(([k]) => /\/create(_(attachment|audio))?$/.test(k)).map(([, e]) => e.statuses);
  const attempts = createOps.reduce((s, st) => s + Object.values(st).reduce((a, b) => a + b, 0), 0);
  const rejected = createOps.reduce((s, st) => s + (st['429'] ?? 0) + (st['503'] ?? 0), 0);
  const acceptOk = okOf(rec, 'create').length ? okOf(rec, 'create') : ['create_attachment', 'create_audio'].flatMap((op) => okOf(rec, op)); // gated on the small JSON create when the mix has one
  const voice = voiceForStep(ctx.voiceReport, n);
  const echoChecked = ECHO_VERIFIED.has(cfg.target) || cfg.truncationProbes > 0;
  const summary = rec.summary({
    steady_create_p95_ms: p95Of(acceptOk, cfg.minSamples), steady_read_p95_ms: p95Of(okOf(rec, 'poll'), cfg.minSamples),
    overload_rate_pct: attempts ? r2((rejected / attempts) * 100) : undefined,
    duplicate_committed_results: c('duplicate_results') + c('duplicate_terminal_events') + c('queue_id_collisions'),
    false_successful_truncations: echoChecked ? c('truncated_results') + c('false_successful_truncations') : undefined,
    attachment_violations: c('attachment_violations'), voice_relay_p95_ms: voice?.worst_p95_ms ?? undefined, ...resources.summary,
  });
  const verdict = judge(summary, thresholds, cfg.requireGoals);
  const lat = (op) => summarize(okOf(rec, op));
  return {
    n, duration_s: r2(wallMs / 1000), issued: stats.issued, accepted: stats.accepted.length, completed: okOf(rec, 'completion').length,
    jobs_per_min: r2((okOf(rec, 'completion').length / wallMs) * 60_000), accept_p95_ms: summary.steady_create_p95_ms ?? null, read_p95_ms: summary.steady_read_p95_ms ?? null,
    overload: { attempts, rejected, rate_pct: summary.overload_rate_pct ?? null },
    latency_ms: Object.fromEntries(['create', 'create_attachment', 'create_audio', 'poll', 'result_fetch', 'events_fetch', 'completion', 'queue_wait_observed'].map((op) => [op, lat(op)])),
    create_statuses: statusesOf(rec, 'create'),
    stages: histogramDelta(before, after, 'ubag_job_stage_duration_seconds').map((h) => ({ stage: h.labels.stage ?? null, adapter_family: h.labels.adapter_family ?? null, count: h.count, p50_ms: h.p50_ms, p95_ms: h.p95_ms })),
    queue_wait: histogramDelta(before, after, 'ubag_queue_job_wait_duration_seconds').map(({ labels, ...h }) => h)[0] ?? null,
    voice, voice_live_frame_age: histogramDelta(before, after, 'ubag_voice_relay_frame_age_seconds').map((h) => ({ direction: h.labels.direction ?? null, count: h.count, p95_ms: h.p95_ms })),
    resources: { containers: resourceRows(resources), host_pressure: resources.host_pressure ?? null, skipped: resources.skipped ?? null },
    counters: { ...rec.counters }, classes: { ...rec.classes }, error_codes: { ...rec.errorCodes }, retry: { ...rec.retry }, violations: rec.violations.slice(0, 10),
    summary, passed: verdict.passed, goals: verdict.goals, failed: verdict.failed, not_measured: verdict.not_measured,
  };
}

// ------------------------------------------------------------ paused queue

/**
 * Consumer-paused mode: --clients clients enqueue --jobs jobs, reads are timed against the deep queue, and nothing may leave the queue.
 * With --resume-wait-seconds the harness then waits for the operator to resume the consumer, and drains and verifies every acked job.
 */
async function pausedMode(ctx, wl, thresholds) {
  const { cfg } = ctx; const rec = newStepContext(ctx, 'paused');
  const stop = await sampleResources(ctx); const before = await ctx.scrape().catch(() => null);
  const text = wl.mix.find((m) => m.kind === 'text') ?? wl.mix[0]; const accepted = []; let issued = 0;
  const t0 = now();
  await Promise.all(range(cfg.clients).map(async (c) => {
    const rand = makeRng(cfg.seed, 'paused', c); let failures = 0;
    while (issued < cfg.jobs && failures < 5) {
      const j = issued; issued += 1;
      const job = await createJob(ctx, idemKey(ctx, `p${c}`, j), { prompt: padPrompt(`ladder paused c${c} job ${j}`, between(rand, text.payload_bytes)), op: 'create' });
      if (job.ok) { failures = 0; accepted.push(job); } else { rec.count('unaccepted_jobs'); failures += 1; await ctx.sleep(250); }
    }
    if (failures >= 5) rec.violation('scenario_errors', `client ${c}: 5 consecutive jobs were not accepted, stopping this client`);
  }));
  const fillMs = now() - t0;
  if (!accepted.length) throw new Error('paused: no job was accepted, nothing to hold');
  if (new Set(accepted.map((a) => a.jobId)).size !== accepted.length) rec.violation('queue_id_collisions', 'two distinct idempotency keys mapped to one job id');
  // reads against the deep queue: a paced GET mix (job, events, list) from --clients readers
  const nRead = cfg.holdSeconds * cfg.readRate; const h0 = now();
  const paths = (id) => [`/v1/jobs/${encodeURIComponent(id)}`, `/v1/jobs/${encodeURIComponent(id)}/events?limit=100`, '/v1/jobs?limit=20'];
  const next = { i: 0 };
  await Promise.all(range(Math.min(cfg.clients, Math.max(1, nRead))).map(async () => {
    while (next.i < nRead) {
      const i = next.i; next.i += 1;
      const wait = h0 + (i * 1000) / cfg.readRate - now(); if (wait > 0) await ctx.sleep(wait);
      rec.observe('read', await ctx.request('GET', paths(accepted[i % accepted.length].jobId)[i % 3]));
    }
  }));
  // sweep every acked job once: it must still exist and still be queued while the consumer is paused
  let queued = 0;
  await pool(accepted, cfg.pollConcurrency, async (a) => {
    const res = await ctx.request('GET', `/v1/jobs/${encodeURIComponent(a.jobId)}`); rec.observe('sweep', res);
    if (res.status === 404) rec.violation('lost_jobs', `${a.jobId}: acked job vanished while the consumer was paused`);
    else if (res.status === 200 && PRE_RUN.has(res.json?.status)) queued += 1;
    else if (res.status === 200) rec.violation('consumer_progressed', `${a.jobId}: status ${res.json?.status} while the consumer should be paused`);
  });
  const midDepth = liveQueueDepth(await ctx.scrape().catch(() => null));
  const depth = midDepth === undefined ? undefined : midDepth - (liveQueueDepth(before) ?? 0); // jobs this run added to the live queue

  let drain = { attempted: false };
  if (cfg.resumeWaitSeconds > 0) {
    ctx.announce(`[ladder] ${accepted.length} jobs are queued. Resume the consumer now (UBAG_WORKER_CONSUMER_ENABLED=true); waiting up to ${cfg.resumeWaitSeconds} s for the first job to leave the queue.`);
    const until = now() + cfg.resumeWaitSeconds * 1000; let resumed = false;
    while (now() < until && !resumed) { // quiet probes: errors are expected while a stack restarts
      const res = await ctx.request('GET', `/v1/jobs/${encodeURIComponent(accepted[0].jobId)}`);
      if (res.status === 200 && !PRE_RUN.has(res.json?.status)) resumed = true; else await ctx.sleep(Math.min(1000, cfg.pollIntervalMs * 2));
    }
    if (!resumed) rec.violation('consumer_not_resumed', `no job left the queue within ${cfg.resumeWaitSeconds} s`);
    else { const { out } = await settleJobs(ctx, accepted); drain = { attempted: true, final: out.byStatus }; }
  }
  const after = before ? await ctx.scrape().catch(() => null) : null;
  const resources = await stop();

  const c = (name) => rec.counters[name] ?? 0;
  const createStatuses = statusesOf(rec, 'create'); const attempts = Object.values(createStatuses).reduce((a, b) => a + b, 0);
  // Execution, results, event logs and the tenant probe only exist after a drain; without one they are UNMEASURED (fails under --require-goals), never a vacuous zero.
  const needsDrain = ['results_verified', 'events_verified', 'tenant_probe_requests', 'unfinished_jobs', 'failed_jobs', 'result_mismatches', 'terminal_event_violations', 'warning_jobs', 'cross_tenant_leaks', 'tenant_probe_unexpected'];
  const summary = rec.summary({
    steady_read_p95_ms: p95Of(okOf(rec, 'read'), 1), queued_jobs: queued, queue_depth_observed: depth, consumer_progressed: c('consumer_progressed'),
    consumer_not_resumed: cfg.resumeWaitSeconds > 0 ? c('consumer_not_resumed') : undefined, duplicate_committed_results: c('duplicate_results') + c('duplicate_terminal_events') + c('queue_id_collisions'),
    overload_rate_pct: attempts ? r2((((createStatuses['429'] ?? 0) + (createStatuses['503'] ?? 0)) / attempts) * 100) : undefined,
    ...(drain.attempted ? {} : Object.fromEntries(needsDrain.map((k) => [k, undefined]))), ...resources.summary,
  });
  const t = { ...thresholds, min_queued_jobs: cfg.jobs, min_queue_depth_observed: cfg.jobs, max_consumer_progressed: 0, ...(cfg.resumeWaitSeconds > 0 ? { max_consumer_not_resumed: 0 } : {}) };
  // not paused-mode goals: the steady-state accept goal (the 100-client BURST limit applies instead), truncation, attachments and the ladder's voice goal
  for (const k of ['max_steady_create_p95_ms', 'max_false_successful_truncations', 'max_attachment_violations', 'max_voice_relay_p95_ms']) delete t[k];
  t.max_create_p95_ms = JSON.parse(readFileSync(cfg.thresholds, 'utf8')).max_create_p95_ms;
  const verdict = judge(summary, t, cfg.requireGoals);
  const lat = (op) => summarize(okOf(rec, op));
  return {
    clients: cfg.clients, jobs_requested: cfg.jobs, accepted: accepted.length, fill_seconds: r2(fillMs / 1000), hold_seconds: cfg.holdSeconds, read_rate: cfg.readRate,
    queued_at_sweep: queued, queue_depth_observed: depth ?? null, accept_p95_ms: summary.create_p95_ms ?? null, read_p95_ms: summary.steady_read_p95_ms ?? null,
    overload: { attempts, rate_pct: summary.overload_rate_pct ?? null }, latency_ms: Object.fromEntries(['create', 'read', 'sweep', 'completion'].map((op) => [op, lat(op)])),
    stages: histogramDelta(before, after, 'ubag_job_stage_duration_seconds').map((h) => ({ stage: h.labels.stage ?? null, adapter_family: h.labels.adapter_family ?? null, count: h.count, p50_ms: h.p50_ms, p95_ms: h.p95_ms })),
    drain, resources: { containers: resourceRows(resources), host_pressure: resources.host_pressure ?? null, skipped: resources.skipped ?? null },
    counters: { ...rec.counters }, classes: { ...rec.classes }, violations: rec.violations.slice(0, 20), retry: { ...rec.retry },
    summary, passed: verdict.passed, goals: verdict.goals, failed: verdict.failed, not_measured: verdict.not_measured,
  };
}

// ------------------------------------------------------------------ driver

export async function run(cfg, deps = {}, env = process.env) {
  const request = deps.request ?? makeRequest(cfg); const wl = cfg.workloadInfo.manifest;
  const voiceReport = cfg.voiceLatency ? JSON.parse(readFileSync(cfg.voiceLatency, 'utf8')) : null;
  if (voiceReport) voiceLatencySummary(voiceReport);
  const audioWl = wl.mix.some((m) => m.kind === 'audio_upload') ? loadWorkload('audio-upload') : null;
  const ctx = {
    cfg, request, rec: null, sleep: deps.sleep ?? defaultSleep, runId: randomBytes(6).toString('hex'), jobIds: new Set(), tenantBudget: 0, deps, voiceReport,
    announce: deps.announce ?? ((m) => console.error(m)), audio: audioWl ? { fixture: audioWl.fixture, prompt: audioWl.prompt, wavs: new Map() } : null,
  };
  ctx.scrape = async () => {
    const res = await request('GET', '/v1/metrics', { headers: { Accept: 'text/plain' } });
    if (res.status !== 200) throw new Error(`/v1/metrics returned ${res.status || res.error}`);
    return parseProm(res.text);
  };
  const thresholds = ladderThresholds(cfg); const started = new Date(); const gatewayMetrics = await ctx.scrape().catch(() => null);
  const report = { steps: [], paused: null };
  if (cfg.mode === 'paused') report.paused = await pausedMode(ctx, wl, thresholds);
  else {
    let stopped = false;
    for (const n of cfg.steps) {
      if (stopped) { report.steps.push({ n, skipped: 'not run: an earlier step failed (--continue-after-fail runs the rest)' }); continue; }
      const step = await runStep(ctx, wl, n, thresholds);
      report.steps.push(step);
      if (!step.passed && !cfg.continueAfterFail) stopped = true;
      if (n !== cfg.steps[cfg.steps.length - 1] && cfg.cooldownSeconds > 0) await ctx.sleep(cfg.cooldownSeconds * 1000);
    }
  }
  const ran = report.paused ? [report.paused] : report.steps.filter((s) => !s.skipped);
  const firstResources = ran.map((s) => s.resources?.containers).find((c) => c && Object.keys(c).length) ?? {};
  const hostClass = checkHostClass(cfg.hostClass, firstResources);
  const sampledLinux = Object.values(firstResources).some((c) => c.cgroup === 'v2' || c.cgroup === 'v1');
  const authoritative = Boolean(cfg.isolatedLabHost && hostClass.verified && sampledLinux);
  const provenance = {
    gateway: gatewayInfoFrom(gatewayMetrics), harness: { ...(deps.harnessGit ?? harnessGit)() }, harness_host: harnessHost(), harness_env: pickEnv(env),
    stack_env: cfg.cgroupTargets.length ? await stackEnv(cfg.cgroupTargets, deps.dockerEnv) : { skipped: 'no --cgroup-containers' },
    container_limits: containerLimits({ containers: firstResources }),
    workload: { name: cfg.workloadInfo.name, sha256: cfg.workloadInfo.sha256, harness_support: wl.harness_support, manifest: wl },
  };
  const strictNote = cfg.requireGoals ? 'unmeasured goal = FAIL (--require-goals)' : 'unmeasured goals are listed, not failed (use --require-goals to fail them)';
  return {
    meta: {
      run_id: ctx.runId, mode: cfg.mode, started_at: started.toISOString(), finished_at: new Date().toISOString(), target_origin: new URL(cfg.baseUrl).origin, target: cfg.target, node: process.version,
      authoritative, classification: authoritative ? 'lab-host (operator asserts isolated Linux stack; limits verified from cgroups)' : 'NON-AUTHORITATIVE',
      non_authoritative_reasons: authoritative ? [] : [!cfg.isolatedLabHost && 'no --isolated-lab-host attestation', !hostClass.verified && `host class not verified: ${hostClass.reason}`, !sampledLinux && 'no cgroup files were read'].filter(Boolean),
      host_class: hostClass, goals_policy: strictNote, provenance,
      config: Object.fromEntries(Object.entries(cfg).filter(([k]) => !['apiKey', 'apiKeyB', 'baseUrl', 'workloadInfo', 'cgroupTargets'].includes(k))),
    },
    thresholds, ...report,
    capacity: report.paused ? null : capacityOf(report.steps),
    passed: report.paused ? report.paused.passed : report.steps.every((s) => !s.skipped && s.passed),
  };
}

// ---------------------------------------------------------------- markdown

const cell = (v) => (v == null ? '-' : String(v));
const table = (head, rows) => [`| ${head.join(' | ')} |`, `| ${head.map(() => '---').join(' | ')} |`, ...rows.map((r) => `| ${r.map(cell).join(' | ')} |`)].join('\n');
const minOf = (step, key) => { const v = Object.values(step.resources?.containers ?? {}).map((c) => c[key]).filter((x) => x != null); return v.length ? Math.min(...v) : null; };
const maxOf = (step, key) => { const v = Object.values(step.resources?.containers ?? {}).map((c) => c[key]).filter((x) => x != null); return v.length ? Math.max(...v) : null; };

export function renderMarkdown(report) {
  const m = report.meta; const pv = m.provenance; const L = [];
  L.push(m.authoritative ? '# UBAG workload ladder capacity report' : '# UBAG workload ladder capacity report (NON-AUTHORITATIVE)', '');
  if (!m.authoritative) L.push(`**NON-AUTHORITATIVE.** ${m.non_authoritative_reasons.join('; ')}. Do not quote these numbers as capacity.`, '');
  L.push(`- run: \`${m.run_id}\` (${m.started_at} -> ${m.finished_at}), mode \`${m.mode}\`, target \`${m.target}\` at ${m.target_origin}`, `- verdict: **${report.passed ? 'PASS' : 'FAIL'}** (${m.goals_policy})`);
  L.push(`- gateway: ${pv.gateway ? `${pv.gateway.version} (commit ${pv.gateway.commit})` : 'unknown (metrics unavailable)'}; harness: ${pv.harness.sha ?? 'unknown'}${pv.harness.dirty ? ' (+uncommitted changes under tests/load)' : ''}`);
  L.push(`- host class: ${m.host_class.class ?? 'undeclared'} ${m.host_class.verified ? `verified (limits ${JSON.stringify(m.host_class.limits_total)})` : `NOT verified (${m.host_class.reason})`}`);
  L.push(`- workload: ${pv.workload.name} sha256 ${pv.workload.sha256.slice(0, 12)}; harness host: ${pv.harness_host.cpu_count} CPU, ${pv.harness_host.mem_total_mb} MB, ${pv.harness_host.platform}`);
  L.push(`- container limits: \`${JSON.stringify(pv.container_limits)}\``, `- flags on (allowlisted stack env): \`${JSON.stringify(pv.stack_env)}\``, '');
  if (report.capacity) {
    const c = report.capacity;
    L.push('## Capacity', '', c.highest_n_meeting_goals == null
      ? '**No step met every goal** (the lowest step already failed or did not run).'
      : `**Highest N meeting every goal: ${c.highest_n_meeting_goals}**${c.every_goal_measured_at_highest ? '' : ` (UNVERIFIED: not measured at that step: ${c.goals_not_measured_at_highest.join(', ')})`}${c.first_failed_step ? `; first failed step: ${c.first_failed_step}` : ''}.`,
      '', 'Closed-loop virtual clients with think time, one job in flight each; "N" is concurrent clients, not N real browser sessions. Capacity is identity-bound, so this is a ceiling for the tested identity set, not a fleet figure.', '');
    L.push('## Steps', '', table(['N', 'accepted', 'done', 'jobs/min', 'completion p95 ms', 'accept p95 ms', 'read p95 ms', 'overload %', 'min mem headroom %', 'max throttle %', 'oom', 'voice p95 ms', 'verdict'],
      report.steps.map((s) => (s.skipped ? [s.n, s.skipped] : [s.n, s.accepted, s.completed, s.jobs_per_min, s.latency_ms.completion.p95, s.accept_p95_ms, s.read_p95_ms, s.overload.rate_pct, minOf(s, 'memory_headroom_pct'), maxOf(s, 'cpu_throttled_pct'), s.summary.oom_kills, s.voice?.worst_p95_ms, s.passed ? 'pass' : `FAIL: ${s.failed.join(', ')}`]))), '');
    L.push('## Integrity counters per step', '', table(['N', 'lost', 'unfinished', 'unaccepted', 'failed', 'result mismatch', 'duplicate commits', 'false-success truncation', 'cross-tenant leaks', 'attachment violations'],
      report.steps.filter((s) => !s.skipped).map((s) => [s.n, s.summary.lost_jobs, s.summary.unfinished_jobs, s.summary.unaccepted_jobs, s.summary.failed_jobs, s.summary.result_mismatches, s.summary.duplicate_committed_results, s.summary.false_successful_truncations ?? 'not measured', s.summary.cross_tenant_leaks, s.summary.attachment_violations])), '');
    const stageRows = report.steps.filter((s) => !s.skipped).flatMap((s) => s.stages.map((g) => [s.n, g.stage, g.adapter_family, g.count, g.p50_ms, g.p95_ms]));
    L.push('## Stage times (gateway histogram delta, bucket resolution)', '', stageRows.length ? table(['N', 'stage', 'family', 'jobs', 'p50 ms', 'p95 ms'], stageRows) : 'No `ubag_job_stage_duration_seconds` observations: the stack needs UBAG_WORKER_STAGE_TIMINGS=1 and a target that reaches a worker.', '');
    L.push('## Goals not measured or failed', '', ...report.steps.filter((s) => !s.skipped && (s.failed.length || s.not_measured.length)).map((s) => `- N=${s.n}: failed [${s.failed.join(', ')}], not measured [${s.not_measured.join(', ')}]`), '');
  } else {
    const p = report.paused;
    L.push('## Paused queue', '', `${p.clients} clients enqueued ${p.accepted}/${p.jobs_requested} jobs in ${p.fill_seconds} s; ${p.queued_at_sweep} still queued at the sweep (gateway queue depth ${p.queue_depth_observed ?? 'unavailable'}); accept p95 ${p.accept_p95_ms} ms (100-client burst limit), read p95 ${p.read_p95_ms} ms over ${p.hold_seconds} s at ${p.read_rate} reads/s; overload ${p.overload.rate_pct ?? '-'} %.`, '',
      p.drain.attempted ? `Drain after resume: \`${JSON.stringify(p.drain.final)}\`.` : 'No drain was run (--resume-wait-seconds 0): execution, results and event logs were not verified.', '',
      table(['goal', 'limit', 'actual', 'status'], p.goals.map((g) => [g.name, g.limit, g.actual, g.status])), '');
  }
  const viol = (report.paused ? [report.paused] : report.steps.filter((s) => !s.skipped)).flatMap((s) => (s.violations ?? []).map((v) => `- [${v.scenario}] ${v.counter}: ${v.detail}`));
  if (viol.length) L.push('## Violation samples', '', ...viol.slice(0, 40), '');
  L.push('## Caveats', '', '- Live voice media is not driven; voice frame age is the in-process relay bench ceiling for the step (`--voice-latency`), or not measured.',
    '- Per-step memory headroom uses the cgroup `memory.peak`, which is lifetime-cumulative, so later steps inherit earlier peaks (a conservative lower bound).',
    '- Stage p50/p95 are quantiles of Prometheus histogram buckets (bucket resolution), not exact values.',
    '- The latency goals judge the API (accept, read). If jobs/min stops growing with N while completion p95 does, the executor is the bottleneck, whatever the highest passing N says.',
    `- accept p95 is the small JSON create (or all creates when the mix has none); attachment and audio accept latency is reported separately.`, '');
  return L.join('\n');
}

export async function main(argv = process.argv.slice(2), env = process.env, deps = {}) {
  let cfg;
  try { cfg = parseArgs(argv, env); } catch (e) { console.error(`[ladder] refused: ${e.message}`); return 2; }
  console.log(`[ladder] ${cfg.mode} ${cfg.mode === 'ladder' ? cfg.steps.join('/') : `${cfg.clients} clients x ${cfg.jobs} jobs`} workload ${cfg.workload} target ${cfg.target} -> ${new URL(cfg.baseUrl).origin}`);
  let report;
  try { report = await run(cfg, deps, env); } catch (e) { console.error(`[ladder] harness error: ${e.message}`); return 2; }
  const dir = resolve(cfg.outDir, `${report.meta.started_at.replace(/[:.]/g, '-')}-${cfg.mode}`);
  mkdirSync(dir, { recursive: true });
  const markdown = renderMarkdown(report);
  writeFileSync(join(dir, 'report.json'), `${JSON.stringify(report, null, 2)}\n`);
  writeFileSync(join(dir, 'summary.md'), `${markdown}\n`);
  console.log(markdown);
  console.log(`[ladder] report written to ${dir}`);
  return report.passed ? 0 : 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().then((code) => process.exit(code));
}
