import { getByPath, isNonEmptyString, isPlainObject, listDuplicateValues } from "./safety.mjs";

export const DEFAULT_HEALTH_BASE_URLS = Object.freeze({
  gateway: "http://127.0.0.1:8080",
  ingress: "http://127.0.0.1:8081",
  prometheus: "http://127.0.0.1:9090",
  grafana: "http://127.0.0.1:3000",
  "nats-monitor": "http://127.0.0.1:8222",
  "minio-api": "http://127.0.0.1:9000"
});

export const HEALTH_PROBES = Object.freeze([
  httpProbe({
    id: "gateway.health",
    tier: "critical",
    service: "ubag-gateway",
    baseUrlKey: "gateway",
    path: "/v1/health",
    expectedStatus: [200],
    requiredJson: { service: "ubag-gateway", status: "ok" },
    requiredJsonFields: ["trace_id", "checked_at", "checks.process"],
    timeoutMs: 2000,
    description: "Gateway process health endpoint returns an operator-correlatable OK response."
  }),
  httpProbe({
    id: "gateway.ready",
    tier: "critical",
    service: "ubag-gateway",
    baseUrlKey: "gateway",
    path: "/v1/ready",
    expectedStatus: [200],
    requiredJson: { service: "ubag-gateway", status: "ready", ready: true },
    requiredJsonFields: ["trace_id", "checked_at", "checks.jobs", "checks.idempotency", "checks.queue", "checks.executor", "checks.artifacts", "checks.templates", "checks.webhooks"],
    timeoutMs: 2000,
    description: "Gateway readiness confirms job, idempotency, queue, executor, artifact, template, and webhook dependencies are usable."
  }),
  httpProbe({
    id: "gateway.version",
    tier: "important",
    service: "ubag-gateway",
    baseUrlKey: "gateway",
    path: "/v1/version",
    expectedStatus: [200],
    requiredJson: { service: "ubag-gateway" },
    requiredJsonFields: ["trace_id", "version", "api_versions", "default_api_version", "commit"],
    timeoutMs: 2000,
    description: "Gateway version endpoint exposes build and API version evidence."
  }),
  httpProbe({
    id: "ingress.gateway-health",
    tier: "critical",
    service: "caddy",
    baseUrlKey: "ingress",
    path: "/v1/health",
    expectedStatus: [200],
    requiredJson: { service: "ubag-gateway", status: "ok" },
    requiredJsonFields: ["trace_id", "checked_at"],
    timeoutMs: 2500,
    description: "Ingress can reach the gateway health endpoint."
  }),
  httpProbe({
    id: "prometheus.ready",
    tier: "supporting",
    service: "prometheus",
    baseUrlKey: "prometheus",
    path: "/-/ready",
    expectedStatus: [200],
    timeoutMs: 2000,
    description: "Prometheus is ready to serve and scrape configured targets."
  }),
  httpProbe({
    id: "grafana.health",
    tier: "supporting",
    service: "grafana",
    baseUrlKey: "grafana",
    path: "/api/health",
    expectedStatus: [200],
    requiredJsonFields: ["database", "version"],
    timeoutMs: 2000,
    description: "Grafana health endpoint confirms dashboard service availability."
  }),
  httpProbe({
    id: "nats.ready",
    tier: "supporting",
    service: "nats",
    baseUrlKey: "nats-monitor",
    path: "/healthz",
    expectedStatus: [200],
    timeoutMs: 2000,
    description: "NATS monitoring endpoint confirms the JetStream service is reachable."
  }),
  httpProbe({
    id: "minio.ready",
    tier: "supporting",
    service: "minio",
    baseUrlKey: "minio-api",
    path: "/minio/health/live",
    expectedStatus: [200],
    timeoutMs: 2000,
    description: "MinIO live endpoint confirms object storage is reachable."
  }),
  Object.freeze({
    id: "small-profile.smoke",
    kind: "command",
    tier: "critical",
    service: "small-profile",
    command: "node tools/run-small-smoke-probe.mjs",
    description: "Compose small profile gateway plus mock-worker smoke path completes."
  })
]);

export function validateHealthProbeRegistry(probes = HEALTH_PROBES) {
  const errors = [];
  if (!Array.isArray(probes) || probes.length === 0) {
    return ["health probe registry must contain at least one probe"];
  }

  for (const duplicate of listDuplicateValues(probes.map((probe) => probe?.id))) {
    errors.push(`health probe id ${duplicate} is duplicated`);
  }

  for (const [index, probe] of probes.entries()) {
    const path = `healthProbes[${index}]`;
    if (!isPlainObject(probe)) {
      errors.push(`${path} must be an object`);
      continue;
    }

    if (!/^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)+$/.test(probe.id ?? "")) {
      errors.push(`${path}.id must be a stable dotted lower-case identifier`);
    }
    if (!["http", "command"].includes(probe.kind)) {
      errors.push(`${path}.kind must be http or command`);
    }
    if (!["critical", "important", "supporting"].includes(probe.tier)) {
      errors.push(`${path}.tier must be critical, important, or supporting`);
    }
    if (!isNonEmptyString(probe.service)) {
      errors.push(`${path}.service must be a non-empty string`);
    }
    if (!isNonEmptyString(probe.description)) {
      errors.push(`${path}.description must be a non-empty string`);
    }

    if (probe.kind === "http") {
      if (!isNonEmptyString(probe.baseUrlKey)) errors.push(`${path}.baseUrlKey is required`);
      if (!String(probe.path ?? "").startsWith("/")) errors.push(`${path}.path must start with /`);
      if (!Array.isArray(probe.expectedStatus) || probe.expectedStatus.length === 0) {
        errors.push(`${path}.expectedStatus must contain at least one status code`);
      }
      if (!Number.isInteger(probe.timeoutMs) || probe.timeoutMs < 100 || probe.timeoutMs > 30000) {
        errors.push(`${path}.timeoutMs must be between 100 and 30000`);
      }
    }

    if (probe.kind === "command" && !isNonEmptyString(probe.command)) {
      errors.push(`${path}.command is required for command probes`);
    }
  }

  return errors;
}

