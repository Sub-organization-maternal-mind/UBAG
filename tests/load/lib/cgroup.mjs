// cgroup (v2 with v1 fallback), CPU-throttle and host-pressure sampling for tests/load/acceptance.mjs.
// One read-only `docker exec ... sh -c` per container per tick; everything else here is pure parsing so
// it can be unit-tested against fixture text. Numbers from a laptop/Docker stack are NON-AUTHORITATIVE.
import { execFile } from 'node:child_process';

const r2 = (n) => Math.round(n * 100) / 100;

// Helper-plane pressure rules (docs/perf-fleet plan): enter pressure at CPU>80% or MemAvailable<20%;
// recovered only after CPU<60% and MemAvailable>25% hold for the whole window (2 min).
export const PRESSURE_RULES = { cpuHighPct: 80, memAvailLowPct: 20, cpuRecoverPct: 60, memAvailRecoverPct: 25, recoveryWindowMs: 120_000 };

// label -> file; v1 labels are prefixed "v1_". Missing files simply produce an empty section.
const FILES = {
  v2_cpu_stat: '/sys/fs/cgroup/cpu.stat', v2_cpu_max: '/sys/fs/cgroup/cpu.max',
  v2_mem_current: '/sys/fs/cgroup/memory.current', v2_mem_peak: '/sys/fs/cgroup/memory.peak', v2_mem_max: '/sys/fs/cgroup/memory.max',
  v2_mem_events: '/sys/fs/cgroup/memory.events', v2_cpu_pressure: '/sys/fs/cgroup/cpu.pressure', v2_mem_pressure: '/sys/fs/cgroup/memory.pressure',
  v1_cpu_stat: '/sys/fs/cgroup/cpu/cpu.stat', v1_cpu_quota: '/sys/fs/cgroup/cpu/cpu.cfs_quota_us', v1_cpu_period: '/sys/fs/cgroup/cpu/cpu.cfs_period_us',
  v1_cpuacct: '/sys/fs/cgroup/cpuacct/cpuacct.usage',
  v1_mem_usage: '/sys/fs/cgroup/memory/memory.usage_in_bytes', v1_mem_peak: '/sys/fs/cgroup/memory/memory.max_usage_in_bytes',
  v1_mem_limit: '/sys/fs/cgroup/memory/memory.limit_in_bytes', v1_oom_control: '/sys/fs/cgroup/memory/memory.oom_control',
  meminfo: '/proc/meminfo', procstat: '/proc/stat',
};
const SCRIPT = `${Object.entries(FILES).map(([k, p]) => `echo "@@${k}"; cat ${p} 2>/dev/null`).join('; ')}; echo "@@nproc"; nproc 2>/dev/null`;
// /proc/meminfo and /proc/stat are the host's view (Docker Desktop: the VM's) unless the runtime fakes them (lxcfs).

export const dockerCgroupExec = (container) => new Promise((res, rej) => {
  execFile('docker', ['exec', container, 'sh', '-c', SCRIPT], { timeout: 20_000, maxBuffer: 1 << 20 }, (err, stdout) => (err ? rej(err) : res(stdout)));
});

export function splitSections(text) {
  const out = {}; let cur = null;
  for (const line of String(text ?? '').split('\n')) {
    const m = /^@@(\w+)$/.exec(line);
    if (m) { cur = m[1]; out[cur] = ''; } else if (cur) out[cur] += `${line}\n`;
  }
  return out;
}

/** "key value" lines -> {key: number}. Also handles the v1 oom_control / v2 events shape. */
export function parseKv(text) {
  const o = {};
  for (const line of String(text ?? '').split('\n')) {
    const m = /^(\w+)\s+(-?\d+)\s*$/.exec(line.trim());
    if (m) o[m[1]] = Number(m[2]);
  }
  return o;
}

const num = (text) => { const m = /^\s*(-?\d+)\s*$/.exec(text ?? ''); return m ? Number(m[1]) : null; };
// cgroup v2 prints the literal "max" for unlimited; v1 prints a huge number (~2^63).
const limit = (n) => (n == null || n >= 2 ** 60 ? null : n);

