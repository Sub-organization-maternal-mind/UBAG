import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';
import test from 'node:test';

import {
  UBAG_SDK_NAME,
  UBAG_SDK_VERSION,
  UbagApiError,
  createUbagClient
} from '../dist/index.js';

const packageRoot = dirname(fileURLToPath(import.meta.url));
const fixturePath = join(packageRoot, '..', '..', 'conformance', 'fixtures', 'v0', 'scenarios.json');
const fixture = JSON.parse(await readFile(fixturePath, 'utf8'));
const baseUrl = 'https://fixture.ubag.local';

test('shared conformance fixtures', async (t) => {
  assert.equal(fixture.suite, 'ubag.v0.sdk.baseline');

  for (const scenario of fixture.scenarios) {
    await t.test(scenario.id, async () => {
      const transport = new FixtureFetch(scenario);
      const client = clientForScenario(scenario, transport.fetch);
      const expectedThrow = scenario.expect.throws;

      if (expectedThrow) {
        await assert.rejects(
          () => invokeScenario(client, scenario),
          (error) => {
            assert.ok(error instanceof UbagApiError);
            assert.equal(error.name, expectedThrow);
            assertErrorExpectations(error, scenario.expect);
            return true;
          }
        );
      } else {
        const result = await invokeScenario(client, scenario);
        assertBodyExpectations(result, scenario.expect);
      }

      assertRecordedRequest(scenario.request, transport.lastRequest);
    });
  }
});

test('createJob adds TypeScript SDK metadata when missing', async () => {
  const scenario = fixture.scenarios.find((item) => item.id === 'jobs.create.accepted');
  const transport = new FixtureFetch(scenario);
  const client = createUbagClient({
    baseUrl,
    appSecret: 'app_secret_fixture',
    fetch: transport.fetch
  });

  await client.createJob(
    {
      client: { app_id: 'fixture-app', app_version: '0.0.0' },
      job: {
        target: 'mock_target',
        command_type: 'echo',
        input: { prompt: 'Hello UBAG' }
      }
    },
    { idempotencyKey: 'idem_ts_sdk' }
  );

  const body = JSON.parse(transport.lastRequest.body);
  assert.equal(body.client.sdk.name, UBAG_SDK_NAME);
  assert.equal(body.client.sdk.version, UBAG_SDK_VERSION);
  assert.equal(body.idempotency_key, 'idem_ts_sdk');
  assert.equal(transport.lastRequest.headers['idempotency-key'], 'idem_ts_sdk');
});

class FixtureFetch {
  constructor(scenario) {
    this.scenario = scenario;
    this.lastRequest = undefined;
    this.fetch = this.fetch.bind(this);
  }

  async fetch(input, init = {}) {
    const url = new URL(String(input));
    const headers = new Headers(init.headers);
    this.lastRequest = {
      method: init.method ?? 'GET',
      path: `${url.pathname}${url.search}`,
      headers: Object.fromEntries([...headers.entries()].map(([key, value]) => [key.toLowerCase(), value])),
      body: await requestBodyText(init.body)
    };

    const response = this.scenario.response;
    return new Response(
      response.body_text ?? (response.body === undefined ? undefined : JSON.stringify(response.body)),
      {
        status: response.status,
        statusText: 'fixture',
        headers: {
          'content-type': 'application/json',
          ...(response.headers ?? {})
        }
      }
    );
  }
}

function clientForScenario(scenario, fetch) {
  const authorization = scenario.request.headers?.Authorization;
  const appSecret = typeof authorization === 'string' && authorization.startsWith('Bearer ')
    ? authorization.slice('Bearer '.length)
    : undefined;
  return createUbagClient({ baseUrl, appSecret, fetch });
}

