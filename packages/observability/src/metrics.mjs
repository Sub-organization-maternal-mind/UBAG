import { isNonEmptyString, listDuplicateValues } from "./safety.mjs";

export const METRIC_NAME_PREFIX = "ubag_";
export const METRIC_NAME_PATTERN = /^ubag_[a-z][a-z0-9_]*$/;
export const METRIC_LABEL_PATTERN = /^[a-z][a-z0-9_]*$/;
export const METRIC_TYPES = Object.freeze(["counter", "gauge", "histogram"]);

export const DISALLOWED_METRIC_LABELS = Object.freeze([
  "id",
  "job_id",
  "trace_id",
  "span_id",
  "request_id",
  "tenant_id",
  "app_id",
  "user_id",
  "session_id",
  "email",
  "ip",
  "url",
  "path",
  "prompt",
  "response"
]);

export const METRIC_LABEL_CARDINALITY_BUDGET = Object.freeze({
  service: "fixed",
  route: "bounded-normalized",
  method: "fixed",
  status_class: "fixed",
  outcome: "fixed",
  check: "bounded",
  queue: "bounded",
  state: "fixed",
  target_family: "bounded",
  command_type: "bounded",
  source: "bounded",
  worker_pool: "bounded",
  adapter_family: "bounded",
  stage: "fixed",
  method_class: "fixed",
  terminal_state: "fixed",
  endpoint_kind: "bounded",
  error_class: "bounded",
  artifact_type: "bounded",
  direction: "fixed"
});

// Closed stage set; mirrors job-event.schema.json data.timings_ms keys.
export const JOB_STAGES = Object.freeze([
  "worker_start",
  "browser_prep",
  "auth_check",
  "attachment_materialize",
  "provider_submit",
  "first_token",
  "provider_stream",
  "extraction"
]);

// Bounded HTTP method class for the route latency histogram (read vs write); additive label.
export const METHOD_CLASSES = Object.freeze(["read", "write", "other"]);

