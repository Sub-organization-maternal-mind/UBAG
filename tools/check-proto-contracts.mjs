import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { join, relative } from 'node:path';

const protoRoot = 'packages/proto/proto';
const genRoot = 'packages/proto/gen/go';
const failures = [];

// Every .proto must be registered here, so a new proto cannot skip the checks.
const specs = {
  'ubag/v1/jobs.proto': {
    rpcs: ['CreateJob', 'ListJobs', 'GetJob', 'CancelJob', 'RetryJob', 'ListJobEvents', 'StreamJobEvents'],
    messages: ['JobSpec', 'JobEvent', 'JobResponse', 'CreateJobRequest', 'GetJobRequest', 'ListJobEventsRequest'],
    parityFields: [
      'string api_version',
      'string idempotency_key',
      'string job_id',
      'string trace_id',
      'string data_json',
      'repeated JobEvent events'
    ]
  },
  // ubag.helper.v1 (P2.3): primary is the gRPC client, the helper is the server.
  'ubag/helper/v1/helper.proto': {
    rpcs: [
      'Handshake',
      'ReportCapacity',
      'RunAttempt',
      'RenewAttempt',
      'CancelAttempt',
      'InspectAttempt',
      'Drain',
      'StageManifest'
    ],
    messages: ['Fence', 'AttemptEvent', 'AttemptOutcome', 'RunAttemptRequest', 'RunAttemptResponse'],
    // The submission boundary and the event key parts must stay in the contract.
    parityFields: [
      'ATTEMPT_EVENT_TYPE_PROMPT_SUBMITTED',
      'uint64 sequence',
      'bool submitted',
      'bool reconcile_required',
      'bool partial',
      'string staging_path_prefix',
      // P4.11: the helper reports its adapter registry digest (handshake + capacity).
      'string registry_digest'
    ],
    // message -> required "type name" field declarations
    requiredFields: {
      Fence: [
        'string job_id',
        'string attempt_id',
        'string node_id',
        'uint64 lease_generation',
        'google.protobuf.Timestamp lease_expires_at',
        'string input_fingerprint',
        'string workload_version'
      ]
    },
    // Every mutating RPC request must carry the Fence.
    fencedRequests: ['RunAttemptRequest', 'RenewAttemptRequest', 'CancelAttemptRequest', 'StageManifestRequest'],
    // Voice RPCs live in helper_voice.proto (P5.7); HelperService carries none.
    forbiddenRpcPattern: /Voice/
  },
  // ubag.helper.v1 voice plane (P5.7): additive service, same package.
  'ubag/helper/v1/helper_voice.proto': {
    rpcs: ['OfferVoice', 'ReconnectVoice', 'ControlVoice', 'RenewVoice', 'StreamVoiceEvents'],
    messages: ['VoiceFence', 'VoiceCredentials', 'OfferVoiceRequest', 'OfferVoiceResponse', 'VoiceEvent'],
    parityFields: [
      'VOICE_CONTROL_OP_MUTE',
      'VOICE_CONTROL_OP_INTERRUPT',
      'VOICE_CONTROL_OP_TERMINATE',
      'bytes relay_key',
      'bytes media_key',
      'uint64 after_sequence'
    ],
    requiredFields: {
      VoiceFence: [
        'string session_id',
        'string tenant_id',
        'string attempt_id',
        'string node_id',
        'uint64 lease_generation',
        'google.protobuf.Timestamp expires_at'
      ]
    },
    // Every voice RPC request must carry the VoiceFence.
    fenceType: 'VoiceFence',
    fencedRequests: [
      'OfferVoiceRequest',
      'ReconnectVoiceRequest',
      'ControlVoiceRequest',
      'RenewVoiceRequest',
      'StreamVoiceEventsRequest'
    ],
    // The helper must never be handed a global secret: no field may be named
    // like one (per-attempt derived keys only).
    forbiddenFieldPattern: /(app_secret|relay_secret|turn_secret|shared_secret|turn_shared_secret)/
  }
};

function listProtos(dir) {
  const found = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) found.push(...listProtos(full));
    else if (entry.name.endsWith('.proto')) found.push(relative(protoRoot, full).replaceAll('\\', '/'));
  }
  return found;
}

function stripComments(text) {
  return text.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '');
}

