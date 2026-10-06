// Offline self-tests for tests/load/acceptance.mjs. Never touches a real host:
// every network scenario runs against an in-process fake gateway on 127.0.0.1.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync, existsSync, readdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { after, describe, it } from 'node:test';
import { fileURLToPath } from 'node:url';
import {
  ACK_FLAG, checkTarget, clockBounds, evaluateThresholds, histogramQuantile, main, metricsDelta, parseArgs, parsePgStatements,
  parseProm, parseRetryAfterMs, percentile, pgDelta, retryDelayMs, run, renderMarkdown, splitSse, summarize, voiceLatencySummary,
} from './acceptance.mjs';
import { containerLimits, gatewayInfoFrom, parseEnvText, pickEnv } from './lib/provenance.mjs';
import { workloadSha256 } from './workloads.mjs';
import { evaluatePressure, parseSample, parseTargets, startCgroupSampler, summarizeContainer } from './lib/cgroup.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const KEY = 'test-key-not-secret';
const KEY_B = 'test-key-b-not-secret';
const MB = 1024 * 1024;

// ---------------------------------------------------------- cgroup fixtures
// Text exactly as `docker exec <c> sh -c` prints it (sections introduced by "@@label").
function v2Fixture({ nrPeriods = 0, nrThrottled = 0, usage = 1_000_000, oomKill = 0, current = 500 * MB, peak = null, memMax = String(1000 * MB), cpuMax = '150000 100000' } = {}) {
  return [
    '@@v2_cpu_stat', `usage_usec ${usage}`, 'user_usec 1', 'system_usec 1', `nr_periods ${nrPeriods}`, `nr_throttled ${nrThrottled}`, `throttled_usec ${nrThrottled * 10_000}`,
    '@@v2_cpu_max', cpuMax, '@@v2_mem_current', String(current), '@@v2_mem_peak', peak == null ? '' : String(peak), '@@v2_mem_max', memMax,
    '@@v2_mem_events', 'low 0', 'high 0', 'max 0', 'oom 0', `oom_kill ${oomKill}`,
    '@@v2_cpu_pressure', 'some avg10=1.25 avg60=0.50 avg300=0.10 total=1000', 'full avg10=0.00 avg60=0.00 avg300=0.00 total=0',
    '@@v2_mem_pressure', 'some avg10=0.75 avg60=0.10 avg300=0.00 total=10', 'full avg10=0.50 avg60=0.00 avg300=0.00 total=5',
    '@@meminfo', 'MemTotal:       24000000 kB', 'MemFree:         1000000 kB', 'MemAvailable:   12000000 kB',
    '@@procstat', 'cpu  100 0 100 800 0 0 0 0 0 0', 'cpu0 50 0 50 400 0 0 0 0 0 0', '@@nproc', '8', '',
  ].join('\n');
}
function v1Fixture({ nrPeriods = 0, nrThrottled = 0, oomKill = 0, peak = 400 * MB, limit = String(1000 * MB) } = {}) {
  return [
    '@@v1_cpu_stat', `nr_periods ${nrPeriods}`, `nr_throttled ${nrThrottled}`, `throttled_time ${nrThrottled * 5_000_000}`,
    '@@v1_cpu_quota', '200000', '@@v1_cpu_period', '100000', '@@v1_cpuacct', '5000000000',
    '@@v1_mem_usage', String(300 * MB), '@@v1_mem_peak', String(peak), '@@v1_mem_limit', limit,
    '@@v1_oom_control', 'oom_kill_disable 0', 'under_oom 0', `oom_kill ${oomKill}`,
    '@@meminfo', 'MemTotal:       8000000 kB', 'MemAvailable:   4000000 kB', '@@procstat', 'cpu  10 0 10 80 0 0 0 0', '@@nproc', '2', '',
  ].join('\n');
}
const fakes = [];
after(() => Promise.all(fakes.map((f) => new Promise((r) => { f.server.closeAllConnections?.(); f.server.close(r); }))));

// ------------------------------------------------------------- fake gateway

/** Minimal multipart/form-data parser: [{name, contentType, data}]. */
function splitMultipart(buf, boundary) {
  const delim = Buffer.from(`--${boundary}`); const out = []; let at = buf.indexOf(delim);
  while (at !== -1) {
    const start = at + delim.length; const next = buf.indexOf(delim, start);
    if (next === -1 || buf.subarray(start, start + 2).toString() === '--') break;
    const seg = buf.subarray(start + 2, next - 2); const split = seg.indexOf('\r\n\r\n');
    const head = seg.subarray(0, split).toString('utf8');
    out.push({ name: /name="([^"]*)"/.exec(head)?.[1], contentType: /Content-Type: (.*)/i.exec(head)?.[1]?.trim(), data: seg.subarray(split + 4) });
    at = next;
  }
  return out;
}

