import { spawnSync } from 'node:child_process';
import { delimiter, resolve } from 'node:path';

const smokePayload = JSON.stringify({
  api_version: '2026-05-22',
  idempotency_key: 'worker-smoke',
  trace_id: 'trace_worker_smoke',
  job: {
    target: 'mock',
    command_type: 'mock.complete',
    input: { prompt: 'hello from the worker smoke test' }
  }
});

const commands = [
  ['python', ['-m', 'unittest', 'discover', '-s', 'adapters/mock/tests']],
  ['python', ['-m', 'unittest', 'discover', '-s', 'apps/worker/tests']],
  ['python', ['-m', 'compileall', '-q', 'apps/worker', 'adapters/mock']]
];

const pythonPath = [
  resolve('apps/worker'),
  resolve('adapters/mock'),
  process.env.PYTHONPATH
].filter(Boolean).join(delimiter);
const env = { ...process.env, PYTHONPATH: pythonPath };

for (const [command, args] of commands) {
  const result = spawnSync(command, args, { stdio: 'inherit', env });
  if (result.error) {
    console.error(`Worker tests blocked: ${command} is not available on PATH.`);
    process.exit(1);
  }
  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
}

const smoke = spawnSync('python', ['apps/worker/run_mock_worker.py', '--payload', smokePayload], {
  encoding: 'utf8',
  env
});

if (smoke.error) {
  console.error('Worker tests blocked: python is not available on PATH.');
  process.exit(1);
}

if (smoke.status !== 0) {
  process.stdout.write(smoke.stdout ?? '');
  process.stderr.write(smoke.stderr ?? '');
  process.exit(smoke.status ?? 1);
}

const events = smoke.stdout
  .trim()
  .split(/\r?\n/)
  .filter(Boolean)
  .map((line) => JSON.parse(line));

if (events.length < 4 || events[0].type !== 'queued' || events.at(-1)?.type !== 'completed') {
  console.error('Worker smoke failed: expected queued ... completed JSONL events.');
  process.exit(1);
}

console.log(`worker smoke passed: ${events.length} JSONL events`);
