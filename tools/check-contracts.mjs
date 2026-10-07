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
    'Tests were removed',
    'cmd /c pnpm lint',
    'cmd /c pnpm typecheck'
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

// ─── Reverse parity: gateway route table → OpenAPI spec ─────────────────────
// routes.go is the one declaration of the gateway's HTTP surface, so every
// concrete pattern it registers must exist in openapi.yaml. A new gateway
// route without a contract entry fails this gate.
//
// chi wildcards ("/v1/jobs/*") are not real paths — they resolve onto a fixed
// set of concrete sub-paths handled by a subtree dispatcher. Each wildcard is
// mapped explicitly to the canonical OpenAPI path templates it serves. An
// unmapped wildcard fails closed instead of being silently skipped (a wrong
// generic guess here would hide real contract gaps).
const gatewayWildcardToOpenApiPaths = {
  '/v1/jobs/*': [
    '/v1/jobs/{job_id}',
    '/v1/jobs/{job_id}/events',
    '/v1/jobs/{job_id}/artifacts',
    '/v1/jobs/{job_id}/artifacts/{key}',
    '/v1/jobs/{job_id}/cancel',
    '/v1/jobs/{job_id}/retry'
  ],
  '/v1/workflows/*': [
    '/v1/workflows/{definition_id}/runs',
    '/v1/workflows/runs/{run_id}'
  ],
  '/v1/templates/*': [
    '/v1/templates/{template_id}',
    '/v1/templates/{template_id}/render'
  ],
  '/v1/scim/v2/Users/*': ['/v1/scim/v2/Users/{id}'],
  '/v1/scim/v2/Groups/*': ['/v1/scim/v2/Groups/{id}'],
  '/v1/alerts/*': [
    '/v1/alerts/{alert_id}/acknowledge',
    '/v1/alerts/{alert_id}/resolve'
  ],
  '/v1/sse/jobs/*': ['/v1/sse/jobs/{job_id}'],
  '/v1/voice/sessions/*': [
    '/v1/voice/sessions/{session_id}',
    '/v1/voice/sessions/{session_id}/connect',
    '/v1/voice/sessions/{session_id}/mute',
    '/v1/voice/sessions/{session_id}/renew',
    '/v1/voice/sessions/{session_id}/terminate'
  ]
};

// The YAML key for a path may be quoted (e.g. "/v1/webhooks/secret:rotate" —
// a literal colon is legal in an OpenAPI path template but some writers quote
// the key), so accept both the bare and quoted forms.
function openApiHasKey(text, path) {
  return text.includes(`  ${path}:`) || text.includes(`  "${path}":`);
}

const routesGoSource = requireFile('apps/gateway/internal/httpapi/routes.go');
if (routesGoSource) {
  // Route table rows look like: {"/v1/health", s.handleHealth},
  const gatewayRoutePatterns = [];
  for (const match of routesGoSource.matchAll(/\{\s*"(\/v1\/[^"]*)"\s*,/g)) {
    gatewayRoutePatterns.push(match[1]);
  }
  if (gatewayRoutePatterns.length === 0) {
    failures.push('routes.go parsed but no route patterns found — check the routeDecl table format');
  }

  if (openApi) {
    for (const pattern of gatewayRoutePatterns) {
      if (pattern.endsWith('/*')) {
        const mapped = gatewayWildcardToOpenApiPaths[pattern];
        if (!mapped) {
          failures.push(
            `Gateway wildcard route ${pattern} is not mapped to OpenAPI path templates in tools/check-contracts.mjs (gatewayWildcardToOpenApiPaths)`
          );
          continue;
        }
        for (const openApiPath of mapped) {
          if (!openApiHasKey(openApi, openApiPath)) {
            failures.push(`OpenAPI missing gateway route ${openApiPath} (served by routes.go wildcard ${pattern})`);
          }
        }
        continue;
      }
      if (!openApiHasKey(openApi, pattern)) {
        failures.push(`OpenAPI missing gateway route ${pattern} (registered in routes.go)`);
      }
    }
  }
}

