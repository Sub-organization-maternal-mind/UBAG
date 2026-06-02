import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const BASE_URL = __ENV.UBAG_GW_URL || 'http://localhost:8081';
const APP_SECRET = __ENV.UBAG_APP_SECRET || '';
const API_VERSION = '2026-05-22';

const errorRate = new Rate('error_rate');
const jobCreateDuration = new Trend('job_create_duration');
const sseTTFB = new Trend('sse_ttfb');

const HEADERS = {
  'Authorization': `Bearer ${APP_SECRET}`,
  'Ubag-Api-Version': API_VERSION,
  'Content-Type': 'application/json',
};

export const options = {
  scenarios: {
    job_create_load: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '10s', target: 5 },   // ramp up
        { duration: '30s', target: 10 },  // steady
        { duration: '10s', target: 0 },   // ramp down
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<2000'],    // 95th percentile < 2s
    error_rate: ['rate<0.05'],             // error rate < 5%
    job_create_duration: ['p(95)<3000'],  // job create p95 < 3s
  },
};

export default function () {
  // ── Health check
  const health = http.get(`${BASE_URL}/v1/health`, { headers: HEADERS });
  check(health, { 'health 200': r => r.status === 200 });
  errorRate.add(health.status !== 200);

  // ── Job creation
  const payload = JSON.stringify({
    job: {
      target: 'https://example.com',
      command_type: 'fetch',
      input: { url: 'https://example.com' },
    },
    client: { app_id: 'load-test', app_version: '1.0.0', sdk: { name: 'k6', version: '1.0.0' } },
  });

  const idempotencyKey = `k6-${__VU}-${__ITER}`;
  const createRes = http.post(`${BASE_URL}/v1/jobs`, payload, {
    headers: { ...HEADERS, 'Idempotency-Key': idempotencyKey },
  });

  jobCreateDuration.add(createRes.timings.duration);
  const createOk = check(createRes, {
    'job create 200/201/202': r => [200, 201, 202].includes(r.status),
    'job has id': r => { try { return !!JSON.parse(r.body)?.job?.id || !!JSON.parse(r.body)?.id; } catch { return false; } },
  });
  errorRate.add(!createOk);

  // ── SSE stream TTFB (just connect, don't consume)
  const sseStart = Date.now();
  const sseRes = http.get(`${BASE_URL}/v1/jobs?stream=1`, {
    headers: { ...HEADERS, 'Accept': 'text/event-stream' },
    timeout: '5s',
  });
  sseTTFB.add(Date.now() - sseStart);

  sleep(1);
}
