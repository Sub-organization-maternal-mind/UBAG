#!/usr/bin/env node
/**
 * UBAG acceptance load harness: "100 concurrent API clients and 1,000 queued jobs".
 *
 * Zero dependencies (node:http-free client: global fetch + async pools).
 * DANGEROUS BY DESIGN: it generates real load. Read docs/load-testing.md first.
 *
 *   UBAG_LOAD_BASE_URL=http://127.0.0.1:8080 UBAG_LOAD_API_KEY=... \
 *     node tests/load/acceptance.mjs --i-understand-this-is-load --scenario queue-1000
 *
 * Scenarios: queue-1000 | clients-100 | duplicates | steady-state | overload | metrics-snapshot | all
 *
 * Integrity gates (fail closed): completed jobs have their result body and event
 * log re-fetched and verified; a second tenant (UBAG_LOAD_API_KEY_B) must not be
 * able to read them; --require-goals additionally loads thresholds.goals.json and
 * treats every unmeasured threshold as a FAILURE.
 */
import { PRESSURE_RULES, parseTargets, startCgroupSampler } from './lib/cgroup.mjs';
import { randomBytes } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { arch, platform } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { performance } from 'node:perf_hooks';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
export const API_VERSION = '2026-05-22';
export const SCENARIOS = ['queue-1000', 'clients-100', 'duplicates', 'steady-state', 'overload', 'metrics-snapshot'];
export const ACK_FLAG = 'i-understand-this-is-load';

// completed_with_warnings is NOT a success: it is counted separately (warning_jobs) and gated.
const SUCCESS = new Set(['completed']);
const WARNED = 'completed_with_warnings';
const TERMINAL = new Set([...SUCCESS, WARNED, 'failed_retryable', 'failed_terminal', 'dead_letter', 'cancelled', 'canceled', 'timed_out']);
const PRE_RUN = new Set(['created', 'queued']);
// Event types that end a job's event log (a leading "job." is stripped before matching).
const TERMINAL_EVENTS = new Set([...TERMINAL, 'failed', 'blocked']);
// 1x1 PNG, used as the "small image data URL" multimodal payload.
const PNG_DATA_URL = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';
const REASON_BY_CODE = {
  'UBAG-OVERLOAD-REQUESTS-001': 'inflight_requests',
  'UBAG-OVERLOAD-UPLOAD-001': 'upload_memory',
  overloaded: 'upload_memory', // OpenAI-facade error shape
  'UBAG-CONCURRENCY-001': 'concurrency',
  'UBAG-QUEUE-BACKPRESSURE-002': 'queue_depth',
  'UBAG-RATE-APP-001': 'rate_limit',
  'UBAG-VOICE-QUEUE-FULL-004': 'voice_queue',
  'UBAG-VOICE-SESSION-LIMIT-003': 'voice_queue',
};

const r2 = (x) => (x == null || !Number.isFinite(x) ? null : Math.round(x * 100) / 100);
const now = () => performance.now();
const range = (n) => Array.from({ length: n }, (_, i) => i);
const defaultSleep = (ms) => new Promise((res) => setTimeout(res, Math.max(0, ms)));

// ---------------------------------------------------------------- statistics

/** Nearest-rank percentile over an ascending-sorted array. */
export function percentile(sorted, p) {
  if (!sorted.length) return null;
  const rank = Math.ceil((p / 100) * sorted.length);
  return sorted[Math.min(sorted.length - 1, Math.max(0, rank - 1))];
}

export function summarize(values) {
  const s = [...values].sort((a, b) => a - b);
  const n = s.length;
  const sum = s.reduce((a, b) => a + b, 0);
  return {
    count: n,
    min: r2(s[0]), max: r2(s[n - 1]), mean: n ? r2(sum / n) : null,
    p50: r2(percentile(s, 50)), p95: r2(percentile(s, 95)), p99: r2(percentile(s, 99)),
  };
}

export async function pool(items, limit, fn) {
  const out = new Array(items.length);
  let next = 0;
  const worker = async () => {
    while (next < items.length) {
      const i = next++;
      out[i] = await fn(items[i], i);
    }
  };
  await Promise.all(Array.from({ length: Math.min(Math.max(1, limit), items.length) }, worker));
  return out;
}

// ------------------------------------------------------------- retry backoff

/**
 * Server retry hint in ms from Retry-After (seconds or HTTP-date) and the
 * structured body error.retry_after_ms. When both exist the larger wins so the
 * client honors the (coarser, ceil'd) header AND the finer body hint.
 */
export function parseRetryAfterMs(headers, body, nowMs = Date.now()) {
  const hints = [];
  const bodyMs = body?.error?.retry_after_ms;
  if (Number.isFinite(bodyMs) && bodyMs >= 0) hints.push(bodyMs);
  const raw = typeof headers?.get === 'function' ? headers.get('retry-after') : headers?.['retry-after'];
  if (raw != null && String(raw).trim() !== '') {
    const text = String(raw).trim();
    if (/^\d+$/.test(text)) hints.push(Number(text) * 1000);
    else {
      const at = Date.parse(text);
      if (Number.isFinite(at)) hints.push(Math.max(0, at - nowMs));
    }
  }
  return hints.length ? Math.max(...hints) : null;
}

/**
 * Sleep before retry number `attempt` (0-based). With a server hint: the hint
 * (capped at capMs) plus optional non-negative jitter, never below the hint
 * unless the cap truncates it. Without a hint: exponential from baseMs.
 */
export function retryDelayMs({ attempt, retryAfterMs = null, capMs = 60_000, baseMs = 250, jitter = 0, rand = Math.random }) {
  const hint = retryAfterMs ?? baseMs * 2 ** attempt;
  const base = Math.min(Math.max(hint, 0), capMs);
  return Math.round(base + base * jitter * rand());
}

// ------------------------------------------------------- target safety guard