const gatewayServer = requireFile('apps/gateway/internal/httpapi/server.go');
// Route declarations live in routes.go (the route table); parity is checked
// across the package's httpapi files. The Ubag-Trace-Id response header is
// declared as a constant in the webhooks signing module (withMetrics writes
// it from there), so that file joins the parity source set too.
const gatewayRoutes = requireFile('apps/gateway/internal/httpapi/routes.go');
const gatewayTraceHeader = requireFile('apps/gateway/internal/webhooks/signing.go');
const gatewayHTTP = [gatewayServer, gatewayRoutes, gatewayTraceHeader].filter(Boolean).join('\n');
if (gatewayServer) {
  for (const path of requiredOpenApiPaths.filter((path) => !path.includes('{'))) {
    if (!gatewayHTTP.includes(`"${path}"`)) {
      failures.push(`Gateway runtime missing route string ${path}`);
    }
  }
  for (const requiredRuntimeTerm of [
    'Ubag-Trace-Id',
    'Ubag-Api-Version-Used',
    'Location',
    'constantTimeEqual',
    'authorizeGatewayAction',
    'ListEvents',
    'replayWebhook',
    'ubag_gateway_http_requests_total',
    'ubag_jobs_created_total',
    'ubag_jobs_current'
  ]) {
    if (!gatewayHTTP.includes(requiredRuntimeTerm)) {
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

// Worker daemon protocol v2 (P2.2): opt-in, so v1 lines must stay valid and the
// job-event data docs must carry the attempt/provisional/reconcile vocabulary.
for (const schemaFile of ['worker-daemon-request.schema.json', 'worker-daemon-job-end.schema.json']) {
  const path = join('packages', 'shared-schemas', 'schemas', schemaFile);
  const schema = parseJson(path);
  if (schema && schema.$schema !== 'https://json-schema.org/draft/2020-12/schema') {
    failures.push(`${path} must use JSON Schema Draft 2020-12`);
  }
  if (schema && schemaFile === 'worker-daemon-request.schema.json') {
    const job = schema.$defs?.job_request;
    if (JSON.stringify(job?.required) !== '["job_id","payload"]') {
      failures.push(`${path} job line must keep the v1 required set (job_id, payload)`);
    }
    if (job?.properties?.proto?.const !== 2) failures.push(`${path} job line proto must be opt-in const 2`);
  }
  if (schema && schemaFile === 'worker-daemon-job-end.schema.json') {
    if (JSON.stringify(schema.required) !== '["__ubag_job_end__","job_id","status"]') {
      failures.push(`${path} must keep the v1 required set (__ubag_job_end__, job_id, status)`);
    }
    for (const field of ['attempt_id', 'slot_id', 'pid', 'warm_key', 'outcome_signal', 'submitted']) {
      if (!schema.properties?.[field]) failures.push(`${path} missing v2 field ${field}`);
    }
  }
}
const jobEventSchema = parseJson('packages/shared-schemas/schemas/job-event.schema.json');
if (jobEventSchema) {
  if (jobEventSchema.additionalProperties !== false) {
    failures.push('job-event.schema.json envelope must stay additionalProperties:false (attempt data goes in data)');
  }
  for (const field of ['attempt_id', 'stream_end_reason', 'partial', 'submitted', 'reconcile_required']) {
    if (!jobEventSchema.properties?.data?.properties?.[field]) failures.push(`job-event.schema.json data missing ${field}`);
  }
}

// Helper node allocation consumer schema (P2.4): the grant fields the primary relies on
// must stay present and required, and the fencing code must exist and stay non-retryable.
const nodeAllocation = parseJson('packages/shared-schemas/schemas/node-allocation.schema.json');
if (nodeAllocation) {
  if (nodeAllocation.$schema !== 'https://json-schema.org/draft/2020-12/schema') {
    failures.push('node-allocation.schema.json must use JSON Schema Draft 2020-12');
  }
  if (nodeAllocation.additionalProperties !== false) {
    failures.push('node-allocation.schema.json must stay additionalProperties:false (no tenant or job ids cross this interface)');
  }
  for (const field of [
    'schema_version', 'node_id', 'region', 'endpoint', 'cert_identity', 'cpu_millis', 'memory_bytes',
    'reservation_state', 'state', 'max_browser_workloads', 'voice_capable', 'valid_until', 'generation'
  ]) {
    if (!nodeAllocation.properties?.[field]) failures.push(`node-allocation.schema.json missing ${field}`);
    if (!nodeAllocation.required?.includes(field)) failures.push(`node-allocation.schema.json must require ${field}`);
  }
}
const errorCatalog = parseJson('packages/shared-schemas/errors.json');
const fencedCode = errorCatalog?.['x-catalog']?.namespaces?.flatMap((n) => n.codes).find((c) => c.code === 'UBAG-WORKER-NODE-FENCED-005');
if (errorCatalog && (!fencedCode || fencedCode.retryable !== false)) {
  failures.push('errors.json must define UBAG-WORKER-NODE-FENCED-005 as non-retryable');
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

// ─── RBAC cross-check: @ubag/security spec ↔ gateway-enforced authz table ───
// packages/security/src/rbac.ts publishes the RBAC spec and
// apps/gateway/internal/authz/authz.go enforces it. Both must declare the same
// role set and the same per-role action sets, or a role like the historical
// (removed) "support" role can exist in the spec but not in the enforcement
// path. Normalization:
//   - roles are the quoted identifiers of the role table in each file;
//   - per-role actions are the quoted action strings of that role's entry;
//   - superadmin is a full grant in both files (Go: the RoleAllows fast path,
//     ts: `new Set(UBAG_ACTIONS)`), so it is compared as the wildcard "*"
//     rather than by enumerating actions.
const rbacSpecSource = requireFile('packages/security/src/rbac.ts');
const authzSource = requireFile('apps/gateway/internal/authz/authz.go');

// rbac.ts: `role: new Set([...])` rows inside ROLE_PERMISSIONS; superadmin uses
// `new Set(UBAG_ACTIONS)`, which we normalize to the wildcard.
function extractTsRoleActions(source) {
  const roleActions = new Map();
  const start = source.indexOf('const ROLE_PERMISSIONS');
  if (start === -1) return roleActions;
  const body = source.slice(start, source.indexOf('};', start));
  const rowPattern = /\b(viewer|developer|operator|admin|superadmin|service|support)\s*:\s*new Set\(([^)]*)\)/g;
  for (const match of body.matchAll(rowPattern)) {
    const [, role, setArg] = match;
    if (setArg.trim() === 'UBAG_ACTIONS') {
      roleActions.set(role, '*');
      continue;
    }
    const actions = [...setArg.matchAll(/"([^"]+)"/g)].map((quoted) => quoted[1]).sort();
    roleActions.set(role, actions);
  }
  return roleActions;
}

// authz.go: `"role": { ... "action": {}, ... }` rows inside the roleActions
// map; the superadmin entry is an intentionally empty map satisfied by the
// RoleAllows fast path, so it is normalized to the wildcard too.
function extractGoRoleActions(source) {
  const roleActions = new Map();
  const start = source.indexOf('var roleActions');
  if (start === -1) return roleActions;
  const body = source.slice(start);
  const rowPattern = /^\t"([a-z]+)": \{/gm;
  const matches = [...body.matchAll(rowPattern)];
  for (let index = 0; index < matches.length; index += 1) {
    const [, role] = matches[index];
    const rowStart = matches[index].index + matches[index][0].length;
    const rowEnd = index + 1 < matches.length ? matches[index + 1].index : body.length;
    const row = body.slice(rowStart, rowEnd);
    // gofmt aligns the empty action structs (e.g. `"job:create":        {},`),
    // so allow arbitrary spacing before `{},`.
    const actions = [...row.matchAll(/"([a-z:_-]+)":\s*\{\}/g)].map((quoted) => quoted[1]).sort();
    roleActions.set(role, role === 'superadmin' ? '*' : actions);
  }
  return roleActions;
}

function describeRoleActions(roleActions) {
  return [...roleActions.entries()]
    .map(([role, actions]) => `${role}=${actions === '*' ? '*' : `[${actions.join(',')}]`}`)
    .join(' ');
}

if (rbacSpecSource && authzSource) {
  const tsRoles = extractTsRoleActions(rbacSpecSource);
  const goRoles = extractGoRoleActions(authzSource);
  const rbacFailures = [];

  if (tsRoles.size === 0) {
    rbacFailures.push('could not parse ROLE_PERMISSIONS rows from packages/security/src/rbac.ts');
  }
  if (goRoles.size === 0) {
    rbacFailures.push('could not parse roleActions rows from apps/gateway/internal/authz/authz.go');
  }
  if (tsRoles.size > 0 && goRoles.size > 0) {
    for (const role of tsRoles.keys()) {
      if (!goRoles.has(role)) {
        rbacFailures.push(`RBAC role "${role}" exists in packages/security/src/rbac.ts but not in the gateway authz roleActions table`);
      }
    }
    for (const role of goRoles.keys()) {
      if (!tsRoles.has(role)) {
        rbacFailures.push(`RBAC role "${role}" exists in the gateway authz roleActions table but not in packages/security/src/rbac.ts`);
      }
    }
    for (const role of tsRoles.keys()) {
      if (!goRoles.has(role)) continue;
      const tsActions = tsRoles.get(role);
      const goActions = goRoles.get(role);
      if (tsActions === '*' || goActions === '*') {
        if (tsActions !== goActions) {
          rbacFailures.push(`RBAC role "${role}" must be a full grant (wildcard) in BOTH rbac.ts and authz.go`);
        }
        continue;
      }
      const mismatched = [
        ...new Set([...tsActions.filter((action) => !goActions.includes(action)), ...goActions.filter((action) => !tsActions.includes(action))])
      ];
      if (mismatched.length > 0) {
        rbacFailures.push(
          `RBAC role "${role}" action sets disagree between rbac.ts and authz.go — ts-only: [${tsActions.filter((action) => !goActions.includes(action)).join(',')}], go-only: [${goActions.filter((action) => !tsActions.includes(action)).join(',')}]`
        );
      }
    }
    if (rbacFailures.length === 0) {
      console.log(`RBAC cross-check ok: ${describeRoleActions(tsRoles)}`);
    }
  }
  failures.push(...rbacFailures);
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

// Verify SDK contract manifests are not stale relative to generate-manifest.mjs
// output. `--check` byte-compares the generated files against a fresh render
// and exits non-zero on any difference, so that call IS the gate; it must be
// allowed to throw. The dead `if (false && ...)` git-status branch that used
// to sit in this try block has been removed: it could never fire, it made the
// block read as a disabled check, and a git-status check would in any case
// false-positive on any legitimately dirty working tree.
try {
  execSync("node tools/make-sdks/generate-manifest.mjs --check", { stdio: "pipe" });
} catch {
  failures.push("SDK contract manifest is stale - run: node tools/make-sdks/generate-manifest.mjs");
}

if (failures.length) {
  console.error(`Contract checks failed:\n${failures.map((failure) => `- ${failure}`).join('\n')}`);
  process.exit(1);
}

console.log('Contract checks passed.');
