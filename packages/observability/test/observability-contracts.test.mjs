import assert from "node:assert/strict";
import { test } from "node:test";
import {
  HEALTH_PROBES,
  OBSERVABILITY_EVENT_NAMES,
  OBSERVABILITY_METRICS,
  SMOKE_CHECKLIST,
  buildHttpProbeRequest,
  createLogRecord,
  createObservabilityEvent,
  evaluateHealthProbeResults,
  getMetricByName,
  renderSmokeChecklistMarkdown,
  runHttpHealthProbes,
  validateEventRegistry,
  validateHealthProbeRegistry,
  validateLogRecord,
  validateMetricRegistry,
  validateObservabilityEvent,
  validateProbeResponse,
  validateSmokeChecklist
} from "../src/index.mjs";

test("metric registry uses stable UBAG names and bounded labels", () => {
  assert.deepEqual(validateMetricRegistry(), []);
  assert.ok(OBSERVABILITY_METRICS.length >= 12);

  const requestCounter = getMetricByName("ubag_gateway_http_requests_total");
  assert.equal(requestCounter.type, "counter");
  assert.deepEqual(requestCounter.labels, ["service", "route", "method", "status_class", "outcome"]);
  assert.equal(getMetricByName("ubag_queue_depth").type, "gauge");
  assert.equal(getMetricByName("ubag_queue_oldest_job_age_seconds").type, "gauge");
  assert.equal(getMetricByName("ubag_worker_jobs_processed_total").type, "counter");
  assert.equal(getMetricByName("ubag_worker_job_duration_seconds").type, "histogram");
  assert.equal(getMetricByName("ubag_worker_result_ingestions_total").type, "counter");
  assert.equal(getMetricByName("ubag_worker_result_ingestion_duration_seconds").type, "histogram");

  const failures = validateMetricRegistry([
    {
      name: "ubag_bad_counter",
      type: "counter",
      owner: "test",
      unit: "count",
      labels: ["job_id"],
      description: "bad fixture"
    }
  ]);
  assert.match(failures.join("\n"), /_total/);
  assert.match(failures.join("\n"), /high-cardinality/);
});

test("event registry and event payload validation enforce stable shape", () => {
  assert.deepEqual(validateEventRegistry(), []);
  assert.ok(OBSERVABILITY_EVENT_NAMES.includes("operations.probe.run.success"));
  assert.ok(OBSERVABILITY_EVENT_NAMES.includes("queue.job.enqueue.success"));
  assert.ok(OBSERVABILITY_EVENT_NAMES.includes("queue.job.lease.success"));
  assert.ok(OBSERVABILITY_EVENT_NAMES.includes("worker.job.run.success"));
  assert.ok(OBSERVABILITY_EVENT_NAMES.includes("worker.result.ingest.success"));
  assert.ok(OBSERVABILITY_EVENT_NAMES.includes("queue.job.dead_letter.terminal"));

  const event = createObservabilityEvent({
    timestamp: "2026-05-23T00:00:00.000Z",
    name: "gateway.request.handle.success",
    environment: "test",
    service: "ubag-gateway",
    source: "gateway-http",
    trace_id: "trace_fixture",
    resource: { type: "route", id: "/v1/health" },
    metadata: { route: "/v1/health", status_class: "2xx" }
  });

  assert.equal(event.result, "success");
  assert.deepEqual(validateObservabilityEvent(event), []);

  const invalid = validateObservabilityEvent({
    ...event,
    name: "gateway.request.handle.success",
    result: "failure",
    metadata: { authorization: "Bearer fixture" }
  });
  assert.match(invalid.join("\n"), /result must match/);
  assert.match(invalid.join("\n"), /authorization/);
});

