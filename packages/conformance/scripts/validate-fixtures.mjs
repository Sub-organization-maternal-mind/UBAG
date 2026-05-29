import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const currentDir = dirname(fileURLToPath(import.meta.url));
const fixturePath = join(currentDir, "..", "fixtures", "v0", "scenarios.json");

const fixture = JSON.parse(await readFile(fixturePath, "utf8"));
const errors = [];

if (fixture.suite !== "ubag.v0.sdk.baseline") {
  errors.push("fixture.suite must be ubag.v0.sdk.baseline");
}

if (fixture.api_version !== "2026-05-22") {
  errors.push("fixture.api_version must be 2026-05-22");
}

if (!Array.isArray(fixture.scenarios) || fixture.scenarios.length === 0) {
  errors.push("fixture.scenarios must contain at least one scenario");
}
if (!Array.isArray(fixture.coverage_scenarios) || fixture.coverage_scenarios.length === 0) {
  errors.push("fixture.coverage_scenarios must contain non-REST contract coverage scenarios");
}

const ids = new Set();
const requiredEndpointIds = new Set([
  "health.ok",
  "ready.ok",
  "version.ok",
  "workflows.list.ok",
  "templates.list.ok",
  "targets.list.ok",
  "adapters.list.ok",
  "apps.list.ok",
  "devices.list.ok",
  "audit.list.ok",
  "webhooks.list.ok",
  "events.list.ok",
  "cache.status.ok",
  "metrics.get.ok",
  "jobs.create.accepted",
  "jobs.create.idempotent-replay",
  "jobs.get.completed",
  "jobs.events.list.ok",
  "jobs.events.stream-sse.ok",
  "jobs.artifacts.list.ok",
  "jobs.artifacts.get.ok",
  "jobs.artifacts.put.accepted",
  "jobs.artifacts.delete.accepted",
  "jobs.list.filtered",
  "jobs.cancel.accepted",
  "jobs.retry.accepted",
  "webhooks.replay.accepted",
  "errors.auth-missing",
  "errors.idempotency-conflict",
  "errors.rate-limited"
]);
const requiredCoverageCategories = new Set([
  "retries",
  "streaming",
  "timeouts",
  "unicode",
  "large_payloads",
  "malformed_responses",
  "webhooks",
  "sidecar"
]);

const coverageCategories = new Set();
for (const [index, scenario] of (fixture.coverage_scenarios ?? []).entries()) {
  const prefix = `coverage_scenarios[${index}]`;
  requireString(scenario.id, `${prefix}.id`);
  requireString(scenario.category, `${prefix}.category`);
  requireString(scenario.title, `${prefix}.title`);
  if (!Array.isArray(scenario.assertions) || scenario.assertions.length === 0) {
    errors.push(`${prefix}.assertions must be a non-empty array`);
  }
  coverageCategories.add(scenario.category);
}

for (const [index, scenario] of (fixture.scenarios ?? []).entries()) {
  const prefix = `scenarios[${index}]`;
  if (!scenario || typeof scenario !== "object") {
    errors.push(`${prefix} must be an object`);
    continue;
  }

  requireString(scenario.id, `${prefix}.id`);
  requireString(scenario.category, `${prefix}.category`);
  requireString(scenario.title, `${prefix}.title`);

  if (ids.has(scenario.id)) {
    errors.push(`${prefix}.id duplicates ${scenario.id}`);
  }
  ids.add(scenario.id);

  if (!scenario.request || typeof scenario.request !== "object") {
    errors.push(`${prefix}.request must be an object`);
  } else {
    requireString(scenario.request.method, `${prefix}.request.method`);
    requireString(scenario.request.path, `${prefix}.request.path`);
    if (!String(scenario.request.path).startsWith("/v1/")) {
      errors.push(`${prefix}.request.path must start with /v1/`);
    }
    if (scenario.request.method === "POST" && scenario.request.path === "/v1/jobs") {
      if (!scenario.request.body || typeof scenario.request.body !== "object") {
        errors.push(`${prefix}.request.body is required for POST /v1/jobs`);
      } else {
        const sdk = scenario.request.body.client?.sdk;
        if (sdk?.name !== "__SDK_NAME__" || sdk?.version !== "__SDK_VERSION__") {
          errors.push(`${prefix}.request.body.client.sdk must use SDK placeholders`);
        }
      }
    }
    if (JSON.stringify(scenario.request).includes("ubag-typescript")) {
      errors.push(`${prefix}.request must not hard-code a language-specific SDK name`);
    }
  }

  if (!scenario.response || typeof scenario.response !== "object") {
    errors.push(`${prefix}.response must be an object`);
  } else if (!Number.isInteger(scenario.response.status)) {
    errors.push(`${prefix}.response.status must be an integer`);
  }

  if (!scenario.expect || typeof scenario.expect !== "object") {
    errors.push(`${prefix}.expect must be an object`);
  } else {
    const expectationKeys = Object.keys(scenario.expect);
    if (!expectationKeys.some((key) => key === "ok" || key === "throws" || key.startsWith("body.") || key.startsWith("error."))) {
      errors.push(`${prefix}.expect must include at least one known expectation key`);
    }
  }
}

for (const requiredId of requiredEndpointIds) {
  if (!ids.has(requiredId)) {
    errors.push(`fixture missing required scenario ${requiredId}`);
  }
}
for (const category of requiredCoverageCategories) {
  if (!coverageCategories.has(category)) {
    errors.push(`fixture missing coverage scenario category ${category}`);
  }
}

if (errors.length > 0) {
  console.error(errors.join("\n"));
  process.exit(1);
}

console.log(`Validated ${fixture.scenarios.length} conformance scenarios from ${fixturePath}`);

function requireString(value, field) {
  if (typeof value !== "string" || value.length === 0) {
    errors.push(`${field} must be a non-empty string`);
  }
}