export function isLoopbackOrPrivateHost(hostname) {
  const h = hostname.toLowerCase();
  if (h === 'localhost' || h.endsWith('.localhost') || h === '[::1]') return true;
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(h);
  if (v4) {
    const [a, b] = [Number(v4[1]), Number(v4[2])];
    return a === 127 || a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168);
  }
  if (h.startsWith('[')) return /^\[(fc|fd)[0-9a-f]{2}:/.test(h) || /^\[fe80:/.test(h);
  return false;
}

/** Validates the base URL; non-loopback/private hosts need UBAG_LOAD_ALLOWED_HOSTS. */
export function checkTarget(raw, env = process.env) {
  let url;
  try { url = new URL(raw); } catch { throw new Error('UBAG_LOAD_BASE_URL is not a valid URL'); }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') throw new Error('base URL must be http or https');
  if (url.username || url.password || url.search || url.hash) throw new Error('base URL must not contain credentials, query or fragment');
  const host = url.hostname.toLowerCase();
  if (!isLoopbackOrPrivateHost(host)) {
    const allowed = String(env.UBAG_LOAD_ALLOWED_HOSTS ?? '').split(',')
      .map((s) => s.trim().toLowerCase()).filter((s) => s && !s.includes('*'));
    if (!allowed.includes(host) && !allowed.includes(url.host.toLowerCase())) {
      throw new Error(`host "${url.host}" is not loopback/private and is not listed in UBAG_LOAD_ALLOWED_HOSTS (wildcards are ignored); never point this at a shared or production host`);
    }
  }
  return url.origin + url.pathname.replace(/\/+$/, '');
}

// ----------------------------------------------------------------- arguments

const NUM = { // flag: [cfgKey, default, min, max]
  jobs: ['jobs', 1000, 1, 100_000], rate: ['rate', 50, 1, 10_000],
  'create-concurrency': ['createConcurrency', 32, 1, 1000],
  clients: ['clients', 100, 1, 1000], iterations: ['iterations', 3, 1, 1000],
  'dup-keys': ['dupKeys', 5, 1, 1000], 'dup-concurrency': ['dupConcurrency', 20, 2, 500],
  'cancel-races': ['cancelRaces', 20, 0, 10_000],
  burst: ['burst', 200, 1, 5000], 'burst-body-bytes': ['burstBodyBytes', 2 << 20, 1024, 40 << 20],
  'inflight-burst': ['inflightBurst', 2500, 0, 20_000],
  'max-body-bytes': ['maxBodyBytes', 1 << 20, 1024, 64 << 20],
  'poll-interval-ms': ['pollIntervalMs', 1000, 5, 60_000], 'poll-concurrency': ['pollConcurrency', 25, 1, 500],
  'deadline-ms': ['deadlineMs', 600_000, 100, 7_200_000],
  'request-timeout-ms': ['requestTimeoutMs', 30_000, 50, 600_000],
  'facade-timeout-ms': ['facadeTimeoutMs', 90_000, 50, 600_000],
  'max-retries': ['maxRetries', 8, 0, 100], 'retry-cap-ms': ['retryCapMs', 60_000, 1, 600_000],
  'recovery-ms': ['recoveryMs', 30_000, 100, 600_000], 'recovery-requests': ['recoveryRequests', 20, 1, 1000],
  'snapshot-seconds': ['snapshotSeconds', 0, 0, 3600],
  'metrics-interval-ms': ['metricsIntervalMs', 5000, 10, 600_000],
  'docker-interval-ms': ['dockerIntervalMs', 5000, 10, 600_000],
  'pressure-window-ms': ['pressureWindowMs', PRESSURE_RULES.recoveryWindowMs, 10, 3_600_000],
  'settle-ms': ['settleMs', 500, 0, 60_000],
  'steady-seconds': ['steadySeconds', 30, 1, 3600], 'steady-rate': ['steadyRate', 5, 1, 200],
  'steady-read-rate': ['steadyReadRate', 20, 1, 1000], 'steady-seed-jobs': ['steadySeedJobs', 10, 1, 1000],
  'tenant-probe-samples': ['tenantProbeSamples', 10, 0, 1000],
};
const STR = { target: 'target', 'command-type': 'commandType', 'docker-stats-container': 'dockerContainer', 'cgroup-containers': 'cgroupContainers', 'out-dir': 'outDir', thresholds: 'thresholds', goals: 'goals' };
const FLAGS = new Set([ACK_FLAG, 'with-upload', 'require-goals']);

export function parseArgs(argv, env = process.env) {
  const cfg = {
    scenarios: [], target: 'mock', commandType: 'chat.prompt', withUpload: false, jitter: 0.1,
    dockerContainer: undefined, cgroupContainers: undefined, outDir: join(here, 'results'), thresholds: join(here, 'thresholds.json'),
    goals: join(here, 'thresholds.goals.json'), requireGoals: false,
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
      if (name === 'with-upload') cfg.withUpload = true;
      if (name === 'require-goals') cfg.requireGoals = true;
      continue;
    }
    if (!(name in NUM) && !(name in STR) && name !== 'scenario') throw new Error(`unknown option --${name}`);
    if (value === undefined) {
      value = argv[++i];
      if (value === undefined || value.startsWith('--')) throw new Error(`--${name} requires a value`);
    }
    if (name === 'scenario') cfg.scenarios.push(...value.split(',').map((s) => s.trim()).filter(Boolean));
    else if (name in STR) cfg[STR[name]] = value;
    else {
      const [key, , min, max] = NUM[name];
      if (!/^\d+$/.test(value) || Number(value) < min || Number(value) > max) throw new Error(`--${name} must be an integer in [${min}, ${max}]`);
      cfg[key] = Number(value);
    }
  }
  // Order matters: the acknowledgement is checked before anything touches the target.
  if (!acknowledged) throw new Error(`this tool generates load against a live gateway; pass --${ACK_FLAG} to proceed (see docs/load-testing.md)`);
  if (!cfg.scenarios.length) throw new Error(`--scenario is required (${SCENARIOS.join(' | ')} | all)`);
  if (cfg.scenarios.includes('all')) cfg.scenarios = [...SCENARIOS];
  for (const s of cfg.scenarios) if (!SCENARIOS.includes(s)) throw new Error(`unknown scenario "${s}"`);
  cfg.scenarios = [...new Set(cfg.scenarios)];
  if (!env.UBAG_LOAD_BASE_URL) throw new Error('UBAG_LOAD_BASE_URL is required');
  if (!env.UBAG_LOAD_API_KEY) throw new Error('UBAG_LOAD_API_KEY is required');
  cfg.baseUrl = checkTarget(env.UBAG_LOAD_BASE_URL, env);
  cfg.apiKey = env.UBAG_LOAD_API_KEY;
  // Optional second tenant for the cross-tenant isolation probe; it must be a different credential.
  cfg.apiKeyB = env.UBAG_LOAD_API_KEY_B || undefined;
  if (cfg.apiKeyB && cfg.apiKeyB === cfg.apiKey) throw new Error('UBAG_LOAD_API_KEY_B must differ from UBAG_LOAD_API_KEY (it must belong to a second tenant)');
  // --docker-stats-container <name> is the legacy single-container spelling of --cgroup-containers.
  try { cfg.cgroupTargets = parseTargets([cfg.cgroupContainers, cfg.dockerContainer].filter(Boolean).join(',')); } catch (e) { throw new Error(`--cgroup-containers: ${e.message}`); }
  return cfg;
}

// ------------------------------------------------------------------ recorder

export class Recorder {
  constructor() {
    this.scenario = 'run';
    this.ops = new Map(); // "scenario/op" -> {all, ok, statuses}
    this.classes = {};
    this.counters = {};
    this.rejections = {}; // client-observed 429/503 by reason
    this.errorCodes = {};
    this.violations = [];
    this.retry = { retries: 0, honored: 0, capped: 0, sleptMs: 0, exhausted: 0 };
  }

  entry(op) {
    const key = `${this.scenario}/${op}`;
    if (!this.ops.has(key)) this.ops.set(key, { all: [], ok: [], statuses: {} });
    return this.ops.get(key);
  }

  count(name, n = 1) { this.counters[name] = (this.counters[name] ?? 0) + n; }

  violation(name, detail) {
    this.count(name);
    if (this.violations.length < 40) this.violations.push({ scenario: this.scenario, counter: name, detail });
  }

  latency(op, ms) { this.entry(op).ok.push(ms); }

  /** Classify + record one HTTP result. Returns the class. */
  observe(op, res, { allowReset = false } = {}) {
    const e = this.entry(op);
    e.all.push(res.ms);
    const label = res.status || res.error || 'error';
    e.statuses[label] = (e.statuses[label] ?? 0) + 1;
    let cls;
    if (res.status === 0) {
      cls = res.error === 'timeout' ? 'hang' : allowReset ? 'safe_reset' : 'network_error';
      if (cls === 'hang') this.count('hangs');
      if (cls === 'network_error') this.count('network_errors');
    } else if (res.status === 429 || res.status === 503) {
      cls = 'overload';
      const header = res.headers?.get('retry-after');
      if (!header) {
        this.count('overload_missing_retry_after');
        if (res.status === 503) { cls = 'server_error'; this.count('server_errors_non_overload'); }
      }
      if (!Number.isFinite(res.json?.error?.retry_after_ms)) this.count('overload_missing_retry_after_ms_body');
      const code = res.json?.error?.code;
      const reason = REASON_BY_CODE[code] ?? `unknown:${res.status}`;
      this.rejections[reason] = (this.rejections[reason] ?? 0) + 1;
    } else if (res.status >= 500) {
      cls = 'server_error';
      this.count('server_errors_non_overload');
    } else if (res.status >= 400) {
      cls = 'client_4xx';
    } else {
      cls = 'ok';
      e.ok.push(res.ms);
    }
    this.classes[cls] = (this.classes[cls] ?? 0) + 1;
    if (res.status >= 400) {
      const code = res.json?.error?.code;
      if (typeof code === 'string') this.errorCodes[code] = (this.errorCodes[code] ?? 0) + 1;
    }
    return cls;
  }

