// Generates generated_contract_manifest.* for the supported TS and Go SDKs
// from the OpenAPI spec + error catalog.
// Run: node tools/make-sdks/generate-manifest.mjs [--check]
import { existsSync, readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { createHash } from "node:crypto";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const API_VERSION = "2026-05-22";
const checkOnly = process.argv.includes("--check");

function sha256(path) {
  return createHash("sha256").update(readFileSync(resolve(ROOT, path))).digest("hex");
}

function loadYamlPaths(yamlText) {
  // Minimal path+method extraction: lines like "  /v1/jobs:" then "    get:".
  const endpoints = {};
  const lines = yamlText.split(/\r?\n/);
  let currentPath = null;
  for (const line of lines) {
    const pathMatch = /^  (\/v1\/[^\s:]+):\s*$/.exec(line);
    if (pathMatch) { currentPath = pathMatch[1]; continue; }
    const methodMatch = /^    (get|post|put|delete|patch):\s*$/.exec(line);
    if (methodMatch && currentPath) {
      const key = `${methodMatch[1].toUpperCase()} ${currentPath}`;
      endpoints[key] = { method: methodMatch[1].toUpperCase(), path: currentPath };
    }
  }
  return endpoints;
}

function loadErrorCodes(errorsJson) {
  const out = {};
  const catalog = errorsJson["x-catalog"];
  if (!catalog) throw new Error("errors.json missing x-catalog key");
  for (const ns of catalog.namespaces ?? []) {
    for (const code of ns.codes ?? []) {
      out[code.code] = {
        category: ns.category ?? ns.namespace ?? "",
        retryable: Boolean(code.retryable),
        ...(code.retry_after_ms !== undefined ? { retry_after_ms: code.retry_after_ms } : {}),
      };
    }
  }
  return out;
}

// Statuses from which a job never transitions again (gateway jobs semantics).
const TERMINAL_STATUSES = [
  "completed",
  "completed_with_warnings",
  "failed_retryable",
  "failed_terminal",
  "dead_letter",
  "cancelled",
  "timed_out",
];

function loadJobVocabularies() {
  const responseSchema = JSON.parse(
    readFileSync(resolve(ROOT, "packages/shared-schemas/schemas/job-response.schema.json"), "utf8"),
  );
  const eventSchema = JSON.parse(
    readFileSync(resolve(ROOT, "packages/shared-schemas/schemas/job-event.schema.json"), "utf8"),
  );
  const statuses = responseSchema?.$defs?.job_status?.enum;
  const eventTypes = eventSchema?.properties?.type?.enum;
  if (!Array.isArray(statuses) || statuses.length === 0) {
    throw new Error("job-response.schema.json missing $defs.job_status.enum");
  }
  if (!Array.isArray(eventTypes) || eventTypes.length === 0) {
    throw new Error("job-event.schema.json missing properties.type.enum");
  }
  for (const terminal of TERMINAL_STATUSES) {
    if (!statuses.includes(terminal)) {
      throw new Error(`terminal status ${terminal} missing from job_status enum`);
    }
  }
  const jobStatuses = {};
  for (const status of statuses) {
    jobStatuses[status] = { terminal: TERMINAL_STATUSES.includes(status) };
  }
  return { jobStatuses, jobEventTypes: eventTypes };
}

function loadErrorCategories() {
  const errorSchema = JSON.parse(
    readFileSync(resolve(ROOT, "packages/shared-schemas/schemas/error.schema.json"), "utf8"),
  );
  const def = errorSchema?.$defs?.error ?? errorSchema?.properties?.error;
  const categories = def?.properties?.category?.enum;
  if (!Array.isArray(categories) || categories.length === 0) {
    throw new Error("error.schema.json missing error category enum");
  }
  return categories;
}

const openapiText = readFileSync(resolve(ROOT, "packages/openapi/openapi.yaml"), "utf8");
const errorsJson = JSON.parse(readFileSync(resolve(ROOT, "packages/shared-schemas/errors.json"), "utf8"));

const endpoints = loadYamlPaths(openapiText);
if (Object.keys(endpoints).length === 0) {
  throw new Error("No endpoints found in openapi.yaml — check YAML indentation");
}
const errorCodes = loadErrorCodes(errorsJson);
const { jobStatuses, jobEventTypes } = loadJobVocabularies();
const errorCategories = loadErrorCategories();
const fingerprints = {
  "job-request": sha256("packages/shared-schemas/schemas/job-request.schema.json"),
  "job-response": sha256("packages/shared-schemas/schemas/job-response.schema.json"),
};

const manifest = { apiVersion: API_VERSION, endpoints, errorCodes, jobStatuses, jobEventTypes, errorCategories, fingerprints };
const outputs = [
  {
    path: "packages/sdk-typescript/src/generated/contract-manifest.ts",
    text: renderTs(manifest),
  },
  {
    path: "packages/sdk-go/generated_contract_manifest.go",
    text: renderGo(manifest),
  },
];

if (checkOnly) {
  const stale = outputs
    .filter((output) => {
      const outputPath = resolve(ROOT, output.path);
      return !existsSync(outputPath) || readFileSync(outputPath, "utf8") !== output.text;
    })
    .map((output) => output.path);
  if (stale.length > 0) {
    console.error(`Generated SDK contract manifests are stale:\n${stale.map((path) => `- ${path}`).join("\n")}`);
    process.exit(1);
  }
  console.log(`SDK contract manifests are fresh: ${Object.keys(endpoints).length} endpoints, ${Object.keys(errorCodes).length} error codes`);
} else {
  for (const output of outputs) {
    const outputPath = resolve(ROOT, output.path);
    mkdirSync(dirname(outputPath), { recursive: true });
    writeFileSync(outputPath, output.text);
  }
  console.log(`Generated SDK manifests: ${Object.keys(endpoints).length} endpoints, ${Object.keys(errorCodes).length} error codes`);
}

function renderTs(m) {
  return `// Generated by tools/make-sdks/generate-manifest.mjs. Do not edit by hand.
export const UBAG_API_VERSION = ${JSON.stringify(m.apiVersion)};
export const UBAG_ENDPOINTS = ${JSON.stringify(m.endpoints, null, 2)} as const;
export const UBAG_ERROR_CODES = ${JSON.stringify(m.errorCodes, null, 2)} as const;
export const UBAG_ERROR_CATEGORIES = ${JSON.stringify(m.errorCategories)} as const;
export const UBAG_JOB_STATUSES = ${JSON.stringify(m.jobStatuses, null, 2)} as const;
export const UBAG_JOB_EVENT_TYPES = ${JSON.stringify(m.jobEventTypes)} as const;
export const UBAG_TERMINAL_JOB_STATUSES = ${JSON.stringify(m.jobEventTypes ? Object.entries(m.jobStatuses).filter(([, v]) => v.terminal).map(([k]) => k) : [])} as const;
export const UBAG_SCHEMA_FINGERPRINTS = ${JSON.stringify(m.fingerprints, null, 2)} as const;
`;
}

function renderGo(m) {
  const endpoints = Object.entries(m.endpoints)
    .map(([k, v]) => `\t${JSON.stringify(k)}: {Method: ${JSON.stringify(v.method)}, Path: ${JSON.stringify(v.path)}},`)
    .join("\n");
  const codes = Object.entries(m.errorCodes)
    .map(([k, v]) => `\t${JSON.stringify(k)}: {Category: ${JSON.stringify(v.category)}, Retryable: ${v.retryable}, RetryAfterMs: ${v.retry_after_ms ?? 0}},`)
    .join("\n");
  const fps = Object.entries(m.fingerprints)
    .map(([k, v]) => `\t${JSON.stringify(k)}: ${JSON.stringify(v)},`)
    .join("\n");
  const statuses = Object.entries(m.jobStatuses)
    .map(([k, v]) => `\t${JSON.stringify(k)}: {Terminal: ${v.terminal}},`)
    .join("\n");
  const eventTypes = m.jobEventTypes
    .map((t) => `\t${JSON.stringify(t)},`)
    .join("\n");
  const categories = m.errorCategories
    .map((c) => `\t${JSON.stringify(c)},`)
    .join("\n");
  return `// Generated by tools/make-sdks/generate-manifest.mjs. Do not edit by hand.
package ubag

const UbagAPIVersion = ${JSON.stringify(m.apiVersion)}

type ManifestEndpoint struct {
\tMethod string
\tPath   string
}

type ManifestErrorCode struct {
\tCategory     string
\tRetryable    bool
\tRetryAfterMs int64
}

type ManifestJobStatus struct {
\tTerminal bool
}

var UbagEndpoints = map[string]ManifestEndpoint{
${endpoints}
}

var UbagErrorCodes = map[string]ManifestErrorCode{
${codes}
}

var UbagJobStatuses = map[string]ManifestJobStatus{
${statuses}
}

var UbagJobEventTypes = []string{
${eventTypes}
}

var UbagErrorCategories = []string{
${categories}
}

var UbagSchemaFingerprints = map[string]string{
${fps}
}
`;
}