async function startFake(opts = {}) {
  const o = { maxBody: 4096, rejectFirstCreates: 0, facadeLimit: Infinity, maxInflight: Infinity, omitRetryAfter: false, dupBug: false, facadeNoBodyMs: false, resultBug: null, dupTerminal: false, leak: false, facadeBad: false, finalStatus: 'completed', slowGetMs: 5, clockSkewMs: 0, sseDelayMs: 15, sseMode: null, ...opts };
  const st = { uploads: [], requests: 0, jobs: new Map(), byKey: new Map(), seq: 0, inflight: 0, facadeInflight: 0, creates: 0, completed: 0, rejections: { inflight_requests: 0, upload_memory: 0 }, uploadBytes: 0, sseOpens: 0, sseOpen: 0 };
  const skewedDate = () => new Date(Date.now() + o.clockSkewMs).toUTCString(); // second-resolution, like a real Date header
  const json = (res, status, body, headers = {}) => { res.writeHead(status, { 'Content-Type': 'application/json', Date: skewedDate(), ...headers }); res.end(JSON.stringify(body)); };
  const overload = (res, reason, facade = false) => {
    st.rejections[reason] += 1;
    const headers = o.omitRetryAfter ? {} : { 'Retry-After': '0' };
    const error = facade ? { message: 'overloaded', type: 'server_error', code: 'overloaded' } : { code: reason === 'upload_memory' ? 'UBAG-OVERLOAD-UPLOAD-001' : 'UBAG-OVERLOAD-REQUESTS-001', category: 'overload', message: 'retry', retryable: true };
    if (!(facade && o.facadeNoBodyMs)) error.retry_after_ms = 15;
    json(res, 503, { error }, headers);
  };
  const readBody = (req, cb) => { const chunks = []; let size = 0; req.on('data', (c) => { size += c.length; if (size <= o.maxBody + 1) chunks.push(c); }); req.on('end', () => cb(Buffer.concat(chunks).toString('utf8'), size)); };
  const server = createServer((req, res) => {
    st.requests += 1;
    const url = new URL(req.url, 'http://x'); const path = url.pathname;
    const tenant = req.headers.authorization === `Bearer ${KEY}` ? 'a' : req.headers.authorization === `Bearer ${KEY_B}` ? 'b' : null;
    if (!tenant) { req.resume(); return json(res, 401, { error: { code: 'UBAG-AUTH-MISSING-001' } }); }
    if (path === '/v1/health' || path === '/v1/ready') { req.resume(); return json(res, 200, { status: 'ok' }); }
    if (path === '/v1/metrics') {
      req.resume(); res.writeHead(200, { 'Content-Type': 'text/plain' });
      return res.end([
        'ubag_gateway_info{version="9.9.9",api_version="v1",commit="deadbeefcafe"} 1',
        '# TYPE ubag_gateway_http_inflight_requests gauge',
        `ubag_gateway_http_inflight_requests{service="ubag-gateway",route="all",method="all"} ${st.inflight}`,
        `ubag_admission_rejections_total{reason="inflight_requests"} ${st.rejections.inflight_requests}`,
        `ubag_admission_rejections_total{reason="upload_memory"} ${st.rejections.upload_memory}`,
        `ubag_upload_memory_inflight_bytes ${st.facadeInflight * 1024}`, 'ubag_upload_memory_budget_bytes 268435456',
        `ubag_gateway_request_latency_seconds_bucket{route="/v1/jobs",le="0.1"} ${st.creates}`,
        `ubag_gateway_request_latency_seconds_bucket{route="/v1/jobs",le="+Inf"} ${st.creates}`,
        `ubag_gateway_request_latency_seconds_sum{route="/v1/jobs"} ${st.creates * 0.01}`,
        `ubag_gateway_request_latency_seconds_count{route="/v1/jobs"} ${st.creates}`,
        `ubag_queue_job_wait_duration_seconds_bucket{queue="default",le="0.5"} ${st.completed}`,
        `ubag_queue_job_wait_duration_seconds_bucket{queue="default",le="+Inf"} ${st.completed}`,
        `ubag_queue_job_wait_duration_seconds_sum{queue="default"} ${st.completed * 0.2}`,
        `ubag_queue_job_wait_duration_seconds_count{queue="default"} ${st.completed}`,
        `ubag_gateway_http_requests_total{service="ubag-gateway",route="/v1/jobs",method="POST",status_class="2xx",outcome="success"} ${st.creates}`,
        `ubag_gateway_http_requests_total{service="ubag-gateway",route="/v1/sse/jobs/*",method="GET",status_class="2xx",outcome="success"} ${st.sseOpens}`,
        `ubag_gateway_http_requests_total{service="ubag-gateway",route="/v1/jobs/{job_id}",method="GET",status_class="2xx",outcome="success"} ${st.requests}`,
        `ubag_sse_connections_current ${st.sseOpen}`,
        `ubag_worker_job_duration_seconds_bucket{adapter="mock",le="1"} ${st.completed}`, `ubag_worker_job_duration_seconds_bucket{adapter="mock",le="+Inf"} ${st.completed}`,
        `ubag_worker_job_duration_seconds_sum{adapter="mock"} ${st.completed * 0.5}`, `ubag_worker_job_duration_seconds_count{adapter="mock"} ${st.completed}`,
        'ubag_admission_tokens_active{kind="tenant"} 1', 'ubag_db_pool_connections{state="open"} 2', 'ubag_voice_sessions_connected_total 0', 'ubag_unrelated_total 9', '',
      ].join('\n'));
    }
    st.inflight += 1; res.on('close', () => { st.inflight -= 1; });
    if (st.inflight > o.maxInflight) { req.resume(); return overload(res, 'inflight_requests'); }

    if (path === '/v1/jobs' && req.method === 'POST' && /^multipart\/form-data/.test(req.headers['content-type'] ?? '')) {
      const chunks = []; req.on('data', (c) => chunks.push(c));
      return req.on('end', () => {
        const idem = req.headers['idempotency-key'];
        if (!/^[\w.:-]{16,128}$/.test(idem ?? '')) return json(res, 400, { error: { code: 'UBAG-VALIDATION-IDEMPOTENCY-KEY-001' } });
        const parts = splitMultipart(Buffer.concat(chunks), /boundary=(.+)$/.exec(req.headers['content-type'])[1]);
        const envelope = JSON.parse(parts.find((p) => p.name === 'job')?.data.toString('utf8') ?? '{}');
        if (!o.acceptAudio) return json(res, 400, { error: { code: 'UBAG-VALIDATION-ATTACHMENTS-UNSUPPORTED-001' } });
        const files = parts.filter((p) => p.name !== 'job');
        const declared = envelope.job?.input?.attachments ?? [];
        if (declared.length !== files.length || declared.some((d) => !files.some((f) => f.name === d.key && f.contentType === d.content_type))) return json(res, 400, { error: { code: 'UBAG-VALIDATION-MULTIPART-PART-MISSING-001' } });
        const id = `job_${++st.seq}`; st.creates += 1; st.uploads.push(...files.map((f) => f.data.length));
        const artifacts = files.map((f) => ({ job_id: id, key: f.name, content_type: f.contentType, size_bytes: f.data.length - (o.artifactBug === 'short' ? 1 : 0), checksum: o.artifactBug === 'checksum' ? 'f'.repeat(64) : createHash('sha256').update(f.data).digest('hex') }));
        st.jobs.set(id, { status: 'queued', polls: 0, target: envelope.job?.target, prompt: envelope.job?.input?.prompt, tenant, artifacts }); st.byKey.set(idem, id);
        return json(res, 202, { job_id: id, status: 'queued', idempotent_replay: false });
      });
    }
    if (path === '/v1/jobs' && req.method === 'POST') {
      if (Number(req.headers['content-length'] ?? 0) > o.maxBody) { req.resume(); return json(res, 413, { error: { code: 'UBAG-VALIDATION-BODY-TOO-LARGE-001' } }); }
      return readBody(req, (text) => {
        let body; try { body = JSON.parse(text); } catch { return json(res, 400, { error: { code: 'UBAG-VALIDATION-JSON-001' } }); }
        const key = req.headers['idempotency-key'];
        if (!/^[\w.:-]{16,128}$/.test(key ?? '')) return json(res, 400, { error: { code: 'UBAG-VALIDATION-IDEMPOTENCY-KEY-001' } });
        if (st.rejectedCreates === undefined) st.rejectedCreates = 0;
        if (st.rejectedCreates < o.rejectFirstCreates) {
          st.rejectedCreates += 1;
          return json(res, 429, { error: { code: 'UBAG-RATE-APP-001', retryable: true, retry_after_ms: 15 } }, { 'Retry-After': '2' });
        }
        if (!o.dupBug && st.byKey.has(key)) return json(res, 200, { job_id: st.byKey.get(key), status: st.jobs.get(st.byKey.get(key)).status, idempotent_replay: true });
        const id = `job_${++st.seq}`; st.creates += 1;
        st.jobs.set(id, { status: 'queued', polls: 0, target: body.job?.target, prompt: body.job?.input?.prompt, tenant }); st.byKey.set(key, id);
        json(res, 202, { job_id: id, status: 'queued', idempotent_replay: false });
      });
    }
    if (path === '/v1/jobs' && req.method === 'GET') {
      req.resume();
      const mine = [...st.jobs.entries()].filter(([, j]) => o.leak || j.tenant === tenant).slice(0, 100);
      return json(res, 200, { jobs: mine.map(([id, j]) => ({ job_id: id, status: j.status })), next_cursor: null });
    }
    const m = /^\/v1\/jobs\/([^/]+)(?:\/(cancel|events|artifacts(?:\/.+)?))?$/.exec(path);
    const visible = (id) => { const j = st.jobs.get(id); return j && (o.leak || j.tenant === tenant) ? j : null; };
    if (m && m[2] === 'events' && req.method === 'GET') {
      req.resume();
      const job = visible(m[1]);
      if (!job) return json(res, 404, { error: { code: 'UBAG-JOB-NOT-FOUND-001' } });
      const types = ['queued', 'running', 'token', ...(job.status === 'cancelled' ? ['cancelled'] : job.polls >= 3 ? [o.finalStatus] : [])];
      if (o.dupTerminal && job.polls >= 3) types.push(o.finalStatus);
      return json(res, 200, { job_id: m[1], events: types.map((type, i) => ({ job_id: m[1], type, sequence: i + 1 })), next_cursor: null });
    }
    const sse = /^\/v1\/sse\/jobs\/([^/]+)$/.exec(path);
    if (sse && req.method === 'GET') {
      req.resume();
      if (!visible(sse[1])) return json(res, 404, { error: { code: 'UBAG-JOB-NOT-FOUND-001' } });
      if (o.sseMode === 'reject') return json(res, 503, { error: { code: 'UBAG-OVERLOAD-REQUESTS-001', retry_after_ms: 15 } }, { 'Retry-After': '0' });
      st.sseOpens += 1; st.sseOpen += 1;
      res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Date: skewedDate() });
      const stamp = (agoMs = 0) => new Date(Date.now() + o.clockSkewMs - agoMs).toISOString();
      const send = (type, sequence, createdAt) => { const id = `${sse[1]}-${sequence}`; res.write(`id: ${id}\nevent: job.${type}\ndata: ${JSON.stringify({ event_id: id, job_id: sse[1], type, sequence, created_at: createdAt })}\n\n`); };
      const born = () => stamp(o.sseMode === 'backlog' ? 60_000 : 0); // 'backlog': every event predates the subscription
      send('queued', 1, stamp(5000)); // always a replayed backlog event
      const d = o.sseDelayMs; const timers = [
        setTimeout(() => send('running', 2, born()), d),
        setTimeout(() => send('token', 3, born()), 2 * d),
        ...(o.sseMode === 'noTerminal' ? [] : [setTimeout(() => send('completed', o.sseMode === 'dupSeq' ? 3 : 4, born()), 3 * d)]),
        setInterval(() => res.write(': ping\n\n'), 30),
      ];
      return res.on('close', () => { st.sseOpen -= 1; timers.forEach((t) => { clearTimeout(t); clearInterval(t); }); });
    }
    if (m && m[2] === 'artifacts' && req.method === 'GET') {
      req.resume();
      if (!visible(m[1])) return json(res, 404, { error: { code: 'UBAG-JOB-NOT-FOUND-001' } });
      return json(res, 200, { job_id: m[1], kind: 'artifacts', data: st.jobs.get(m[1]).artifacts ?? [] });
    }
    if (m && !(m[2]) && req.method === 'GET') {
      req.resume();
      return setTimeout(() => {
        const job = visible(m[1]);
        if (!job) return json(res, 404, { error: { code: 'UBAG-JOB-NOT-FOUND-001' } });
        job.polls += 1;
        if (job.status !== 'cancelled') job.status = job.polls === 1 ? 'queued' : job.polls === 2 ? 'running' : o.finalStatus;
        if (job.status === 'completed' && !job.counted) { job.counted = true; st.completed += 1; }
        const body = { job_id: m[1], status: job.status };
        if (job.status === 'completed' || job.status === 'completed_with_warnings') {
          const full = `Mock response for chat.prompt on mock (${m[1]}): ${job.prompt}`;
          const text = o.resultBug === 'wrong' ? `Mock response for chat.prompt on mock (job_other): ${job.prompt}`
            : o.resultBug === 'truncated' ? full.slice(0, full.length - 8)
              : o.resultBug === 'duplicated' ? `${full} ${job.prompt}` : full;
          body.result = { output: { text } };
        }
        json(res, 200, body);
      }, o.slowGetMs);
    }
    if (m && m[2] === 'cancel' && req.method === 'POST') {
      return readBody(req, () => {
        if (!/^[\w.:-]{16,128}$/.test(req.headers['idempotency-key'] ?? '')) return json(res, 400, { error: { code: 'UBAG-VALIDATION-IDEMPOTENCY-KEY-001' } });
        const job = visible(m[1]);
        if (!job) return json(res, 404, { error: { code: 'UBAG-JOB-NOT-FOUND-001' } });
        if (job.status !== 'completed') job.status = 'cancelled';
        json(res, 202, { job_id: m[1], status: job.status, idempotent_replay: false });
      });
    }
    if (m && m[2]?.startsWith('artifacts/') && req.method === 'PUT') { req.resume(); return json(res, 400, { error: { code: 'UBAG-VALIDATION-ARTIFACT-NOT-DECLARED-001' } }); }
    if (path === '/v1/openai/chat/completions' && req.method === 'POST') {
      if (st.facadeInflight >= o.facadeLimit) { req.resume(); return overload(res, 'upload_memory', true); }
      if (o.facadeBad) { req.resume(); return json(res, 400, { error: { code: 'invalid_request' } }); }
      st.facadeInflight += 1;
      return readBody(req, (text, size) => {
        setTimeout(() => {
          st.facadeInflight -= 1;
          let body; try { body = JSON.parse(text); } catch { return json(res, size > o.maxBody ? 413 : 400, { error: { code: 'invalid_request' } }); }
          const parts = (body.messages ?? []).flatMap((x) => (Array.isArray(x.content) ? x.content : []));
          for (const p of parts) if (p.type === 'image_url' && !/^data:image\/(png|jpeg|webp|gif);base64,[A-Za-z0-9+/=]+$/.test(p.image_url?.url ?? '')) return json(res, 400, { error: { code: 'invalid_request' } });
          json(res, 200, { choices: [{ message: { role: 'assistant', content: 'ok' } }] });
        }, 30);
      });
    }
    req.resume(); json(res, 404, { error: { code: 'UBAG-NOT-FOUND-001' } });
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const fake = { server, st, port: server.address().port, env: { UBAG_LOAD_BASE_URL: `http://127.0.0.1:${server.address().port}`, UBAG_LOAD_API_KEY: KEY, UBAG_LOAD_API_KEY_B: KEY_B } };
  fakes.push(fake);
  return fake;
}