  opsTable() {
    const out = {};
    for (const [key, e] of [...this.ops.entries()].sort()) {
      out[key] = { all: summarize(e.all), ok: summarize(e.ok), statuses: e.statuses };
    }
    return out;
  }

  /** Flat metrics that thresholds.json is evaluated against. */
  summary(extra = {}) {
    // Burst create p95 excludes the steady-state scenario, which has its own (tighter) goal threshold.
    const createOk = [...this.ops.entries()].filter(([k]) => k.endsWith('/create') && !k.startsWith('steady-state/')).flatMap(([, e]) => e.ok);
    const p95Of = (key) => { const ok = this.ops.get(key)?.ok; return ok?.length ? summarize(ok).p95 : undefined; };
    const c = (n) => this.counters[n] ?? 0;
    return {
      server_errors_non_overload: c('server_errors_non_overload'),
      overload_missing_retry_after: c('overload_missing_retry_after'),
      overload_missing_retry_after_ms_body: c('overload_missing_retry_after_ms_body'),
      hangs: c('hangs'), network_errors: c('network_errors'),
      lost_jobs: c('lost_jobs'), unfinished_jobs: c('unfinished_jobs'), unaccepted_jobs: c('unaccepted_jobs'),
      failed_jobs: c('failed_jobs'), queue_id_collisions: c('queue_id_collisions'),
      dup_violations: c('dup_violations'), dup_replay_flag_mismatch: c('dup_replay_flag_mismatch'),
      unsafe_payload_outcomes: c('unsafe_payload_outcomes'), cancel_inconsistencies: c('cancel_inconsistencies'),
      unrecovered: c('unrecovered'), scenario_errors: c('scenario_errors'),
      warning_jobs: c('warning_jobs'), result_mismatches: c('result_mismatches'), results_verified: c('results_verified'),
      terminal_event_violations: c('terminal_event_violations'), events_verified: c('events_verified'),
      cross_tenant_leaks: c('cross_tenant_leaks'), tenant_probe_unexpected: c('tenant_probe_unexpected'), tenant_probe_requests: c('tenant_probe_requests'),
      facade_image_failures: c('facade_image_failures'),
      create_p95_ms: createOk.length ? summarize(createOk).p95 : undefined,
      steady_create_p95_ms: p95Of('steady-state/create'), steady_read_p95_ms: p95Of('steady-state/read'),
      ...extra,
    };
  }
}

// ---------------------------------------------------------------- thresholds

/**
 * `max_<key>` => summary[key] <= limit; `min_<key>` => summary[key] >= limit.
 * An unmeasured key is skipped, unless `strict` (--require-goals): then it FAILS,
 * so a goal can never pass because the scenario that measures it did not run.
 */
export function evaluateThresholds(summary, thresholds, { strict = false } = {}) {
  const results = [];
  for (const [name, limit] of Object.entries(thresholds)) {
    if (name.startsWith('_')) continue;
    const m = /^(max|min)_(.+)$/.exec(name);
    if (!m || typeof limit !== 'number') { results.push({ name, limit, actual: null, ok: false, note: 'malformed threshold' }); continue; }
    const actual = summary[m[2]];
    if (actual === undefined || actual === null || (typeof actual === 'number' && !Number.isFinite(actual))) {
      results.push(strict ? { name, limit, actual: null, ok: false, note: 'not measured (required by --require-goals)' } : { name, limit, actual: null, ok: true, note: 'not measured (scenario not run)' });
      continue;
    }
    results.push({ name, limit, actual, ok: m[1] === 'max' ? actual <= limit : actual >= limit });
  }
  return { passed: results.every((r) => r.ok), results };
}

// ------------------------------------------------------------------- metrics

const TRACKED = [
  'ubag_gateway_http_inflight_requests', 'ubag_admission_rejections_total', 'ubag_upload_memory_inflight_bytes',
  'ubag_upload_memory_budget_bytes', 'ubag_gateway_request_latency_seconds', 'ubag_queue_job_wait_duration_seconds',
  'ubag_admission_tokens_active', 'ubag_db_pool_', 'ubag_voice_', 'ubag_queue_depth',
];
const GAUGES = ['ubag_gateway_http_inflight_requests', 'ubag_upload_memory_inflight_bytes', 'ubag_admission_tokens_active', 'ubag_db_pool_connections', 'ubag_queue_depth'];
const isTracked = (key) => TRACKED.some((p) => key.startsWith(p));

export function parseProm(text) {
  const out = new Map();
  for (const line of text.split('\n')) {
    if (!line || line.startsWith('#')) continue;
    const m = /^([^\s{]+)(\{[^}]*\})?\s+(\S+)/.exec(line);
    if (!m) continue;
    const v = Number(m[3]);
    if (Number.isFinite(v)) out.set(m[1] + (m[2] ?? ''), v);
  }
  return out;
}

/** Prometheus-style linear-interpolated quantile over cumulative buckets [{le, count}] ascending. */
export function histogramQuantile(q, buckets) {
  if (!buckets.length) return null;
  const total = buckets[buckets.length - 1].count;
  if (total <= 0) return null;
  const rank = q * total;
  let prevLe = 0; let prevCount = 0;
  for (const b of buckets) {
    if (b.count >= rank) {
      if (!Number.isFinite(b.le)) return prevLe;
      if (b.count === prevCount) return b.le;
      return prevLe + (b.le - prevLe) * ((rank - prevCount) / (b.count - prevCount));
    }
    prevLe = Number.isFinite(b.le) ? b.le : prevLe; prevCount = b.count;
  }
  return prevLe;
}

export function metricsDelta(before, after, maxSampled = new Map()) {
  const series = {}; const hist = new Map();
  for (const key of new Set([...(before?.keys() ?? []), ...(after?.keys() ?? [])])) {
    if (!isTracked(key)) continue;
    const b = before?.get(key) ?? 0; const a = after?.get(key) ?? 0;
    const name = key.split('{')[0];
    const hm = /^(.*)_(bucket|sum|count)$/.exec(name);
    if (hm && (hm[1] === 'ubag_gateway_request_latency_seconds' || hm[1] === 'ubag_queue_job_wait_duration_seconds')) {
      const labels = key.slice(name.length);
      const le = /(?:^\{|,)le="([^"]+)"/.exec(labels)?.[1];
      const base = `${hm[1]}${labels.replace(/,?le="[^"]+"/, '').replace('{,', '{').replace(/^\{\}$/, '')}`;
      const h = hist.get(base) ?? { buckets: [], sum: 0, count: 0 };
      if (hm[2] === 'bucket') h.buckets.push({ le: le === '+Inf' ? Infinity : Number(le), count: a - b });
      else h[hm[2]] = a - b;
      hist.set(base, h);
      continue;
    }
    const entry = { before: b, after: a, delta: r2(a - b) };
    if (maxSampled.has(key)) entry.max_sampled = maxSampled.get(key);
    series[key] = entry;
  }
  const histograms = {};
  for (const [base, h] of hist) {
    h.buckets.sort((x, y) => x.le - y.le);
    histograms[base] = {
      count: h.count, mean_s: h.count > 0 ? r2(h.sum / h.count) : null,
      p50_s: r2(histogramQuantile(0.5, h.buckets)), p95_s: r2(histogramQuantile(0.95, h.buckets)), p99_s: r2(histogramQuantile(0.99, h.buckets)),
    };
  }
  const rejections = {};
  for (const [key, v] of Object.entries(series)) {
    const m = /^ubag_admission_rejections_total\{reason="([^"]+)"\}$/.exec(key);
    if (m) rejections[m[1]] = v.delta;
  }
  return { series, histograms, rejections_by_reason: rejections };
}

