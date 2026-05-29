import { readFileSync } from 'node:fs';

const protoPath = 'packages/proto/proto/ubag/v1/jobs.proto';
const text = readFileSync(protoPath, 'utf8');
const failures = [];

for (const rpc of ['CreateJob', 'ListJobs', 'GetJob', 'CancelJob', 'RetryJob', 'ListJobEvents', 'StreamJobEvents']) {
  if (!new RegExp(`rpc\\s+${rpc}\\s*\\(`).test(text)) {
    failures.push(`missing ${rpc} RPC`);
  }
}

for (const message of ['JobSpec', 'JobEvent', 'JobResponse', 'CreateJobRequest', 'GetJobRequest', 'ListJobEventsRequest']) {
  if (!new RegExp(`message\\s+${message}\\s*\\{`).test(text)) {
    failures.push(`missing ${message} message`);
  }
}

const fieldNumbersByMessage = new Map();
let currentMessage = '';
for (const line of text.split(/\r?\n/)) {
  const messageMatch = line.match(/^\s*message\s+([A-Za-z0-9_]+)\s*\{/);
  if (messageMatch) {
    currentMessage = messageMatch[1];
    fieldNumbersByMessage.set(currentMessage, new Set());
    continue;
  }
  if (currentMessage && /^\s*}/.test(line)) {
    currentMessage = '';
    continue;
  }
  if (!currentMessage) continue;
  const fieldMatch = line.match(/=\s*(\d+)\s*;/);
  if (!fieldMatch) continue;
  const number = fieldMatch[1];
  const seen = fieldNumbersByMessage.get(currentMessage);
  if (seen.has(number)) {
    failures.push(`${currentMessage} reuses field number ${number}`);
  }
  seen.add(number);
}

for (const parityField of [
  'string api_version',
  'string idempotency_key',
  'string job_id',
  'string trace_id',
  'string data_json',
  'repeated JobEvent events'
]) {
  if (!text.includes(parityField)) {
    failures.push(`missing parity field "${parityField}"`);
  }
}

if (failures.length > 0) {
  console.error(`Proto contract checks failed:\n${failures.map((failure) => `- ${failure}`).join('\n')}`);
  process.exit(1);
}

console.log('Proto contract checks passed.');