const FAST = ['--poll-interval-ms', '10', '--settle-ms', '0', '--metrics-interval-ms', '10', '--max-body-bytes', '4096', '--recovery-ms', '2000'];
const cfgFor = (fake, extra, env = {}) => parseArgs([`--${ACK_FLAG}`, ...FAST, ...extra], { ...fake.env, ...env });
const outDir = () => mkdtempSync(join(tmpdir(), 'ubag-load-'));
// Shape written by the Go bench (UBAG_VOICE_LATENCY_REPORT); values are fixtures, not measurements.
const voiceRows = (p95Speaker = 0.6) => [1, 5, 10, 20].flatMap((sessions) => [
  { sessions, direction: 'mic', samples: 100 * sessions, p50_ms: 0.1, p95_ms: 0.3, p99_ms: 0.5 },
  { sessions, direction: 'speaker', samples: 100 * sessions, p50_ms: 0.2, p95_ms: p95Speaker, p99_ms: 0.9 },
]);
const voiceReportFile = (rows = voiceRows(), schema = 'ubag-voice-latency/v1') => {
  const file = join(outDir(), 'voice-latency.json');
  writeFileSync(file, JSON.stringify({ schema, non_authoritative: true, rows }));
  return file;
};

// --------------------------------------------------------------- pure logic

describe('report aggregation math', () => {
  it('computes nearest-rank percentiles and summary stats', () => {
    const values = Array.from({ length: 100 }, (_, i) => 100 - i); // unsorted 100..1
    assert.deepEqual(summarize(values), { count: 100, min: 1, max: 100, mean: 50.5, p50: 50, p95: 95, p99: 99 });
    assert.equal(percentile([5], 99), 5);
    assert.equal(percentile([], 50), null);
    assert.equal(summarize([]).p95, null);
    assert.equal(summarize([1, 2, 3, 4]).p50, 2);
  });

  it('computes histogram quantiles and metric deltas over tracked series only', () => {
    const buckets = [{ le: 1, count: 50 }, { le: 2, count: 100 }, { le: Infinity, count: 100 }];
    assert.equal(histogramQuantile(0.5, buckets), 1);
    assert.equal(histogramQuantile(0.75, buckets), 1.5);
    assert.equal(histogramQuantile(0.5, [{ le: 1, count: 0 }]), null);
    const before = parseProm([
      'ubag_admission_rejections_total{reason="upload_memory"} 2', 'ubag_gateway_http_inflight_requests{service="x"} 1', 'other_metric 5',
      'ubag_queue_job_wait_duration_seconds_bucket{queue="q",le="1"} 0', 'ubag_queue_job_wait_duration_seconds_bucket{queue="q",le="+Inf"} 0',
      'ubag_queue_job_wait_duration_seconds_sum{queue="q"} 0', 'ubag_queue_job_wait_duration_seconds_count{queue="q"} 0',
    ].join('\n'));
    const after = parseProm([
      'ubag_admission_rejections_total{reason="upload_memory"} 7', 'ubag_gateway_http_inflight_requests{service="x"} 0', 'other_metric 9',
      'ubag_queue_job_wait_duration_seconds_bucket{queue="q",le="1"} 10', 'ubag_queue_job_wait_duration_seconds_bucket{queue="q",le="+Inf"} 10',
      'ubag_queue_job_wait_duration_seconds_sum{queue="q"} 4', 'ubag_queue_job_wait_duration_seconds_count{queue="q"} 10',
    ].join('\n'));
    const d = metricsDelta(before, after, new Map([['ubag_gateway_http_inflight_requests{service="x"}', 40]]));
    assert.equal(d.rejections_by_reason.upload_memory, 5);
    assert.equal(d.series['ubag_gateway_http_inflight_requests{service="x"}'].max_sampled, 40);
    assert.equal(d.series.other_metric, undefined);
    const h = d.histograms['ubag_queue_job_wait_duration_seconds{queue="q"}'];
    assert.equal(h.count, 10); assert.equal(h.mean_s, 0.4); assert.equal(h.p50_s, 0.5);
  });

  it('parses cgroup v2 fixtures: throttle, memory headroom, oom_kill, PSI', () => {
    const a = parseSample(v2Fixture({ nrPeriods: 100, nrThrottled: 0, oomKill: 1, current: 500 * MB }), 0);
    const b = parseSample(v2Fixture({ nrPeriods: 300, nrThrottled: 60, usage: 3_000_000, oomKill: 3, current: 600 * MB, peak: 800 * MB }), 10_000);
    assert.equal(a.version, 'v2'); assert.equal(a.cpu.quota_cores, 1.5); assert.equal(a.mem.max, 1000 * MB);
    const c = summarizeContainer([a, b]);
    assert.equal(c.cpu_throttled_pct, 30); // 60 of 200 new periods
    assert.equal(c.cpu_throttled_ms, 600);
    assert.equal(c.cpu_cores_avg, 0.2);
    assert.equal(c.memory_headroom_pct, 20); assert.equal(c.oom_kills, 2);
    assert.equal(c.psi_memory_full_avg10_max, 0.5); assert.equal(c.psi_cpu_some_avg10_max, 1.25);
    assert.equal(parseSample(v2Fixture({ memMax: 'max', cpuMax: 'max 100000' })).mem.max, null);
  });

  it('parses cgroup v1 fallback fixtures', () => {
    const a = parseSample(v1Fixture({}), 0); const b = parseSample(v1Fixture({ nrPeriods: 50, nrThrottled: 10, oomKill: 2, peak: 900 * MB }), 1000);
    assert.equal(a.version, 'v1'); assert.equal(a.cpu.quota_cores, 2); assert.equal(a.pressure.cpu, null);
    const c = summarizeContainer([a, b]);
    assert.equal(c.cgroup, 'v1'); assert.equal(c.cpu_throttled_pct, 20); assert.equal(c.oom_kills, 2); assert.equal(c.memory_headroom_pct, 10);
    assert.equal(parseSample(v1Fixture({ limit: '9223372036854771712' })).mem.max, null); // v1 "unlimited"
    assert.throws(() => parseSample('@@nproc\n4\n'), /no readable cgroup/);
  });

  it('evaluates the helper pressure rules as booleans, with the 2-minute recovery window', () => {
    // rows: [t ms, busy jiffies in this 100-jiffy step (= cpu %), MemAvailable %]
    const mk = (rows) => { let busy = 0; let total = 0; return rows.map(([t, cpu, avail]) => { total += 100; busy += cpu; return { t, mem_total_kb: 1000, mem_available_kb: avail * 10, cpu: { busy, total } }; }); };
    const calm = evaluatePressure(mk([[0, 0, 50], [60_000, 30, 50], [120_000, 30, 50]]));
    assert.equal(calm.pressure_triggered, false); assert.equal(calm.recovered, null); assert.equal(calm.cpu_over_80, false);
    const spike = evaluatePressure(mk([[0, 0, 50], [30_000, 90, 50], [60_000, 30, 50], [120_000, 30, 50]]));
    assert.equal(spike.cpu_over_80, true); assert.equal(spike.recovered, false); assert.equal(spike.recovery_observed_ms, 60_000);
    const healed = evaluatePressure(mk([[0, 0, 50], [30_000, 90, 50], [60_000, 30, 50], [120_000, 30, 50], [180_000, 40, 50]]));
    assert.equal(healed.recovered, true); assert.equal(healed.recovery_observed_ms, 120_000);
    const mem = evaluatePressure(mk([[0, 0, 50], [30_000, 10, 15], [60_000, 10, 24], [200_000, 10, 24]]));
    assert.equal(mem.mem_available_under_20, true); assert.equal(mem.recovered, false); // 24% never exceeds the 25% recovery bar
    assert.equal(mem.host_mem_available_pct_min, 15);
    assert.equal(evaluatePressure([]).sampled, false);
  });

  it('samples every target, reports gateway/browser/worker, and skips a dead container without failing the rest', async () => {
    const targets = parseTargets('gateway=g,browser=b,worker=w');
    let n = 0;
    const exec = async (name) => {
      if (name === 'w') throw Object.assign(new Error('No such container'), { code: 1 });
      if (name === 'g') n += 1;
      return v2Fixture({ nrPeriods: n * 100, nrThrottled: n * 10, current: 400 * MB });
    };
    const stop = startCgroupSampler(targets, 15, { exec });
    await new Promise((r) => setTimeout(r, 50));
    const out = await stop();
    assert.equal(out.containers.gateway.cgroup, 'v2'); assert.ok(out.containers.gateway.samples >= 2);
    assert.equal(out.containers.gateway.cpu_throttled_pct, 10);
    assert.match(out.containers.worker.skipped, /cgroup read failed/);
    assert.equal(out.summary.memory_headroom_pct, 60); assert.equal(out.summary.oom_kills, 0);
    assert.equal(out.host_pressure.sampled, true);
    const none = await startCgroupSampler(targets, 15, { exec: async () => { throw Object.assign(new Error('x'), { code: 'ENOENT' }); } })();
    assert.match(none.skipped, /unavailable/);
    assert.equal(none.summary, undefined);
  });

  it('rejects unsafe container targets', () => {
    assert.throws(() => parseTargets('gw=--privileged'), /invalid container target/);
    assert.throws(() => parseTargets('a;b'), /invalid container target/);
    assert.deepEqual(parseTargets('x, gw=ubag-gateway'), [{ role: 'x', container: 'x' }, { role: 'gw', container: 'ubag-gateway' }]);
  });
});

