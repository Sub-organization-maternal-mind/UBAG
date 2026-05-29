import {
  ENVIRONMENT_NAMES,
  RESULT_VALUES,
  SERVICE_NAME_PATTERN,
  isNonEmptyString,
  isPlainObject,
  validateIsoTimestamp,
  validateNoSensitiveFields
} from "./safety.mjs";
import { OBSERVABILITY_EVENT_NAMES, validateEventName } from "./events.mjs";

export const LOG_LEVELS = Object.freeze(["debug", "info", "warn", "error", "fatal"]);

export const REQUIRED_LOG_FIELDS = Object.freeze([
  "timestamp",
  "level",
  "environment",
  "service",
  "message",
  "trace_id"
]);

export function validateLogRecord(record, options = {}) {
  const knownEventNames = options.knownEventNames ?? OBSERVABILITY_EVENT_NAMES;
  const errors = [];

  if (!isPlainObject(record)) {
    return ["log record must be an object"];
  }

  for (const field of REQUIRED_LOG_FIELDS) {
    if (!(field in record)) {
      errors.push(`log.${field} is required`);
    }
  }

  errors.push(...validateIsoTimestamp(record.timestamp, "log.timestamp"));

  if (!LOG_LEVELS.includes(record.level)) {
    errors.push(`log.level must be one of ${LOG_LEVELS.join(", ")}`);
  }
  if (!ENVIRONMENT_NAMES.includes(record.environment)) {
    errors.push(`log.environment must be one of ${ENVIRONMENT_NAMES.join(", ")}`);
  }
  if (!SERVICE_NAME_PATTERN.test(record.service ?? "")) {
    errors.push("log.service must be a stable lower-case service name");
  }
  if (!isNonEmptyString(record.message)) {
    errors.push("log.message must be a non-empty string");
  }
  if (!isNonEmptyString(record.trace_id)) {
    errors.push("log.trace_id must be present for cross-signal correlation");
  } else if (String(record.trace_id).length > 128) {
    errors.push("log.trace_id must be 128 characters or fewer");
  }

  if (record.span_id !== undefined && !isNonEmptyString(record.span_id)) {
    errors.push("log.span_id must be a non-empty string when present");
  }

  if (record.result !== undefined && !RESULT_VALUES.includes(record.result)) {
    errors.push(`log.result must be one of ${RESULT_VALUES.join(", ")}`);
  }

  if (record.event_name !== undefined) {
    errors.push(...validateEventName(record.event_name).map((error) => `log.event_name: ${error}`));
    if (knownEventNames && !knownEventNames.includes(record.event_name)) {
      errors.push(`log.event_name ${String(record.event_name)} is not registered`);
    }
  }

  for (const boundedField of ["route", "target_family", "adapter_family", "command_type", "status_class"]) {
    if (record[boundedField] !== undefined && !isNonEmptyString(record[boundedField])) {
      errors.push(`log.${boundedField} must be a non-empty bounded string when present`);
    }
  }

  if (record.metadata !== undefined && !isPlainObject(record.metadata)) {
    errors.push("log.metadata must be an object when present");
  }

  errors.push(...validateNoSensitiveFields(record, "log"));

  return errors;
}

export function createLogRecord(input) {
  const record = {
    timestamp: input.timestamp ?? new Date().toISOString(),
    level: input.level,
    environment: input.environment,
    service: input.service,
    message: input.message,
    trace_id: input.trace_id,
    span_id: input.span_id,
    result: input.result,
    event_name: input.event_name,
    route: input.route,
    target_family: input.target_family,
    adapter_family: input.adapter_family,
    command_type: input.command_type,
    status_class: input.status_class,
    metadata: input.metadata ?? {}
  };

  const errors = validateLogRecord(record, input.options);
  if (errors.length > 0) {
    throw new Error(`Invalid log record:\n${errors.join("\n")}`);
  }

  return Object.freeze(record);
}
