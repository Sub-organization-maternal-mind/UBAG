export const ENVIRONMENT_NAMES = Object.freeze([
  "local",
  "test",
  "dev",
  "staging",
  "production"
]);

export const RESULT_VALUES = Object.freeze([
  "requested",
  "accepted",
  "success",
  "failure",
  "skipped",
  "partial",
  "pending",
  "blocked",
  "retryable",
  "terminal",
  "degraded",
  "recovered",
  "denied"
]);

export const FORBIDDEN_FIELD_PATTERNS = Object.freeze([
  /(^|_)(authorization|cookie|set_cookie|password|passwd|secret|token|api_key|apikey|private_key|credential|session_cookie)($|_)/i,
  /(^|_)(raw_prompt|raw_response|html|screenshot_base64|card_number|cvv)($|_)/i
]);

export const STABLE_ID_PATTERN = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;
export const SERVICE_NAME_PATTERN = /^[a-z][a-z0-9-]{1,62}$/;

export function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function isNonEmptyString(value) {
  return typeof value === "string" && value.trim().length > 0;
}

export function validateIsoTimestamp(value, field) {
  if (!isNonEmptyString(value)) {
    return [`${field} must be a non-empty ISO-8601 string`];
  }

  const parsed = Date.parse(value);
  if (Number.isNaN(parsed) || new Date(parsed).toISOString() !== value) {
    return [`${field} must be a UTC ISO-8601 timestamp with millisecond precision`];
  }

  return [];
}

export function validateNoSensitiveFields(value, path = "record") {
  const errors = [];
  walk(value, path, errors);
  return errors;
}

export function getByPath(value, path) {
  const segments = path.split(".");
  let current = value;
  for (const segment of segments) {
    if (!isPlainObject(current) || !(segment in current)) {
      return undefined;
    }
    current = current[segment];
  }
  return current;
}

export function listDuplicateValues(values) {
  const seen = new Set();
  const duplicates = new Set();
  for (const value of values) {
    if (seen.has(value)) duplicates.add(value);
    seen.add(value);
  }
  return [...duplicates].sort();
}

function walk(value, path, errors) {
  if (Array.isArray(value)) {
    value.forEach((item, index) => walk(item, `${path}[${index}]`, errors));
    return;
  }

  if (!isPlainObject(value)) {
    return;
  }

  for (const [key, child] of Object.entries(value)) {
    if (FORBIDDEN_FIELD_PATTERNS.some((pattern) => pattern.test(key))) {
      errors.push(`${path}.${key} must not be emitted in observability payloads`);
    }
    walk(child, `${path}.${key}`, errors);
  }
}