// --------------------------------------------------------------- HTTP client

export function makeRequest(cfg, fetchImpl = fetch) {
  return async function request(method, path, { body, headers = {}, timeoutMs = cfg.requestTimeoutMs } = {}) {
    const t0 = now();
    try {
      const res = await fetchImpl(cfg.baseUrl + path, {
        method, body, redirect: 'error', signal: AbortSignal.timeout(timeoutMs),
        headers: { Accept: 'application/json', 'Content-Type': 'application/json', 'Ubag-Api-Version': API_VERSION, Authorization: `Bearer ${cfg.apiKey}`, ...headers },
      });
      const text = await res.text();
      let json; try { json = JSON.parse(text); } catch { /* non-JSON body */ }
      return { status: res.status, headers: res.headers, json, text, ms: now() - t0 };
    } catch (e) {
      const timeout = e?.name === 'TimeoutError' || e?.name === 'AbortError' || e?.cause?.name === 'TimeoutError';
      return { status: 0, error: timeout ? 'timeout' : 'network', detail: String(e?.cause?.code ?? e?.message ?? e), ms: now() - t0 };
    }
  };
}

// ------------------------------------------------------------- scenario core

function idemKey(ctx, tag, i) {
  const key = `lt-${ctx.runId}-${tag}-${i}`;
  if (key.length < 16 || key.length > 128) throw new Error(`bad idempotency key length: ${key}`);
  return key;
}

const jobPayload = (cfg, key, prompt) => ({
  api_version: API_VERSION, idempotency_key: key,
  client: { app_id: 'ubag-load-acceptance', app_version: '1.0.0', sdk: { name: 'ubag-load', version: '1.0.0' } },
  job: { target: cfg.target, command_type: cfg.commandType, input: { prompt } },
});

/** Run `fn` retrying on retryStatuses, backing off per Retry-After, up to cfg.maxRetries. */
async function callWithRetry(ctx, op, fn, { retryStatuses = [429, 503], observeOpts } = {}) {
  const { cfg, rec } = ctx;
  for (let attempt = 0; ; attempt += 1) {
    const res = await fn();
    rec.observe(op, res, observeOpts);
    if (!retryStatuses.includes(res.status)) return { res, attempts: attempt + 1 };
    if (attempt >= cfg.maxRetries) { rec.retry.exhausted += 1; return { res, attempts: attempt + 1, exhausted: true }; }
    const hint = parseRetryAfterMs(res.headers, res.json);
    const delay = retryDelayMs({ attempt, retryAfterMs: hint, capMs: cfg.retryCapMs, jitter: cfg.jitter });
    rec.retry.retries += 1; rec.retry.sleptMs += delay;
    if (hint != null) { if (delay < hint) rec.retry.capped += 1; else rec.retry.honored += 1; }
    await ctx.sleep(delay);
  }
}

// The idempotency key doubles as the job's nonce: it is appended to the prompt (identical across
// duplicate requests for one key, unique per job) so the result body can be checked against it.
async function createJob(ctx, key, { prompt = 'load job', retryStatuses, op = 'create' } = {}) {
  const t0 = now();
  const fullPrompt = `${prompt} [${key}]`;
  const body = JSON.stringify(jobPayload(ctx.cfg, key, fullPrompt));
  const { res, attempts } = await callWithRetry(ctx, op, () => ctx.request('POST', '/v1/jobs', { body, headers: { 'Idempotency-Key': key } }), { retryStatuses });
  const ok = (res.status === 200 || res.status === 202) && typeof res.json?.job_id === 'string';
  if (ok) { ctx.rec.latency(`${op}_incl_retries`, now() - t0); ctx.jobIds.add(res.json.job_id); }
  return { ok, status: res.status, jobId: res.json?.job_id, replay: res.json?.idempotent_replay === true, attempts, key, prompt: fullPrompt, acceptedAt: now(), res };
}

/** Poll many jobs with bounded concurrency until terminal / lost / deadline. */
async function pollMany(ctx, jobs, deadlineAt) {
  const { cfg, rec } = ctx;
  const states = jobs.map((j) => ({ ...j, next: 0, status: null, firstRunAt: null, terminalAt: null, lost: false }));
  let pending = states;
  while (pending.length && now() < deadlineAt) {
    const due = pending.filter((j) => j.next <= now());
    await pool(due, cfg.pollConcurrency, async (j) => {
      const res = await ctx.request('GET', `/v1/jobs/${encodeURIComponent(j.jobId)}`);
      rec.observe('poll', res);
      j.next = now() + cfg.pollIntervalMs;
      if (res.status === 200) {
        j.status = res.json?.status;
        if (!j.firstRunAt && j.status && !PRE_RUN.has(j.status)) j.firstRunAt = now();
        if (TERMINAL.has(j.status)) j.terminalAt = now();
      } else if (res.status === 404) j.lost = true;
      else if (res.status === 429 || res.status === 503) {
        j.next = now() + retryDelayMs({ attempt: 0, retryAfterMs: parseRetryAfterMs(res.headers, res.json), capMs: cfg.retryCapMs, jitter: cfg.jitter });
      }
    });
    pending = pending.filter((j) => !j.terminalAt && !j.lost);
    if (pending.length) await ctx.sleep(Math.max(5, Math.min(100, Math.min(...pending.map((j) => j.next)) - now())));
  }
  return states;
}

async function settleJobs(ctx, jobs, { count = true, verify = true } = {}) {
  const states = await pollMany(ctx, jobs, now() + ctx.cfg.deadlineMs);
  const out = { completed: 0, warned: 0, failed: 0, lost: 0, unfinished: 0, byStatus: {} };
  for (const s of states) {
    const label = s.lost ? 'lost(404)' : s.terminalAt ? s.status : `unfinished(${s.status ?? 'unknown'})`;
    out.byStatus[label] = (out.byStatus[label] ?? 0) + 1;
    if (s.lost) { out.lost += 1; if (count) ctx.rec.count('lost_jobs'); } else if (!s.terminalAt) { out.unfinished += 1; if (count) ctx.rec.count('unfinished_jobs'); } else if (s.status === WARNED) { out.warned += 1; if (count) ctx.rec.count('warning_jobs'); } else if (!SUCCESS.has(s.status)) { out.failed += 1; if (count) ctx.rec.count('failed_jobs'); } else {
      out.completed += 1;
      ctx.rec.latency('completion', s.terminalAt - s.acceptedAt);
      if (s.firstRunAt) ctx.rec.latency('queue_wait_observed', s.firstRunAt - s.acceptedAt);
    }
  }
  if (verify) await pool(states.filter((s) => s.terminalAt && SUCCESS.has(s.status)), ctx.cfg.pollConcurrency, (s) => verifyJob(ctx, s));
  return { out, states };
}

