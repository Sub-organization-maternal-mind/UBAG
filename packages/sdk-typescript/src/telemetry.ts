// Minimal W3C trace-context helpers. The SDK accepts an optional tracer object
// matching the @opentelemetry/api Tracer shape but does not hard-depend on it.

export interface SpanLike {
  setAttribute(key: string, value: string | number | boolean): void;
  end(): void;
}

export interface TracerLike {
  startSpan(name: string): SpanLike;
}

export interface TelemetryOptions {
  tracer?: TracerLike;
}

export interface ParsedTraceparent {
  traceId: string;
  spanId: string;
}

const TRACEPARENT_RE = /^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$/;

export function buildTraceparent(traceId: string, spanId: string): string {
  return `00-${traceId}-${spanId}-01`;
}

export function parseTraceparent(value: string): ParsedTraceparent | null {
  const m = TRACEPARENT_RE.exec(value);
  if (!m || !m[1] || !m[2]) return null;
  return { traceId: m[1], spanId: m[2] };
}

// withSpan wraps an async operation in a span when a tracer is supplied.
export async function withSpan<T>(
  telemetry: TelemetryOptions | undefined,
  name: string,
  attributes: Record<string, string | number | boolean>,
  fn: () => Promise<T>,
): Promise<T> {
  const span = telemetry?.tracer?.startSpan(name);
  if (span) {
    for (const [k, v] of Object.entries(attributes)) span.setAttribute(k, v);
  }
  try {
    return await fn();
  } finally {
    span?.end();
  }
}
