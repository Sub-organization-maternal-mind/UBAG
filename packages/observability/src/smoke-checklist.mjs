import { HEALTH_PROBES } from "./health.mjs";
import { OBSERVABILITY_EVENT_NAMES } from "./events.mjs";
import { OBSERVABILITY_METRICS } from "./metrics.mjs";
import { isNonEmptyString, listDuplicateValues } from "./safety.mjs";

export const SMOKE_CHECKLIST_VERSION = "2026-05-23";

export const SMOKE_CHECKLIST = Object.freeze([
  smokeItem({
    id: "smoke.gateway.health",
    category: "gateway",
    mode: "automated",
    severity: "blocker",
    title: "Gateway health returns an OK JSON response with a trace id.",
    evidence: "Health probe result for gateway.health.",
    automatedBy: "health-probe:gateway.health"
  }),
  smokeItem({
    id: "smoke.gateway.ready",
    category: "gateway",
    mode: "automated",
    severity: "blocker",
    title: "Gateway readiness confirms jobs, idempotency, queue, executor, templates, artifacts, and webhooks are ready.",
    evidence: "Health probe result for gateway.ready.",
    automatedBy: "health-probe:gateway.ready"
  }),
  smokeItem({
    id: "smoke.queue.gateway-dispatch",
    category: "queue",
    mode: "automated",
    severity: "blocker",
    title: "Gateway create-job dispatches accepted work exactly once to the configured executor queue.",
    evidence: "Gateway tests for executor enqueue, idempotent replay, payload policy rejection, readiness, and queue metrics.",
    automatedBy: "command:cmd /c pnpm test:gateway"
  }),
  smokeItem({
    id: "smoke.gateway.version",
    category: "gateway",
    mode: "automated",
    severity: "must",
    title: "Gateway version exposes build, commit, and API version evidence.",
    evidence: "Health probe result for gateway.version.",
    automatedBy: "health-probe:gateway.version"
  }),
  smokeItem({
    id: "smoke.ingress.gateway-health",
    category: "ingress",
    mode: "automated",
    severity: "blocker",
    title: "Ingress routes gateway health without losing the response contract.",
    evidence: "Health probe result for ingress.gateway-health.",
    automatedBy: "health-probe:ingress.gateway-health"
  }),
  smokeItem({
    id: "smoke.worker.mock-jsonl-run",
    category: "worker",
    mode: "automated",
    severity: "blocker",
    title: "Mock worker smoke command completes and emits JSONL lifecycle events.",
    evidence: "Small profile smoke output or local worker test log.",
    automatedBy: "health-probe:small-profile.smoke"
  }),
  smokeItem({
    id: "smoke.worker.file-spool-ingestion",
    category: "worker",
    mode: "automated",
    severity: "blocker",
    title: "File-spool worker consumer leases gateway work, runs the Python worker, and ingests terminal results.",
    evidence: "Gateway worker consumer tests cover lease, result ingestion, failure handling, and cancellation.",
    automatedBy: "command:cmd /c pnpm test:gateway"
  }),
  smokeItem({
    id: "smoke.observability.metric-registry",
    category: "observability",
    mode: "automated",
    severity: "blocker",
    title: "Metric names are stable, prefixed, typed, and free of high-cardinality labels.",
    evidence: "pnpm test:observability metric registry test output.",
    automatedBy: "metric-registry"
  }),
  smokeItem({
    id: "smoke.observability.log-shape",
    category: "observability",
    mode: "automated",
    severity: "blocker",
    title: "Structured logs include severity, service, environment, trace id, and privacy-safe metadata.",
    evidence: "pnpm test:observability log shape test output.",
    automatedBy: "log-validator"
  }),
  smokeItem({
    id: "smoke.observability.event-shape",
    category: "observability",
    mode: "automated",
    severity: "blocker",
    title: "Events use domain.resource.action.outcome names and privacy-safe payloads.",
    evidence: "pnpm test:observability event shape test output.",
    automatedBy: "event-validator"
  }),
  smokeItem({
    id: "smoke.ops.prometheus-ready",
    category: "ops",
    mode: "automated",
    severity: "must",
    title: "Prometheus readiness endpoint is reachable when observability profile is enabled.",
    evidence: "Health probe result for prometheus.ready.",
    automatedBy: "health-probe:prometheus.ready"
  }),
  smokeItem({
    id: "smoke.ops.grafana-health",
    category: "ops",
    mode: "automated",
    severity: "must",
    title: "Grafana health endpoint is reachable when observability profile is enabled.",
    evidence: "Health probe result for grafana.health.",
    automatedBy: "health-probe:grafana.health"
  }),
  smokeItem({
    id: "smoke.release.evidence-recorded",
    category: "release",
    mode: "manual",
    severity: "must",
    title: "Release notes record command, environment, result, timestamp, and evidence location.",
    evidence: "Release note, CI artifact, screenshot, or operator run log.",
    automatedBy: null
  })
]);

