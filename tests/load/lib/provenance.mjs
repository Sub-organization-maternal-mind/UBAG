// Run provenance for tests/load/acceptance.mjs (manifest validation lives in ../workloads.mjs).
// Pure helpers (allowlisting, parsing) so they can be unit-tested offline; the only side effects are
// a `git rev-parse` and an optional read-only `docker exec <c> env` (allowlist-filtered).
import { execFile, execFileSync } from 'node:child_process';
import { arch, cpus, platform, release, totalmem } from 'node:os';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const loadDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');

// ---------------------------------------------------------------- provenance

// Non-secret tuning knobs only. Matching is by exact name or the UBAG_ADMISSION_ prefix; nothing else is ever copied.
const ENV_EXACT = new Set([
  'UBAG_WORKER_CONCURRENCY', 'UBAG_WORKER_DAEMON', 'UBAG_WORKER_POLL_INTERVAL_MS', 'UBAG_GATEWAY_MAX_INFLIGHT_REQUESTS',
  'UBAG_GATEWAY_STORE', 'UBAG_VOICE_STORE', 'UBAG_EXECUTOR_MODE', 'GOMAXPROCS', 'GOMEMLIMIT',
  // perf-fleet flags: a published ladder number must say which of these were on (BINDING section 4, P7.2/P7.9). Names only; none is a secret.
  'UBAG_WORKER_CONSUMER_ENABLED', 'UBAG_WORKER_MAX_RUNTIME_MS', 'UBAG_WORKER_POOL_SIZE', 'UBAG_WORKER_POOL_MAX', 'UBAG_WORKER_POOL_WAIT_MS',
  'UBAG_WORKER_STAGE_TIMINGS', 'UBAG_WORKER_STRICT_STREAM_END', 'UBAG_WORKER_STRICT_SUBMIT', 'UBAG_WORKER_STREAM_EVENTS', 'UBAG_WORKER_STREAM_INGEST',
  'UBAG_WORKER_ATTEMPT_EVENT_IDS', 'UBAG_EVENT_NOTIFY', 'UBAG_FILESPOOL_HONOR_NOT_BEFORE', 'UBAG_SYNTHETIC_PROVIDER', 'UBAG_VOICE_QUEUE_MAX_AGE_MS',
]);
const SAFE_VALUE = /^[A-Za-z0-9_.,:\- ]{0,120}$/;
export const isAllowedEnvName = (name) => ENV_EXACT.has(name) || name.startsWith('UBAG_ADMISSION_');

/** Allowlist filter over an env-like object. A value that does not look like a plain knob is replaced, never copied. */
export function pickEnv(env) {
  const out = {};
  for (const name of Object.keys(env).sort()) {
    if (isAllowedEnvName(name) && env[name] !== undefined) out[name] = SAFE_VALUE.test(String(env[name])) ? String(env[name]) : '[withheld: unexpected value shape]';
  }
  return out;
}

/** `KEY=value` lines (the output of `env`) -> object. */
export function parseEnvText(text) {
  const out = {};
  for (const line of String(text).split('\n')) {
    const i = line.indexOf('=');
    if (i > 0) out[line.slice(0, i)] = line.slice(i + 1).replace(/\r$/, '');
  }
  return out;
}

export const dockerEnvExec = (container) => new Promise((res, rej) => {
  execFile('docker', ['exec', container, 'env'], { timeout: 20_000, maxBuffer: 1 << 20 }, (err, stdout) => (err ? rej(err) : res(stdout)));
});

/** Labels of the `ubag_gateway_info{...} 1` series from a parseProm() map, or null. */
export function gatewayInfoFrom(metrics) {
  if (!metrics) return null;
  for (const key of metrics.keys()) {
    if (!key.startsWith('ubag_gateway_info{')) continue;
    const label = (n) => new RegExp(`${n}="([^"]*)"`).exec(key)?.[1] ?? null;
    return { version: label('version'), api_version: label('api_version'), commit: label('commit') };
  }
  return null;
}

/** Harness git revision; `dirty` means tests/load has uncommitted changes (so the sha alone does not identify the harness). */
export function harnessGit(cwd = loadDir) {
  const git = (...args) => execFileSync('git', args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'], timeout: 10_000 }).trim();
  try { return { sha: git('rev-parse', 'HEAD'), dirty: git('status', '--porcelain', '--', loadDir) !== '' }; } catch { return { sha: null, dirty: null }; }
}

/** Where the harness ran (not necessarily where the gateway runs; see resources.host_pressure for that side). */
export const harnessHost = () => ({ platform: `${platform()}-${arch()}`, os_release: release(), cpu_count: cpus().length, cpu_model: cpus()[0]?.model ?? null, mem_total_mb: Math.round(totalmem() / 1048576) });

/** Limits of the sampled containers, lifted from the resource summary (null = unlimited / not read). */
export function containerLimits(resources) {
  const out = {};
  for (const [role, c] of Object.entries(resources?.containers ?? {})) {
    out[role] = c.skipped ? { container: c.container, skipped: c.skipped } : { container: c.container, cpu_cores: c.cpu_limit_cores ?? null, memory_mb: c.memory_limit_mb ?? null };
  }
  return out;
}

/** Allowlisted env of each sampled container; an unreadable container is recorded as skipped. */
export async function stackEnv(targets, readEnv = dockerEnvExec) {
  const out = {};
  for (const t of targets) {
    try { out[t.role] = pickEnv(parseEnvText(await readEnv(t.container))); } catch (e) { out[t.role] = { skipped: `env read failed: ${e.code ?? e.message}` }; }
  }
  return out;
}
