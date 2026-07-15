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
export type UbagConversationMissing = "fail" | "restart" | (string & {});

/**
 * Per-job provider UI settings, keyed by the target adapter's own setting keys.
 * Discover the available keys and values from the adapter's model_catalog via
 * the adapters endpoint — they differ per provider (gemini_web: model,
 * thinking; deepseek_web: mode, deepthink).
 */
export type UbagModelSettings = Record<string, string | boolean>;

export interface UbagJobOptions {
  priority?: UbagJobPriority;
  timeout_seconds?: number;
  return_mode?: UbagReturnMode;
  response_formats?: string[];
  retry_policy?: UbagRetryPolicy;
  cache_policy?: UbagCachePolicy;
  conversation_missing?: UbagConversationMissing;
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
  model_settings?: UbagModelSettings | null;
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

export type UbagAlertStatus =
  | "open"
  | "notified"
  | "acknowledged"
  | "resolved"
  | "expired"
  | (string & {});

export interface UbagAlert {
  alert_id: string;
  tenant_id: string;
  app_id: string;
  job_id: string;
  session_id?: string;
  target_id?: string;
  kind: string;
  message?: string;
  status: UbagAlertStatus;
  created_at: string;
  notified_at?: string;
  acknowledged_at?: string;
  resolved_at?: string;
  attributes?: UbagJsonObject;
  [key: string]: UbagJsonValue | undefined;
}

export interface UbagListAlertsParams {
  limit?: number;
  status?: UbagAlertStatus;
}

export interface UbagAlertActionRequest {
  api_version?: string;
  idempotency_key?: string;
  reason?: string;
  metadata?: UbagJsonObject;
}

export interface UbagAlertListResponse {
  api_version: string;
  kind: "alerts" | (string & {});
  data: UbagAlert[];
  next_cursor: string | null;
  trace_id: string;
}

export interface UbagAlertMutationResponse {
  api_version: string;
  kind: "alert" | (string & {});
  data: UbagAlert;
  trace_id: string;
}

export interface UbagListConversationsParams {
  limit?: number;
}

/**
 * Durable binding from a caller-owned conversation key to a provider chat
 * thread, scoped to (tenant_id, app_id, target). provider_thread_ref is a
 * provider chat URL only — never session or credential material.
 */
export interface UbagConversation {
  tenant_id: string;
  app_id: string;
  target: string;
  conversation_key: string;
  provider_thread_ref?: string;
  state: "active" | "broken" | (string & {});
  created_at: string;
  last_used_at: string;
  last_job_id?: string;
}

export interface UbagConversationListResponse {
  api_version: string;
  conversations: UbagConversation[];
  next_cursor: string | null;
}

/**
 * Secret-free alert configuration. SMTP credentials and any password field are
 * intentionally absent; only the SMTP host (never credentials) is exposed.
 */
export interface UbagAlertConfig {
  api_version: string;
  kind: "alert_config" | (string & {});
  sink_type: string;
  smtp_configured: boolean;
  smtp_host?: string;
  store_kind?: string;
  recipient_count: number;
  recipients?: string[];
  trace_id: string;
}

export interface UbagBrowserInstance {
  instance_id: string;
  worker_id: string;
  tenant_id: string;
  engine: string;
  remote_endpoint?: string;
  state: string;
  context_count: number;
  tab_count: number;
  rss_bytes?: number;
  created_at: string;
  recycle_at?: string;
}

/**
 * Provider browser context. The storage-state URI is redacted server-side
 * (INV-5): only the has_storage_state boolean is exposed and storage_state_uri
 * is never present.
 */
export interface UbagProviderContext {
  context_id: string;
  instance_id: string;
  tenant_id: string;
  target_id: string;
  identity_ref: string;
  login_state: string;
  conversation_model: string;
  fingerprint_id?: string;
  proxy_id?: string;
  has_storage_state: boolean;
  max_tabs: number;
  created_at: string;
  last_health_at?: string;
  recycle_at?: string;
}

export interface UbagBrowserTab {
  tab_id: string;
  context_id: string;
  state: string;
  conversation_id?: string;
  current_job_id?: string;
  jobs_completed: number;
  rss_bytes?: number;
  last_health_at?: string;
  created_at: string;
  recycle_at?: string;
}

export interface UbagConcurrencyView {
  target: string;
  identity_ref: string;
  current_cap: number;
  min: number;
  max: number;
  in_flight: number;
  last_change_reason?: string;
  last_change_at: string;
}

export interface UbagListBrowserInstancesParams {
  limit?: number;
  state?: string;
}

export interface UbagListProviderContextsParams {
  limit?: number;
  instance_id?: string;
}

export interface UbagListBrowserTabsParams {
  limit?: number;
  context_id?: string;
  state?: string;
}

export interface UbagBrowserInstanceListResponse {
  api_version: string;
  kind: "browser_instances" | (string & {});
  data: UbagBrowserInstance[];
  next_cursor: string | null;
  trace_id: string;
}

export interface UbagProviderContextListResponse {
  api_version: string;
  kind: "provider_contexts" | (string & {});
  data: UbagProviderContext[];
  next_cursor: string | null;
  trace_id: string;
}

export interface UbagBrowserTabListResponse {
  api_version: string;
  kind: "browser_tabs" | (string & {});
  data: UbagBrowserTab[];
  next_cursor: string | null;
  trace_id: string;
}

export interface UbagListConcurrencyParams {
  limit?: number;
  cursor?: string;
}

export interface UbagConcurrencyListResponse {
  api_version: string;
  kind: "concurrency_ceilings" | (string & {});
  data: UbagConcurrencyView[];
  next_cursor: string | null;
  trace_id: string;
}

export interface UbagBrowserTopologySummary {
  api_version: string;
  kind: "browser_topology_summary" | (string & {});
  tenant_id: string;
  total_instances: number;
  total_contexts: number;
  total_tabs: number;
  instances_by_state: Record<string, number>;
  contexts_by_login_state: Record<string, number>;
  tabs_by_state: Record<string, number>;
  trace_id: string;
}

export interface UbagLogoutResult {
  api_version: string;
  revoked: boolean;
  trace_id: string;
}

export interface UbagSsoLogoutRequest {
  api_version?: string;
  idempotency_key?: string;
}

export interface UbagAuditExportRange {
  from_sequence?: number;
  to_sequence?: number;
}

export interface UbagAuditExportRequest {
  api_version?: string;
  idempotency_key?: string;
  since?: string;
  until?: string;
  limit?: number;
  range?: UbagAuditExportRange;
}

export interface UbagAuditRecord {
  id: string;
  seq: number;
  tenant_id: string;
  app_id: string;
  actor: string;
  action: string;
  resource: string;
  outcome: string;
  occurred_at: string;
  attributes?: UbagJsonObject;
  prev_hash: string;
  record_hash: string;
}

export interface UbagAuditExportStats {
  enqueued: number;
  exported: number;
  dropped: number;
  failed: number;
}

export interface UbagAuditExportResult {
  api_version: string;
  status: "accepted" | (string & {});
  stats: UbagAuditExportStats;
  chain_valid: boolean;
  head_hash: string;
  count: number;
  records: UbagAuditRecord[];
  trace_id: string;
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
