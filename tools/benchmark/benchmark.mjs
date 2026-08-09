import { execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { arch, platform } from 'node:os';
import { performance } from 'node:perf_hooks';

export const API_VERSION = '2026-05-22';

const DEFAULT_BASE_URL = 'http://127.0.0.1:8080';
const FIXED_PROMPT = 'Return the exact text: UBAG_BENCHMARK_OK';
const MAX_RESPONSE_BYTES = 1024 * 1024;
const SUCCESS_STATUSES = new Set(['completed', 'completed_with_warnings']);
const TERMINAL_STATUSES = new Set([
  ...SUCCESS_STATUSES,
  'failed_retryable',
  'failed_terminal',
  'dead_letter',
  'cancelled',
  'timed_out',
]);
const KNOWN_STATUSES = new Set([
  'created',
  'queued',
  'assigned',
  'running',
  'token_streaming',
  'completing',
  ...TERMINAL_STATUSES,
]);

const VALUE_OPTIONS = new Set([
  'app-secret',
  'base-url',
  'output',
  'poll-interval-ms',
  'samples',
  'scenario',
  'timeout-ms',
  'warmups',
]);
const FLAG_OPTIONS = new Set(['allow-remote', 'json']);

function optionValue(args, index) {
  const token = args[index];
  const equals = token.indexOf('=');
  if (equals !== -1) {
    return { name: token.slice(2, equals), value: token.slice(equals + 1), next: index };
  }
  const name = token.slice(2);
  const value = args[index + 1];
  if (value === undefined || value.startsWith('--')) {
    throw new Error(`--${name} requires a value`);
  }
  return { name, value, next: index + 1 };
}

function boundedInteger(name, raw, minimum, maximum) {
  if (!/^(0|[1-9][0-9]*)$/.test(String(raw))) {
    throw new Error(`--${name} must be an integer from ${minimum} to ${maximum}`);
  }
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < minimum || value > maximum) {
    throw new Error(`--${name} must be an integer from ${minimum} to ${maximum}`);
  }
  return value;
}

function isLoopback(hostname) {
  return hostname === 'localhost'
    || hostname === '[::1]'
    || /^127(?:\.[0-9]{1,3}){3}$/.test(hostname);
}

function normalizeBaseUrl(raw, allowRemote) {
  let url;
  try {
    url = new URL(raw);
  } catch {
    throw new Error('--base-url must be a valid HTTP or HTTPS URL');
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new Error('--base-url must use HTTP or HTTPS');
  }
  if (url.username || url.password || url.search || url.hash) {
    throw new Error('--base-url must not contain credentials, query parameters, or a fragment');
  }
  const loopback = isLoopback(url.hostname);
  if (!allowRemote && !loopback) {
    throw new Error('non-loopback base URLs require explicit --allow-remote');
  }
  if (!loopback && url.protocol !== 'https:') {
    throw new Error('non-loopback base URLs must use HTTPS');
  }
  url.pathname = url.pathname.replace(/\/+$/, '');
  return url.toString().replace(/\/$/, '');
}

export function buildConfig(args, env = process.env) {
  const parsed = {
    allowRemote: false,
    appSecret: undefined,
    baseUrl: env.UBAG_BASE_URL ?? env.UBAG_GATEWAY_URL ?? DEFAULT_BASE_URL,
    json: false,
    output: undefined,
    pollIntervalMs: 50,
    samples: 10,
    scenario: 'acceptance',
    timeoutMs: 30_000,
    warmups: 1,
  };

  for (let index = 0; index < args.length; index += 1) {
    const token = args[index];
    if (!token.startsWith('--')) {
      throw new Error(`unexpected positional argument at position ${index + 1}`);
    }
    const plainName = token.slice(2).split('=', 1)[0];
    if (FLAG_OPTIONS.has(plainName)) {
      if (token.includes('=')) throw new Error(`--${plainName} does not accept a value`);
      if (plainName === 'allow-remote') parsed.allowRemote = true;
      if (plainName === 'json') parsed.json = true;
      continue;
    }
    if (!VALUE_OPTIONS.has(plainName)) {
      throw new Error(`unknown option --${plainName}`);
    }
    const { name, value, next } = optionValue(args, index);
    index = next;
    switch (name) {
      case 'app-secret':
        parsed.appSecret = value || undefined;
        break;
      case 'base-url':
        parsed.baseUrl = value;
        break;
      case 'output':
        if (value.trim().length === 0) {
          throw new Error('--output must be a non-empty path');
        }
        parsed.output = value;
        break;
      case 'poll-interval-ms':
        parsed.pollIntervalMs = boundedInteger(name, value, 1, 60_000);
        break;
      case 'samples':
        parsed.samples = boundedInteger(name, value, 1, 10_000);
        break;
      case 'scenario':
        parsed.scenario = value;
        break;
      case 'timeout-ms':
        parsed.timeoutMs = boundedInteger(name, value, 1, 3_600_000);
        break;
      case 'warmups':
        parsed.warmups = boundedInteger(name, value, 0, 1_000);
        break;
      default:
        throw new Error(`unknown option --${name}`);
    }
  }

  if (!parsed.appSecret && env.UBAG_APP_SECRET) {
    parsed.appSecret = env.UBAG_APP_SECRET;
  }
  if (!['acceptance', 'mock-e2e'].includes(parsed.scenario)) {
    throw new Error('--scenario must be acceptance or mock-e2e');
  }
  parsed.baseUrl = normalizeBaseUrl(parsed.baseUrl, parsed.allowRemote);
  return parsed;
}