describe('Retry-After backoff logic', () => {
  it('prefers the larger of header and body hint, supports HTTP-date', () => {
    const h = (v) => new Headers(v == null ? {} : { 'Retry-After': v });
    assert.equal(parseRetryAfterMs(h('2'), undefined), 2000);
    assert.equal(parseRetryAfterMs(h('1'), { error: { retry_after_ms: 1500 } }), 1500);
    assert.equal(parseRetryAfterMs(h('3'), { error: { retry_after_ms: 1500 } }), 3000);
    assert.equal(parseRetryAfterMs(h(null), { error: { retry_after_ms: 250 } }), 250);
    assert.equal(parseRetryAfterMs(h(null), {}), null);
    assert.equal(parseRetryAfterMs(h('garbage'), {}), null);
    const at = new Date(1_000_000 + 5000).toUTCString();
    assert.equal(parseRetryAfterMs(h(at), {}, Math.floor(1_000_000 / 1000) * 1000), Date.parse(at) - Math.floor(1_000_000 / 1000) * 1000);
  });

  it('honors the hint, caps it, adds jitter only upward, and falls back to exponential', () => {
    assert.equal(retryDelayMs({ attempt: 3, retryAfterMs: 1000 }), 1000);
    assert.equal(retryDelayMs({ attempt: 0, retryAfterMs: 90_000, capMs: 60_000 }), 60_000);
    assert.equal(retryDelayMs({ attempt: 0, retryAfterMs: 1000, jitter: 0.5, rand: () => 0 }), 1000);
    assert.equal(retryDelayMs({ attempt: 0, retryAfterMs: 1000, jitter: 0.5, rand: () => 1 }), 1500);
    assert.deepEqual([0, 1, 2, 3].map((attempt) => retryDelayMs({ attempt })), [250, 500, 1000, 2000]);
    assert.equal(retryDelayMs({ attempt: 20, capMs: 5000 }), 5000);
  });

  it('the scenario client really backs off per the server hint and retries (bounded)', async () => {
    const fake = await startFake({ rejectFirstCreates: 3 });
    const delays = [];
    const cfg = cfgFor(fake, ['--scenario', 'queue-1000', '--jobs', '5', '--rate', '5000', '--max-retries', '5']);
    const report = await run(cfg, { sleep: (ms) => { delays.push(ms); return new Promise((r) => setTimeout(r, Math.min(ms, 5))); } });
    assert.equal(report.retry.retries, 3);
    assert.equal(report.retry.honored, 3);
    // hint = max(header "2" => 2000ms, body 15ms); jitter only adds, so every backoff >= 2000ms
    assert.equal(delays.filter((d) => d >= 2000).length, 3);
    assert.equal(report.summary.unaccepted_jobs, 0);
    // bounded: with max-retries 1 and 3 rejections, some job gives up and is counted
    const fake2 = await startFake({ rejectFirstCreates: 100 });
    const r2 = await run(cfgFor(fake2, ['--scenario', 'queue-1000', '--jobs', '2', '--rate', '5000', '--max-retries', '1']), { sleep: async () => {} });
    assert.equal(r2.summary.unaccepted_jobs, 2);
    assert.equal(r2.retry.exhausted, 2);
  });
});

describe('host allowlist refusal', () => {
  it('accepts loopback and private ranges without an allowlist', () => {
    for (const u of ['http://127.0.0.1:8080', 'http://localhost:8080/', 'http://[::1]:8080', 'http://10.1.2.3', 'http://172.16.0.5', 'http://172.31.255.1', 'http://192.168.1.9:3000', 'http://gw.localhost:80']) {
      assert.doesNotThrow(() => checkTarget(u, {}), u);
    }
  });

  it('refuses public/unlisted hosts and honors the explicit allowlist', () => {
    for (const u of ['https://vps.example.com', 'http://8.8.8.8', 'http://172.32.0.1', 'http://172.15.0.1', 'http://11.0.0.1', 'http://gateway:8080']) {
      assert.throws(() => checkTarget(u, {}), /not loopback\/private/, u);
    }
    assert.throws(() => checkTarget('https://vps.example.com', { UBAG_LOAD_ALLOWED_HOSTS: '*' }), /not loopback\/private/);
    assert.throws(() => checkTarget('https://vps.example.com', { UBAG_LOAD_ALLOWED_HOSTS: 'other.example.com' }), /not loopback\/private/);
    assert.equal(checkTarget('https://Vps.Example.com/api/', { UBAG_LOAD_ALLOWED_HOSTS: ' a.com, vps.example.com ' }), 'https://vps.example.com/api');
    assert.equal(checkTarget('https://vps.example.com:8443', { UBAG_LOAD_ALLOWED_HOSTS: 'vps.example.com:8443' }), 'https://vps.example.com:8443');
    assert.throws(() => checkTarget('ftp://127.0.0.1', {}), /http or https/);
    assert.throws(() => checkTarget('http://user:pw@127.0.0.1', {}), /credentials/);
  });

  it('refuses to start without the acknowledgement flag, scenario, key, or an allowed host', () => {
    const env = { UBAG_LOAD_BASE_URL: 'http://127.0.0.1:1', UBAG_LOAD_API_KEY: 'k' };
    assert.throws(() => parseArgs(['--scenario', 'queue-1000'], env), /--i-understand-this-is-load/);
    assert.throws(() => parseArgs(['--scenario', 'queue-1000'], {}), /--i-understand-this-is-load/); // ack checked first
    assert.throws(() => parseArgs([`--${ACK_FLAG}`], env), /--scenario is required/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--scenario', 'nope'], env), /unknown scenario/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--scenario', 'all'], { ...env, UBAG_LOAD_API_KEY: '' }), /UBAG_LOAD_API_KEY/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--scenario', 'all'], { UBAG_LOAD_API_KEY: 'k' }), /UBAG_LOAD_BASE_URL/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--scenario', 'all'], { ...env, UBAG_LOAD_BASE_URL: 'https://prod.example.com' }), /UBAG_LOAD_ALLOWED_HOSTS/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--scenario', 'all', '--jobs', '0'], env), /--jobs/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--scenario', 'all', '--bogus'], env), /unknown option/);
    const ok = parseArgs([`--${ACK_FLAG}`, '--scenario', 'all', '--docker-stats-container', 'ubag-gateway'], env);
    assert.equal(ok.scenarios.length, 7); assert.equal(ok.dockerContainer, 'ubag-gateway');
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--scenario', 'all', '--docker-stats-container', '--privileged'], env), /requires a value|invalid container target/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--scenario', 'all', '--cgroup-containers', 'gw=a b'], env), /invalid container target/);
    assert.deepEqual(ok.cgroupTargets, [{ role: 'ubag-gateway', container: 'ubag-gateway' }]);
    assert.deepEqual(parseArgs([`--${ACK_FLAG}`, '--scenario', 'all', '--cgroup-containers', 'gateway=g,browser=b,worker=w'], env).cgroupTargets.map((t) => t.role), ['gateway', 'browser', 'worker']);
  });

  it('main() exits 2 and sends no request when refused', async () => {
    const fake = await startFake();
    assert.equal(await main(['--scenario', 'queue-1000'], fake.env), 2);
    assert.equal(await main([`--${ACK_FLAG}`, '--scenario', 'queue-1000'], { ...fake.env, UBAG_LOAD_BASE_URL: 'https://prod.example.com' }), 2);
    assert.equal(fake.st.requests, 0);
  });
});