// ------------------------------------------------- result / event / tenant integrity

const countOf = (text, needle) => (needle ? text.split(needle).length - 1 : 0);

/** Re-fetch a completed job and check the result body, the event log and (optionally) tenant isolation. */
async function verifyJob(ctx, s) {
  const { rec, cfg } = ctx;
  const res = await ctx.request('GET', `/v1/jobs/${encodeURIComponent(s.jobId)}`);
  rec.observe('result_fetch', res);
  const bad = (why) => rec.violation('result_mismatches', `${s.jobId}: ${why}`);
  if (res.status !== 200) bad(`result fetch returned HTTP ${res.status || res.error}`);
  else if (res.json?.job_id !== s.jobId) bad(`result carries job_id ${res.json?.job_id}`);
  else if (!SUCCESS.has(res.json?.status)) bad(`status changed to ${res.json?.status} after it was observed terminal`);
  else {
    const out = res.json?.result?.output;
    const text = out?.text ?? out?.markdown ?? out?.plain_text;
    if (typeof text !== 'string' || !text.trim()) bad('empty result text');
    else if (cfg.target === 'mock') { // the mock adapter echoes "...(<job_id>): <prompt>"; the nonce ends the prompt
      if (!text.includes(s.jobId)) bad('result text does not name this job (wrong result)');
      else if (!text.endsWith(s.prompt)) bad('result text does not end with the submitted prompt (truncated or wrong result)');
      else if (countOf(text, s.key) !== 1) bad(`nonce appears ${countOf(text, s.key)} times in the result (duplicated result)`);
      else rec.count('results_verified');
    } else rec.count('results_verified');
  }
  await verifyEvents(ctx, s);
  if (cfg.apiKeyB && ctx.tenantBudget > 0) { ctx.tenantBudget -= 1; await probeTenant(ctx, s.jobId); }
}

async function verifyEvents(ctx, s) {
  const { rec } = ctx; const events = []; let cursor = null;
  const bad = (why) => rec.violation('terminal_event_violations', `${s.jobId}: ${why}`);
  for (let page = 0; page < 20; page += 1) { // bounded: 20 pages x 100 events
    const res = await ctx.request('GET', `/v1/jobs/${encodeURIComponent(s.jobId)}/events?limit=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`);
    rec.observe('events_fetch', res);
    if (res.status !== 200 || !Array.isArray(res.json?.events)) return bad(`events fetch returned HTTP ${res.status || res.error}`);
    events.push(...res.json.events);
    cursor = res.json.next_cursor ?? null;
    if (!cursor) break;
  }
  if (cursor) return bad('event log exceeds 2000 events (not fully read)');
  if (events.some((e) => e?.job_id !== undefined && e.job_id !== s.jobId)) return bad("event log contains another job's events");
  const seqs = events.map((e) => e?.sequence).filter(Number.isFinite);
  if (new Set(seqs).size !== seqs.length) return bad('duplicate event sequence numbers');
  const terminal = events.filter((e) => TERMINAL_EVENTS.has(String(e?.type ?? '').replace(/^job\./, '')));
  if (terminal.length !== 1) return bad(`${terminal.length} terminal events (expected exactly 1)`);
  rec.count('events_verified');
}

/** Tenant B must get 403/404 on tenant A's job (GET, events, artifacts) and must not see it in its list. */
async function probeTenant(ctx, jobId) {
  const { rec, cfg } = ctx; const as = { headers: { Authorization: `Bearer ${cfg.apiKeyB}` } };
  const id = encodeURIComponent(jobId);
  for (const [op, path] of [['tenant_get', `/v1/jobs/${id}`], ['tenant_events', `/v1/jobs/${id}/events`], ['tenant_artifacts', `/v1/jobs/${id}/artifacts`]]) {
    const { res } = await callWithRetry(ctx, op, () => ctx.request('GET', path, as));
    if (res.status === 429 || res.status === 503 || res.status === 0) { rec.count('tenant_probe_inconclusive'); continue; }
    rec.count('tenant_probe_requests');
    if (res.status >= 200 && res.status < 300) rec.violation('cross_tenant_leaks', `${op}: tenant B read tenant A job ${jobId} (HTTP ${res.status})`);
    else if (res.status !== 403 && res.status !== 404) rec.violation('tenant_probe_unexpected', `${op}: expected 403/404, got HTTP ${res.status}`);
  }
  const { res } = await callWithRetry(ctx, 'tenant_list', () => ctx.request('GET', '/v1/jobs?limit=100', as));
  if (res.status === 429 || res.status === 503 || res.status === 0) return rec.count('tenant_probe_inconclusive');
  rec.count('tenant_probe_requests');
  if (res.status !== 200) return rec.violation('tenant_probe_unexpected', `tenant_list: expected 200, got HTTP ${res.status}`);
  const leaked = (res.json?.jobs ?? []).filter((j) => ctx.jobIds.has(j?.job_id));
  if (leaked.length) rec.violation('cross_tenant_leaks', `tenant_list: tenant B listing contains ${leaked.length} of tenant A's jobs`);
}

const statusesOf = (ctx, op) => ({ ...(ctx.rec.entry(op).statuses) });

// ----------------------------------------------------------------- scenarios

async function queue1000(ctx) {
  const { cfg, rec } = ctx;
  const interval = 1000 / cfg.rate; const t0 = now(); const accepted = [];
  await pool(range(cfg.jobs), cfg.createConcurrency, async (i) => {
    const wait = t0 + i * interval - now();
    if (wait > 0) await ctx.sleep(wait);
    const r = await createJob(ctx, idemKey(ctx, 'q', i), { prompt: `load queue job ${i}` });
    if (r.ok) accepted.push(r); else rec.count('unaccepted_jobs');
  });
  const enqueueMs = now() - t0;
  const ids = new Set(accepted.map((a) => a.jobId));
  if (ids.size !== accepted.length) rec.violation('queue_id_collisions', `${accepted.length - ids.size} distinct keys mapped to an existing job id`);
  const { out } = await settleJobs(ctx, accepted);
  return {
    requested: cfg.jobs, accepted: accepted.length, enqueue_ms: r2(enqueueMs), create_attempt_statuses: statusesOf(ctx, 'create'),
    replays_on_unique_keys: accepted.filter((a) => a.replay).length, final: out.byStatus,
    retry: { ...rec.retry },
  };
}