export const OBSERVABILITY_METRICS = Object.freeze([
  metric({
    name: "ubag_gateway_http_requests_total",
    type: "counter",
    owner: "gateway",
    unit: "requests",
    labels: ["service", "route", "method", "status_class", "outcome"],
    description: "Count of gateway HTTP requests by normalized route and status class."
  }),
  metric({
    name: "ubag_gateway_http_request_duration_seconds",
    type: "histogram",
    owner: "gateway",
    unit: "seconds",
    labels: ["service", "route", "method", "status_class", "method_class"],
    description: "Gateway request latency by normalized route, status class, and read/write method class."
  }),
  metric({
    name: "ubag_gateway_http_inflight_requests",
    type: "gauge",
    owner: "gateway",
    unit: "requests",
    labels: ["service", "route", "method"],
    description: "Current gateway requests in flight."
  }),
  metric({
    name: "ubag_gateway_ready",
    type: "gauge",
    owner: "gateway",
    unit: "boolean",
    labels: ["service", "check"],
    description: "Readiness of gateway dependencies, reported as 1 for ready and 0 for not ready."
  }),
  metric({
    name: "ubag_jobs_created_total",
    type: "counter",
    owner: "gateway",
    unit: "jobs",
    labels: ["target_family", "command_type", "source", "outcome"],
    description: "Jobs accepted or rejected by the gateway."
  }),
  metric({
    name: "ubag_jobs_current",
    type: "gauge",
    owner: "gateway",
    unit: "jobs",
    labels: ["target_family", "state"],
    description: "Current jobs grouped by lifecycle state."
  }),
  metric({
    name: "ubag_jobs_duration_seconds",
    type: "histogram",
    owner: "gateway",
    unit: "seconds",
    labels: ["target_family", "command_type", "terminal_state"],
    description: "End-to-end job duration for terminal jobs."
  }),
  metric({
    name: "ubag_queue_depth",
    type: "gauge",
    owner: "queue",
    unit: "jobs",
    labels: ["queue", "state"],
    description: "Queued work depth by queue and stable state."
  }),
  metric({
    name: "ubag_queue_oldest_job_age_seconds",
    type: "gauge",
    owner: "queue",
    unit: "seconds",
    labels: ["queue", "state"],
    description: "Age of the oldest queued job by queue and state."
  }),
  metric({
    name: "ubag_queue_job_wait_duration_seconds",
    type: "histogram",
    owner: "queue",
    unit: "seconds",
    labels: ["queue"],
    description: "Time from a job entering the runnable queue until a worker leases it."
  }),
  metric({
    name: "ubag_worker_jobs_processed_total",
    type: "counter",
    owner: "worker",
    unit: "jobs",
    labels: ["worker_pool", "adapter_family", "outcome"],
    description: "Worker job executions by worker pool, adapter family, and outcome."
  }),
  metric({
    name: "ubag_worker_job_duration_seconds",
    type: "histogram",
    owner: "worker",
    unit: "seconds",
    labels: ["worker_pool", "adapter_family", "outcome"],
    description: "Worker execution duration by worker pool, adapter family, and outcome."
  }),
  metric({
    name: "ubag_worker_result_ingestions_total",
    type: "counter",
    owner: "worker",
    unit: "events",
    labels: ["worker_pool", "adapter_family", "outcome", "error_class"],
    description: "Worker result ingestion outcomes by worker pool, adapter family, outcome, and bounded error class."
  }),
  metric({
    name: "ubag_worker_result_ingestion_duration_seconds",
    type: "histogram",
    owner: "worker",
    unit: "seconds",
    labels: ["worker_pool", "adapter_family", "outcome"],
    description: "Worker result ingestion duration by worker pool, adapter family, and outcome."
  }),
  metric({
    name: "ubag_job_stage_duration_seconds",
    type: "histogram",
    owner: "gateway",
    unit: "seconds",
    labels: ["stage", "adapter_family"],
    description: "Per-stage job duration parsed from worker data.timings_ms; stage is the closed JOB_STAGES set."
  }),
  metric({
    name: "ubag_voice_relay_frame_age_seconds",
    type: "histogram",
    owner: "gateway",
    unit: "seconds",
    labels: ["direction"],
    description: "Gateway-side voice relay frame age; direction is the fixed set mic (RTP read to relay Send returned) and speaker (relay Recv to speaker WriteSample returned). Buckets 5ms..1.28s."
  }),
  metric({
    name: "ubag_voice_inbound_jitter_seconds",
    type: "histogram",
    owner: "gateway",
    unit: "seconds",
    labels: [],
    description: "Client-to-gateway inbound audio RTP jitter sampled from pc.GetStats on a fixed interval, one observation per live media session per sample; no per-session labels. Buckets 1ms..640ms."
  }),
  metric({
    name: "ubag_voice_inbound_packets_received_total",
    type: "counter",
    owner: "gateway",
    unit: "packets",
    labels: [],
    description: "Inbound client audio RTP packets received (pc.GetStats delta); with the lost counter gives the loss ratio."
  }),
  metric({
    name: "ubag_voice_inbound_packets_lost_total",
    type: "counter",
    owner: "gateway",
    unit: "packets",
    labels: [],
    description: "Inbound client audio RTP packets lost (pc.GetStats delta, never negative)."
  }),
  metric({
    name: "ubag_voice_mic_queue_depth",
    type: "gauge",
    owner: "gateway",
    unit: "frames",
    labels: [],
    description: "Mic frames currently queued toward the provider relay, summed across live media sessions at scrape time (each queue is bounded at 64); no per-session labels."
  }),
  metric({
    name: "ubag_adapter_requests_total",
    type: "counter",
    owner: "adapter",
    unit: "requests",
    labels: ["adapter_family", "target_family", "outcome", "error_class"],
    description: "Provider adapter requests by stable target family and outcome."
  }),
  metric({
    name: "ubag_adapter_request_duration_seconds",
    type: "histogram",
    owner: "adapter",
    unit: "seconds",
    labels: ["adapter_family", "target_family", "outcome"],
    description: "Provider adapter request duration by stable target family and outcome."
  }),
  metric({
    name: "ubag_webhook_deliveries_total",
    type: "counter",
    owner: "gateway",
    unit: "deliveries",
    labels: ["endpoint_kind", "outcome", "error_class"],
    description: "Webhook delivery attempts by endpoint kind and outcome."
  }),
  metric({
    name: "ubag_webhook_delivery_duration_seconds",
    type: "histogram",
    owner: "gateway",
    unit: "seconds",
    labels: ["endpoint_kind", "outcome"],
    description: "Webhook delivery latency by endpoint kind and outcome."
  }),
  metric({
    name: "ubag_webhook_outbox_depth",
    type: "gauge",
    owner: "gateway",
    unit: "deliveries",
    labels: ["endpoint_kind", "state"],
    description: "Webhook outbox depth by endpoint kind and delivery state."
  }),
  metric({
    name: "ubag_webhook_outbox_oldest_age_seconds",
    type: "gauge",
    owner: "gateway",
    unit: "seconds",
    labels: ["endpoint_kind", "state"],
    description: "Age of the oldest webhook outbox delivery by endpoint kind and state."
  }),
  metric({
    name: "ubag_idempotency_replays_total",
    type: "counter",
    owner: "gateway",
    unit: "replays",
    labels: ["service", "outcome"],
    description: "Idempotency replay decisions by service and outcome."
  }),
  metric({
    name: "ubag_sse_connections_current",
    type: "gauge",
    owner: "gateway",
    unit: "connections",
    labels: ["service"],
    description: "Current SSE connections served by the gateway."
  }),
  metric({
    name: "ubag_event_wakeups_total",
    type: "counter",
    owner: "gateway",
    unit: "wakeups",
    labels: ["source"],
    description: "SSE waiter wakeups by source (local hub wake, notify reserved, fallback poll)."
  }),
  metric({
    name: "ubag_sse_event_frame_lag_seconds",
    type: "histogram",
    owner: "gateway",
    unit: "seconds",
    labels: ["service"],
    description: "Lag between a job event being stored and its SSE frame being written (live frames only)."
  }),
  metric({
    name: "ubag_artifact_captures_total",
    type: "counter",
    owner: "worker",
    unit: "artifacts",
    labels: ["artifact_type", "outcome"],
    description: "Worker artifact capture attempts by artifact type and outcome."
  })
]);

