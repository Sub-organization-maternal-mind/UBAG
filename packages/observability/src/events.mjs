import {
  ENVIRONMENT_NAMES,
  RESULT_VALUES,
  SERVICE_NAME_PATTERN,
  STABLE_ID_PATTERN,
  isNonEmptyString,
  isPlainObject,
  listDuplicateValues,
  validateIsoTimestamp,
  validateNoSensitiveFields
} from "./safety.mjs";

export const EVENT_OUTCOMES = Object.freeze([
  "requested",
  "accepted",
  "success",
  "failure",
  "blocked",
  "skipped",
  "partial",
  "retryable",
  "terminal",
  "degraded",
  "recovered"
]);

export const EVENT_NAME_PATTERN = new RegExp(
  `^[a-z][a-z0-9_]*\\.[a-z][a-z0-9_]*\\.[a-z][a-z0-9_]*\\.(${EVENT_OUTCOMES.join("|")})$`
);

export const OBSERVABILITY_EVENT_NAMES = Object.freeze([
  "gateway.request.handle.success",
  "gateway.request.handle.failure",
  "gateway.readiness.check.success",
  "gateway.readiness.check.failure",
  "jobs.job.create.requested",
  "jobs.job.create.success",
  "jobs.job.create.failure",
  "jobs.job.cancel.requested",
  "jobs.job.cancel.success",
  "jobs.job.cancel.failure",
  "jobs.job.retry.requested",
  "jobs.job.retry.success",
  "jobs.job.retry.failure",
  "queue.job.enqueue.success",
  "queue.job.enqueue.failure",
  "queue.job.lease.success",
  "queue.job.lease.failure",
  "queue.job.dead_letter.terminal",
  "worker.job.run.success",
  "worker.job.run.failure",
  "worker.result.ingest.success",
  "worker.result.ingest.failure",
  "adapter.request.submit.success",
  "adapter.request.submit.failure",
  "webhook.outbox.enqueue.success",
  "webhook.outbox.enqueue.failure",
  "webhook.delivery.sign.success",
  "webhook.delivery.sign.failure",
  "webhook.delivery.dispatch.success",
  "webhook.delivery.dispatch.failure",
  "webhook.delivery.dispatch.retryable",
  "webhook.delivery.dead_letter.terminal",
  "webhook.delivery.replay.requested",
  "webhook.delivery.replay.success",
  "webhook.delivery.replay.failure",
  "webhook.worker.poll.success",
  "webhook.worker.poll.failure",
  "operations.probe.run.success",
  "operations.probe.run.failure",
  "operations.smoke.run.success",
  "operations.smoke.run.failure",
  "system.release.evidence.accepted",
  "system.release.evidence.failure"
]);

export function validateEventName(name) {
  const errors = [];
  if (!EVENT_NAME_PATTERN.test(name ?? "")) {
    errors.push(`event name ${String(name)} must match domain.resource.action.outcome`);
  }
  return errors;
}

export function validateEventRegistry(eventNames = OBSERVABILITY_EVENT_NAMES) {
  const errors = [];
  if (!Array.isArray(eventNames) || eventNames.length === 0) {
    return ["event registry must contain at least one event name"];
  }

  for (const duplicate of listDuplicateValues(eventNames)) {
    errors.push(`event name ${duplicate} is duplicated`);
  }

  for (const [index, eventName] of eventNames.entries()) {
    const path = `eventNames[${index}]`;
    if (!isNonEmptyString(eventName)) {
      errors.push(`${path} must be a non-empty string`);
      continue;
    }
    errors.push(...validateEventName(eventName).map((error) => `${path}: ${error}`));
  }

  return errors;
}

export function validateObservabilityEvent(event, options = {}) {
  const knownNames = options.knownNames ?? OBSERVABILITY_EVENT_NAMES;
  const errors = [];

  if (!isPlainObject(event)) {
    return ["event must be an object"];
  }

  errors.push(...validateEventName(event.name));
  if (knownNames && !knownNames.includes(event.name)) {
    errors.push(`event.name ${String(event.name)} is not registered`);
  }

  errors.push(...validateIsoTimestamp(event.timestamp, "event.timestamp"));

  if (!ENVIRONMENT_NAMES.includes(event.environment)) {
    errors.push(`event.environment must be one of ${ENVIRONMENT_NAMES.join(", ")}`);
  }
  if (!SERVICE_NAME_PATTERN.test(event.service ?? "")) {
    errors.push("event.service must be a stable lower-case service name");
  }
  if (!isNonEmptyString(event.source)) {
    errors.push("event.source must identify the emitter");
  }
  if (!RESULT_VALUES.includes(event.result)) {
    errors.push(`event.result must be one of ${RESULT_VALUES.join(", ")}`);
  }

  const nameOutcome = typeof event.name === "string" ? event.name.split(".").at(-1) : undefined;
  if (nameOutcome && EVENT_OUTCOMES.includes(nameOutcome) && event.result !== nameOutcome) {
    errors.push("event.result must match the outcome segment in event.name");
  }

  if (!isNonEmptyString(event.trace_id)) {
    errors.push("event.trace_id must be present for cross-signal correlation");
  } else if (String(event.trace_id).length > 128) {
    errors.push("event.trace_id must be 128 characters or fewer");
  }

  if (event.resource !== undefined) {
    if (!isPlainObject(event.resource)) {
      errors.push("event.resource must be an object when present");
    } else {
      if (!STABLE_ID_PATTERN.test(event.resource.type ?? "")) {
        errors.push("event.resource.type must be a stable lower-case identifier");
      }
      if (!isNonEmptyString(event.resource.id)) {
        errors.push("event.resource.id must be a non-empty string");
      }
    }
  }

  if (event.metadata !== undefined && !isPlainObject(event.metadata)) {
    errors.push("event.metadata must be an object when present");
  }

  errors.push(...validateNoSensitiveFields(event, "event"));

  return errors;
}

export function createObservabilityEvent(input) {
  const event = {
    timestamp: input.timestamp ?? new Date().toISOString(),
    name: input.name,
    environment: input.environment,
    service: input.service,
    source: input.source,
    result: input.result ?? eventOutcome(input.name),
    trace_id: input.trace_id,
    resource: input.resource,
    metadata: input.metadata ?? {}
  };

  const errors = validateObservabilityEvent(event, input.options);
  if (errors.length > 0) {
    throw new Error(`Invalid observability event:\n${errors.join("\n")}`);
  }

  return Object.freeze(event);
}

export function eventOutcome(name) {
  if (typeof name !== "string") return undefined;
  const outcome = name.split(".").at(-1);
  return EVENT_OUTCOMES.includes(outcome) ? outcome : undefined;
}
