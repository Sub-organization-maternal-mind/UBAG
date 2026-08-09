import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  API_VERSION,
  buildConfig,
  calculateStats,
  runBenchmark,
} from '../benchmark.mjs';

const createdAt = '2026-08-09T12:00:00.000Z';
const cliPath = fileURLToPath(new URL('../run.mjs', import.meta.url));

function jobResponse(jobId, status, overrides = {}) {
  return {
    api_version: API_VERSION,
    job_id: jobId,
    idempotent_replay: false,
    status,
    target: 'mock',
    result: null,
    metadata: {},
    trace_id: 'trace_benchmark',
    events_url: `/v1/jobs/${jobId}/events`,
    created_at: createdAt,
    updated_at: createdAt,
    ...overrides,
  };
}

async function readJson(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

async function withServer(handler, callback) {
  const server = createServer(handler);
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  try {
    await callback(`http://127.0.0.1:${server.address().port}`);
  } finally {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  }
}

function jobEvent(jobId, sequence, type, created_at) {
  return {
    event_id: `evt_${sequence}`,
    job_id: jobId,
    api_version: API_VERSION,
    type,
    created_at,
    sequence,
    data: {},
    trace_id: 'trace_benchmark',
  };
}

function config(baseUrl, ...args) {
  return buildConfig([
    '--base-url', baseUrl,
    '--warmups', '0',
    '--samples', '1',
    '--poll-interval-ms', '5',
    '--timeout-ms', '500',
    ...args,
  ], {});
}

function runCli(args, timeout = 10_000) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cliPath, ...args], {
      windowsHide: true,
    });
    let stdout = '';
    let stderr = '';
    const timer = setTimeout(() => {
      child.kill();
      reject(new Error(`benchmark CLI timed out: ${args.join(' ')}`));
    }, timeout);
    child.stdout.on('data', (chunk) => {
      stdout += chunk;
    });
    child.stderr.on('data', (chunk) => {
      stderr += chunk;
    });
    child.once('error', (error) => {
      clearTimeout(timer);
      reject(error);
    });
    child.once('close', (code) => {
      clearTimeout(timer);
      if (code !== 0) {
        reject(new Error(`benchmark CLI exited ${code}\n${stderr}\n${stdout}`));
        return;
      }
      resolve({ stdout, stderr });
    });
  });
}

test('acceptance sends the canonical deterministic mock request and measures only POST acceptance', async () => {
  const requests = [];

  await withServer(async (request, response) => {
    const body = await readJson(request);
    requests.push({ method: request.method, url: request.url, headers: request.headers, body });
    response.writeHead(202, {
      'content-type': 'application/json',
      location: `/v1/jobs/job_accept${requests.length}`,
    });
    response.end(JSON.stringify(jobResponse(`job_accept${requests.length}`, 'queued')));
  }, async (baseUrl) => {
    const result = await runBenchmark(config(
      baseUrl,
      '--scenario', 'acceptance',
      '--warmups', '1',
      '--samples', '2',
      '--app-secret', 'benchmark-test-secret',
    ));

    assert.equal(requests.length, 3);
    const keys = new Set();
    for (const request of requests) {
      assert.equal(request.method, 'POST');
      assert.equal(request.url, '/v1/jobs');
      assert.equal(request.headers['content-type'], 'application/json');
      assert.equal(request.headers['ubag-api-version'], API_VERSION);
      assert.equal(request.headers.authorization, 'Bearer benchmark-test-secret');
      assert.equal(request.headers['idempotency-key'], request.body.idempotency_key);
      assert.match(request.body.idempotency_key, /^[A-Za-z0-9._:-]{16,128}$/);
      keys.add(request.body.idempotency_key);
      assert.deepEqual(request.body, {
        api_version: API_VERSION,
        idempotency_key: request.body.idempotency_key,
        client: {
          app_id: 'ubag-benchmark',
          app_version: '1.0.0',
          sdk: { name: 'ubag-benchmark', version: '1.0.0' },
        },
        job: {
          target: 'mock',
          command_type: 'chat.prompt',
          input: { prompt: 'Return the exact text: UBAG_BENCHMARK_OK' },
        },
      });
    }
    assert.equal(keys.size, 3);
    assert.equal(result.metadata.auth_configured, true);
    assert.equal(result.metadata.base_url, baseUrl);
    assert.equal(result.samples.acceptance_ms.length, 2);
    assert.deepEqual(Object.keys(result.samples), ['acceptance_ms']);
    assert.equal(result.stats.acceptance_ms.count, 2);
    assert.ok(!JSON.stringify(result).includes('benchmark-test-secret'));
    assert.ok(!JSON.stringify(result).includes('job_accept'));
  });
});

