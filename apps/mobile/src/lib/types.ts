// Shapes mirrored from packages/openapi/openapi.yaml and the shared JSON
// schemas. Only the read-only fields the monitoring app consumes are typed;
// unknown fields are tolerated so a newer gateway never breaks the client.

export const API_VERSION = "2026-05-22";

export type JobStatus =
  | "created"
  | "queued"
  | "assigned"
  | "running"
  | "token_streaming"
  | "completing"
  | "completed"
  | "completed_with_warnings"
  | "failed_retryable"
  | "failed_terminal"
  | "dead_letter"
  | "cancelled"
  | "timed_out";

export const JOB_STATUSES: JobStatus[] = [
  "created",
  "queued",
  "assigned",
  "running",
  "token_streaming",
  "completing",
  "completed",
  "completed_with_warnings",
  "failed_retryable",
  "failed_terminal",
  "dead_letter",
  "cancelled",
  "timed_out",
];

export type JobEventType =
  | "created"
  | "queued"
  | "assigned"
  | "running"
  | "browser_opened"
  | "session.manual_action_required"
  | "prompt_submitted"
  | "token"
  | "token_streaming"
  | "completing"
  | "completed"
  | "completed_with_warnings"
  | "failed_retryable"
  | "failed_terminal"
  | "dead_letter"
  | "cancelled"
  | "timed_out"
  | "artifact_created"
  | "blocked"
  | "warning";

export interface HealthStatus {
  status: "ok";
  service: string;
  version?: string;
  checked_at: string;
  checks?: Record<string, unknown>;
  trace_id: string;
}

export interface ReadinessDependency {
  name: string;
  status: "ready" | "not_ready" | "degraded";
  latency_ms?: number;
  message?: string;
}

export interface ReadinessStatus {
  ready: boolean;
  status: "ready" | "not_ready";
  service: string;
  version?: string;
  checked_at: string;
  checks?: Record<string, unknown>;
  dependencies?: ReadinessDependency[];
  trace_id: string;
}

export interface VersionStatus {
  service: string;
  version: string;
  commit?: string | null;
  built_at?: string | null;
  api_versions: string[];
  default_api_version: string;
  trace_id: string;
}

export interface JobResponse {
  api_version: string;
  job_id: string;
  idempotent_replay?: boolean;
  status: JobStatus;
  target: string;
  result?: unknown;
  metadata?: Record<string, unknown>;
  trace_id: string;
  events_url: string;
  created_at?: string;
  updated_at?: string;
}

export interface JobListResponse {
  api_version: string;
  jobs: JobResponse[];
  next_cursor: string | null;
  trace_id: string;
}

export interface JobEvent {
  event_id: string;
  job_id: string;
  api_version: string;
  type: JobEventType;
  created_at: string;
  sequence: number;
  data: Record<string, unknown>;
  trace_id: string;
}

export interface JobEventListResponse {
  api_version: string;
  job_id: string;
  events: JobEvent[];
  next_cursor: string | null;
  trace_id: string;
}

export interface CollectionResponse {
  api_version: string;
  kind: string;
  data: Record<string, unknown>[];
  next_cursor: string | null;
  trace_id: string;
}

export interface CacheStatus {
  api_version: string;
  profile: "edge" | "small" | "standard" | "enterprise";
  enabled: boolean;
  entries: Record<string, unknown>[];
  trace_id: string;
}

// Parsed Prometheus metric sample (single value, labels flattened).
export interface MetricSample {
  name: string;
  labels: Record<string, string>;
  value: number;
}