async function invokeScenario(client, scenario) {
  const request = scenario.request;
  const parsed = new URL(request.path, baseUrl);
  const route = parsed.pathname;
  const options = requestOptionsFromHeaders(request.headers ?? {});

  if (request.method === 'GET' && route === '/v1/health') return client.health(options);
  if (request.method === 'GET' && route === '/v1/ready') return client.ready(options);
  if (request.method === 'GET' && route === '/v1/version') return client.version();
  if (request.method === 'GET' && route === '/v1/workflows') return client.listWorkflows(options);
  if (request.method === 'GET' && route === '/v1/templates') return client.listTemplates(options);
  if (request.method === 'GET' && route === '/v1/targets') return client.listTargets(listParamsFromSearch(parsed), options);
  if (request.method === 'GET' && route === '/v1/adapters') return client.listAdapters(listParamsFromSearch(parsed), options);
  if (request.method === 'GET' && route === '/v1/apps') return client.listApps(listParamsFromSearch(parsed), options);
  if (request.method === 'GET' && route === '/v1/devices') return client.listDevices(listParamsFromSearch(parsed), options);
  if (request.method === 'GET' && route === '/v1/audit') return client.listAuditEvents(listParamsFromSearch(parsed), options);
  if (request.method === 'GET' && route === '/v1/webhooks') return client.listWebhooks(listParamsFromSearch(parsed), options);
  if (request.method === 'GET' && route === '/v1/events') return client.listEvents(listParamsFromSearch(parsed), options);
  if (request.method === 'GET' && route === '/v1/cache') return client.cacheStatus(options);
  if (request.method === 'GET' && route === '/v1/metrics') {
    return { body: await client.getMetrics(options) };
  }
  if (request.method === 'GET' && route === '/v1/jobs') {
    return client.listJobs({
      cursor: parsed.searchParams.get('cursor') ?? undefined,
      limit: parsed.searchParams.get('limit') === null ? undefined : Number(parsed.searchParams.get('limit')),
      status: parsed.searchParams.get('filter[status]') ?? undefined,
      target: parsed.searchParams.get('filter[target]') ?? undefined,
      sort: parsed.searchParams.get('sort') ?? undefined
    }, options);
  }
  if (request.method === 'GET' && route.endsWith('/events') && route.startsWith('/v1/jobs/')) {
    const jobId = route.slice('/v1/jobs/'.length, -'/events'.length);
    return client.listJobEvents(jobId, {
      cursor: parsed.searchParams.get('cursor') ?? undefined,
      after_sequence: parsed.searchParams.get('after_sequence') === null ? undefined : Number(parsed.searchParams.get('after_sequence')),
      limit: parsed.searchParams.get('limit') === null ? undefined : Number(parsed.searchParams.get('limit'))
    }, options);
  }
  if (request.method === 'GET' && route.endsWith('/artifacts') && route.startsWith('/v1/jobs/')) {
    const jobId = route.slice('/v1/jobs/'.length, -'/artifacts'.length);
    return client.listJobArtifacts(jobId, options);
  }
  if (request.method === 'GET' && route.startsWith('/v1/sse/jobs/')) {
    const response = await client.streamJobEventsSse(route.slice('/v1/sse/jobs/'.length), options);
    return { body: await response.text() };
  }
  if (request.method === 'GET' && route.startsWith('/v1/jobs/') && route.includes('/artifacts/')) {
    const { jobId, key } = parseArtifactRoute(route);
    const artifact = await client.getJobArtifact(jobId, key, options);
    return {
      content_type: artifact.content_type,
      checksum: artifact.checksum,
      body: Buffer.from(artifact.body).toString('utf8')
    };
  }
  if (request.method === 'PUT' && route.startsWith('/v1/jobs/') && route.includes('/artifacts/')) {
    const { jobId, key } = parseArtifactRoute(route);
    return client.putJobArtifact(jobId, key, new Blob([request.body_text ?? '']), {
      ...options,
      contentType: request.headers?.['Content-Type'] ?? 'application/octet-stream'
    });
  }
  if (request.method === 'DELETE' && route.startsWith('/v1/jobs/') && route.includes('/artifacts/')) {
    const { jobId, key } = parseArtifactRoute(route);
    return client.deleteJobArtifact(jobId, key, options);
  }
  if (request.method === 'GET' && route.startsWith('/v1/jobs/')) {
    return client.getJob(route.slice('/v1/jobs/'.length), options);
  }
  if (request.method === 'POST' && route === '/v1/jobs') {
    return client.createJob(resolveSdkPlaceholders(request.body), options);
  }
  if (request.method === 'POST' && route.endsWith('/cancel')) {
    const jobId = route.slice('/v1/jobs/'.length, -'/cancel'.length);
    return client.cancelJob(jobId, resolveSdkPlaceholders(request.body), options);
  }
  if (request.method === 'POST' && route.endsWith('/retry')) {
    const jobId = route.slice('/v1/jobs/'.length, -'/retry'.length);
    return client.retryJob(jobId, resolveSdkPlaceholders(request.body), options);
  }
  if (request.method === 'POST' && route === '/v1/webhooks/replay') {
    return client.replayWebhookDelivery(resolveSdkPlaceholders(request.body ?? {}), options);
  }

  throw new Error(`No SDK mapping for ${request.method} ${request.path}`);
}