export function buildHttpProbeRequest(probe, baseUrls = DEFAULT_HEALTH_BASE_URLS) {
  if (probe.kind !== "http") {
    throw new Error(`Probe ${probe.id} is not an HTTP probe`);
  }

  const baseUrl = baseUrls[probe.baseUrlKey];
  if (!baseUrl) {
    throw new Error(`Missing base URL for ${probe.baseUrlKey}`);
  }

  return Object.freeze({
    id: probe.id,
    method: probe.method,
    url: new URL(probe.path, ensureTrailingSlash(baseUrl)).toString(),
    timeoutMs: probe.timeoutMs
  });
}

export function validateProbeResponse(probe, response) {
  const errors = [];
  if (probe.kind !== "http") {
    return errors;
  }

  if (!probe.expectedStatus.includes(response.status)) {
    errors.push(`${probe.id} returned HTTP ${response.status}, expected ${probe.expectedStatus.join(" or ")}`);
  }
  if (typeof response.durationMs !== "number" || response.durationMs < 0) {
    errors.push(`${probe.id} durationMs must be a non-negative number`);
  } else if (response.durationMs > probe.timeoutMs) {
    errors.push(`${probe.id} exceeded timeout budget ${probe.timeoutMs}ms`);
  }

  if (probe.requiredJson || probe.requiredJsonFields) {
    if (!isPlainObject(response.body)) {
      errors.push(`${probe.id} response body must be JSON object`);
      return errors;
    }

    for (const [path, expected] of Object.entries(probe.requiredJson ?? {})) {
      const actual = getByPath(response.body, path);
      if (actual !== expected) {
        errors.push(`${probe.id} body.${path} must be ${JSON.stringify(expected)}`);
      }
    }

    for (const path of probe.requiredJsonFields ?? []) {
      const actual = getByPath(response.body, path);
      if (actual === undefined || actual === null || actual === "") {
        errors.push(`${probe.id} body.${path} is required`);
      }
    }
  }

  return errors;
}

export function evaluateHealthProbeResults(results) {
  const failures = [];
  for (const result of results) {
    if (!result.ok) {
      failures.push(result);
    }
  }

  const criticalFailures = failures.filter((result) => result.tier === "critical");
  return Object.freeze({
    ok: failures.length === 0,
    status: criticalFailures.length > 0 ? "critical" : failures.length > 0 ? "degraded" : "ok",
    total: results.length,
    passed: results.length - failures.length,
    failed: failures.length,
    failures: Object.freeze(failures)
  });
}

export async function runHttpHealthProbes(options = {}) {
  const probes = options.probes ?? HEALTH_PROBES.filter((probe) => probe.kind === "http");
  const baseUrls = options.baseUrls ?? DEFAULT_HEALTH_BASE_URLS;
  const fetchFn = options.fetchFn ?? globalThis.fetch;
  if (typeof fetchFn !== "function") {
    throw new Error("runHttpHealthProbes requires a fetch implementation");
  }

  const results = [];
  for (const probe of probes) {
    const request = buildHttpProbeRequest(probe, baseUrls);
    const startedAt = Date.now();
    try {
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), request.timeoutMs);
      let response;
      try {
        response = await fetchFn(request.url, {
          method: request.method,
          signal: controller.signal,
          headers: { accept: "application/json, text/plain;q=0.9" }
        });
      } finally {
        clearTimeout(timer);
      }

      const body = await readProbeBody(response);
      const durationMs = Date.now() - startedAt;
      const errors = validateProbeResponse(probe, { status: response.status, body, durationMs });
      results.push(freezeResult(probe, {
        ok: errors.length === 0,
        status: response.status,
        durationMs,
        errors
      }));
    } catch (error) {
      results.push(freezeResult(probe, {
        ok: false,
        status: 0,
        durationMs: Date.now() - startedAt,
        errors: [error instanceof Error ? error.message : String(error)]
      }));
    }
  }

  return results;
}

function httpProbe(definition) {
  return Object.freeze({
    kind: "http",
    method: "GET",
    ...definition,
    expectedStatus: Object.freeze([...definition.expectedStatus]),
    requiredJson: Object.freeze({ ...(definition.requiredJson ?? {}) }),
    requiredJsonFields: Object.freeze([...(definition.requiredJsonFields ?? [])])
  });
}

function freezeResult(probe, result) {
  return Object.freeze({
    id: probe.id,
    tier: probe.tier,
    service: probe.service,
    kind: probe.kind,
    ...result,
    errors: Object.freeze([...(result.errors ?? [])])
  });
}

function ensureTrailingSlash(value) {
  return value.endsWith("/") ? value : `${value}/`;
}

async function readProbeBody(response) {
  const contentType = response.headers?.get?.("content-type") ?? "";
  const text = await response.text();
  if (contentType.includes("application/json") || text.trim().startsWith("{")) {
    try {
      return JSON.parse(text);
    } catch {
      return text;
    }
  }
  return text;
}