async function runKind(ctx, kind, c, it) {
  const { cfg, rec } = ctx; const tag = `${c}-${it}`;
  const unsafeUnless = (label, res, ok) => {
    if (res.status === 429 || res.status === 503) return rec.count(`${label}_inconclusive_overload`);
    if (!ok(res)) rec.violation('unsafe_payload_outcomes', `${label}: HTTP ${res.status || res.error}`);
  };
  const is4xx = (res) => res.status >= 400 && res.status < 500;
  if (kind === 'text' || kind === 'upload') {
    const job = await createJob(ctx, idemKey(ctx, 'c', tag), { prompt: `client ${c} iteration ${it}` });
    if (!job.ok) { rec.count('unaccepted_jobs'); return; }
    if (kind === 'upload') {
      const up = await ctx.request('PUT', `/v1/jobs/${job.jobId}/artifacts/load-${tag}.txt`, { body: 'load attachment', headers: { 'Content-Type': 'text/plain' } });
      rec.observe('upload', up); // any 2xx/4xx/overload is fine; 5xx and hangs are counted by observe
    }
    await settleJobs(ctx, [job]);
  } else if (kind === 'facade_image') {
    const body = JSON.stringify({ model: cfg.target, stream: false, messages: [{ role: 'user', content: [{ type: 'text', text: 'describe the image' }, { type: 'image_url', image_url: { url: PNG_DATA_URL } }] }] });
    const { res } = await callWithRetry(ctx, 'facade_image', () => ctx.request('POST', '/v1/openai/chat/completions', { body, timeoutMs: cfg.facadeTimeoutMs }));
    const content = res.json?.choices?.[0]?.message?.content;
    if (res.status !== 200 || typeof content !== 'string' || !content) rec.violation('facade_image_failures', `valid image request: HTTP ${res.status || res.error}`);
  } else if (kind === 'malformed') {
    const key = idemKey(ctx, 'm', tag);
    const bad = await ctx.request('POST', '/v1/jobs', { body: '{"job": {', headers: { 'Idempotency-Key': key } });
    rec.observe('malformed_json', bad); unsafeUnless('malformed_json', bad, is4xx);
    const badFile = JSON.stringify({ model: cfg.target, stream: false, messages: [{ role: 'user', content: [{ type: 'text', text: 'x' }, { type: 'image_url', image_url: { url: 'data:image/png;base64,@@not-base64@@' } }] }] });
    const bf = await ctx.request('POST', '/v1/openai/chat/completions', { body: badFile });
    rec.observe('malformed_file', bf); unsafeUnless('malformed_file', bf, is4xx);
  } else if (kind === 'oversized') {
    const res = await ctx.request('POST', '/v1/jobs', { body: ctx.oversizeBody, headers: { 'Idempotency-Key': idemKey(ctx, 'o', tag) } });
    rec.observe('oversized', res, { allowReset: true });
    if (res.status === 0 && res.error !== 'timeout') rec.count('oversized_safe_resets');
    else if (res.status !== 0) unsafeUnless('oversized', res, (r) => r.status === 413);
  }
}

async function clients100(ctx) {
  const { cfg } = ctx;
  const kinds = ['text', 'facade_image', 'malformed', 'oversized', ...(cfg.withUpload ? ['upload'] : [])];
  ctx.oversizeBody = Buffer.from(JSON.stringify({ pad: 'x'.repeat(cfg.maxBodyBytes + 65_536) }));
  const counts = {};
  await pool(range(cfg.clients), cfg.clients, async (c) => {
    for (let it = 0; it < cfg.iterations; it += 1) {
      const kind = kinds[(c + it) % kinds.length];
      counts[kind] = (counts[kind] ?? 0) + 1;
      await runKind(ctx, kind, c, it);
    }
  });
  return { clients: cfg.clients, iterations: cfg.iterations, requests_by_kind: counts, ops: Object.fromEntries(Object.entries(ctx.rec.opsTable()).filter(([k]) => k.startsWith('clients-100/')).map(([k, v]) => [k, v.statuses])), retry: { ...ctx.rec.retry } };
}

async function duplicates(ctx) {
  const { cfg, rec } = ctx; const perKey = [];
  for (let k = 0; k < cfg.dupKeys; k += 1) {
    const key = idemKey(ctx, 'd', k);
    const results = await Promise.all(range(cfg.dupConcurrency).map(() => createJob(ctx, key, { prompt: `duplicate ${k}`, retryStatuses: [409, 429, 503] })));
    const ok = results.filter((r) => r.ok); const ids = new Set(ok.map((r) => r.jobId));
    const nonReplay = ok.filter((r) => !r.replay).length;
    perKey.push({ key, responses: results.length, accepted: ok.length, distinct_jobs: ids.size, non_replay: nonReplay, replays: ok.length - nonReplay });
    if (ids.size !== 1) rec.violation('dup_violations', `${key}: ${ids.size} distinct jobs from ${results.length} concurrent identical requests`);
    if (ok.length < results.length) rec.count('unaccepted_jobs', results.length - ok.length);
    if (ok.length && nonReplay !== 1) rec.violation('dup_replay_flag_mismatch', `${key}: ${nonReplay} responses claimed to be the original (expected 1)`);
  }
  const distinct = perKey.map((p) => p.distinct_jobs);

  const cancelResults = { cancelled: 0, raced_to_completed: 0, other_terminal: 0, flipped: 0 };
  const jobs = [];
  await pool(range(cfg.cancelRaces), 10, async (i) => {
    const job = await createJob(ctx, idemKey(ctx, 'k', i), { prompt: `cancel race ${i}` });
    if (!job.ok) { rec.count('unaccepted_jobs'); return; }
    const ck = idemKey(ctx, 'x', i);
    const cancel = (k) => callWithRetry(ctx, 'cancel', () => ctx.request('POST', `/v1/jobs/${job.jobId}/cancel`, { body: '{}', headers: { 'Idempotency-Key': k } }));
    const results = await Promise.all([cancel(ck), cancel(ck), cancel(idemKey(ctx, 'y', i))]); // same-key pair + distinct key
    job.cancelAccepted = results.some((r) => r.res.status === 200 || r.res.status === 202);
    jobs.push(job);
  });
  const { states } = await settleJobs(ctx, jobs, { count: false, verify: false });
  await ctx.sleep(cfg.settleMs);
  await pool(states, 10, async (s) => {
    if (s.lost || !s.terminalAt) return rec.violation('cancel_inconsistencies', `${s.jobId}: ${s.lost ? 'job vanished' : `not terminal after cancel (status ${s.status})`}`);
    const again = await ctx.request('GET', `/v1/jobs/${s.jobId}`); rec.observe('cancel_recheck', again);
    if (again.status === 200 && again.json?.status !== s.status) { cancelResults.flipped += 1; return rec.violation('cancel_inconsistencies', `${s.jobId}: terminal state flipped ${s.status} -> ${again.json?.status}`); }
    if (s.status === 'cancelled' || s.status === 'canceled') cancelResults.cancelled += 1;
    else if (SUCCESS.has(s.status) || s.status === WARNED) cancelResults.raced_to_completed += 1;
    else cancelResults.other_terminal += 1;
    if (s.cancelAccepted === false && (s.status === 'cancelled' || s.status === 'canceled')) rec.violation('cancel_inconsistencies', `${s.jobId}: cancelled without an accepted cancel`);
  });
  return { keys: cfg.dupKeys, concurrency: cfg.dupConcurrency, per_key: perKey, cancel_races: cfg.cancelRaces, cancel_outcomes: cancelResults, retry: { ...rec.retry }, _summary: { dup_jobs_per_key_max: Math.max(...distinct), dup_jobs_per_key_min: Math.min(...distinct) } };
}