/** PSI file -> {some_avg10, full_avg10} (percent), or null when the file is absent. */
export function parsePressure(text) {
  if (!String(text ?? '').trim()) return null;
  const o = {};
  for (const line of text.split('\n')) {
    const m = /^(some|full)\s+avg10=([\d.]+)/.exec(line);
    if (m) o[`${m[1]}_avg10`] = Number(m[2]);
  }
  return o;
}

export function parseMeminfo(text) {
  const kv = {};
  for (const line of String(text ?? '').split('\n')) {
    const m = /^(\w+):\s+(\d+)\s*kB/.exec(line);
    if (m) kv[m[1]] = Number(m[2]);
  }
  return kv.MemTotal ? { mem_total_kb: kv.MemTotal, mem_available_kb: kv.MemAvailable ?? kv.MemFree ?? null } : null;
}

/** First line of /proc/stat -> {busy, total} jiffies (idle+iowait are not busy). */
export function parseProcStat(text) {
  const m = /^cpu\s+(.*)$/m.exec(String(text ?? ''));
  if (!m) return null;
  const f = m[1].trim().split(/\s+/).map(Number);
  if (f.length < 5 || f.some((x) => !Number.isFinite(x))) return null;
  const total = f.slice(0, 8).reduce((a, b) => a + b, 0);
  return { busy: total - f[3] - f[4], total };
}

/** Raw `docker exec` output -> one normalised sample. Throws when no cgroup files were readable. */
export function parseSample(text, t = Date.now()) {
  const s = splitSections(text);
  const isV2 = num(s.v2_mem_current) != null;
  const isV1 = !isV2 && num(s.v1_mem_usage) != null;
  if (!isV2 && !isV1) throw new Error('no readable cgroup v2 or v1 memory files in container');
  const host = { ...(parseMeminfo(s.meminfo) ?? {}), cpu: parseProcStat(s.procstat), nproc: num(s.nproc) };
  if (isV2) {
    const cpu = parseKv(s.v2_cpu_stat); const ev = parseKv(s.v2_mem_events);
    const [quota, period] = String(s.v2_cpu_max ?? '').trim().split(/\s+/);
    return {
      t, version: 'v2', host,
      cpu: { usage_usec: cpu.usage_usec ?? null, nr_periods: cpu.nr_periods ?? 0, nr_throttled: cpu.nr_throttled ?? 0, throttled_usec: cpu.throttled_usec ?? 0, quota_cores: quota && quota !== 'max' && Number(period) > 0 ? Number(quota) / Number(period) : null },
      mem: { current: num(s.v2_mem_current), peak: num(s.v2_mem_peak), max: s.v2_mem_max?.trim() === 'max' ? null : limit(num(s.v2_mem_max)), oom_kill: ev.oom_kill ?? 0, oom: ev.oom ?? 0, max_events: ev.max ?? 0 },
      pressure: { cpu: parsePressure(s.v2_cpu_pressure), memory: parsePressure(s.v2_mem_pressure) },
    };
  }
  const cpu = parseKv(s.v1_cpu_stat); const oom = parseKv(s.v1_oom_control);
  const quota = num(s.v1_cpu_quota); const period = num(s.v1_cpu_period);
  const acct = num(s.v1_cpuacct);
  return {
    t, version: 'v1', host,
    cpu: { usage_usec: acct == null ? null : Math.round(acct / 1000), nr_periods: cpu.nr_periods ?? 0, nr_throttled: cpu.nr_throttled ?? 0, throttled_usec: Math.round((cpu.throttled_time ?? 0) / 1000), quota_cores: quota > 0 && period > 0 ? quota / period : null },
    mem: { current: num(s.v1_mem_usage), peak: num(s.v1_mem_peak), max: limit(num(s.v1_mem_limit)), oom_kill: oom.oom_kill ?? 0, oom: oom.under_oom ?? 0, max_events: 0 },
    pressure: { cpu: null, memory: null }, // PSI is a cgroup v2 / kernel 4.20+ feature
  };
}

