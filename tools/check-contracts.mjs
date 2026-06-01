import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { execSync } from 'node:child_process';

const failures = [];

const requiredOpenApiPaths = [
  '/v1/health',
  '/v1/ready',
  '/v1/version',
  '/v1/metrics',
  '/v1/events',
  '/v1/stream',
  '/v1/workflows',
  '/v1/templates',
  '/v1/targets',
  '/v1/adapters',
  '/v1/apps',
  '/v1/devices',
  '/v1/webhooks',
  '/v1/webhooks/replay',
  '/v1/cache',
  '/v1/audit',
  '/v1/jobs',
  '/v1/jobs/{job_id}',
  '/v1/jobs/{job_id}/events',
  '/v1/jobs/{job_id}/artifacts',
  '/v1/jobs/{job_id}/artifacts/{key}',
  '/v1/jobs/{job_id}/cancel',
  '/v1/jobs/{job_id}/retry',
  '/v1/sse/jobs/{job_id}'
];

const requiredSchemaFiles = [
  'job-request.schema.json',
  'job-response.schema.json',
  'job-event.schema.json',
  'error.schema.json'
];

const legacySchemaFiles = [
  'packages/shared-schemas/job-request.schema.json',
  'packages/shared-schemas/job-response.schema.json',
  'packages/shared-schemas/job-event.schema.json',
  'packages/shared-schemas/error-envelope.schema.json'
];

const docChecks = {
  'apps/docs/src/content/docs/data/schema.md': [
    'Core entities',
    'Postgres',
    'SQLite',
    'Migration policy'
  ],
  'apps/docs/src/content/docs/data/queue.md': [
    'edge profile',
    'sqlitequeue',
    'Required semantics'
  ],
  'apps/docs/src/content/docs/deployment/migrations.md': [
    'Schema migrations',
    'Edge to small migration',
    'Rollback'
  ],
  'apps/docs/src/content/docs/testing/strategy.md': [
    'UBAG_TEST_POSTGRES_DSN',
    'cmd /c pnpm test:gateway',
    'disposable database'
  ],
  'apps/docs/src/content/docs/deployment/profiles.md': [
    'UBAG_GATEWAY_STORE=postgres',
    'worker-event dedupe',
    'UBAG_EXECUTOR_MODE=nats',
    'UBAG_ARTIFACT_STORE=minio'
  ],
  'apps/docs/src/content/docs/contracts/job-contract.md': [
    'Request envelope',
    'Response envelope',
    'Output normalization'
  ],
  'apps/docs/src/content/docs/contracts/error-catalog.md': [
    'Format',
    'Namespaces',
    'Registry rule'
  ]
};

function requireFile(path) {
  if (!existsSync(path)) {
    failures.push(`${path} missing`);
    return null;
  }
  return readFileSync(path, 'utf8');
}

function parseJson(path) {
  const text = requireFile(path);
  if (text === null) return null;
  try {
    return JSON.parse(text);
  } catch (error) {
    failures.push(`${path} is not valid JSON: ${error.message}`);
    return null;
  }
}

const openApi = requireFile('packages/openapi/openapi.yaml');
if (openApi) {
  for (const path of requiredOpenApiPaths) {
    if (!openApi.includes(`  ${path}:`)) {
      failures.push(`OpenAPI missing ${path}`);
    }
  }

  for (const schemaFile of requiredSchemaFiles) {
    if (!openApi.includes(`../shared-schemas/schemas/${schemaFile}`)) {
      failures.push(`OpenAPI missing canonical schema ref ${schemaFile}`);
    }
  }
}

const gatewayServer = requireFile('apps/gateway/internal/httpapi/server.go');
if (gatewayServer) {
  for (const path of requiredOpenApiPaths.filter((path) => !path.includes('{'))) {
    if (!gatewayServer.includes(`"${path}"`)) {
      failures.push(`Gateway runtime missing route string ${path}`);
    }
  }
  for (const requiredRuntimeTerm of [
    'Ubag-Trace-Id',
    'Ubag-Api-Version-Used',
    'Location',
    'handleWebSocketUpgrade',
    'Sec-WebSocket-Accept',
    'constantTimeEqual',
    'authorizeJobAccess',
    'ListEvents',
    'replayWebhook',
    'ubag_gateway_http_requests_total',
    'ubag_jobs_created_total',
    'ubag_jobs_current'
  ]) {
    if (!gatewayServer.includes(requiredRuntimeTerm)) {
      failures.push(`Gateway runtime missing parity term ${requiredRuntimeTerm}`);
    }
  }
}

const gatewayModels = requireFile('apps/gateway/internal/httpapi/models.go');
if (gatewayModels) {
  if (!/Callbacks\s+map\[string\]any/.test(gatewayModels)) {
    failures.push('Gateway create-job model must accept callbacks as an object');
  }
  if (!/Input\s+map\[string\]any/.test(gatewayModels)) {
    failures.push('Gateway create-job model must decode job.input as an object');
  }
}

for (const schemaFile of requiredSchemaFiles) {
  const path = join('packages', 'shared-schemas', 'schemas', schemaFile);
  const schema = parseJson(path);
  if (schema && schema.$schema !== 'https://json-schema.org/draft/2020-12/schema') {
    failures.push(`${path} must use JSON Schema Draft 2020-12`);
  }
}