async function overload(ctx) {
  const { cfg, rec } = ctx;
  // Phase A: upload-memory pressure (large facade bodies admitted by Content-Length reservation).
  const big = Buffer.from(JSON.stringify({ model: cfg.target, stream: false, messages: [{ role: 'user', content: 'x'.repeat(cfg.burstBodyBytes) }] }));
  const upload = await Promise.all(range(cfg.burst).map(() => ctx.request('POST', '/v1/openai/chat/completions', { body: big, timeoutMs: cfg.facadeTimeoutMs })));
  // A server that refuses a multi-MiB body before reading it (the point of the
  // upload-memory budget) may reset the connection while the client is still
  // writing; that reset IS the early rejection, not a fault. Clients that send
  // `Expect: 100-continue` (curl does above 1 MiB) are refused cleanly instead.
  upload.forEach((r) => rec.observe('burst_upload', r, { allowReset: true }));
  // Phase B: in-flight request pressure with a list query heavy enough to overlap.
  const probe = await Promise.all(range(cfg.inflightBurst).map(() => ctx.request('GET', '/v1/jobs?limit=100')));
  probe.forEach((r) => rec.observe('burst_inflight', r));
  const rejected = [...upload, ...probe].filter((r) => r.status === 429 || r.status === 503).length;
  // Phase C: recovery.
  const t0 = now(); let healthy = false;
  while (now() - t0 < cfg.recoveryMs) {
    const [h, rd] = await Promise.all([ctx.request('GET', '/v1/health'), ctx.request('GET', '/v1/ready')]);
    if (h.status === 200 && rd.status === 200) { healthy = true; break; }
    await ctx.sleep(500);
  }
  if (!healthy) rec.violation('unrecovered', 'health/ready did not return 200 within the recovery window');
  const recovery = await pool(range(cfg.recoveryRequests), 5, async (i) => {
    const job = await createJob(ctx, idemKey(ctx, 'r', i), { prompt: `recovery ${i}` });
    if (!job.ok) rec.violation('unrecovered', `recovery create failed: HTTP ${job.status}`);
    return job;
  });
  const okJobs = recovery.filter((j) => j.ok);
  if (okJobs.length) await settleJobs(ctx, okJobs);
  return {
    _summary: { overload_rejections: rejected },
    burst_upload: { requests: cfg.burst, body_bytes: big.length, statuses: statusesOf(ctx, 'burst_upload') },
    burst_inflight: { requests: cfg.inflightBurst, statuses: statusesOf(ctx, 'burst_inflight') },
    rejected_responses: rejected, rejected_any: rejected > 0, recovered: healthy && okJobs.length === recovery.length,
    note: rejected === 0 ? 'no 429/503 produced: limits were not reached (raise --burst / --burst-body-bytes / --inflight-burst or lower the gateway limits on the test stack)' : undefined,
  };
}

/**
 * Steady-state: a modest, constant create rate plus a dedicated GET mix (job, events, list) for
 * --steady-seconds. Latencies land in steady-state/create and steady-state/read; the goal
 * thresholds (thresholds.goals.json) gate their p95, separate from the 100-client burst limit.
 */
async function steadyState(ctx) {
  const { cfg, rec } = ctx;
  const seeds = (await pool(range(cfg.steadySeedJobs), 5, (i) => createJob(ctx, idemKey(ctx, 's', i), { prompt: `steady seed ${i}`, op: 'seed_create' }))).filter((j) => j.ok);
  if (!seeds.length) throw new Error('steady-state: no seed job was accepted, nothing to read');
  const t0 = now(); const accepted = [];
  const nCreate = cfg.steadySeconds * cfg.steadyRate; const nRead = cfg.steadySeconds * cfg.steadyReadRate;
  const paced = (n, perSec, concurrency, fn) => pool(range(n), concurrency, async (i) => {
    const wait = t0 + (i * 1000) / perSec - now();
    if (wait > 0) await ctx.sleep(wait);
    return fn(i);
  });
  const readPaths = (id) => [`/v1/jobs/${encodeURIComponent(id)}`, `/v1/jobs/${encodeURIComponent(id)}/events?limit=100`, '/v1/jobs?limit=20'];
  await Promise.all([
    paced(nCreate, cfg.steadyRate, cfg.createConcurrency, async (i) => {
      const r = await createJob(ctx, idemKey(ctx, 'w', i), { prompt: `steady job ${i}`, retryStatuses: [] });
      if (r.ok) accepted.push(r); else rec.count('unaccepted_jobs');
    }),
    paced(nRead, cfg.steadyReadRate, cfg.pollConcurrency, async (i) => {
      const paths = readPaths(seeds[i % seeds.length].jobId);
      rec.observe('read', await ctx.request('GET', paths[i % paths.length]));
    }),
  ]);
  const { out } = await settleJobs(ctx, [...seeds, ...accepted]);
  return { seconds: cfg.steadySeconds, create_rate: cfg.steadyRate, read_rate: cfg.steadyReadRate, creates: nCreate, accepted: accepted.length, reads: nRead, final: out.byStatus };
}

async function snapshotScenario(ctx) {
  if (ctx.cfg.snapshotSeconds > 0) await ctx.sleep(ctx.cfg.snapshotSeconds * 1000);
  return { idle_window_seconds: ctx.cfg.snapshotSeconds };
}

const RUNNERS = { 'queue-1000': queue1000, 'clients-100': clients100, duplicates, 'steady-state': steadyState, overload, 'metrics-snapshot': snapshotScenario };

// -------------------------------------------------------------------- driver

export async function run(cfg, deps = {}) {
  const request = deps.request ?? makeRequest(cfg);
  const ctx = { cfg, request, rec: new Recorder(), sleep: deps.sleep ?? defaultSleep, runId: randomBytes(6).toString('hex'), jobIds: new Set(), tenantBudget: cfg.apiKeyB ? cfg.tenantProbeSamples : 0 };
  const started = new Date();
  const scrape = async () => {
    const res = await request('GET', '/v1/metrics', { headers: { Accept: 'text/plain' } });
    if (res.status !== 200) throw new Error(`/v1/metrics returned ${res.status || res.error}`);
    return parseProm(res.text);
  };

  const maxSampled = new Map();
  let before = null; let metricsError = null;
  try { before = await scrape(); } catch (e) { metricsError = e.message; }
  const sampler = setInterval(async () => {
    try {
      const m = await scrape();
      for (const [k, v] of m) if (GAUGES.some((g) => k.startsWith(g)) && v > (maxSampled.get(k) ?? -Infinity)) maxSampled.set(k, v);
    } catch { /* sampling is best-effort */ }
  }, cfg.metricsIntervalMs);
  sampler.unref?.();
  const stopCgroup = cfg.cgroupTargets.length ? startCgroupSampler(cfg.cgroupTargets, cfg.dockerIntervalMs, { exec: deps.dockerExec, rules: { ...PRESSURE_RULES, recoveryWindowMs: cfg.pressureWindowMs } }) : null;

  const report = { scenarios: {} }; const extra = {};
  for (const name of cfg.scenarios) {
    ctx.rec.scenario = name; const t0 = now();
    try {
      const result = await RUNNERS[name](ctx);
      Object.assign(extra, result._summary ?? {}); delete result._summary;
      report.scenarios[name] = { duration_ms: r2(now() - t0), ...result };
    } catch (e) {
      ctx.rec.violation('scenario_errors', `${name}: ${e.message}`);
      report.scenarios[name] = { duration_ms: r2(now() - t0), error: e.message };
    }
  }
  clearInterval(sampler);
  let metrics = { available: false, error: metricsError };
  if (before) {
    try { metrics = { available: true, ...metricsDelta(before, await scrape(), maxSampled) }; } catch (e) { metrics = { available: false, error: e.message }; }
  }
  const resources = stopCgroup ? await stopCgroup() : { skipped: 'not requested (--cgroup-containers)' };

  const summary = { ...ctx.rec.summary(extra), ...resources.summary };
  const thresholds = JSON.parse(readFileSync(cfg.thresholds, 'utf8'));
  if (cfg.requireGoals) Object.assign(thresholds, JSON.parse(readFileSync(cfg.goals, 'utf8'))); // goals tighten/extend the base set
  const verdict = { ...evaluateThresholds(summary, thresholds, { strict: cfg.requireGoals }), goals_required: cfg.requireGoals };
  return {
    meta: {
      run_id: ctx.runId, started_at: started.toISOString(), finished_at: new Date().toISOString(),
      target_origin: new URL(cfg.baseUrl).origin, node: process.version, platform: `${platform()}-${arch()}`,
      scenarios: cfg.scenarios, config: Object.fromEntries(Object.entries(cfg).filter(([k]) => !['apiKey', 'apiKeyB', 'baseUrl', 'scenarios'].includes(k))),
    },
    ...report, summary, ops: ctx.rec.opsTable(), classes: ctx.rec.classes, error_codes: ctx.rec.errorCodes,
    client_rejections_by_reason: ctx.rec.rejections, retry: ctx.rec.retry, violations: ctx.rec.violations,
    metrics, resources, thresholds: verdict,
  };
}