/** Per-container figures from >=1 chronological samples. */
export function summarizeContainer(samples) {
  const first = samples[0]; const last = samples[samples.length - 1];
  const wallS = (last.t - first.t) / 1000;
  const periods = last.cpu.nr_periods - first.cpu.nr_periods;
  const peak = Math.max(...samples.map((s) => Math.max(s.mem.current ?? 0, s.mem.peak ?? 0)));
  const maxBytes = last.mem.max;
  const maxOf = (pick) => { const v = samples.map(pick).filter(Number.isFinite); return v.length ? Math.max(...v) : null; };
  return {
    cgroup: last.version, samples: samples.length,
    cpu_limit_cores: last.cpu.quota_cores,
    cpu_cores_avg: samples.length > 1 && wallS > 0 && last.cpu.usage_usec != null ? r2((last.cpu.usage_usec - first.cpu.usage_usec) / 1e6 / wallS) : null,
    // share of CFS periods in which the cgroup was throttled; 0 when no CPU limit produced any period
    cpu_throttled_pct: samples.length > 1 ? (periods > 0 ? r2(((last.cpu.nr_throttled - first.cpu.nr_throttled) / periods) * 100) : 0) : null,
    cpu_throttled_ms: samples.length > 1 ? r2((last.cpu.throttled_usec - first.cpu.throttled_usec) / 1000) : null,
    memory_peak_mb: r2(peak / 1048576), memory_limit_mb: maxBytes == null ? null : r2(maxBytes / 1048576),
    memory_headroom_pct: maxBytes == null ? null : r2(Math.max(0, (1 - peak / maxBytes) * 100)),
    oom_kills: Math.max(0, last.mem.oom_kill - first.mem.oom_kill), oom_events: Math.max(0, last.mem.oom - first.mem.oom),
    psi_cpu_some_avg10_max: maxOf((s) => s.pressure.cpu?.some_avg10), psi_memory_some_avg10_max: maxOf((s) => s.pressure.memory?.some_avg10),
    psi_memory_full_avg10_max: maxOf((s) => s.pressure.memory?.full_avg10),
  };
}

/**
 * Helper pressure rules over host-wide samples (one per tick, chronological).
 * Booleans: whether CPU>80% / MemAvailable<20% was ever seen, whether pressure triggered, and whether the host
 * then held CPU<60% and MemAvailable>25% for the whole recovery window up to the last sample.
 */
export function evaluatePressure(hostSamples, rules = PRESSURE_RULES) {
  const pts = [];
  for (let i = 0; i < hostSamples.length; i += 1) {
    const h = hostSamples[i]; const prev = hostSamples[i - 1];
    const avail = h.mem_total_kb && h.mem_available_kb != null ? (h.mem_available_kb / h.mem_total_kb) * 100 : null;
    let cpu = null;
    if (prev?.cpu && h.cpu && h.cpu.total > prev.cpu.total) cpu = ((h.cpu.busy - prev.cpu.busy) / (h.cpu.total - prev.cpu.total)) * 100;
    if (cpu != null || avail != null) pts.push({ t: h.t, cpu, avail });
  }
  if (!pts.length) return { sampled: false };
  const trig = (p) => (p.cpu != null && p.cpu > rules.cpuHighPct) || (p.avail != null && p.avail < rules.memAvailLowPct);
  const ok = (p) => (p.cpu == null || p.cpu < rules.cpuRecoverPct) && (p.avail == null || p.avail > rules.memAvailRecoverPct);
  const cpus = pts.map((p) => p.cpu).filter((x) => x != null); const avs = pts.map((p) => p.avail).filter((x) => x != null);
  const triggered = pts.some(trig);
  // length of the trailing run of recovery-grade samples
  let i = pts.length - 1; while (i >= 0 && ok(pts[i]) && !trig(pts[i])) i -= 1;
  const streakMs = i === pts.length - 1 ? 0 : pts[pts.length - 1].t - pts[i + 1].t;
  return {
    sampled: true, points: pts.length,
    cpu_over_80: pts.some((p) => p.cpu != null && p.cpu > rules.cpuHighPct),
    mem_available_under_20: pts.some((p) => p.avail != null && p.avail < rules.memAvailLowPct),
    pressure_triggered: triggered,
    // null when never triggered (nothing to recover from)
    recovered: triggered ? streakMs >= rules.recoveryWindowMs : null,
    recovery_observed_ms: streakMs,
    host_cpu_pct_max: cpus.length ? r2(Math.max(...cpus)) : null,
    host_mem_available_pct_min: avs.length ? r2(Math.min(...avs)) : null,
    rules: { ...rules },
  };
}

