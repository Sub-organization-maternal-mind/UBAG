// Shared offline test support for tests/load: an in-process fake gateway on 127.0.0.1 and cgroup text fixtures.
// Moved verbatim out of acceptance.test.mjs so ladder.test.mjs can reuse it; options only ever add behaviour.
import { createServer } from 'node:http';
import { createHash } from 'node:crypto';

export const KEY = 'test-key-not-secret';
export const KEY_B = 'test-key-b-not-secret';
export const MB = 1024 * 1024;

// ---------------------------------------------------------- cgroup fixtures
// Text exactly as `docker exec <c> sh -c` prints it (sections introduced by "@@label").
export function v2Fixture({ nrPeriods = 0, nrThrottled = 0, usage = 1_000_000, oomKill = 0, current = 500 * MB, peak = null, memMax = String(1000 * MB), cpuMax = '150000 100000' } = {}) {
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
export function v1Fixture({ nrPeriods = 0, nrThrottled = 0, oomKill = 0, peak = 400 * MB, limit = String(1000 * MB) } = {}) {
  return [
    '@@v1_cpu_stat', `nr_periods ${nrPeriods}`, `nr_throttled ${nrThrottled}`, `throttled_time ${nrThrottled * 5_000_000}`,
    '@@v1_cpu_quota', '200000', '@@v1_cpu_period', '100000', '@@v1_cpuacct', '5000000000',
    '@@v1_mem_usage', String(300 * MB), '@@v1_mem_peak', String(peak), '@@v1_mem_limit', limit,
    '@@v1_oom_control', 'oom_kill_disable 0', 'under_oom 0', `oom_kill ${oomKill}`,
    '@@meminfo', 'MemTotal:       8000000 kB', 'MemAvailable:   4000000 kB', '@@procstat', 'cpu  10 0 10 80 0 0 0 0', '@@nproc', '2', '',
  ].join('\n');
}
const fakes = [];
/** Close every fake started so far (call from an after() hook). */
export const closeFakes = () => Promise.all(fakes.map((f) => new Promise((r) => { f.server.closeAllConnections?.(); f.server.close(r); })));

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

export async function startFake(opts = {}) {
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
