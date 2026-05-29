import { createServer } from 'node:http';
import { spawn } from 'node:child_process';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import assert from 'node:assert/strict';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const cliPath = fileURLToPath(new URL('../dist/index.js', import.meta.url));

test('CLI exercises gateway job commands against a fixture server', async () => {
  const requests = [];
  const server = createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const body = Buffer.concat(chunks).toString('utf8');
    requests.push({
      method: request.method,
      url: request.url,
      headers: request.headers,
      body
    });

    response.setHeader('content-type', 'application/json');
    response.setHeader('connection', 'close');
    if (request.url === '/v1/health') {
      response.end(JSON.stringify({ service: 'ubag-gateway', status: 'ok', trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/ready') {
      response.end(JSON.stringify({ ready: true, service: 'ubag-gateway', trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/version') {
      response.end(JSON.stringify({ service: 'ubag-gateway', version: '0.0.0', default_api_version: '2026-05-22', api_versions: ['2026-05-22'], trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/jobs' && request.method === 'GET') {
      response.end(JSON.stringify({ api_version: '2026-05-22', jobs: [], next_cursor: null, trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/events?limit=1' && request.method === 'GET') {
      response.end(JSON.stringify({ api_version: '2026-05-22', kind: 'events', data: [], next_cursor: null, trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/targets?limit=1' && request.method === 'GET') {
      response.end(JSON.stringify({ api_version: '2026-05-22', kind: 'targets', data: [], next_cursor: null, trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/adapters?limit=1' && request.method === 'GET') {
      response.end(JSON.stringify({ api_version: '2026-05-22', kind: 'adapters', data: [], next_cursor: null, trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/apps?limit=1' && request.method === 'GET') {
      response.end(JSON.stringify({ api_version: '2026-05-22', kind: 'apps', data: [], next_cursor: null, trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/devices?limit=1' && request.method === 'GET') {
      response.end(JSON.stringify({ api_version: '2026-05-22', kind: 'devices', data: [], next_cursor: null, trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/audit?limit=1' && request.method === 'GET') {
      response.end(JSON.stringify({ api_version: '2026-05-22', kind: 'audit', data: [], next_cursor: null, trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/webhooks?limit=1' && request.method === 'GET') {
      response.end(JSON.stringify({ api_version: '2026-05-22', kind: 'webhooks', data: [], next_cursor: null, trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/cache' && request.method === 'GET') {
      response.end(JSON.stringify({ api_version: '2026-05-22', profile: 'standard', enabled: false, entries: [], trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/metrics' && request.method === 'GET') {
      response.setHeader('content-type', 'text/plain; version=0.0.4');
      response.end('ubag_gateway_requests_total 1\n');
      return;
    }
    if (request.url === '/v1/jobs/job_cli/events?limit=1' && request.method === 'GET') {
      response.end(JSON.stringify({ api_version: '2026-05-22', job_id: 'job_cli', events: [], next_cursor: null, trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/jobs/job_cli/artifacts' && request.method === 'GET') {
      response.end(JSON.stringify({ api_version: '2026-05-22', job_id: 'job_cli', kind: 'artifacts', data: [], trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/jobs/job_cli/artifacts/report.txt' && request.method === 'GET') {
      response.setHeader('content-type', 'text/plain');
      response.setHeader('ubag-artifact-checksum', 'sha256_cli');
      response.end('artifact body');
      return;
    }
    if (request.url === '/v1/jobs/job_cli/artifacts/report.txt' && request.method === 'PUT') {
      response.statusCode = 201;
      response.end(JSON.stringify({ api_version: '2026-05-22', artifact: { job_id: 'job_cli', key: 'report.txt', content_type: request.headers['content-type'], size_bytes: Buffer.byteLength(body), checksum: 'sha256_cli', created_at: '2026-05-22T00:00:00Z' }, idempotent_replay: false, trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/jobs/job_cli/artifacts/report.txt' && request.method === 'DELETE') {
      response.statusCode = 204;
      response.end();
      return;
    }
    if (request.url === '/v1/webhooks/replay' && request.method === 'POST') {
      response.statusCode = 202;
      response.end(JSON.stringify({ api_version: '2026-05-22', status: 'accepted', delivery_id: 'whdel_cli', idempotent_replay: false, audit_event: 'webhook.delivery_replayed', trace_id: 'trace_cli' }));
      return;
    }
    if (request.url === '/v1/jobs' && request.method === 'POST') {
      response.statusCode = 202;
      response.end(JSON.stringify(jobResponse('queued')));
      return;
    }
    if (request.url === '/v1/jobs/job_cli' && request.method === 'GET') {
      response.end(JSON.stringify(jobResponse('queued')));
      return;
    }
    if (request.url === '/v1/sse/jobs/job_cli' && request.method === 'GET') {
      response.setHeader('content-type', 'text/event-stream');
      response.end('event: job.queued\ndata: {"type":"queued","job_id":"job_cli"}\n\n');
      return;
    }
    if (request.url === '/v1/jobs/job_cli/cancel' && request.method === 'POST') {
      response.statusCode = 202;
      response.end(JSON.stringify(jobResponse('cancelled')));
      return;
    }
    if (request.url === '/v1/jobs/job_cli/retry' && request.method === 'POST') {
      response.statusCode = 202;
      response.end(JSON.stringify(jobResponse('queued')));
      return;
    }

    response.statusCode = 404;
    response.end(JSON.stringify({ error: { code: 'UBAG-VALIDATION-ROUTE-001', category: 'validation', message: 'not found', retryable: false, trace_id: 'trace_cli' } }));
  });

  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const tempDir = await mkdtemp(join(tmpdir(), 'ubag-cli-'));
  try {
    const baseUrl = `http://127.0.0.1:${server.address().port}`;
    const artifactPath = join(tempDir, 'report.txt');
    await writeFile(artifactPath, 'artifact body', 'utf8');
    await runCli(['health', '--json'], baseUrl);
    await runCli(['ready', '--json'], baseUrl);
    await runCli(['version', '--json'], baseUrl);
    await runCli(['create-job', '--idempotency-key', 'idem_create_cli_0001', '--prompt', 'hello', '--json'], baseUrl);
    await runCli(['get-job', 'job_cli', '--json'], baseUrl);
    await runCli(['list-jobs', '--json'], baseUrl);
    await runCli(['list-events', '--limit', '1', '--json'], baseUrl);
    await runCli(['list-targets', '--limit', '1', '--json'], baseUrl);
    await runCli(['list-adapters', '--limit', '1', '--json'], baseUrl);
    await runCli(['list-apps', '--limit', '1', '--json'], baseUrl);
    await runCli(['list-devices', '--limit', '1', '--json'], baseUrl);
    await runCli(['list-audit-events', '--limit', '1', '--json'], baseUrl);
    await runCli(['list-webhooks', '--limit', '1', '--json'], baseUrl);
    await runCli(['list-job-events', 'job_cli', '--limit', '1', '--json'], baseUrl);
    await runCli(['list-artifacts', 'job_cli', '--json'], baseUrl);
    await runCli(['put-artifact', 'job_cli', 'report.txt', '--file', artifactPath, '--content-type', 'text/plain', '--idempotency-key', 'idem_put_artifact_cli', '--json'], baseUrl);
    const artifactOutput = await runCli(['get-artifact', 'job_cli', 'report.txt', '--json'], baseUrl);
    await runCli(['delete-artifact', 'job_cli', 'report.txt', '--idempotency-key', 'idem_delete_artifact_cli', '--json'], baseUrl);
    await runCli(['replay-webhook', '--delivery-id', 'whdel_cli', '--idempotency-key', 'idem_webhook_cli_0001', '--json'], baseUrl);
    await runCli(['cache-status', '--json'], baseUrl);
    await runCli(['metrics', '--json'], baseUrl);
    await runCli(['cancel-job', 'job_cli', '--idempotency-key', 'idem_cancel_cli_0001', '--reason', 'test', '--json'], baseUrl);
    await runCli(['retry-job', 'job_cli', '--idempotency-key', 'idem_retry_cli_0001', '--json'], baseUrl);
    const sseOutput = await runCli(['stream-sse', 'job_cli', '--json'], baseUrl);

    const createRequest = requests.find((item) => item.url === '/v1/jobs' && item.method === 'POST');
    assert.equal(createRequest?.headers['idempotency-key'], 'idem_create_cli_0001');
    assert.equal(JSON.parse(createRequest.body).job.input.prompt, 'hello');
    assert.equal(requests.find((item) => item.url === '/v1/jobs/job_cli')?.method, 'GET');
    assert.equal(requests.find((item) => item.url === '/v1/jobs/job_cli/events?limit=1')?.method, 'GET');
    assert.equal(requests.find((item) => item.url === '/v1/jobs/job_cli/artifacts/report.txt' && item.method === 'PUT')?.headers['idempotency-key'], 'idem_put_artifact_cli');
    assert.equal(requests.find((item) => item.url === '/v1/jobs/job_cli/artifacts/report.txt' && item.method === 'DELETE')?.headers['idempotency-key'], 'idem_delete_artifact_cli');
    assert.equal(JSON.parse(requests.find((item) => item.url === '/v1/webhooks/replay')?.body).delivery_id, 'whdel_cli');
    assert.equal(requests.find((item) => item.url === '/v1/jobs/job_cli/cancel')?.headers['idempotency-key'], 'idem_cancel_cli_0001');
    assert.equal(requests.find((item) => item.url === '/v1/jobs/job_cli/retry')?.headers['idempotency-key'], 'idem_retry_cli_0001');
    assert.equal(JSON.parse(artifactOutput).body_base64, Buffer.from('artifact body').toString('base64'));
    assert.equal(JSON.parse(sseOutput).events[0].event, 'job.queued');
  } finally {
    await rm(tempDir, { recursive: true, force: true });
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  }
});

test('CLI rejects missing option values before consuming the next option', async () => {
  await assert.rejects(
    () => runCli(['create-job', '--target', '--json'], 'http://127.0.0.1:1'),
    /missing value for --target/
  );
});

function runCli(args, baseUrl) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cliPath, '--base-url', baseUrl, ...args], {
      windowsHide: true
    });
    let stdout = '';
    let stderr = '';
    const timer = setTimeout(() => {
      child.kill();
      reject(new Error(`CLI timed out: ${args.join(' ')}`));
    }, 10000);
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
      try {
        if (code !== 0) {
          throw new Error(`${stderr}\n${stdout}`);
        }
        resolve(stdout);
      } catch (error) {
        reject(error);
      }
    });
  });
}

function jobResponse(status) {
  return {
    api_version: '2026-05-22',
    job_id: 'job_cli',
    idempotent_replay: false,
    status,
    target: 'mock_target',
    metadata: {},
    trace_id: 'trace_cli',
    events_url: '/v1/jobs/job_cli/events'
  };
}
