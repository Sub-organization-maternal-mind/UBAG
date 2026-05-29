// Network layer for the UBAG gateway REST surface.
//
// On a Tauri target every request is issued through the Rust-side HTTP plugin
// (`@tauri-apps/plugin-http`). That avoids browser CORS entirely, which matters
// because the user points the app at an arbitrary self-hosted gateway URL that
// will not emit CORS headers. During plain web development we transparently
// fall back to the platform `fetch`.
//
// The app-secret is read from secure storage per-request and sent only in the
// Authorization header. It is never logged or placed in URLs.

import { API_VERSION } from "./types";
import type {
  CacheStatus,
  CollectionResponse,
  HealthStatus,
  JobEventListResponse,
  JobListResponse,
  JobResponse,
  JobStatus,
  MetricSample,
  ReadinessStatus,
  VersionStatus,
} from "./types";
import { getAppSecret } from "./secureStore";
import { normalizeGatewayUrl } from "./settings";

export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly traceId?: string;

  constructor(status: number, message: string, code?: string, traceId?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.traceId = traceId;
  }
}

type FetchFn = typeof fetch;

let cachedFetch: FetchFn | null = null;

function isTauri(): boolean {
  return typeof window !== "undefined" && "__TAURI_INTERNALS__" in window;
}

async function resolveFetch(): Promise<FetchFn> {
  if (cachedFetch) {
    return cachedFetch;
  }
  if (isTauri()) {
    const mod = await import("@tauri-apps/plugin-http");
    cachedFetch = mod.fetch as unknown as FetchFn;
  } else {
    cachedFetch = globalThis.fetch.bind(globalThis);
  }
  return cachedFetch;
}

interface RequestOptions {
  /** When false the Authorization header is omitted (public endpoints). */
  auth?: boolean;
  /** Expected response content type. Defaults to JSON. */
  expect?: "json" | "text";
  signal?: AbortSignal;
}

async function buildHeaders(auth: boolean): Promise<Record<string, string>> {
  const headers: Record<string, string> = {
    Accept: "application/json",
    "UBAG-Api-Version": API_VERSION,
  };
  if (auth) {
    const secret = await getAppSecret();
    if (secret) {
      headers["Authorization"] = `Bearer ${secret}`;
    }
  }
  return headers;
}

async function request<T>(
  gatewayUrl: string,
  path: string,
  options: RequestOptions = {}
): Promise<T> {
  const { auth = true, expect = "json", signal } = options;
  const base = normalizeGatewayUrl(gatewayUrl);
  const url = `${base}${path}`;
  const doFetch = await resolveFetch();
  const headers = await buildHeaders(auth);

  let response: Response;
  try {
    response = await doFetch(url, { method: "GET", headers, signal });
  } catch (err) {
    const reason = err instanceof Error ? err.message : "network error";
    throw new ApiError(0, `Cannot reach gateway: ${reason}`);
  }

  if (!response.ok) {
    let code: string | undefined;
    let traceId: string | undefined;
    let message = `Request failed (${response.status})`;
    try {
      const body = (await response.json()) as {
        error?: { code?: string; message?: string };
        trace_id?: string;
      };
      if (body?.error?.message) {
        message = body.error.message;
      }
      code = body?.error?.code;
      traceId = body?.trace_id;
    } catch {
      // Non-JSON error body; keep the generic message.
    }
    if (response.status === 401) {
      message = "Unauthorized — check the gateway URL and app secret.";
    }
    throw new ApiError(response.status, message, code, traceId);
  }

  if (expect === "text") {
    return (await response.text()) as unknown as T;
  }
  return (await response.json()) as T;
}

// ---- System -------------------------------------------------------------

export const getHealth = (gw: string, signal?: AbortSignal) =>
  request<HealthStatus>(gw, "/v1/health", { auth: false, signal });

export const getReadiness = (gw: string, signal?: AbortSignal) =>
  request<ReadinessStatus>(gw, "/v1/ready", { auth: false, signal });

export const getVersion = (gw: string, signal?: AbortSignal) =>
  request<VersionStatus>(gw, "/v1/version", { auth: false, signal });

export const getMetricsText = (gw: string, signal?: AbortSignal) =>
  request<string>(gw, "/v1/metrics", { auth: false, expect: "text", signal });

// ---- Jobs ---------------------------------------------------------------

export interface JobsQuery {
  status?: JobStatus | "";
  target?: string;
  limit?: number;
  cursor?: string;
}

export function listJobs(gw: string, query: JobsQuery = {}, signal?: AbortSignal) {
  const params = new URLSearchParams();
  if (query.status) {
    params.set("filter[status]", query.status);
  }
  if (query.target) {
    params.set("filter[target]", query.target);
  }
  params.set("limit", String(query.limit ?? 50));
  if (query.cursor) {
    params.set("cursor", query.cursor);
  }
  const qs = params.toString();
  return request<JobListResponse>(gw, `/v1/jobs${qs ? `?${qs}` : ""}`, { signal });
}

export const getJob = (gw: string, jobId: string, signal?: AbortSignal) =>
  request<JobResponse>(gw, `/v1/jobs/${encodeURIComponent(jobId)}`, { signal });

export function listJobEvents(
  gw: string,
  jobId: string,
  opts: { afterSequence?: number; limit?: number } = {},
  signal?: AbortSignal
) {
  const params = new URLSearchParams();
  params.set("limit", String(opts.limit ?? 100));
  if (typeof opts.afterSequence === "number" && opts.afterSequence > 0) {
    params.set("after_sequence", String(opts.afterSequence));
  }
  return request<JobEventListResponse>(
    gw,
    `/v1/jobs/${encodeURIComponent(jobId)}/events?${params.toString()}`,
    { signal }
  );
}

// ---- Webhooks / Audit / Cache ------------------------------------------

export const listWebhooks = (gw: string, signal?: AbortSignal) =>
  request<CollectionResponse>(gw, "/v1/webhooks", { signal });

export function listAudit(gw: string, limit = 50, signal?: AbortSignal) {
  const params = new URLSearchParams({ limit: String(limit) });
  return request<CollectionResponse>(gw, `/v1/audit?${params.toString()}`, { signal });
}

export const getCacheStatus = (gw: string, signal?: AbortSignal) =>
  request<CacheStatus>(gw, "/v1/cache", { signal });

// ---- Prometheus parsing -------------------------------------------------

// Minimal Prometheus text-exposition parser. Ignores HELP/TYPE comments and
// histogram/summary suffixes the overview does not surface.
export function parseMetrics(text: string): MetricSample[] {
  const samples: MetricSample[] = [];
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) {
      continue;
    }
    const match = trimmed.match(/^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{([^}]*)\})?\s+([^\s]+)/);
    if (!match) {
      continue;
    }
    const [, name, , rawLabels, rawValue] = match;
    const value = Number(rawValue);
    if (!Number.isFinite(value)) {
      continue;
    }
    const labels: Record<string, string> = {};
    if (rawLabels) {
      for (const pair of rawLabels.split(",")) {
        const eq = pair.indexOf("=");
        if (eq > 0) {
          const key = pair.slice(0, eq).trim();
          const val = pair.slice(eq + 1).trim().replace(/^"|"$/g, "");
          labels[key] = val;
        }
      }
    }
    samples.push({ name, labels, value });
  }
  return samples;
}

export function sumMetric(samples: MetricSample[], name: string): number | null {
  const matching = samples.filter((s) => s.name === name);
  if (matching.length === 0) {
    return null;
  }
  return matching.reduce((acc, s) => acc + s.value, 0);
}