export function validateMetricRegistry(metrics = OBSERVABILITY_METRICS) {
  const errors = [];

  if (!Array.isArray(metrics) || metrics.length === 0) {
    return ["metric registry must contain at least one metric"];
  }

  for (const duplicate of listDuplicateValues(metrics.map((item) => item?.name))) {
    errors.push(`metric name ${duplicate} is duplicated`);
  }

  for (const [index, candidate] of metrics.entries()) {
    const path = `metrics[${index}]`;
    if (!candidate || typeof candidate !== "object" || Array.isArray(candidate)) {
      errors.push(`${path} must be an object`);
      continue;
    }

    if (!METRIC_NAME_PATTERN.test(candidate.name ?? "")) {
      errors.push(`${path}.name must match ${METRIC_NAME_PATTERN}`);
    }
    if (!String(candidate.name ?? "").startsWith(METRIC_NAME_PREFIX)) {
      errors.push(`${path}.name must start with ${METRIC_NAME_PREFIX}`);
    }
    if (!METRIC_TYPES.includes(candidate.type)) {
      errors.push(`${path}.type must be one of ${METRIC_TYPES.join(", ")}`);
    }
    if (candidate.type === "counter" && !String(candidate.name).endsWith("_total")) {
      errors.push(`${path}.name must end with _total for counter metrics`);
    }
    if (candidate.type !== "counter" && String(candidate.name).endsWith("_total")) {
      errors.push(`${path}.name must not end with _total unless it is a counter`);
    }
    if (candidate.type === "histogram" && !String(candidate.name).endsWith("_seconds")) {
      errors.push(`${path}.name must end with _seconds for duration histograms`);
    }
    if (!isNonEmptyString(candidate.owner)) {
      errors.push(`${path}.owner must be a non-empty string`);
    }
    if (!isNonEmptyString(candidate.unit)) {
      errors.push(`${path}.unit must be a non-empty string`);
    }
    if (!isNonEmptyString(candidate.description)) {
      errors.push(`${path}.description must be a non-empty string`);
    }

    if (!Array.isArray(candidate.labels)) {
      errors.push(`${path}.labels must be an array`);
      continue;
    }

    if (candidate.labels.length > 6) {
      errors.push(`${path}.labels must stay within the six-label cardinality budget`);
    }

    for (const duplicate of listDuplicateValues(candidate.labels)) {
      errors.push(`${path}.labels contains duplicate label ${duplicate}`);
    }

    for (const label of candidate.labels) {
      if (!METRIC_LABEL_PATTERN.test(label)) {
        errors.push(`${path}.labels contains invalid label ${label}`);
      }
      if (DISALLOWED_METRIC_LABELS.includes(label)) {
        errors.push(`${path}.labels contains high-cardinality or sensitive label ${label}`);
      }
      if (!(label in METRIC_LABEL_CARDINALITY_BUDGET)) {
        errors.push(`${path}.labels contains label ${label} without a declared cardinality budget`);
      }
    }
  }

  return errors;
}

export function assertValidMetricRegistry(metrics = OBSERVABILITY_METRICS) {
  const errors = validateMetricRegistry(metrics);
  if (errors.length > 0) {
    throw new Error(`Invalid observability metric registry:\n${errors.join("\n")}`);
  }
}

export function getMetricByName(name, metrics = OBSERVABILITY_METRICS) {
  return metrics.find((item) => item.name === name);
}

function metric(definition) {
  return Object.freeze({
    ...definition,
    labels: Object.freeze([...definition.labels])
  });
}