test('CLI --output writes the sanitized benchmark JSON file without leaking secrets or job data', async () => {
  const tempDir = await mkdtemp(join(tmpdir(), 'ubag-benchmark-cli-'));
  try {
    const outputPath = join(tempDir, 'artifacts', 'result.json');
    await withServer(async (request, response) => {
      const body = await readJson(request);
      assert.equal(request.method, 'POST');
      assert.equal(request.url, '/v1/jobs');
      assert.equal(typeof request.headers.authorization, 'string');
      assert.equal(body.job.input.prompt, 'Return the exact text: UBAG_BENCHMARK_OK');
      response.writeHead(202, {
        'content-type': 'application/json',
        location: '/v1/jobs/job_output1',
      });
      response.end(JSON.stringify(jobResponse('job_output1', 'queued')));
    }, async (baseUrl) => {
      const { stdout, stderr } = await runCli([
        '--base-url', baseUrl,
        '--scenario', 'acceptance',
        '--warmups', '0',
        '--samples', '1',
        '--timeout-ms', '500',
        '--app-secret', 'benchmark-test-secret',
        '--json',
        '--output', outputPath,
      ]);

      assert.equal(stderr, '');
      const fileText = await readFile(outputPath, 'utf8');
      assert.match(fileText, /\n$/);

      const stdoutResult = JSON.parse(stdout);
      const fileResult = JSON.parse(fileText);
      assert.deepEqual(fileResult, stdoutResult);
      assert.equal(fileResult.schema_version, 'ubag.benchmark.v1');
      assert.equal(fileResult.metadata.base_url, baseUrl);
      assert.equal(fileResult.metadata.scenario, 'acceptance');
      assert.equal(fileResult.metadata.auth_configured, true);
      assert.equal(fileResult.metadata.samples, 1);
      assert.equal(fileResult.metadata.warmups, 0);
      assert.deepEqual(Object.keys(fileResult.samples), ['acceptance_ms']);
      assert.equal(fileResult.samples.acceptance_ms.length, 1);
      assert.equal(fileResult.stats.acceptance_ms.count, 1);
      assert.equal(fileResult.stats.acceptance_ms.sample_variance, 0);

      const serialized = JSON.stringify(fileResult);
      assert.ok(!serialized.includes('benchmark-test-secret'));
      assert.ok(!serialized.includes('job_output1'));
      assert.ok(!serialized.includes('UBAG_BENCHMARK_OK'));
    });
  } finally {
    await rm(tempDir, { recursive: true, force: true });
  }
});

test('CLI help prefers environment auth over --app-secret because process args may be visible', async () => {
  const { stdout, stderr } = await runCli(['--help']);
  assert.equal(stderr, '');
  assert.match(stdout, /UBAG_APP_SECRET/);
  assert.match(stdout, /prefer/i);
  assert.match(stdout, /process listings/i);
});

test('mock-e2e follows Location, polls completion, and derives timings from canonical events', async () => {
  const paths = [];

  await withServer(async (request, response) => {
    paths.push(`${request.method} ${request.url}`);
    response.setHeader('content-type', 'application/json');
    if (request.method === 'POST' && request.url === '/v1/jobs') {
      await readJson(request);
      response.writeHead(202, {
        'content-type': 'application/json',
        location: '/v1/jobs/job_e2e1',
      });
      response.end(JSON.stringify(jobResponse('job_e2e1', 'queued')));
      return;
    }
    if (request.method === 'GET' && request.url === '/v1/jobs/job_e2e1') {
      response.end(JSON.stringify(jobResponse('job_e2e1', 'completed')));
      return;
    }
    if (request.method === 'GET' && request.url === '/v1/jobs/job_e2e1/events') {
      response.end(JSON.stringify({
        api_version: API_VERSION,
        job_id: 'job_e2e1',
        events: [
          jobEvent('job_e2e1', 1, 'queued', '2026-08-09T12:00:00.000Z'),
          jobEvent('job_e2e1', 2, 'assigned', '2026-08-09T12:00:00.010Z'),
          jobEvent('job_e2e1', 3, 'running', '2026-08-09T12:00:00.020Z'),
          jobEvent('job_e2e1', 4, 'completed', '2026-08-09T12:00:00.030Z'),
        ],
        next_cursor: null,
        trace_id: 'trace_benchmark',
      }));
      return;
    }
    response.writeHead(404);
    response.end('{}');
  }, async (baseUrl) => {
    const result = await runBenchmark(config(baseUrl, '--scenario', 'mock-e2e'));

    assert.deepEqual(paths, [
      'POST /v1/jobs',
      'GET /v1/jobs/job_e2e1',
      'GET /v1/jobs/job_e2e1/events',
    ]);
    assert.equal(result.samples.acceptance_ms.length, 1);
    assert.equal(result.samples.total_ms.length, 1);
    assert.deepEqual(result.samples.queue_ms, [10]);
    assert.deepEqual(result.samples.worker_ms, [20]);
  });
});