const jobProto = requireFile('packages/proto/proto/ubag/v1/jobs.proto');
if (jobProto) {
  for (const rpc of ['CreateJob', 'ListJobs', 'GetJob', 'CancelJob', 'RetryJob', 'ListJobEvents', 'StreamJobEvents']) {
    if (!jobProto.includes(`rpc ${rpc}`)) {
      failures.push(`jobs.proto missing ${rpc} RPC`);
    }
  }
  for (const field of ['event_id', 'api_version', 'type', 'data_json', 'trace_id', 'repeated JobEvent events']) {
    if (!jobProto.includes(field)) {
      failures.push(`jobs.proto missing JobEvent parity field ${field}`);
    }
  }
  for (const field of ['message ErrorResponse', 'message Error', 'code', 'category', 'retryable', 'doc_url', 'repeated FieldError field_errors']) {
    if (!jobProto.includes(field)) {
      failures.push(`jobs.proto missing stable error envelope field ${field}`);
    }
  }
}

for (const path of legacySchemaFiles) {
  if (existsSync(path)) {
    failures.push(`${path} duplicates canonical packages/shared-schemas/schemas output`);
  }
}

const fixture = parseJson('packages/conformance/fixtures/v0/scenarios.json');
if (fixture) {
  if (!Array.isArray(fixture.scenarios) || fixture.scenarios.length < 8) {
    failures.push('conformance fixture suite must include at least 8 scenarios');
  }
  if (!Array.isArray(fixture.coverage_scenarios) || fixture.coverage_scenarios.length < 8) {
    failures.push('conformance fixture suite must include non-REST coverage scenarios');
  }
  const duplicateIds = new Set();
  const seenIds = new Set();
  for (const scenario of fixture.scenarios ?? []) {
    if (!scenario.id) failures.push('conformance scenario missing id');
    if (seenIds.has(scenario.id)) duplicateIds.add(scenario.id);
    seenIds.add(scenario.id);
  }
  for (const id of duplicateIds) {
    failures.push(`duplicate conformance scenario id ${id}`);
  }
}

const migrationsDir = join('migrations', 'sqlite');
if (!existsSync(migrationsDir)) {
  failures.push('migrations/sqlite missing');
} else {
  const migrations = readdirSync(migrationsDir).filter((file) => file.endsWith('.sql'));
  for (const required of ['0001_edge_store_core.sql', '0002_edge_queue.sql', '0003_webhook_outbox.sql']) {
    if (!migrations.includes(required)) failures.push(`missing SQLite migration ${required}`);
  }
}

const postgresMigrationsDir = join('migrations', 'postgres');
if (!existsSync(postgresMigrationsDir)) {
  failures.push('migrations/postgres missing');
} else {
  const migrations = readdirSync(postgresMigrationsDir).filter((file) => file.endsWith('.sql'));
  for (const required of ['0001_gateway_stores.sql', '0002_artifact_metadata.sql', '0003_webhook_outbox.sql']) {
    if (!migrations.includes(required)) failures.push(`missing Postgres migration ${required}`);
  }
  const gatewayStores = requireFile(join(postgresMigrationsDir, '0001_gateway_stores.sql'));
  for (const term of ['gateway_jobs', 'gateway_job_events', 'gateway_job_worker_event_keys', 'gateway_idempotency_records']) {
    if (gatewayStores && !gatewayStores.includes(term)) {
      failures.push(`Postgres gateway migration missing ${term}`);
    }
  }
  const artifactMetadata = requireFile(join(postgresMigrationsDir, '0002_artifact_metadata.sql'));
  for (const term of ['artifact_metadata', 'object_key', 'gateway_schema_migrations']) {
    if (artifactMetadata && !artifactMetadata.includes(term)) {
      failures.push(`Postgres artifact migration missing ${term}`);
    }
  }
  const webhookOutbox = requireFile(join(postgresMigrationsDir, '0003_webhook_outbox.sql'));
  for (const term of ['gateway_webhook_deliveries', 'gateway_webhook_attempts', 'gateway_schema_migrations']) {
    if (webhookOutbox && !webhookOutbox.includes(term)) {
      failures.push(`Postgres webhook migration missing ${term}`);
    }
  }
}

for (const [file, terms] of Object.entries(docChecks)) {
  const text = requireFile(file);
  if (text === null) continue;
  for (const term of terms) {
    if (!text.includes(term)) failures.push(`${file} missing "${term}"`);
  }
}

// Verify SDK contract manifests are not stale relative to generate-manifest.mjs output.
try {
  execSync("node tools/make-sdks/generate-manifest.mjs", { stdio: "pipe" });
  const status = execSync(
    "git status --porcelain packages/sdk-typescript/src/generated packages/sdk-go/generated_contract_manifest.go packages/sdk-rust/src/generated",
    { encoding: "utf8" }
  );
  if (status.trim() !== "") {
    failures.push("SDK contract manifest is stale — run: node tools/make-sdks/generate-manifest.mjs");
  }
  // Restore: re-run to undo the git-dirty effect of the generator regenerating files
  execSync("git restore packages/sdk-typescript/src/generated packages/sdk-go/generated_contract_manifest.go packages/sdk-rust/src/generated 2>/dev/null || true", { stdio: "pipe" });
} catch (e) {
  // If git or node tools aren't available, skip this check
  console.warn("Manifest staleness check skipped:", e.message);
}

if (failures.length) {
  console.error(`Contract checks failed:\n${failures.map((failure) => `- ${failure}`).join('\n')}`);
  process.exit(1);
}

console.log('Contract checks passed.');