function parseArtifactRoute(route) {
  const marker = '/artifacts/';
  const body = route.slice('/v1/jobs/'.length);
  const markerIndex = body.indexOf(marker);
  return {
    jobId: decodeURIComponent(body.slice(0, markerIndex)),
    key: decodeURIComponent(body.slice(markerIndex + marker.length))
  };
}

function listParamsFromSearch(parsed) {
  return {
    cursor: parsed.searchParams.get('cursor') ?? undefined,
    limit: parsed.searchParams.get('limit') === null ? undefined : Number(parsed.searchParams.get('limit'))
  };
}

function requestOptionsFromHeaders(headers) {
  const options = {};
  if (headers['Ubag-Api-Version']) options.apiVersion = headers['Ubag-Api-Version'];
  if (headers['Idempotency-Key']) options.idempotencyKey = headers['Idempotency-Key'];
  return options;
}

function assertRecordedRequest(expected, recorded) {
  assert.ok(recorded);
  assert.equal(recorded.method, expected.method);
  assert.equal(recorded.path, expected.path);
  for (const [key, value] of Object.entries(expected.headers ?? {})) {
    assert.equal(recorded.headers[key.toLowerCase()], value, key);
  }
  if ('body' in expected) {
    assert.deepEqual(JSON.parse(recorded.body), resolveSdkPlaceholders(expected.body));
  }
  if ('body_text' in expected) {
    assert.equal(recorded.body, expected.body_text);
  }
}

function assertBodyExpectations(body, expect) {
  if ('ok' in expect) assert.equal(expect.ok, true);
  for (const [key, value] of Object.entries(expect)) {
    if (!key.startsWith('body.')) continue;
    assert.deepEqual(valueAtPath(body, key.slice('body.'.length)), value, key);
  }
}

function assertErrorExpectations(error, expect) {
  if ('error.code' in expect) assert.equal(error.code, expect['error.code']);
  if ('error.retryable' in expect) assert.equal(error.retryable, expect['error.retryable']);
  if ('error.retry_after_ms' in expect) assert.equal(error.retryAfterMs, expect['error.retry_after_ms']);
}

function valueAtPath(value, path) {
  let current = value;
  for (const part of path.split('.')) {
    current = part === 'length' ? current.length : current[part];
  }
  return current;
}

function resolveSdkPlaceholders(value) {
  if (Array.isArray(value)) return value.map(resolveSdkPlaceholders);
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, resolveSdkPlaceholders(item)]));
  }
  if (value === '__SDK_NAME__') return UBAG_SDK_NAME;
  if (value === '__SDK_VERSION__') return UBAG_SDK_VERSION;
  return value;
}

async function requestBodyText(body) {
  if (body === undefined) return undefined;
  if (typeof body === 'string') return body;
  if (body instanceof Uint8Array) return Buffer.from(body).toString('utf8');
  if (typeof body.text === 'function') return body.text();
  if (typeof body.arrayBuffer === 'function') return Buffer.from(await body.arrayBuffer()).toString('utf8');
  return String(body);
}
