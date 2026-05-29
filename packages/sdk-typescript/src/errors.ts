import type { UbagErrorDetails, UbagErrorEnvelope, UbagJsonObject } from "./types.js";

export interface UbagApiErrorOptions {
  status: number;
  statusText: string;
  url: string;
  method: string;
  headers: Record<string, string>;
  envelope?: UbagErrorEnvelope | undefined;
  body?: unknown;
}

export class UbagApiError extends Error {
  readonly name = "UbagApiError";
  readonly status: number;
  readonly statusText: string;
  readonly url: string;
  readonly method: string;
  readonly headers: Record<string, string>;
  readonly envelope: UbagErrorEnvelope | undefined;
  readonly body?: unknown;

  constructor(options: UbagApiErrorOptions) {
    const message =
      options.envelope?.error.message ??
      `UBAG API request failed with HTTP ${options.status} ${options.statusText}`;

    super(message);
    this.status = options.status;
    this.statusText = options.statusText;
    this.url = options.url;
    this.method = options.method;
    this.headers = options.headers;
    this.envelope = options.envelope;
    this.body = options.body;
  }

  get error(): UbagErrorDetails | undefined {
    return this.envelope?.error;
  }

  get code(): string | undefined {
    return this.error?.code;
  }

  get category(): string | undefined {
    return this.error?.category;
  }

  get retryable(): boolean {
    return this.error?.retryable ?? false;
  }

  get retryAfterMs(): number | undefined {
    if (this.error?.retry_after_ms !== undefined) {
      return this.error.retry_after_ms;
    }

    const retryAfter = this.headers["retry-after"];
    if (retryAfter === undefined) {
      return undefined;
    }

    const seconds = Number(retryAfter);
    if (Number.isFinite(seconds)) {
      return Math.max(0, seconds * 1000);
    }

    const dateMs = Date.parse(retryAfter);
    if (Number.isFinite(dateMs)) {
      return Math.max(0, dateMs - Date.now());
    }

    return undefined;
  }

  get traceId(): string | undefined {
    return this.error?.trace_id ?? this.headers["ubag-trace-id"] ?? this.headers["x-request-id"];
  }
}

export class UbagTransportError extends Error {
  readonly name = "UbagTransportError";
  readonly url: string;
  readonly method: string;

  constructor(message: string, options: { url: string; method: string; cause?: unknown }) {
    super(message, options.cause === undefined ? undefined : { cause: options.cause });
    this.url = options.url;
    this.method = options.method;
  }
}

export function isUbagErrorEnvelope(value: unknown): value is UbagErrorEnvelope {
  if (!isObject(value) || !isObject(value.error)) {
    return false;
  }

  const error = value.error;
  return (
    typeof error.code === "string" &&
    error.code.startsWith("UBAG-") &&
    typeof error.category === "string" &&
    typeof error.message === "string" &&
    typeof error.retryable === "boolean" &&
    typeof error.trace_id === "string"
  );
}

function isObject(value: unknown): value is UbagJsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