function endpoint(baseUrl, path) {
  const url = new URL(baseUrl);
  url.pathname = `${url.pathname.replace(/\/+$/, '')}/${path.replace(/^\/+/, '')}`;
  return url;
}

function requestHeaders(config, idempotencyKey) {
  const headers = {
    Accept: 'application/json',
    'Content-Type': 'application/json',
    'Ubag-Api-Version': API_VERSION,
  };
  if (idempotencyKey) headers['Idempotency-Key'] = idempotencyKey;
  if (config.appSecret) headers.Authorization = `Bearer ${config.appSecret}`;
  return headers;
}

function createPayload(idempotencyKey) {
  return {
    api_version: API_VERSION,
    idempotency_key: idempotencyKey,
    client: {
      app_id: 'ubag-benchmark',
      app_version: '1.0.0',
      sdk: { name: 'ubag-benchmark', version: '1.0.0' },
    },
    job: {
      target: 'mock',
      command_type: 'chat.prompt',
      input: { prompt: FIXED_PROMPT },
    },
  };
}

async function readBoundedJson(response) {
  const reader = response.body?.getReader();
  if (!reader) throw new Error('benchmark endpoint returned malformed JSON');
  const chunks = [];
  let size = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > MAX_RESPONSE_BYTES) {
      await reader.cancel();
      throw new Error('benchmark endpoint response exceeded 1 MiB');
    }
    chunks.push(value);
  }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  try {
    return JSON.parse(new TextDecoder().decode(bytes));
  } catch {
    throw new Error('benchmark endpoint returned malformed JSON');
  }
}

async function requestJson(url, options, deadline, expectedStatus = 200) {
  const remaining = deadline - performance.now();
  if (remaining <= 0) throw new Error('benchmark sample timed out');
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), remaining);
  try {
    const response = await fetch(url, {
      ...options,
      redirect: 'error',
      signal: controller.signal,
    });
    if (response.status !== expectedStatus) {
      await response.body?.cancel();
      throw new Error(`benchmark request failed with HTTP ${response.status}`);
    }
    const data = await readBoundedJson(response);
    return { data, headers: response.headers };
  } catch (error) {
    if (controller.signal.aborted) throw new Error('benchmark sample timed out');
    if (String(error?.cause?.message || error?.message || '').toLowerCase().includes('redirect')) {
      throw new Error('benchmark request rejected an HTTP redirect');
    }
    throw error;
  } finally {
    clearTimeout(timer);
  }
}

function validateJobResponse(value, expectedJobId) {
  const valid = value
    && typeof value === 'object'
    && !Array.isArray(value)
    && value.api_version === API_VERSION
    && typeof value.job_id === 'string'
    && /^job_[A-Za-z0-9]+$/.test(value.job_id)
    && (expectedJobId === undefined || value.job_id === expectedJobId)
    && typeof value.idempotent_replay === 'boolean'
    && KNOWN_STATUSES.has(value.status)
    && value.target === 'mock'
    && value.metadata
    && typeof value.metadata === 'object'
    && !Array.isArray(value.metadata)
    && typeof value.trace_id === 'string'
    && value.trace_id.length > 0
    && value.events_url === `/v1/jobs/${value.job_id}/events`;
  if (!valid) throw new Error('benchmark endpoint returned a malformed job response');
  return value;
}