describe('threshold evaluation', () => {
  it('applies max_/min_ rules, skips unmeasured keys, flags malformed ones', () => {
    const t = { max_a: 0, max_p95: 2000, min_dup: 1, max_missing: 0, _comment: 'x' };
    assert.deepEqual(evaluateThresholds({ a: 0, p95: 2000, dup: 1 }, t).passed, true);
    const bad = evaluateThresholds({ a: 1, p95: 2001, dup: 0 }, t);
    assert.equal(bad.passed, false);
    assert.deepEqual(bad.results.filter((r) => !r.ok).map((r) => r.name).sort(), ['max_a', 'max_p95', 'min_dup']);
    assert.equal(bad.results.find((r) => r.name === 'max_missing').ok, true);
    assert.equal(evaluateThresholds({}, { weird: 3 }).passed, false);
  });

  it('ships sensible defaults in thresholds.json', () => {
    const t = JSON.parse(readFileSync(join(here, 'thresholds.json'), 'utf8'));
    assert.equal(t.max_server_errors_non_overload, 0);
    assert.equal(t.max_lost_jobs, 0);
    assert.equal(t.min_dup_jobs_per_key_min, 1);
    assert.equal(t.max_dup_jobs_per_key_max, 1);
    assert.equal(t.max_create_p95_ms, 2000);
  });
});

// ----------------------------------------------------- scenario smoke tests

describe('scenario smoke runs against the in-process fake gateway', () => {
  it('queue-1000 (scaled down): every distinct key enqueues and completes', async () => {
    const fake = await startFake();
    const report = await run(cfgFor(fake, ['--scenario', 'queue-1000', '--jobs', '60', '--rate', '5000']));
    const q = report.scenarios['queue-1000'];
    assert.equal(q.requested, 60); assert.equal(q.accepted, 60);
    assert.deepEqual(q.final, { completed: 60 });
    assert.equal(q.create_attempt_statuses['202'], 60);
    assert.equal(fake.st.creates, 60);
    assert.equal(report.summary.lost_jobs, 0); assert.equal(report.summary.unfinished_jobs, 0);
    assert.ok(report.ops['queue-1000/completion'].ok.count === 60);
    assert.ok(report.ops['queue-1000/queue_wait_observed'].ok.count > 0);
    assert.equal(report.thresholds.passed, true);
  });

  it('clients-100 (scaled): mixed payloads; malformed/oversized fail safely with 4xx', async () => {
    const fake = await startFake();
    const report = await run(cfgFor(fake, ['--scenario', 'clients-100', '--clients', '16', '--iterations', '3', '--with-upload']));
    const c = report.scenarios['clients-100'];
    assert.equal(Object.keys(c.requests_by_kind).sort().join(), 'facade_image,malformed,oversized,text,upload');
    assert.equal(report.summary.unsafe_payload_outcomes, 0, JSON.stringify(report.violations));
    assert.equal(report.summary.server_errors_non_overload, 0);
    assert.equal(report.summary.hangs, 0);
    assert.ok((report.ops['clients-100/oversized'].statuses['413'] ?? 0) + (report.ops['clients-100/oversized'].statuses.network ?? 0) > 0);
    assert.equal(report.ops['clients-100/malformed_json'].statuses['400'] > 0, true);
    assert.equal(report.ops['clients-100/malformed_file'].statuses['400'] > 0, true);
    assert.equal(report.thresholds.passed, true, JSON.stringify(report.thresholds.results.filter((r) => !r.ok)));
  });

  it('clients-100 flags an oversized request the gateway accepts', async () => {
    const fake = await startFake({ maxBody: 10 * 1024 * 1024 }); // gateway limit far above what the harness assumes
    const report = await run(cfgFor(fake, ['--scenario', 'clients-100', '--clients', '4', '--iterations', '1']));
    assert.ok(report.summary.unsafe_payload_outcomes >= 1);
    assert.equal(report.thresholds.passed, false);
  });

  it('duplicates: one job per key under concurrent identical requests, consistent cancel races', async () => {
    const fake = await startFake();
    const report = await run(cfgFor(fake, ['--scenario', 'duplicates', '--dup-keys', '3', '--dup-concurrency', '10', '--cancel-races', '6']));
    const d = report.scenarios.duplicates;
    assert.equal(d.per_key.length, 3);
    for (const p of d.per_key) { assert.equal(p.distinct_jobs, 1); assert.equal(p.non_replay, 1); assert.equal(p.replays, 9); }
    assert.equal(report.summary.dup_jobs_per_key_max, 1); assert.equal(report.summary.dup_jobs_per_key_min, 1);
    assert.equal(d.cancel_outcomes.cancelled, 6);
    assert.equal(report.summary.cancel_inconsistencies, 0, JSON.stringify(report.violations));
    assert.equal(fake.st.creates, 3 + 6);
    assert.equal(report.thresholds.passed, true);
  });

  it('duplicates detects a gateway that creates a job per duplicate request', async () => {
    const fake = await startFake({ dupBug: true });
    const report = await run(cfgFor(fake, ['--scenario', 'duplicates', '--dup-keys', '2', '--dup-concurrency', '4', '--cancel-races', '0']));
    assert.equal(report.summary.dup_violations, 2);
    assert.equal(report.summary.dup_jobs_per_key_max, 4);
    assert.equal(report.thresholds.passed, false);
  });

  it('overload: every 429/503 carries Retry-After + retry_after_ms and the gateway recovers', async () => {
    const fake = await startFake({ facadeLimit: 3, maxInflight: 25 });
    const report = await run(cfgFor(fake, ['--scenario', 'overload', '--burst', '30', '--burst-body-bytes', '1024', '--inflight-burst', '60', '--recovery-requests', '4']));
    const o = report.scenarios.overload;
    assert.ok(o.rejected_responses > 0, 'the fake must have rejected something');
    assert.equal(o.recovered, true);
    assert.equal(report.summary.overload_missing_retry_after, 0);
    assert.equal(report.summary.overload_missing_retry_after_ms_body, 0);
    assert.equal(report.summary.server_errors_non_overload, 0);
    assert.equal(report.summary.unrecovered, 0);
    assert.ok(Object.keys(report.client_rejections_by_reason).length > 0);
    assert.equal(report.thresholds.passed, true, JSON.stringify(report.thresholds.results.filter((r) => !r.ok)));
  });

  it('overload fails the run when a 503 lacks Retry-After, and notes missing body hints separately', async () => {
    const bad = await startFake({ facadeLimit: 2, omitRetryAfter: true });
    const r1 = await run(cfgFor(bad, ['--scenario', 'overload', '--burst', '20', '--burst-body-bytes', '1024', '--inflight-burst', '0', '--recovery-requests', '2']));
    assert.ok(r1.summary.overload_missing_retry_after > 0);
    assert.ok(r1.summary.server_errors_non_overload > 0);
    assert.equal(r1.thresholds.passed, false);
    const noBody = await startFake({ facadeLimit: 2, facadeNoBodyMs: true });
    const r2 = await run(cfgFor(noBody, ['--scenario', 'overload', '--burst', '20', '--burst-body-bytes', '1024', '--inflight-burst', '0', '--recovery-requests', '2']));
    assert.equal(r2.summary.overload_missing_retry_after, 0);
    assert.ok(r2.summary.overload_missing_retry_after_ms_body > 0);
    assert.equal(r2.thresholds.passed, false);
  });

  it('metrics-snapshot: reports deltas of tracked metrics and cgroup resource samples when available', async () => {
    const fake = await startFake({ facadeLimit: 2 });
    const exec = async () => v2Fixture({ current: 64 * MB });
    const report = await run(cfgFor(fake, ['--scenario', 'overload,metrics-snapshot', '--burst', '20', '--burst-body-bytes', '1024', '--inflight-burst', '0', '--recovery-requests', '1', '--cgroup-containers', 'gateway=fake-gw', '--docker-interval-ms', '10']), { dockerExec: exec });
    assert.equal(report.metrics.available, true);
    assert.ok(report.metrics.rejections_by_reason.upload_memory > 0);
    assert.ok(report.metrics.series['ubag_upload_memory_budget_bytes'].after === 268435456);
    assert.ok('ubag_gateway_request_latency_seconds{route="/v1/jobs"}' in report.metrics.histograms);
    assert.ok('ubag_queue_job_wait_duration_seconds{queue="default"}' in report.metrics.histograms);
    assert.ok(report.metrics.series['ubag_admission_tokens_active{kind="tenant"}']);
    assert.ok(report.metrics.series['ubag_db_pool_connections{state="open"}']);
    assert.ok(report.resources.containers.gateway.samples >= 2); assert.equal(report.resources.containers.gateway.memory_peak_mb, 64);
    assert.equal(report.resources.containers.gateway.memory_headroom_pct, 93.6); assert.equal(report.summary.memory_headroom_pct, 93.6);
    assert.equal(report.resources.containers.gateway.cpu_throttled_pct, 0); assert.equal(report.resources.host_pressure.pressure_triggered, false);
    assert.match(renderMarkdown(report), /Helper pressure rules/);
    const md = renderMarkdown(report);
    assert.match(md, /Rejections by reason/); assert.match(md, /Resource usage/);
    assert.ok(!JSON.stringify(report).includes(KEY), 'API key must never appear in the report');
  });

  it('metrics-snapshot degrades gracefully without docker or /v1/metrics', async () => {
    const fake = await startFake();
    const noDocker = async () => { throw Object.assign(new Error('spawn docker ENOENT'), { code: 'ENOENT' }); };
    const report = await run(cfgFor(fake, ['--scenario', 'metrics-snapshot', '--docker-stats-container', 'x']), { dockerExec: noDocker });
    assert.match(report.resources.skipped, /unavailable/);
    assert.equal(report.summary.memory_headroom_pct, undefined);
    const noMetrics = await run(cfgFor(fake, ['--scenario', 'metrics-snapshot']), { request: async (method, path) => (path === '/v1/metrics' ? { status: 404, ms: 1 } : { status: 200, ms: 1, json: {} }) });
    assert.equal(noMetrics.metrics.available, false);
    assert.match(renderMarkdown(noMetrics), /unavailable/);
  });

  it('main(): all scenarios write report.json + summary.md and exit 0; bad gateway exits 1', async () => {
    const fake = await startFake({ facadeLimit: 3 });
    const dir = outDir();
    const args = [`--${ACK_FLAG}`, ...FAST, '--scenario', 'all', '--jobs', '20', '--rate', '5000', '--clients', '8', '--iterations', '2', '--dup-keys', '2', '--dup-concurrency', '5', '--cancel-races', '3', '--burst', '15', '--burst-body-bytes', '1024', '--inflight-burst', '0', '--recovery-requests', '2', '--steady-seconds', '1', '--steady-rate', '5', '--steady-read-rate', '20', '--steady-seed-jobs', '2', '--events-subscribers', '3', '--out-dir', dir];
    const logs = []; const orig = console.log; console.log = (...a) => logs.push(a.join(' '));
    let code; try { code = await main(args, fake.env); } finally { console.log = orig; }
    assert.equal(code, 0, logs.join('\n'));
    const [stamp] = readdirSync(dir);
    assert.ok(existsSync(join(dir, stamp, 'report.json')) && existsSync(join(dir, stamp, 'summary.md')));
    const report = JSON.parse(readFileSync(join(dir, stamp, 'report.json'), 'utf8'));
    assert.equal(Object.keys(report.scenarios).length, 7);
    assert.match(readFileSync(join(dir, stamp, 'summary.md'), 'utf8'), /verdict: \*\*PASS\*\*/);

    const broken = await startFake({ dupBug: true });
    const origErr = console.log; console.log = () => {};
    let code2; try { code2 = await main([`--${ACK_FLAG}`, ...FAST, '--scenario', 'duplicates', '--dup-keys', '1', '--dup-concurrency', '3', '--cancel-races', '0', '--out-dir', outDir()], broken.env); } finally { console.log = origErr; }
    assert.equal(code2, 1);
  });
});

