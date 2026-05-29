import { createServer, request as httpRequest } from 'node:http';
import assert from 'node:assert/strict';
import test from 'node:test';

import { assertLoopbackHost, createSidecarServer, isLoopbackHost } from '../dist/index.js';

test('sidecar health is loopback aware', async () => {
  const gateway = await listen(createServer((request, response) => {
    response.setHeader('content-type', 'application/json');
    response.end(JSON.stringify({ service: 'ubag-gateway', status: 'ok', trace_id: 'trace_gateway' }));
  }));
  const sidecar = await listen(createSidecarServer({ gatewayBaseUrl: gateway.url, host: '127.0.0.1' }));

  try {
    const response = await fetch(`${sidecar.url}/health`);
    assert.equal(response.status, 200);
    const body = await response.json();
    assert.equal(body.service, 'ubag-sidecar');
    assert.equal(body.status, 'ok');
    assert.equal(body.loopback_only, true);
    assert.equal(body.gateway_base_url, `${gateway.url}/`);
  } finally {
    await sidecar.close();
    await gateway.close();
  }
});

test('sidecar proxies v1 gateway requests and preserves body', async () => {
  let recorded;
  const gateway = await listen(createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    recorded = {
      method: request.method,
      url: request.url,
      sidecar: request.headers['x-ubag-sidecar'],
      idempotencyKey: request.headers['idempotency-key'],
      body: Buffer.concat(chunks).toString('utf8')
    };
    response.statusCode = 202;
    response.setHeader('content-type', 'application/json');
    response.end(JSON.stringify({ api_version: '2026-05-22', job_id: 'job_sidecar', status: 'queued', trace_id: 'trace_sidecar' }));
  }));
  const sidecar = await listen(createSidecarServer({ gatewayBaseUrl: gateway.url }));

  try {
    const response = await fetch(`${sidecar.url}/v1/jobs`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ hello: 'sidecar' })
    });
    assert.equal(response.status, 202);
    assert.equal((await response.json()).job_id, 'job_sidecar');
    assert.equal(recorded.method, 'POST');
    assert.equal(recorded.url, '/v1/jobs');
    assert.equal(recorded.sidecar, 'loopback');
    assert.match(recorded.idempotencyKey, /^[0-9A-HJKMNP-TV-Z]{26}$/);
    assert.deepEqual(JSON.parse(recorded.body), {
      hello: 'sidecar',
      idempotency_key: recorded.idempotencyKey
    });
  } finally {
    await sidecar.close();
    await gateway.close();
  }
});

test('sidecar injects idempotency for mutating artifact routes', async () => {
  const recorded = [];
  const gateway = await listen(createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    recorded.push({
      method: request.method,
      url: request.url,
      idempotencyKey: request.headers['idempotency-key'],
      body: Buffer.concat(chunks).toString('utf8')
    });
    response.statusCode = request.method === 'DELETE' ? 204 : 202;
    response.end();
  }));
  const sidecar = await listen(createSidecarServer({ gatewayBaseUrl: gateway.url }));

  try {
    const put = await fetch(`${sidecar.url}/v1/jobs/job_123/artifacts/report.txt`, {
      method: 'PUT',
      headers: { 'content-type': 'text/plain' },
      body: 'artifact'
    });
    assert.equal(put.status, 202);

    const del = await fetch(`${sidecar.url}/v1/jobs/job_123/artifacts/report.txt`, {
      method: 'DELETE'
    });
    assert.equal(del.status, 204);

    assert.equal(recorded.length, 2);
    assert.equal(recorded[0].method, 'PUT');
    assert.equal(recorded[1].method, 'DELETE');
    assert.match(recorded[0].idempotencyKey, /^[0-9A-HJKMNP-TV-Z]{26}$/);
    assert.match(recorded[1].idempotencyKey, /^[0-9A-HJKMNP-TV-Z]{26}$/);
    assert.equal(recorded[0].body, 'artifact');
  } finally {
    await sidecar.close();
    await gateway.close();
  }
});

test('sidecar never treats absolute-form URLs as proxy targets', async () => {
  let gatewayHit = false;
  const gateway = await listen(createServer((request, response) => {
    gatewayHit = true;
    assert.equal(request.url, '/v1/health?probe=1');
    response.setHeader('content-type', 'application/json');
    const body = JSON.stringify({ service: 'ubag-gateway', status: 'ok', trace_id: 'trace_gateway' });
    response.setHeader('content-length', String(Buffer.byteLength(body)));
    response.setHeader('connection', 'keep-alive');
    response.end(body);
  }));
  const sidecar = await listen(createSidecarServer({ gatewayBaseUrl: gateway.url }));

  try {
    const response = await fetch(`${sidecar.url}/v1/health?probe=1`, {
      headers: { host: 'attacker.invalid' }
    });
    assert.equal(response.status, 200);
    assert.equal(gatewayHit, true);
    assert.equal((await response.json()).status, 'ok');
  } finally {
    await sidecar.close();
    await gateway.close();
  }
});

test('sidecar rejects non-loopback absolute-form proxy targets', async () => {
  let gatewayHit = false;
  const gateway = await listen(createServer((request, response) => {
    gatewayHit = true;
    response.end('unexpected');
  }));
  const sidecar = await listen(createSidecarServer({ gatewayBaseUrl: gateway.url }));

  try {
    const response = await rawRequest(sidecar.url, 'http://example.invalid/v1/health');
    assert.equal(response.statusCode, 502);
    assert.equal(gatewayHit, false);
    assert.match(response.body, /relative \/v1 routes/);
  } finally {
    await sidecar.close();
    await gateway.close();
  }
});

test('sidecar factory enforces loopback binding by default', () => {
  assert.throws(
    () => createSidecarServer({ gatewayBaseUrl: 'http://127.0.0.1:8080', host: '0.0.0.0' }),
    /loopback/
  );
});

test('sidecar rejects accidental public binding by default', () => {
  assert.equal(isLoopbackHost('127.0.0.1'), true);
  assert.equal(isLoopbackHost('0.0.0.0'), false);
  assert.throws(() => assertLoopbackHost('0.0.0.0'), /loopback/);
  assert.doesNotThrow(() => assertLoopbackHost('0.0.0.0', true));
});

function listen(server) {
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      resolve({
        url: `http://127.0.0.1:${address.port}`,
        close: () => new Promise((closeResolve) => server.close(closeResolve))
      });
    });
  });
}

function rawRequest(baseUrl, path) {
  const url = new URL(baseUrl);
  return new Promise((resolve, reject) => {
    const request = httpRequest({
      host: url.hostname,
      port: url.port,
      method: 'GET',
      path
    }, (response) => {
      const chunks = [];
      response.on('data', (chunk) => chunks.push(chunk));
      response.on('end', () => resolve({
        statusCode: response.statusCode,
        body: Buffer.concat(chunks).toString('utf8')
      }));
    });
    request.on('error', reject);
    request.end();
  });
}
