export const UBAG_DEFAULT_API_VERSION = "2026-05-22";
export const UBAG_SDK_NAME = "ubag-typescript";
export const UBAG_SDK_VERSION = "0.0.0";

export type UbagJsonPrimitive = string | number | boolean | null;
export type UbagJsonValue = UbagJsonPrimitive | UbagJsonObject | UbagJsonArray;
export interface UbagJsonObject {
  [key: string]: UbagJsonValue;
}
export type UbagJsonArray = UbagJsonValue[];

export interface UbagSdkMetadata {
  name: string;
  version: string;
}

export interface UbagClientMetadata {
  app_id: string;
  app_version?: string;
  device_id?: string;
  user_ref?: string;
  sdk?: UbagSdkMetadata;
}

export type UbagJobPriority = "low" | "normal" | "high" | "urgent" | (string & {});
export type UbagReturnMode = "accepted" | "final" | "stream" | (string & {});
export type UbagRetryPolicy = "none" | "default" | "aggressive" | (string & {});
export type UbagCachePolicy = "none" | "semantic_30d" | (string & {});

export interface UbagJobOptions {
  priority?: UbagJobPriority;
  timeout_seconds?: number;
  return_mode?: UbagReturnMode;
  response_formats?: string[];
  retry_policy?: UbagRetryPolicy;
  cache_policy?: UbagCachePolicy;
  trace_context?: string;
  [key: string]: UbagJsonValue | undefined;
}

export interface UbagJobCallbacks {
  webhook_url?: string;
  webhook_secret_id?: string;
  [key: string]: UbagJsonValue | undefined;
}

export interface UbagJobCommand {
  target: string;
  command_type: string;
  conversation_id?: string;
  template_id?: string;
  input: UbagJsonObject;
  options?: UbagJobOptions;
  callbacks?: UbagJobCallbacks;
  context?: UbagJsonObject;
}

export interface UbagCreateJobRequest {
  api_version?: string;
  idempotency_key?: string;
  client: UbagClientMetadata;
  job: UbagJobCommand;
}

export type UbagJobStatus =
  | "created"
  | "queued"
  | "accepted"
  | "assigned"
  | "running"
  | "token_streaming"
  | "completing"
  | "completed"
  | "completed_with_warnings"
  | "failed"
  | "failed_retryable"
  | "failed_terminal"
  | "dead_letter"
  | "cancelled"
  | "timed_out"
  | "retrying"
  | (string & {});

export interface UbagJobOutput {
  text?: string;
  markdown?: string;
  plain_text?: string;
  html?: string;
  sections?: UbagJsonObject;
  [key: string]: UbagJsonValue | undefined;
}

export interface UbagJobResult {
  output?: UbagJobOutput;
  validation?: UbagJsonObject;
  cached?: boolean;
  cache_source?: string | null;
}

export interface UbagJobMetadata {
  queued_at?: string;
  started_at?: string;
  completed_at?: string;
  duration_ms?: number;
  browser_session_id?: string;
  adapter?: string;
  worker?: string;
  retries?: number;
  cost?: UbagJsonObject;
  [key: string]: UbagJsonValue | undefined;
}

export interface UbagJobResponse {
  api_version: string;
  job_id: string;
  idempotent_replay: boolean;
  status: UbagJobStatus;
  target: string;
  result?: UbagJobResult;
  metadata?: UbagJobMetadata;
  trace_id: string;
  events_url?: string;
}

export interface UbagListJobsParams {
  cursor?: string;
  limit?: number;
  status?: UbagJobStatus;
  target?: string;
  sort?: string;
  fields?: string[];
  include?: string[];
}

export interface UbagListJobsResponse {
  api_version: string;
  jobs: UbagJobResponse[];
  next_cursor?: string | null;
  trace_id?: string;
}

export interface UbagCollectionResponse {
  api_version: string;
  kind: string;
  data: UbagJsonObject[];
  next_cursor?: string | null;
  trace_id?: string;
}

export interface UbagListEventsParams {
  cursor?: string;
  limit?: number;
}

export interface UbagListJobEventsParams {
  cursor?: string;
  after_sequence?: number;
  limit?: number;
}

export interface UbagJobEvent {
  event_id: string;
  job_id: string;
  api_version: string;
  type: string;
  created_at: string;
  sequence: number;
  data: UbagJsonObject;
  trace_id: string;
}

export interface UbagJobEventsResponse {
  api_version: string;
  job_id: string;
  events: UbagJobEvent[];
  next_cursor: string | null;
  trace_id?: string;
}

export interface UbagArtifactRecord {
  job_id: string;
  key: string;
  content_type: string;
  size_bytes: number;
  checksum?: string;
  created_at: string;
}

export interface UbagArtifactListResponse {
  api_version: string;
  job_id: string;
  kind: "artifacts" | (string & {});
  data: UbagArtifactRecord[];
  trace_id?: string;
}

export interface UbagArtifactResponse {
  api_version: string;
  artifact: UbagArtifactRecord;
  idempotent_replay?: boolean;
  trace_id?: string;
}

export interface UbagArtifactDownloadResponse {
  body: Uint8Array;
  content_type?: string;
  checksum?: string;
}

export interface UbagCacheStatusResponse {
  api_version: string;
  profile: string;
  enabled: boolean;
  entries: UbagJsonValue[];
  trace_id?: string;
}

export interface UbagJobMutationRequest {
  api_version?: string;
  idempotency_key?: string;
  job_id?: string;
  reason?: string;
  metadata?: UbagJsonObject;
}

export interface UbagWebhookReplayRequest {
  api_version?: string;
  idempotency_key?: string;
  delivery_id?: string;
  reason?: string;
  metadata?: UbagJsonObject;
}

export interface UbagWebhookReplayResponse {
  api_version: string;
  status: "accepted" | (string & {});
  delivery_id?: string;
  idempotent_replay?: boolean;
  audit_event: "webhook.delivery_replayed" | (string & {});
  metadata?: UbagJsonObject;
  trace_id?: string;
}

export interface UbagHealthResponse {
  status: "ok" | "degraded" | "down" | (string & {});
  service?: string;
  version?: string;
  checked_at?: string;
  checks?: Record<string, "ok" | "degraded" | "down" | (string & {})>;
  trace_id?: string;
}

export interface UbagReadyResponse {
  ready: boolean;
  service?: string;
  status?: string;
  version?: string;
  checked_at?: string;
  checks?: Record<string, boolean | string>;
  trace_id?: string;
}

export interface UbagVersionResponse {
  service: string;
  version: string;
  api_versions: string[];
  default_api_version: string;
  commit?: string;
  built_at?: string;
  trace_id?: string;
}

export type UbagErrorCategory =
  | "auth"
  | "validation"
  | "quota"
  | "rate"
  | "queue"
  | "worker"
  | "browser"
  | "adapter"
  | "target"
  | "template"
  | "cache"
  | "webhook"
  | "internal"
  | (string & {});

export interface UbagErrorDetails {
  code: `UBAG-${string}`;
  category: UbagErrorCategory;
  message: string;
  retryable: boolean;
  retry_after_ms?: number;
  details?: UbagJsonObject;
  doc_url?: string;
  trace_id: string;
}

export interface UbagErrorEnvelope {
  error: UbagErrorDetails;
}