// ------------------------------------------------ integrity gates (P0.5)

describe('acceptance integrity gates fail closed', () => {
  const QUICK = ['--scenario', 'queue-1000', '--jobs', '6', '--rate', '5000'];
  const failed = (report) => report.thresholds.results.filter((r) => !r.ok).map((r) => r.name);

  it('a clean run verifies every result and event log and probes the second tenant', async () => {
    const fake = await startFake();
    const report = await run(cfgFor(fake, QUICK));
    assert.equal(report.summary.results_verified, 6);
    assert.equal(report.summary.events_verified, 6);
    assert.ok(report.summary.tenant_probe_requests > 0);
    assert.equal(report.summary.cross_tenant_leaks, 0);
    assert.equal(report.thresholds.passed, true, JSON.stringify(failed(report)));
    assert.ok(!JSON.stringify(report).includes(KEY_B), 'second tenant key must never appear in the report');
  });

  for (const bug of ['wrong', 'truncated', 'duplicated']) {
    it(`a ${bug} result body is a FAIL`, async () => {
      const fake = await startFake({ resultBug: bug });
      const report = await run(cfgFor(fake, QUICK));
      assert.equal(report.summary.result_mismatches, 6, bug);
      assert.equal(report.summary.results_verified, 0);
      assert.equal(report.thresholds.passed, false);
      assert.ok(failed(report).includes('max_result_mismatches'));
    });
  }

  it('a duplicated terminal event is a FAIL', async () => {
    const fake = await startFake({ dupTerminal: true });
    const report = await run(cfgFor(fake, QUICK));
    assert.equal(report.summary.terminal_event_violations, 6);
    assert.ok(failed(report).includes('max_terminal_event_violations'));
  });

  it('a cross-tenant read is a FAIL', async () => {
    const fake = await startFake({ leak: true });
    const report = await run(cfgFor(fake, QUICK));
    assert.ok(report.summary.cross_tenant_leaks > 0);
    assert.ok(failed(report).includes('max_cross_tenant_leaks'));
  });

  it('an unusable second-tenant credential is not silently accepted', async () => {
    const fake = await startFake();
    const report = await run(cfgFor(fake, QUICK, { UBAG_LOAD_API_KEY_B: 'not-a-valid-key' }));
    assert.ok(report.summary.tenant_probe_unexpected > 0); // 401 is neither 403 nor 404
    assert.ok(failed(report).includes('max_tenant_probe_unexpected'));
    assert.throws(() => cfgFor(fake, QUICK, { UBAG_LOAD_API_KEY_B: KEY }), /must differ/);
  });

  it('completed_with_warnings is counted separately and is not a success', async () => {
    const fake = await startFake({ finalStatus: 'completed_with_warnings' });
    const report = await run(cfgFor(fake, QUICK));
    assert.equal(report.summary.warning_jobs, 6);
    assert.equal(report.summary.failed_jobs, 0);
    assert.equal(report.summary.results_verified, 0);
    assert.ok(failed(report).includes('max_warning_jobs'));
  });

  it('facade_image failures are gated', async () => {
    const fake = await startFake({ facadeBad: true });
    const report = await run(cfgFor(fake, ['--scenario', 'clients-100', '--clients', '4', '--iterations', '2']));
    assert.ok(report.summary.facade_image_failures > 0);
    assert.ok(failed(report).includes('max_facade_image_failures'));
  });

  it('an overload scenario that produces zero rejections is a FAIL', async () => {
    const fake = await startFake(); // no limits: nothing is ever rejected
    const report = await run(cfgFor(fake, ['--scenario', 'overload', '--burst', '5', '--burst-body-bytes', '1024', '--inflight-burst', '5', '--recovery-requests', '1']));
    assert.equal(report.summary.overload_rejections, 0);
    assert.ok(failed(report).includes('min_overload_rejections'));
  });

  it('--require-goals turns every unmeasured threshold into a FAIL', async () => {
    assert.equal(evaluateThresholds({}, { max_x: 1 }, { strict: true }).passed, false);
    assert.equal(evaluateThresholds({}, { max_x: 1 }).passed, true);
    const fake = await startFake();
    const report = await run(cfgFor(fake, [...QUICK, '--require-goals']));
    const unmeasured = report.thresholds.results.filter((r) => !r.ok && /not measured/.test(r.note ?? '')).map((r) => r.name);
    assert.ok(unmeasured.includes('max_steady_create_p95_ms') && unmeasured.includes('max_steady_read_p95_ms'), JSON.stringify(unmeasured));
    assert.ok(unmeasured.includes('min_memory_headroom_pct') && unmeasured.includes('max_oom_kills'), 'no cgroup sampling => headroom/oom goals fail closed');
    assert.equal(report.thresholds.passed, false);
    assert.equal(report.thresholds.goals_required, true);
    assert.match(renderMarkdown(report), /--require-goals/);
  });

  it('--require-goals fails when the second tenant is not configured', async () => {
    const fake = await startFake();
    const report = await run(cfgFor(fake, [...QUICK, '--require-goals'], { UBAG_LOAD_API_KEY_B: '' }));
    assert.equal(report.summary.tenant_probe_requests, 0);
    assert.ok(failed(report).includes('min_tenant_probe_requests'));
  });

  it('steady-state measures its own create and read p95; a full --require-goals run passes against a healthy fake', async () => {
    const fake = await startFake({ facadeLimit: 3 });
    const report = await run(cfgFor(fake, ['--scenario', 'all', '--require-goals', '--jobs', '12', '--rate', '5000', '--clients', '8', '--iterations', '2', '--dup-keys', '2', '--dup-concurrency', '5', '--cancel-races', '3', '--burst', '15', '--burst-body-bytes', '1024', '--inflight-burst', '0', '--recovery-requests', '2', '--steady-seconds', '1', '--steady-rate', '5', '--steady-read-rate', '20', '--steady-seed-jobs', '2', '--cgroup-containers', 'gateway=g,browser=b,worker=w', '--docker-interval-ms', '20', '--events-subscribers', '3', '--voice-latency', voiceReportFile()]), { dockerExec: async () => v2Fixture({ current: 300 * MB }) });
    assert.equal(report.summary.memory_headroom_pct, 70); assert.equal(report.summary.oom_kills, 0);
    assert.ok(Number.isFinite(report.summary.steady_create_p95_ms));
    assert.ok(Number.isFinite(report.summary.steady_read_p95_ms));
    assert.ok(report.ops['steady-state/read'].ok.count >= 15);
    assert.equal(report.thresholds.passed, true, JSON.stringify(report.thresholds.results.filter((r) => !r.ok)));
    assert.equal(report.summary.voice_relay_p95_ms, 0.6);
    assert.equal(report.summary.voice_relay_max_sessions, 20);
  });

  it('--require-goals fails closed without a voice latency report, and fails on a slow or malformed one', async () => {
    const fake = await startFake();
    const missing = await run(cfgFor(fake, [...QUICK, '--require-goals']));
    assert.ok(missing.thresholds.results.some((r) => r.name === 'max_voice_relay_p95_ms' && !r.ok && /not measured/.test(r.note)));
    const slow = await run(cfgFor(fake, [...QUICK, '--require-goals', '--voice-latency', voiceReportFile(voiceRows(100.5))]));
    assert.equal(slow.summary.voice_relay_p95_ms, 100.5);
    assert.ok(failed(slow).includes('max_voice_relay_p95_ms'));
    const bad = await run(cfgFor(fake, [...QUICK, '--require-goals', '--voice-latency', voiceReportFile(voiceRows(), 'nope')]));
    assert.equal(bad.summary.voice_relay_p95_ms, undefined);
    assert.ok(bad.violations.some((v) => /voice-latency: schema/.test(v.detail)));
    assert.ok(failed(bad).includes('max_voice_relay_p95_ms'));
  });

  it('voiceLatencySummary validates the bench report shape', () => {
    assert.deepEqual(voiceLatencySummary({ schema: 'ubag-voice-latency/v1', rows: voiceRows(12.34) }), { voice_relay_p95_ms: 12.34, voice_relay_max_sessions: 20 });
    const ok = voiceRows();
    assert.throws(() => voiceLatencySummary({ schema: 'ubag-voice-latency/v1', rows: ok.filter((r) => r.direction === 'mic') }), /both mic and speaker/);
    assert.throws(() => voiceLatencySummary({ schema: 'ubag-voice-latency/v1', rows: [{ ...ok[0], samples: 0 }, ok[1]] }), /no samples/);
    assert.throws(() => voiceLatencySummary({ schema: 'ubag-voice-latency/v1', rows: [{ ...ok[0], p95_ms: 'x' }, ok[1]] }), /non-numeric/);
    assert.throws(() => voiceLatencySummary({ schema: 'ubag-voice-latency/v1', rows: [] }), /non-empty/);
  });

  it('ships the plan goals in thresholds.goals.json and keeps 2000 ms as the separate burst limit', () => {
    const g = JSON.parse(readFileSync(join(here, 'thresholds.goals.json'), 'utf8'));
    assert.equal(g.max_steady_create_p95_ms, 200);
    assert.equal(g.max_steady_read_p95_ms, 100);
    assert.equal(g.max_voice_relay_p95_ms, 100);
    assert.equal(g.min_memory_headroom_pct, 20);
    assert.equal(g.max_oom_kills, 0);
    const t = JSON.parse(readFileSync(join(here, 'thresholds.json'), 'utf8'));
    assert.equal(t.max_create_p95_ms, 2000);
    assert.ok(t.min_overload_rejections >= 1);
  });
});