test("log shape validation requires correlation and blocks sensitive payload fields", () => {
  const log = createLogRecord({
    timestamp: "2026-05-23T00:00:00.000Z",
    level: "info",
    environment: "test",
    service: "ubag-gateway",
    message: "handled request",
    trace_id: "trace_fixture",
    event_name: "gateway.request.handle.success",
    result: "success",
    route: "/v1/health",
    status_class: "2xx",
    metadata: { duration_ms: 12 }
  });

  assert.deepEqual(validateLogRecord(log), []);

  const failures = validateLogRecord({
    ...log,
    trace_id: "",
    metadata: { device_token: "must-not-emit" }
  });
  assert.match(failures.join("\n"), /trace_id/);
  assert.match(failures.join("\n"), /device_token/);
});

test("health probe registry validates HTTP probes and response contracts", () => {
  assert.deepEqual(validateHealthProbeRegistry(), []);

  const gatewayHealth = HEALTH_PROBES.find((probe) => probe.id === "gateway.health");
  const request = buildHttpProbeRequest(gatewayHealth, { gateway: "http://localhost:8080" });
  assert.deepEqual(request, {
    id: "gateway.health",
    method: "GET",
    url: "http://localhost:8080/v1/health",
    timeoutMs: 2000
  });

  assert.deepEqual(
    validateProbeResponse(gatewayHealth, {
      status: 200,
      durationMs: 8,
      body: {
        service: "ubag-gateway",
        status: "ok",
        checked_at: "2026-05-23T00:00:00.000Z",
        checks: { process: "ok" },
        trace_id: "trace_fixture"
      }
    }),
    []
  );

  const failures = validateProbeResponse(gatewayHealth, {
    status: 500,
    durationMs: 9,
    body: { service: "ubag-gateway" }
  });
  assert.match(failures.join("\n"), /HTTP 500/);
  assert.match(failures.join("\n"), /trace_id/);
});

test("health probe runner supports fake fetch and summarizes degraded state", async () => {
  const probes = HEALTH_PROBES.filter((probe) => ["gateway.health", "prometheus.ready"].includes(probe.id));
  const results = await runHttpHealthProbes({
    probes,
    baseUrls: {
      gateway: "http://gateway.local",
      prometheus: "http://prometheus.local"
    },
    fetchFn: async (url) => {
      if (url.includes("prometheus")) {
        return response(503, "not ready", "text/plain");
      }
      return response(
        200,
        JSON.stringify({
          service: "ubag-gateway",
          status: "ok",
          checked_at: "2026-05-23T00:00:00.000Z",
          checks: { process: "ok" },
          trace_id: "trace_fixture"
        }),
        "application/json"
      );
    }
  });

  const report = evaluateHealthProbeResults(results);
  assert.equal(report.ok, false);
  assert.equal(report.status, "degraded");
  assert.equal(report.passed, 1);
  assert.equal(report.failed, 1);
});

test("smoke checklist covers observability contracts and references real probes", () => {
  assert.deepEqual(validateSmokeChecklist(), []);
  assert.ok(SMOKE_CHECKLIST.some((item) => item.automatedBy === "metric-registry"));
  assert.ok(SMOKE_CHECKLIST.some((item) => item.automatedBy === "event-validator"));
  assert.ok(SMOKE_CHECKLIST.some((item) => item.automatedBy === "log-validator"));
  assert.ok(SMOKE_CHECKLIST.some((item) => item.id === "smoke.queue.gateway-dispatch"));
  assert.ok(SMOKE_CHECKLIST.some((item) => item.id === "smoke.worker.file-spool-ingestion"));

  const markdown = renderSmokeChecklistMarkdown();
  assert.match(markdown, /smoke.gateway.health/);
  assert.match(markdown, /smoke.queue.gateway-dispatch/);
  assert.match(markdown, /smoke.worker.file-spool-ingestion/);
  assert.match(markdown, /smoke.observability.metric-registry/);
});

function response(status, body, contentType) {
  return {
    status,
    headers: {
      get(name) {
        return name.toLowerCase() === "content-type" ? contentType : undefined;
      }
    },
    async text() {
      return body;
    }
  };
}