function canonicalResourceUrl(baseUrl, location, jobId) {
  const fallback = endpoint(baseUrl, `/v1/jobs/${jobId}`);
  if (!location) return fallback;
  let candidate;
  try {
    candidate = new URL(location, fallback);
  } catch {
    throw new Error('benchmark endpoint returned a malformed job Location');
  }
  if (candidate.origin !== fallback.origin
      || candidate.username
      || candidate.password
      || candidate.search
      || candidate.hash
      || candidate.pathname !== fallback.pathname) {
    throw new Error('benchmark endpoint returned a malformed job Location');
  }
  return candidate;
}

function parseTimestamp(value) {
  if (typeof value !== 'string') return undefined;
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : undefined;
}

function validEventSequence(value) {
  return Number.isSafeInteger(value) && value >= 1 ? value : undefined;
}

function deriveFromEvents(value, expectedJobId) {
  if (!value
      || typeof value !== 'object'
      || value.api_version !== API_VERSION
      || value.job_id !== expectedJobId
      || !Array.isArray(value.events)
      || typeof value.trace_id !== 'string') {
    throw new Error('benchmark endpoint returned a malformed job events response');
  }
  for (const event of value.events) {
    if (!event
        || typeof event !== 'object'
        || !/^evt_[A-Za-z0-9]+$/.test(event.event_id)
        || event.job_id !== expectedJobId
        || event.api_version !== API_VERSION
        || typeof event.type !== 'string'
        || parseTimestamp(event.created_at) === undefined
        || !event.data
        || typeof event.data !== 'object'
        || typeof event.trace_id !== 'string'
        || event.trace_id.length === 0) {
      throw new Error('benchmark endpoint returned a malformed job events response');
    }
  }
  const firstTime = (types) => {
    const matching = [];
    for (const event of value.events) {
      if (!event || !types.has(event.type)) continue;
      const timestamp = parseTimestamp(event.created_at);
      if (timestamp === undefined) continue;
      matching.push({
        eventId: event.event_id,
        sequence: validEventSequence(event.sequence),
        timestamp,
      });
    }
    const sequenced = matching.filter((event) => event.sequence !== undefined);
    const ordered = sequenced.length > 0 ? sequenced : matching;
    ordered.sort((left, right) => {
      if (sequenced.length > 0 && left.sequence !== right.sequence) {
        return left.sequence - right.sequence;
      }
      if (left.timestamp !== right.timestamp) {
        return left.timestamp - right.timestamp;
      }
      return left.eventId.localeCompare(right.eventId);
    });
    return ordered[0]?.timestamp;
  };
  const queued = firstTime(new Set(['queued']));
  const assigned = firstTime(new Set(['assigned']));
  const completed = firstTime(new Set(['completed', 'completed_with_warnings']));
  const timing = {};
  if (queued !== undefined && assigned !== undefined && assigned >= queued) {
    timing.queue_ms = assigned - queued;
  }
  if (assigned !== undefined && completed !== undefined && completed >= assigned) {
    timing.worker_ms = completed - assigned;
  }
  return timing;
}

function safeEventsUrl(baseUrl, job) {
  const expected = endpoint(baseUrl, job.events_url);
  if (expected.pathname !== endpoint(baseUrl, `/v1/jobs/${job.job_id}/events`).pathname) {
    throw new Error('benchmark endpoint returned a malformed events URL');
  }
  return expected;
}

function sleepWithinDeadline(milliseconds, deadline) {
  const remaining = deadline - performance.now();
  if (remaining <= 0) return Promise.reject(new Error('benchmark sample timed out'));
  return new Promise((resolve) => setTimeout(resolve, Math.min(milliseconds, remaining)));
}