describe('audio-upload workload scenario (opt-in)', () => {
  const AUDIO = ['--scenario', 'audio-upload', '--target', 'chatgpt_web', '--audio-jobs', '3'];
  const failed = (report) => report.thresholds.results.filter((r) => !r.ok).map((r) => r.name);
  const env = { UBAG_LOAD_BASE_URL: 'http://127.0.0.1:1', UBAG_LOAD_API_KEY: 'k' };

  it('is not part of "all" and must be requested by name', () => {
    assert.ok(!parseArgs([`--${ACK_FLAG}`, '--scenario', 'all'], env).scenarios.includes('audio-upload'));
    assert.deepEqual(parseArgs([`--${ACK_FLAG}`, '--scenario', 'audio-upload'], env).scenarios, ['audio-upload']);
  });

  it('uploads the synthetic WAV, verifies the stored size and sha256, and passes against a healthy fake', async () => {
    const fake = await startFake({ acceptAudio: true });
    const report = await run(cfgFor(fake, AUDIO));
    assert.equal(report.summary.audio_upload_violations, 0, JSON.stringify(report.violations));
    assert.equal(report.summary.audio_artifacts_verified, 3);
    assert.equal(fake.st.uploads.length, 3);
    assert.ok(fake.st.uploads.every((n) => n === 44 + 5 * 16000 * 2)); // short profile: 5 s, 16 kHz, mono, 16-bit
    assert.equal(report.scenarios['audio-upload'].accepted, 3);
    assert.equal(report.thresholds.passed, true, JSON.stringify(report.thresholds.results.filter((r) => !r.ok)));
    assert.match(renderMarkdown(report), /## audio-upload/);
  });

  it('a target that rejects audio attachments (e.g. mock) is a FAIL, never a silent pass', async () => {
    const fake = await startFake(); // acceptAudio off: UBAG-VALIDATION-ATTACHMENTS-UNSUPPORTED-001
    const report = await run(cfgFor(fake, ['--scenario', 'audio-upload', '--audio-jobs', '2']));
    assert.equal(report.summary.audio_upload_violations, 2);
    assert.ok(failed(report).includes('max_audio_upload_violations'));
    assert.match(report.violations[0].detail, /must accept audio\/wav/);
  });

  for (const bug of ['short', 'checksum']) {
    it(`corrupted stored audio (${bug}) is a FAIL`, async () => {
      const fake = await startFake({ acceptAudio: true, artifactBug: bug });
      const report = await run(cfgFor(fake, AUDIO));
      assert.equal(report.summary.audio_upload_violations, 3);
      assert.ok(failed(report).includes('max_audio_upload_violations'));
    });
  }

  it('rejects an unknown fixture profile instead of guessing', async () => {
    const fake = await startFake({ acceptAudio: true });
    const report = await run(cfgFor(fake, [...AUDIO, '--audio-profile', 'huge']));
    assert.match(report.scenarios['audio-upload'].error, /unknown fixture profile/);
    assert.ok(failed(report).includes('max_scenario_errors'));
  });
});

// ------------------------------------------------ event delivery latency (P0.12)

describe('events-latency scenario', () => {
  const EV = ['--scenario', 'events-latency', '--events-subscribers', '4'];
  const failed = (report) => report.thresholds.results.filter((r) => !r.ok).map((r) => r.name);

  it('parses SSE frames across chunk boundaries and counts heartbeats', () => {
    const a = splitSse('id: 1\nevent: job.queued\ndata: {"a":1}\n\n: ping\n\nid: 2\nda');
    assert.deepEqual(a.frames, [{ id: '1', event: 'job.queued', data: '{"a":1}' }, { comment: true }]);
    assert.equal(a.rest, 'id: 2\nda');
    assert.deepEqual(splitSse(`${a.rest}ta: x\ndata: y\n\n`).frames, [{ id: '2', data: 'x\ny' }]);
  });

  it('parseProm keeps series whose quoted label values contain braces (route="/v1/jobs/{job_id}")', () => {
    const m = parseProm('ubag_gateway_http_requests_total{service="g",route="/v1/jobs/{job_id}/events",method="GET"} 7\nplain_total 2');
    assert.equal(m.get('ubag_gateway_http_requests_total{service="g",route="/v1/jobs/{job_id}/events",method="GET"}'), 7);
    assert.equal(m.get('plain_total'), 2);
  });

  it('narrows the clock offset by intersecting Date-header intervals', () => {
    assert.equal(clockBounds([]), null);
    // one sample only says "somewhere in a 1 s window"
    assert.deepEqual(clockBounds([{ server: 10_000, local: 10_300 }]), { offsetMs: 200, uncertaintyMs: 500, samples: 1 });
    // a second sample just after a second boundary pins the offset to within the sample spacing
    assert.deepEqual(clockBounds([{ server: 10_000, local: 10_980 }, { server: 11_000, local: 11_010 }]), { offsetMs: 5, uncertaintyMs: 15, samples: 2 });
  });

  it('measures per-event delivery latency calibrated to a skewed server clock; an uncalibrated run is visibly uncertain', async () => {
    const fake = await startFake({ clockSkewMs: -7000 });
    const report = await run(cfgFor(fake, EV));
    const ev = report.scenarios['events-latency'];
    assert.equal(ev.accepted, 4); assert.equal(ev.streams_terminal, 4);
    assert.equal(ev.live_events, 12); assert.equal(ev.backlog_events, 4); // running/token/completed live, queued replayed
    assert.ok(ev.clock.uncertaintyMs <= 50, JSON.stringify(ev.clock));
    assert.ok(Math.abs(ev.clock.offsetMs + 7000) <= 100, JSON.stringify(ev.clock));
    assert.equal(report.summary.event_latency_samples, 12);
    assert.ok(report.summary.event_latency_p95_ms < 500, `p95 ${report.summary.event_latency_p95_ms}`);
    assert.ok(report.ops['events-latency/sse_open'].ok.count === 4);
    assert.equal(report.thresholds.passed, true, JSON.stringify(failed(report)));
    assert.match(renderMarkdown(report), /## events-latency/);
    // calibration budget 0 = a single sample: the 1 s Date resolution leaves +/-500 ms and the report says so
    const loose = await run(cfgFor(fake, [...EV, '--events-calibrate-ms', '0']));
    assert.ok(loose.scenarios['events-latency'].clock.uncertaintyMs > 100);
  });

  it('deltas gateway request counters by route and folds the worker histogram', async () => {
    const fake = await startFake();
    const report = await run(cfgFor(fake, [...EV, '--events-calibrate-ms', '0']));
    assert.equal(report.metrics.requests_by_route['/v1/sse/jobs/*'].total, 4);
    assert.equal(report.metrics.requests_by_route['/v1/jobs'].by_status_class['2xx'], 4);
    assert.ok(report.metrics.requests_by_route['/v1/jobs/{job_id}'].total > 0, 'braced route labels must survive parsing');
    assert.ok('ubag_worker_job_duration_seconds{adapter="mock"}' in report.metrics.histograms);
    assert.ok('ubag_sse_connections_current' in report.metrics.series);
    const d = metricsDelta(parseProm('ubag_job_stage_duration_seconds_bucket{stage="x",le="1"} 0\nubag_job_stage_duration_seconds_bucket{stage="x",le="+Inf"} 0'),
      parseProm('ubag_job_stage_duration_seconds_bucket{stage="x",le="1"} 4\nubag_job_stage_duration_seconds_bucket{stage="x",le="+Inf"} 4\nubag_job_stage_duration_seconds_count{stage="x"} 4\nubag_job_stage_duration_seconds_sum{stage="x"} 2'));
    assert.equal(d.histograms['ubag_job_stage_duration_seconds{stage="x"}'].mean_s, 0.5);
    assert.match(renderMarkdown(report), /requests delta/);
  });

  it('a rejected stream, a stream with no terminal event and malformed ordering are FAILs', async () => {
    const rejected = await run(cfgFor(await startFake({ sseMode: 'reject' }), [...EV, '--events-calibrate-ms', '0']));
    assert.equal(rejected.summary.sse_stream_failures, 4);
    assert.ok(failed(rejected).includes('max_sse_stream_failures'));
    const silent = await run(cfgFor(await startFake({ sseMode: 'noTerminal' }), [...EV, '--events-subscribers', '2', '--events-timeout-ms', '300', '--events-calibrate-ms', '0']));
    assert.equal(silent.summary.sse_stream_failures, 2);
    const dup = await run(cfgFor(await startFake({ sseMode: 'dupSeq' }), [...EV, '--events-calibrate-ms', '0']));
    assert.ok(dup.summary.sse_event_violations >= 4, JSON.stringify(dup.violations));
    assert.ok(failed(dup).includes('max_sse_event_violations'));
  });

  it('a run that only ever saw replayed backlog measured nothing and FAILs', async () => {
    const report = await run(cfgFor(await startFake({ sseMode: 'backlog' }), [...EV, '--events-calibrate-ms', '0']));
    assert.equal(report.summary.event_latency_samples, 0);
    assert.ok(failed(report).includes('min_event_latency_samples'));
  });

  it('idle window: holds subscribers, diffs request counters and pg_stat_statements', async () => {
    const fake = await startFake();
    const seen = []; let calls = 0;
    const pgExec = async (sql) => { seen.push(sql); calls += 1; return calls <= 2 ? '' : ['q1\u001f50\u001f12.5\u001f50\u001fSELECT 1', 'q2\u001f3\u001f1\u001f3\u001fSELECT 2'].join('\n'); };
    const report = await run(cfgFor(fake, [...EV, '--events-subscribers', '3', '--events-idle-seconds', '1', '--pg-stat-container', 'fake-pg']), { pgExec });
    const idle = report.scenarios['events-latency'].idle;
    assert.equal(idle.subscribers, 3);
    assert.ok(idle.pings_received > 0);
    assert.equal(idle.http_requests_by_route['/v1/sse/jobs/*'].total, 3);
    assert.ok(idle.seconds >= 0.9);
    const pg = report.scenarios['events-latency'].pg_stat_statements;
    assert.equal(pg.total_calls_delta, 53); assert.equal(pg.top[0].queryid, 'q1');
    assert.ok(report.summary.events_idle_pg_calls_per_s > 0);
    assert.ok(seen.every((q) => /FROM pg_stat_statements/.test(q) && !/reset/i.test(q)), 'must only read the stats');
    assert.equal(report.summary.event_latency_samples, 9, 'idle holders must not feed delivery latency');
    assert.match(renderMarkdown(report), /idle window/);
  });

  it('pg_stat_statements is optional: absent flag and failing psql are reported, not fatal', async () => {
    assert.equal(pgDelta(parsePgStatements('a\u001f1\u001f1\u001f1\u001fx'), parsePgStatements('a\u001f4\u001f2\u001f4\u001fx\nb\u001f2\u001f1\u001f2\u001fy')).total_calls_delta, 5);
    const fake = await startFake();
    const none = await run(cfgFor(fake, [...EV, '--events-calibrate-ms', '0']));
    assert.match(none.scenarios['events-latency'].pg_stat_statements.skipped, /not requested/);
    const broken = await run(cfgFor(fake, [...EV, '--pg-stat-container', 'fake-pg', '--events-calibrate-ms', '0']), { pgExec: async () => { throw new Error('relation "pg_stat_statements" does not exist'); } });
    assert.match(broken.scenarios['events-latency'].pg_stat_statements.skipped, /unavailable/);
    assert.equal(broken.thresholds.passed, true);
    assert.throws(() => cfgFor(fake, [...EV, '--pg-stat-container', 'x;rm -rf /']), /invalid name/);
  });
});

// ------------------------------------------------------------ run provenance

describe('run provenance', () => {
  it('pickEnv copies only allowlisted names and never an odd-looking value', () => {
    const got = pickEnv({
      UBAG_WORKER_CONCURRENCY: '1', UBAG_WORKER_DAEMON: 'true', UBAG_ADMISSION_MAX_UPLOAD_MEMORY_BYTES: '268435456', UBAG_GATEWAY_STORE: 'postgres',
      UBAG_GATEWAY_DATABASE_URL: 'postgres://u:pw@h/db', UBAG_LOAD_API_KEY: 'secret', UBAG_VOICE_RELAY_SECRET: 'secret', UBAG_EXECUTOR_MODE: 'postgres://u:pw@h/db',
    });
    assert.deepEqual(got, { UBAG_ADMISSION_MAX_UPLOAD_MEMORY_BYTES: '268435456', UBAG_EXECUTOR_MODE: '[withheld: unexpected value shape]', UBAG_GATEWAY_STORE: 'postgres', UBAG_WORKER_CONCURRENCY: '1', UBAG_WORKER_DAEMON: 'true' });
    assert.deepEqual(parseEnvText('A=1\nB=x=y\r\nnoequals\n'), { A: '1', B: 'x=y' });
  });

  it('gatewayInfoFrom and containerLimits read what the harness already scraped', () => {
    assert.deepEqual(gatewayInfoFrom(parseProm('ubag_gateway_info{version="1.0",api_version="v1",commit="abc"} 1\n')), { version: '1.0', api_version: 'v1', commit: 'abc' });
    assert.equal(gatewayInfoFrom(parseProm('other 1\n')), null);
    assert.equal(gatewayInfoFrom(null), null);
    assert.deepEqual(containerLimits({ containers: { gateway: { container: 'g', cpu_limit_cores: 1, memory_limit_mb: 1300 }, worker: { container: 'w', skipped: 'x' } } }), { gateway: { container: 'g', cpu_cores: 1, memory_mb: 1300 }, worker: { container: 'w', skipped: 'x' } });
  });

  it('report.meta.provenance carries commit, env allowlist, host, container limits, harness sha and manifest hash', async () => {
    const fake = await startFake();
    const env = { ...fake.env, UBAG_WORKER_CONCURRENCY: '3', UBAG_ADMISSION_MAX_INFLIGHT: '7', UBAG_VOICE_RELAY_SECRET: 'must-not-leak' };
    const cfg = parseArgs([`--${ACK_FLAG}`, ...FAST, '--scenario', 'metrics-snapshot', '--workload', 'mixed', '--cgroup-containers', 'gateway=g,worker=w', '--docker-interval-ms', '20'], env);
    const report = await run(cfg, {
      dockerExec: async () => v2Fixture({ current: 300 * MB, cpuMax: '100000 100000' }),
      dockerEnv: async (c) => { if (c === 'w') throw new Error('gone'); return 'UBAG_GATEWAY_STORE=postgres\nUBAG_ADMISSION_X=2\nSECRET_TOKEN=tok-xyz\nUBAG_WORKER_DAEMON=true\n'; },
      harnessGit: () => ({ sha: '0123456789abcdef', dirty: false }),
    }, env);
    const pv = report.meta.provenance;
    assert.deepEqual(pv.gateway, { version: '9.9.9', api_version: 'v1', commit: 'deadbeefcafe' });
    assert.deepEqual(pv.harness, { sha: '0123456789abcdef', dirty: false });
    assert.deepEqual(pv.harness_env, { UBAG_ADMISSION_MAX_INFLIGHT: '7', UBAG_WORKER_CONCURRENCY: '3' });
    assert.deepEqual(pv.stack_env.gateway, { UBAG_ADMISSION_X: '2', UBAG_GATEWAY_STORE: 'postgres', UBAG_WORKER_DAEMON: 'true' });
    assert.match(pv.stack_env.worker.skipped, /env read failed/);
    assert.deepEqual(pv.container_limits.gateway, { container: 'g', cpu_cores: 1, memory_mb: 1000 });
    assert.ok(pv.harness_host.cpu_count >= 1 && pv.harness_host.mem_total_mb > 0);
    assert.equal(pv.workload.name, 'mixed');
    assert.equal(pv.workload.sha256, workloadSha256('mixed'));
    assert.match(pv.workload.sha256, /^[0-9a-f]{64}$/);
    assert.equal(pv.workload.manifest.mix.length, 3);
    const md = renderMarkdown(report);
    const text = JSON.stringify(report) + md;
    assert.ok(!text.includes('must-not-leak') && !text.includes('tok-xyz') && !text.includes('SECRET_TOKEN'));
    assert.match(md, /## Provenance[\s\S]*deadbeefcafe[\s\S]*mixed sha256/);
  });

  it('--workload refuses an unknown manifest before any load is generated; no --workload records null', async () => {
    const fake = await startFake();
    assert.throws(() => cfgFor(fake, ['--scenario', 'metrics-snapshot', '--workload', 'does-not-exist']));
    assert.throws(() => cfgFor(fake, ['--scenario', 'metrics-snapshot', '--workload', '../etc']), /invalid workload name/);
    const report = await run(cfgFor(fake, ['--scenario', 'metrics-snapshot']), { harnessGit: () => ({ sha: null, dirty: null }) });
    assert.equal(report.meta.provenance.workload, null);
    assert.equal(report.meta.provenance.stack_env.skipped, 'no --cgroup-containers');
    assert.equal(report.meta.config.workloadInfo, undefined);
  });
});