test('mock-e2e derives timings from earliest matching numeric event sequence rather than array order', async () => {
  await withServer(async (request, response) => {
    response.setHeader('content-type', 'application/json');
    if (request.method === 'POST') {
      await readJson(request);
      response.writeHead(202, { location: '/v1/jobs/job_sequence1' });
      response.end(JSON.stringify(jobResponse('job_sequence1', 'queued')));
      return;
    }
    if (request.url.endsWith('/events')) {
      response.end(JSON.stringify({
        api_version: API_VERSION,
        job_id: 'job_sequence1',
        events: [
          jobEvent('job_sequence1', 5, 'queued', '2026-08-09T12:00:00.050Z'),
          jobEvent('job_sequence1', 7, 'completed', '2026-08-09T12:00:00.070Z'),
          jobEvent('job_sequence1', 'badsequence', 'assigned', '2026-08-09T12:00:00.015Z'),
          jobEvent('job_sequence1', 3, 'running', '2026-08-09T12:00:00.030Z'),
          jobEvent('job_sequence1', 2, 'assigned', '2026-08-09T12:00:00.020Z'),
          jobEvent('job_sequence1', 1, 'queued', '2026-08-09T12:00:00.010Z'),
        ],
        next_cursor: null,
        trace_id: 'trace_benchmark',
      }));
      return;
    }
    response.end(JSON.stringify(jobResponse('job_sequence1', 'completed')));
  }, async (baseUrl) => {
    const result = await runBenchmark(config(baseUrl, '--scenario', 'mock-e2e'));
    assert.deepEqual(result.samples.queue_ms, [10]);
    assert.deepEqual(result.samples.worker_ms, [50]);
  });
});

test('mock-e2e falls back to timestamp order when matching events have no valid numeric sequence', async () => {
  await withServer(async (request, response) => {
    response.setHeader('content-type', 'application/json');
    if (request.method === 'POST') {
      await readJson(request);
      response.writeHead(202, { location: '/v1/jobs/job_sequence2' });
      response.end(JSON.stringify(jobResponse('job_sequence2', 'queued')));
      return;
    }
    if (request.url.endsWith('/events')) {
      response.end(JSON.stringify({
        api_version: API_VERSION,
        job_id: 'job_sequence2',
        events: [
          jobEvent('job_sequence2', 'laterqueued', 'queued', '2026-08-09T12:00:00.050Z'),
          jobEvent('job_sequence2', 'completed', 'completed', '2026-08-09T12:00:00.070Z'),
          jobEvent('job_sequence2', 'assigned', 'assigned', '2026-08-09T12:00:00.020Z'),
          jobEvent('job_sequence2', 'started', 'running', '2026-08-09T12:00:00.020Z'),
          jobEvent('job_sequence2', 'firstqueued', 'queued', '2026-08-09T12:00:00.010Z'),
        ],
        next_cursor: null,
        trace_id: 'trace_benchmark',
      }));
      return;
    }
    response.end(JSON.stringify(jobResponse('job_sequence2', 'completed')));
  }, async (baseUrl) => {
    const result = await runBenchmark(config(baseUrl, '--scenario', 'mock-e2e'));
    assert.deepEqual(result.samples.queue_ms, [10]);
    assert.deepEqual(result.samples.worker_ms, [50]);
  });
});

test('mock-e2e does not invent queue or worker timings without supporting timestamps', async () => {
  await withServer(async (request, response) => {
    response.setHeader('content-type', 'application/json');
    if (request.method === 'POST') {
      await readJson(request);
      response.writeHead(202, { location: '/v1/jobs/job_notiming1' });
      response.end(JSON.stringify(jobResponse('job_notiming1', 'queued')));
      return;
    }
    if (request.url.endsWith('/events')) {
      response.end(JSON.stringify({
        api_version: API_VERSION,
        job_id: 'job_notiming1',
        events: [jobEvent('job_notiming1', 1, 'completed', '2026-08-09T12:00:00.030Z')],
        next_cursor: null,
        trace_id: 'trace_benchmark',
      }));
      return;
    }
    response.end(JSON.stringify(jobResponse('job_notiming1', 'completed')));
  }, async (baseUrl) => {
    const result = await runBenchmark(config(baseUrl, '--scenario', 'mock-e2e'));
    assert.deepEqual(Object.keys(result.samples), ['acceptance_ms', 'total_ms']);
  });
});