// Field/enum-value numbers must be unique inside each message/enum scope. A
// oneof shares its parent message's number space.
function checkNumbers(file, code) {
  const stack = [];
  for (const line of code.split(/\r?\n/)) {
    const open = line.match(/^\s*(message|enum|oneof|service)\s+([A-Za-z0-9_]+)\s*\{/);
    if (open) {
      const [, kind, name] = open;
      const scope = kind === 'oneof' ? stack.at(-1)?.scope : kind === 'service' ? null : { name, numbers: new Set() };
      stack.push({ kind, name, scope });
      continue;
    }
    if (/^\s*}/.test(line)) {
      stack.pop();
      continue;
    }
    const top = stack.at(-1);
    if (!top?.scope) continue;
    const field = line.match(/=\s*(\d+)\s*[;[]/);
    if (!field) continue;
    if (top.scope.numbers.has(field[1])) {
      failures.push(`${file}: ${top.scope.name} reuses number ${field[1]}`);
    }
    top.scope.numbers.add(field[1]);
  }
}

function messageBody(code, name) {
  const match = code.match(new RegExp(`message\\s+${name}\\s*\\{([\\s\\S]*?)\\n\\}`));
  return match ? match[1] : null;
}

const protos = listProtos(protoRoot);
for (const file of protos) {
  if (!specs[file]) failures.push(`${file}: no contract spec registered in tools/check-proto-contracts.mjs`);
}

for (const [file, spec] of Object.entries(specs)) {
  const path = join(protoRoot, file);
  if (!existsSync(path)) {
    failures.push(`${file}: missing`);
    continue;
  }
  const text = readFileSync(path, 'utf8');
  const code = stripComments(text);

  for (const rpc of spec.rpcs) {
    if (!new RegExp(`rpc\\s+${rpc}\\s*\\(`).test(code)) failures.push(`${file}: missing ${rpc} RPC`);
  }
  for (const message of spec.messages) {
    if (!new RegExp(`message\\s+${message}\\s*\\{`).test(code)) failures.push(`${file}: missing ${message} message`);
  }
  for (const field of spec.parityFields ?? []) {
    if (!code.includes(field)) failures.push(`${file}: missing parity field "${field}"`);
  }
  for (const [message, fields] of Object.entries(spec.requiredFields ?? {})) {
    const body = messageBody(code, message);
    if (body === null) continue;
    for (const field of fields) {
      if (!new RegExp(`${field.replace(/\s+/g, '\\s+')}\\s*=\\s*\\d+`).test(body)) {
        failures.push(`${file}: ${message} missing field "${field}"`);
      }
    }
  }
  for (const message of spec.fencedRequests ?? []) {
    const body = messageBody(code, message);
    if (body === null) failures.push(`${file}: missing ${message} message`);
    else {
      const fenceType = spec.fenceType ?? 'Fence';
      if (!new RegExp(`\\b${fenceType}\\s+fence\\s*=\\s*\\d+`).test(body)) {
        failures.push(`${file}: ${message} must carry "${fenceType} fence"`);
      }
    }
  }
  if (spec.forbiddenRpcPattern) {
    for (const match of code.matchAll(/rpc\s+([A-Za-z0-9_]+)\s*\(/g)) {
      if (spec.forbiddenRpcPattern.test(match[1])) failures.push(`${file}: RPC ${match[1]} is not allowed yet`);
    }
  }
  if (spec.forbiddenFieldPattern) {
    const hit = code.match(spec.forbiddenFieldPattern);
    if (hit) failures.push(`${file}: field "${hit[0]}" would hand the helper a global secret`);
  }
  checkNumbers(file, code);

  // Checked-in generated Go must exist and expose every RPC (freshness smoke).
  const genBase = join(genRoot, file.replace(/\.proto$/, ''));
  if (!existsSync(`${genBase}.pb.go`)) failures.push(`${file}: generated ${genBase}.pb.go missing`);
  if (spec.rpcs.length > 0) {
    const grpcPath = `${genBase}_grpc.pb.go`;
    if (!existsSync(grpcPath)) {
      failures.push(`${file}: generated ${grpcPath} missing`);
    } else {
      const grpcText = readFileSync(grpcPath, 'utf8');
      for (const rpc of spec.rpcs) {
        if (!grpcText.includes(`${rpc}(`)) failures.push(`${file}: generated gRPC code is stale (no ${rpc})`);
      }
    }
  }
}

if (failures.length > 0) {
  console.error(`Proto contract checks failed:\n${failures.map((failure) => `- ${failure}`).join('\n')}`);
  process.exit(1);
}

console.log('Proto contract checks passed.');