async function runSample(config) {
  const startedAt = performance.now();
  const deadline = startedAt + config.timeoutMs;
  const idempotencyKey = `bench:${randomUUID()}`;
  const createUrl = endpoint(config.baseUrl, '/v1/jobs');
  const created = await requestJson(createUrl, {
    method: 'POST',
    headers: requestHeaders(config, idempotencyKey),
    body: JSON.stringify(createPayload(idempotencyKey)),
  }, deadline, 202);
  const acceptedJob = validateJobResponse(created.data);
  if (acceptedJob.idempotent_replay) {
    throw new Error('benchmark endpoint replayed a supposedly unique request');
  }
  if (TERMINAL_STATUSES.has(acceptedJob.status) && !SUCCESS_STATUSES.has(acceptedJob.status)) {
    throw new Error(`benchmark job ended in ${acceptedJob.status}`);
  }
  const acceptanceMs = performance.now() - startedAt;
  if (config.scenario === 'acceptance') return { acceptance_ms: acceptanceMs };

  const resourceUrl = canonicalResourceUrl(
    config.baseUrl,
    created.headers.get('location'),
    acceptedJob.job_id,
  );
  let terminalJob;
  while (!terminalJob) {
    const polled = await requestJson(resourceUrl, {
      method: 'GET',
      headers: requestHeaders(config),
    }, deadline);
    const job = validateJobResponse(polled.data, acceptedJob.job_id);
    if (TERMINAL_STATUSES.has(job.status)) {
      terminalJob = job;
      break;
    }
    await sleepWithinDeadline(config.pollIntervalMs, deadline);
  }

  const totalMs = performance.now() - startedAt;
  if (!SUCCESS_STATUSES.has(terminalJob.status)) {
    throw new Error(`benchmark job ended in ${terminalJob.status}`);
  }

  const events = await requestJson(safeEventsUrl(config.baseUrl, terminalJob), {
    method: 'GET',
    headers: requestHeaders(config),
  }, deadline);
  const timing = deriveFromEvents(events.data, terminalJob.job_id);
  return { acceptance_ms: acceptanceMs, total_ms: totalMs, ...timing };
}

function gitValue(args) {
  try {
    return execFileSync('git', args, {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim();
  } catch {
    return '';
  }
}

function metadata(config) {
  const commit = gitValue(['rev-parse', 'HEAD']);
  return {
    git_commit: commit || null,
    git_dirty: gitValue(['status', '--porcelain']).length > 0,
    os: platform(),
    arch: arch(),
    node_version: process.version,
    timestamp_utc: new Date().toISOString(),
    scenario: config.scenario,
    base_url: config.baseUrl,
    auth_configured: Boolean(config.appSecret),
    warmups: config.warmups,
    samples: config.samples,
    poll_interval_ms: config.pollIntervalMs,
    timeout_ms: config.timeoutMs,
    timing_basis: {
      queue_ms: 'queued event to gateway assigned event',
      worker_ms: 'gateway assigned event to successful terminal event',
    },
  };
}

function percentile(sorted, fraction) {
  if (sorted.length === 1) return sorted[0];
  const position = (sorted.length - 1) * fraction;
  const lower = Math.floor(position);
  const upper = Math.ceil(position);
  const weight = position - lower;
  return sorted[lower] + (sorted[upper] - sorted[lower]) * weight;
}

export function calculateStats(samples) {
  if (!Array.isArray(samples) || samples.length === 0 || samples.some((value) => !Number.isFinite(value))) {
    throw new Error('statistics require at least one finite numeric sample');
  }
  const sorted = [...samples].sort((left, right) => left - right);
  const mean = samples.reduce((sum, value) => sum + value, 0) / samples.length;
  const sampleVariance = samples.length === 1
    ? 0
    : samples.reduce((sum, value) => sum + ((value - mean) ** 2), 0) / (samples.length - 1);
  return {
    count: samples.length,
    min: sorted[0],
    max: sorted.at(-1),
    mean,
    p50: percentile(sorted, 0.5),
    p95: percentile(sorted, 0.95),
    p99: percentile(sorted, 0.99),
    sample_variance: sampleVariance,
  };
}

export async function runBenchmark(config) {
  for (let index = 0; index < config.warmups; index += 1) {
    await runSample(config);
  }
  const collected = [];
  for (let index = 0; index < config.samples; index += 1) {
    collected.push(await runSample(config));
  }

  const samples = {};
  for (const metric of ['acceptance_ms', 'total_ms', 'queue_ms', 'worker_ms']) {
    const values = collected
      .map((sample) => sample[metric])
      .filter((value) => Number.isFinite(value));
    if (values.length > 0) samples[metric] = values;
  }
  return {
    schema_version: 'ubag.benchmark.v1',
    metadata: metadata(config),
    samples,
    stats: Object.fromEntries(
      Object.entries(samples).map(([metric, values]) => [metric, calculateStats(values)]),
    ),
  };
}

export function formatHuman(result) {
  const lines = [
    `UBAG benchmark (${result.metadata.scenario})`,
    `base: ${result.metadata.base_url}`,
    `samples: ${result.metadata.samples} after ${result.metadata.warmups} warmup(s)`,
  ];
  for (const [metric, stats] of Object.entries(result.stats)) {
    lines.push(
      `${metric}: count=${stats.count} mean=${stats.mean.toFixed(3)}ms `
      + `p50=${stats.p50.toFixed(3)}ms p95=${stats.p95.toFixed(3)}ms p99=${stats.p99.toFixed(3)}ms`,
    );
  }
  return lines.join('\n');
}