// ------------------------------------------------------------------- markdown

const cell = (v) => (v == null ? '-' : String(v));
const table = (head, rows) => [`| ${head.join(' | ')} |`, `| ${head.map(() => '---').join(' | ')} |`, ...rows.map((r) => `| ${r.map(cell).join(' | ')} |`)].join('\n');

export function renderMarkdown(report) {
  const m = report.meta; const lines = [];
  lines.push(`# UBAG acceptance load report`, '', `- run: \`${m.run_id}\` (${m.started_at} -> ${m.finished_at})`, `- target: ${m.target_origin}`, `- scenarios: ${m.scenarios.join(', ')}`, `- verdict: **${report.thresholds.passed ? 'PASS' : 'FAIL'}**${report.thresholds.goals_required ? ' (--require-goals: unmeasured = FAIL)' : ''}`, '');
  lines.push('## Thresholds', '', table(['threshold', 'limit', 'actual', 'result'], report.thresholds.results.map((r) => [r.name, r.limit, r.actual, r.ok ? `ok${r.note ? ` (${r.note})` : ''}` : 'VIOLATED'])), '');
  lines.push('## Latency by operation (ms)', '', table(['scenario/op', 'attempts', 'ok n', 'ok p50', 'ok p95', 'ok p99', 'all p95', 'max', 'statuses'],
    Object.entries(report.ops).map(([k, v]) => [k, v.all.count, v.ok.count, v.ok.p50, v.ok.p95, v.ok.p99, v.all.p95, v.all.max, JSON.stringify(v.statuses)])), '');
  lines.push('## Error classes', '', table(['class', 'count'], Object.entries(report.classes)), '');
  lines.push('## Rejections by reason', '', table(['reason', 'client-observed', 'gateway metric delta'], [...new Set([...Object.keys(report.client_rejections_by_reason), ...Object.keys(report.metrics.rejections_by_reason ?? {})])].map((r) => [r, report.client_rejections_by_reason[r] ?? 0, report.metrics.rejections_by_reason?.[r] ?? 'n/a'])), '');
  lines.push(`Retry-After handling: ${report.retry.retries} retries, ${report.retry.honored} honored in full, ${report.retry.capped} truncated by --retry-cap-ms, ${report.retry.exhausted} gave up after --max-retries, ${report.retry.sleptMs} ms slept.`, '');
  const q = report.scenarios['queue-1000'];
  if (q && !q.error) lines.push('## queue-1000', '', `requested ${q.requested}, accepted ${q.accepted}, enqueue took ${q.enqueue_ms} ms`, `create attempt statuses: \`${JSON.stringify(q.create_attempt_statuses)}\``, `final states: \`${JSON.stringify(q.final)}\``, '');
  const d = report.scenarios.duplicates;
  if (d && !d.error) lines.push('## duplicates', '', table(['key', 'responses', 'accepted', 'distinct jobs', 'original', 'replays'], d.per_key.map((p) => [p.key, p.responses, p.accepted, p.distinct_jobs, p.non_replay, p.replays])), '', `cancel outcomes: \`${JSON.stringify(d.cancel_outcomes)}\``, '');
  const st = report.scenarios['steady-state'];
  if (st && !st.error) lines.push('## steady-state', '', `${st.seconds}s at ${st.create_rate} creates/s and ${st.read_rate} reads/s; accepted ${st.accepted}/${st.creates}; final states: \`${JSON.stringify(st.final)}\``, '');
  const o = report.scenarios.overload;
  if (o && !o.error) lines.push('## overload', '', `rejected responses: ${o.rejected_responses}; recovered: ${o.recovered}`, o.note ? `note: ${o.note}` : '', '');
  lines.push('## Gateway metrics (before -> after)', '');
  if (!report.metrics.available) lines.push(`unavailable: ${report.metrics.error ?? 'unknown'}`, '');
  else {
    lines.push(table(['histogram', 'count', 'mean s', 'p50 s', 'p95 s', 'p99 s'], Object.entries(report.metrics.histograms).map(([k, h]) => [k, h.count, h.mean_s, h.p50_s, h.p95_s, h.p99_s])), '');
    lines.push(table(['series', 'before', 'after', 'delta', 'max sampled'], Object.entries(report.metrics.series).map(([k, v]) => [k, v.before, v.after, v.delta, v.max_sampled])), '');
  }
  lines.push('## Resource usage (cgroup, NON-AUTHORITATIVE off the lab host)', '');
  const res = report.resources;
  if (res.skipped) lines.push(`skipped: ${res.skipped}`, '');
  if (res.containers) {
    lines.push(table(['role', 'container', 'cgroup', 'samples', 'cpu cores avg', 'throttled %', 'mem peak MB', 'mem limit MB', 'headroom %', 'oom_kill', 'psi cpu/mem avg10 max'],
      Object.entries(res.containers).map(([role, c]) => (c.skipped ? [role, c.container, `skipped: ${c.skipped}`] : [role, c.container, c.cgroup, c.samples, c.cpu_cores_avg, c.cpu_throttled_pct, c.memory_peak_mb, c.memory_limit_mb ?? 'unlimited', c.memory_headroom_pct, c.oom_kills, `${c.psi_cpu_some_avg10_max ?? '-'}/${c.psi_memory_some_avg10_max ?? '-'}`]))), '');
    const hp = res.host_pressure;
    if (hp?.sampled) lines.push(`Helper pressure rules (CPU>${hp.rules.cpuHighPct}%, mem available<${hp.rules.memAvailLowPct}%, recovery <${hp.rules.cpuRecoverPct}%/>${hp.rules.memAvailRecoverPct}% for ${hp.rules.recoveryWindowMs} ms): cpu_over_80=${hp.cpu_over_80}, mem_available_under_20=${hp.mem_available_under_20}, pressure_triggered=${hp.pressure_triggered}, recovered=${hp.recovered}`, '');
    else lines.push('Helper pressure rules: host CPU/memory not sampled.', '');
  }
  if (report.violations.length) lines.push('## Violation samples', '', ...report.violations.map((v) => `- [${v.scenario}] ${v.counter}: ${v.detail}`), '');
  return lines.join('\n');
}

export async function main(argv = process.argv.slice(2), env = process.env, deps = {}) {
  let cfg;
  try { cfg = parseArgs(argv, env); } catch (e) { console.error(`[load] refused: ${e.message}`); return 2; }
  console.log(`[load] ${cfg.scenarios.join(',')} -> ${new URL(cfg.baseUrl).origin}`);
  let report;
  try { report = await run(cfg, deps); } catch (e) { console.error(`[load] harness error: ${e.message}`); return 2; }
  const dir = resolve(cfg.outDir, report.meta.started_at.replace(/[:.]/g, '-'));
  mkdirSync(dir, { recursive: true });
  const markdown = renderMarkdown(report);
  writeFileSync(join(dir, 'report.json'), `${JSON.stringify(report, null, 2)}\n`);
  writeFileSync(join(dir, 'summary.md'), `${markdown}\n`);
  console.log(markdown);
  console.log(`[load] report written to ${dir}`);
  return report.thresholds.passed ? 0 : 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().then((code) => process.exit(code));
}