export function validateSmokeChecklist(checklist = SMOKE_CHECKLIST) {
  const errors = [];

  if (!Array.isArray(checklist) || checklist.length === 0) {
    return ["smoke checklist must contain at least one item"];
  }

  const healthProbeIds = new Set(HEALTH_PROBES.map((probe) => probe.id));
  const metricNames = new Set(OBSERVABILITY_METRICS.map((metric) => metric.name));
  const eventNames = new Set(OBSERVABILITY_EVENT_NAMES);

  for (const duplicate of listDuplicateValues(checklist.map((item) => item?.id))) {
    errors.push(`smoke checklist id ${duplicate} is duplicated`);
  }

  for (const [index, item] of checklist.entries()) {
    const path = `smokeChecklist[${index}]`;
    if (!item || typeof item !== "object" || Array.isArray(item)) {
      errors.push(`${path} must be an object`);
      continue;
    }

    if (!/^smoke\.[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)+$/.test(item.id ?? "")) {
      errors.push(`${path}.id must be a stable smoke.* identifier`);
    }
    if (!["gateway", "ingress", "queue", "worker", "observability", "ops", "release"].includes(item.category)) {
      errors.push(`${path}.category is not recognized`);
    }
    if (!["automated", "manual"].includes(item.mode)) {
      errors.push(`${path}.mode must be automated or manual`);
    }
    if (!["blocker", "must", "should"].includes(item.severity)) {
      errors.push(`${path}.severity must be blocker, must, or should`);
    }
    if (!isNonEmptyString(item.title)) {
      errors.push(`${path}.title must be a non-empty string`);
    }
    if (!isNonEmptyString(item.evidence)) {
      errors.push(`${path}.evidence must describe required proof`);
    }
    if (item.mode === "automated" && !isNonEmptyString(item.automatedBy)) {
      errors.push(`${path}.automatedBy is required for automated checks`);
    }
    if (item.mode === "manual" && item.automatedBy !== null) {
      errors.push(`${path}.automatedBy must be null for manual checks`);
    }

    if (typeof item.automatedBy === "string" && item.automatedBy.startsWith("health-probe:")) {
      const probeId = item.automatedBy.slice("health-probe:".length);
      if (!healthProbeIds.has(probeId)) {
        errors.push(`${path}.automatedBy references missing health probe ${probeId}`);
      }
    }
    if (item.automatedBy === "metric-registry" && metricNames.size === 0) {
      errors.push(`${path}.automatedBy references an empty metric registry`);
    }
    if (item.automatedBy === "event-validator" && eventNames.size === 0) {
      errors.push(`${path}.automatedBy references an empty event registry`);
    }
  }

  return errors;
}

export function renderSmokeChecklistMarkdown(checklist = SMOKE_CHECKLIST) {
  const rows = [
    `# UBAG Smoke Checklist (${SMOKE_CHECKLIST_VERSION})`,
    "",
    "| ID | Severity | Mode | Evidence |",
    "| --- | --- | --- | --- |"
  ];

  for (const item of checklist) {
    rows.push(`| ${item.id} | ${item.severity} | ${item.mode} | ${item.evidence} |`);
  }

  return `${rows.join("\n")}\n`;
}

function smokeItem(definition) {
  return Object.freeze({ ...definition });
}
