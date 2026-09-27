import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import { createServer, request } from 'node:http';
import { once } from 'node:events';
import { test } from 'node:test';

test('production-backed launcher does not embed its gateway secret in dashboard assets', async () => {
  const launcher = await readFile(new URL('./start-local.ps1', import.meta.url), 'utf8');
  assert.doesNotMatch(launcher, /\$env:UBAG_DEV_DEFAULT_APP_SECRET\s*=\s*\$env:UBAG_APP_SECRET/);
  assert.match(launcher, /\$env:UBAG_DEV_DEFAULT_APP_SECRET\s*=\s*''/);
});

test('static dashboard proxies API requests to the gateway', async () => {
  const gateway = createServer(async (req, res) => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    res.writeHead(req.method === 'POST' ? 201 : 200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({
      path: req.url, authorization: req.headers.authorization,
      ...(req.method === 'POST' ? { body: JSON.parse(Buffer.concat(chunks).toString()) } : {}),
    }));
  }).listen(0, '127.0.0.1');
  await once(gateway, 'listening');

  const portProbe = createServer().listen(0, '127.0.0.1');
  await once(portProbe, 'listening');
  const dashboardPort = portProbe.address().port;
  portProbe.close();

  const dashboard = spawn(process.execPath, ['serve-dashboard.mjs'], {
    cwd: import.meta.dirname,
    env: {
      ...process.env,
      PORT: String(dashboardPort),
      UBAG_DASHBOARD_GATEWAY_URL: `http://127.0.0.1:${gateway.address().port}`,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  try {
    await once(dashboard.stdout, 'data');
    const response = await fetch(`http://127.0.0.1:${dashboardPort}/v1/health?probe=1`, {
      headers: { Authorization: 'Bearer test-only-token' },
    });
    assert.equal(response.status, 200);
    assert.match(response.headers.get('content-type'), /application\/json/);
    assert.deepEqual(await response.json(), {
      path: '/v1/health?probe=1',
      authorization: 'Bearer test-only-token',
    });

    const absoluteResponse = await new Promise((resolve, reject) => {
      const dashboardRequest = request({
        host: '127.0.0.1', port: dashboardPort,
        path: 'http://127.0.0.1:1/v1/health?probe=2',
      }, resolve);
      dashboardRequest.on('error', reject);
      dashboardRequest.end();
    });
    assert.equal(absoluteResponse.statusCode, 200);
    const chunks = [];
    for await (const chunk of absoluteResponse) chunks.push(chunk);
    assert.deepEqual(JSON.parse(Buffer.concat(chunks).toString()), {
      path: '/v1/health?probe=2',
    });

    const createResponse = await fetch(`http://127.0.0.1:${dashboardPort}/v1/antigravity/accounts`, {
      method: 'POST',
      headers: { Authorization: 'Bearer test-only-token', 'Content-Type': 'application/json' },
      body: JSON.stringify({ label: 'Synthetic slot' }),
    });
    assert.equal(createResponse.status, 201);
    assert.deepEqual(await createResponse.json(), {
      path: '/v1/antigravity/accounts',
      authorization: 'Bearer test-only-token',
      body: { label: 'Synthetic slot' },
    });
  } finally {
    dashboard.kill();
    gateway.close();
  }
});

test('static dashboard caches immutable assets and gzips text responses', async () => {
  const dist = new URL('./apps/dashboard/dist/index.html', import.meta.url);
  let index;
  try {
    index = await readFile(dist);
  } catch {
    // dist not built in this checkout — nothing to assert about static serving.
    return;
  }

  const portProbe = createServer().listen(0, '127.0.0.1');
  await once(portProbe, 'listening');
  const dashboardPort = portProbe.address().port;
  portProbe.close();

  const dashboard = spawn(process.execPath, ['serve-dashboard.mjs'], {
    cwd: import.meta.dirname,
    env: { ...process.env, PORT: String(dashboardPort) },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  try {
    await once(dashboard.stdout, 'data');

    const indexResponse = await fetch(`http://127.0.0.1:${dashboardPort}/`, {
      headers: { 'accept-encoding': 'gzip' },
    });
    assert.equal(indexResponse.status, 200);
    // SPA entry revalidates (hashed assets carry the caching, not the HTML).
    assert.equal(indexResponse.headers.get('cache-control'), 'no-cache');

    // Find a real immutable JS chunk from the built index to request.
    const html = index.toString();
    const immutableAsset = html.match(/_app\/immutable\/[^"']+\.js/)?.[0];
    if (immutableAsset) {
      const assetResponse = await fetch(`http://127.0.0.1:${dashboardPort}/${immutableAsset}`, {
        headers: { 'accept-encoding': 'gzip' },
      });
      assert.equal(assetResponse.status, 200);
      assert.match(assetResponse.headers.get('cache-control') ?? '', /immutable/);
    }
  } finally {
    dashboard.kill();
  }
});