/**
 * Parse `role=container,role2=container2` (a bare name uses itself as the role).
 * Container names are validated because they become `docker exec` arguments.
 */
export function parseTargets(spec) {
  return String(spec).split(',').map((s) => s.trim()).filter(Boolean).map((part) => {
    const eq = part.indexOf('=');
    const role = eq === -1 ? part : part.slice(0, eq); const container = eq === -1 ? part : part.slice(eq + 1);
    if (!/^[A-Za-z0-9][\w.-]*$/.test(container) || !/^[A-Za-z0-9][\w.-]*$/.test(role)) throw new Error(`invalid container target "${part}"`);
    return { role, container };
  });
}

/** Whole-run roll-up: the flat keys thresholds.goals.json is evaluated against. */
export function rollup(containers) {
  const vals = Object.values(containers).filter((c) => !c.skipped);
  const known = (k) => vals.map((c) => c[k]).filter((x) => x != null);
  const min = (a) => (a.length ? Math.min(...a) : undefined);
  const max = (a) => (a.length ? Math.max(...a) : undefined);
  const out = {};
  const head = min(known('memory_headroom_pct')); if (head !== undefined) out.memory_headroom_pct = head;
  const thr = max(known('cpu_throttled_pct')); if (thr !== undefined) out.cpu_throttled_pct = thr;
  if (vals.length) out.oom_kills = vals.reduce((a, c) => a + c.oom_kills, 0);
  return out;
}

/**
 * Samples each target every `intervalMs` (plus once immediately and once on stop, so even a short run has a delta).
 * A target whose first read fails is reported as skipped with the reason; the others continue.
 */
export function startCgroupSampler(targets, intervalMs, { exec = dockerCgroupExec, rules = PRESSURE_RULES, now = Date.now } = {}) {
  const series = new Map(targets.map((t) => [t.role, []])); const skipped = new Map(); const hostSeries = [];
  let stopped = false; let timer; let inflight = Promise.resolve();
  const readOne = async (t) => {
    if (skipped.has(t.role)) return null;
    try { const s = parseSample(await exec(t.container), now()); series.get(t.role).push(s); return s; } catch (e) {
      if (!series.get(t.role).length) skipped.set(t.role, `cgroup read failed: ${e.code ?? e.message}`);
      return null;
    }
  };
  const tick = async () => {
    const got = (await Promise.all(targets.map(readOne))).filter(Boolean);
    const h = got.find((s) => s.host.mem_total_kb || s.host.cpu);
    if (h) hostSeries.push({ t: h.t, ...h.host });
  };
  inflight = tick();
  timer = setInterval(() => { if (!stopped) inflight = inflight.then(tick); }, intervalMs);
  timer.unref?.();
  return async () => {
    clearInterval(timer); stopped = true; await inflight;
    await tick(); // closing sample
    const containers = {};
    for (const t of targets) {
      const s = series.get(t.role);
      containers[t.role] = skipped.has(t.role) || !s.length ? { container: t.container, skipped: skipped.get(t.role) ?? 'no samples collected' } : { container: t.container, ...summarizeContainer(s) };
    }
    if (Object.values(containers).every((c) => c.skipped)) return { skipped: `cgroup sampling unavailable: ${Object.values(containers)[0]?.skipped ?? 'no targets'}`, containers };
    return { containers, host_pressure: evaluatePressure(hostSeries, rules), summary: rollup(containers) };
  };
}