test('calculateStats uses interpolated percentiles and sample variance', () => {
  assert.deepEqual(calculateStats([1, 2, 3, 4]), {
    count: 4,
    min: 1,
    max: 4,
    mean: 2.5,
    p50: 2.5,
    p95: 3.8499999999999996,
    p99: 3.9699999999999998,
    sample_variance: 1.6666666666666667,
  });
  assert.equal(calculateStats([7]).sample_variance, 0);
});

test('rejects HTTP failures without exposing response bodies', async () => {
  await withServer((_request, response) => {
    response.writeHead(503, { 'content-type': 'application/json' });
    response.end('{"secret":"must-not-appear"}');
  }, async (baseUrl) => {
    await assert.rejects(
      runBenchmark(config(baseUrl)),
      (error) => /HTTP 503/.test(error.message) && !error.message.includes('must-not-appear'),
    );
  });
});

test('rejects redirects before forwarding benchmark authorization', async () => {
  let redirectedRequests = 0;
  await withServer((_request, redirectResponse) => {
    redirectedRequests += 1;
    redirectResponse.writeHead(200, { 'content-type': 'application/json' });
    redirectResponse.end(JSON.stringify(jobResponse('job_redirected', 'queued')));
  }, async (redirectBaseUrl) => {
    await withServer((_request, response) => {
      response.writeHead(307, { location: `${redirectBaseUrl}/v1/jobs` });
      response.end();
    }, async (baseUrl) => {
      await assert.rejects(
        runBenchmark(config(baseUrl, '--app-secret', 'must-not-be-forwarded')),
        /redirect/i,
      );
    });
  });
  assert.equal(redirectedRequests, 0);
});

test('rejects malformed acceptance responses', async () => {
  await withServer((_request, response) => {
    response.writeHead(202, { 'content-type': 'application/json' });
    response.end('{"status":"queued"}');
  }, async (baseUrl) => {
    await assert.rejects(runBenchmark(config(baseUrl)), /malformed job response/i);
  });
});

test('times out when a job remains nonterminal', async () => {
  await withServer(async (request, response) => {
    response.setHeader('content-type', 'application/json');
    if (request.method === 'POST') {
      await readJson(request);
      response.writeHead(202, { location: '/v1/jobs/job_wait1' });
      response.end(JSON.stringify(jobResponse('job_wait1', 'queued')));
      return;
    }
    response.end(JSON.stringify(jobResponse('job_wait1', 'running')));
  }, async (baseUrl) => {
    await assert.rejects(
      runBenchmark(config(baseUrl, '--scenario', 'mock-e2e', '--timeout-ms', '40')),
      /timed out/i,
    );
  });
});

test('rejects failed terminal jobs', async () => {
  await withServer(async (request, response) => {
    response.setHeader('content-type', 'application/json');
    if (request.method === 'POST') {
      await readJson(request);
      response.writeHead(202, { location: '/v1/jobs/job_fail1' });
      response.end(JSON.stringify(jobResponse('job_fail1', 'queued')));
      return;
    }
    response.end(JSON.stringify(jobResponse('job_fail1', 'failed_terminal')));
  }, async (baseUrl) => {
    await assert.rejects(
      runBenchmark(config(baseUrl, '--scenario', 'mock-e2e')),
      /failed_terminal/,
    );
  });
});

test('rejects non-loopback base URLs unless remote access is explicit', () => {
  assert.throws(
    () => buildConfig(['--base-url', 'https://bench.example.test'], {}),
    /non-loopback.*--allow-remote/i,
  );
  assert.equal(
    buildConfig(['--base-url', 'https://bench.example.test', '--allow-remote'], {}).baseUrl,
    'https://bench.example.test',
  );
  assert.equal(
    buildConfig(['--base-url', 'http://127.0.0.1:8080'], {}).baseUrl,
    'http://127.0.0.1:8080',
  );
});

test('rejects remote HTTP base URLs even when --allow-remote and auth are supplied', () => {
  assert.throws(
    () => buildConfig([
      '--base-url', 'http://bench.example.test',
      '--allow-remote',
      '--app-secret', 'benchmark-test-secret',
    ], {}),
    /https/i,
  );
});

test('buildConfig rejects an empty --output path', () => {
  assert.throws(
    () => buildConfig(['--output', ''], {}),
    /non-empty path/i,
  );
});
