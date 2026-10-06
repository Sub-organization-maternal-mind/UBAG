// Standard gateway response shape
export interface GwResponse<T = unknown> {
  status: number;
  data: T | null;
  denied: boolean;
  /** True when the request failed because no/invalid credentials were supplied (401). */
  unauthorized: boolean;
  error: string | null;
  /** Parsed Retry-After header (ms) on 429/503 responses, when present. */
  retryAfterMs?: number;
}

// Job types
// job-response.schema.json $defs.queue_reason. Unknown values are tolerated.
export type QueueReason =
  | 'waiting_for_worker'
  | 'waiting_for_identity'
  | 'waiting_for_capacity'
  | 'waiting_for_node'
  | 'retry_backoff'
  | 'temporarily_unavailable';

export interface Job {
  id: string;
  job_id?: string;
  target: string;
  command_type: string;
  status: string;
  created_at: string;
  updated_at: string;
  input?: Record<string, unknown>;
  metadata?: Record<string, unknown>;
  result?: unknown;
  error?: string;
  // Coarse, tenant-safe reason a queued job has not started. Reported only while
  // status is queued, and only when the gateway computes it; absent means unknown.
  queue_reason?: QueueReason | (string & {}) | null;
  queue_reason_since?: string | null;
}

export interface JobCreateResponse {
  job_id: string;
  status: string;
  target: string;
  created_at: string;
  updated_at: string;
}

export interface JobsResponse {
  jobs: Job[];
  next_cursor?: string;
}

export interface JobEnvelope {
  api_version: string;
  client: {
    app_id: string;
    app_version: string;
    sdk: {
      name: string;
      version: string;
    };
  };
  job: {
    target: string;
    command_type: string;
    template_id?: string;
    input: Record<string, unknown>;
    options?: Record<string, unknown>;
  };
}

// Conversation types — real gateway shape from GET /v1/conversations
// (ConversationListResponse). A conversation is a durable binding from a
// caller-owned conversation key to a provider chat thread. provider_thread_ref
// is a chat URL only — never cookies, storage state, or credential material.
export interface Conversation {
  tenant_id: string;
  app_id: string;
  target: string;
  conversation_key: string;
  provider_thread_ref?: string;
  state: 'active' | 'broken';
  created_at: string;
  last_used_at: string;
  last_job_id?: string;
}

export interface ConversationsResponse {
  api_version?: string;
  conversations: Conversation[];
  next_cursor?: string | null;
}

// Target types — real gateway shape from /v1/targets
export interface Target {
  key: string;
  display_name: string;
  adapter_key: string;
  manual_login_required: boolean;
  safe_mode: boolean;
  // Legacy / optional (kept to avoid breakage if referenced elsewhere)
  id?: string;
  name?: string;
  url?: string;
  adapter?: string;
  status?: string;
}

export interface ListResponse<T> {
  items: T[];
  next_cursor?: string;
}

// Health
export interface HealthResponse {
  status: string;
  version?: string;
  uptime?: number;
}

// Browser — mirrors packages/openapi (BrowserInstance, ProviderContext, BrowserTab,
// BrowserTopologySummary, ConcurrencyView). Keep field names identical to the contract.
export interface BrowserInstance {
  instance_id: string;
  worker_id: string;
  tenant_id: string;
  engine: string;
  state: string;
  context_count: number;
  tab_count: number;
  created_at: string;
  remote_endpoint?: string;
  rss_bytes?: number;
  recycle_at?: string;
  // NOT in the OpenAPI contract: the gateway never emits it today. The browser page only
  // embeds it when present AND loopback-scoped; remove once an operator-viewer contract exists.
  novnc_url?: string;
}

export interface BrowserContext {
  context_id: string;
  instance_id: string;
  tenant_id: string;
  target_id: string;
  identity_ref: string;
  login_state: string;
  conversation_model: string;
  has_storage_state: boolean;
  max_tabs: number;
  created_at: string;
  fingerprint_id?: string;
  proxy_id?: string;
  last_health_at?: string;
  recycle_at?: string;
}

export interface BrowserTab {
  tab_id: string;
  context_id: string;
  state: string;
  jobs_completed: number;
  created_at: string;
  conversation_id?: string;
  current_job_id?: string;
  rss_bytes?: number;
  last_health_at?: string;
  recycle_at?: string;
}

// BrowserTopologySummary — GET /v1/browser/summary
export interface BrowserSummary {
  tenant_id?: string;
  total_instances: number;
  total_contexts: number;
  total_tabs: number;
  instances_by_state: Record<string, number>;
  contexts_by_login_state: Record<string, number>;
  tabs_by_state: Record<string, number>;
}

// ConcurrencyView — GET /v1/concurrency (concurrency:read)
export interface ConcurrencyView {
  target: string;
  identity_ref: string;
  current_cap: number;
  min: number;
  max: number;
  in_flight: number;
  last_change_reason?: string;
  last_change_at: string;
}

// Adapter — real gateway shape from /v1/adapters
export interface Adapter {
  key: string;
  kind: string;
  stage: string;
  capabilities: string[];
  // Legacy / optional
  id?: string;
  name?: string;
  version?: string;
  status?: string;
}

// App
export interface App {
  id: string;
  name: string;
  version?: string;
  status?: string;
}

// Device
export interface Device {
  id: string;
  name: string;
  type?: string;
  status?: string;
}

// Template — real gateway shape from /v1/templates
export interface Template {
  id: string;
  command_type: string;
  description: string;
  created_at: string;
  // Legacy / optional
  name?: string;
  version?: string;
}

// Workflow
export interface WorkflowStep {
  id: string;
  name: string;
  status?: string;
  depends_on?: string[];
}

export interface Workflow {
  id: string;
  name: string;
  status?: string;
  step_count?: number;
  created_at?: string;
  steps?: WorkflowStep[];
}

export interface WorkflowRun {
  id: string;
  definition_id: string;
  state: string;
  current_step: number;
  steps: Array<{
    step_id: string;
    state: string;
    job_id?: string;
    error?: string;
  }>;
  created_at: string;
  updated_at: string;
}

// Webhook
export interface Webhook {
  id: string;
  url: string;
  events?: string[];
  status?: string;
}

// Audit entry
export interface AuditEntry {
  id: string;
  timestamp: string;
  actor: string;
  action: string;
  resource?: string;
  hash?: string;
  prev_hash?: string;
}

// JobsSummary — GET /v1/jobs/summary (uncapped counts; never derive totals from a list page)
export interface JobsSummary {
  total: number;
  counts_by_status: Record<string, number>;
  queued_by_reason: Record<string, number>;
  oldest_queued_at: string | null;
}

// Metrics
export interface MetricsResponse {
  jobs_total?: number;
  jobs_active?: number;
  jobs_failed?: number;
  jobs_queued?: number;
  targets_total?: number;
  browser_instances?: number;
  [key: string]: unknown;
}

// Fleet — mirrors packages/openapi (FleetNode, FleetSummary). Operator-only reads
// (fleet:read); the gateway answers 501 when it has no fleet source. A node carries
// an opaque node_id and label only: never an address or a browser endpoint.
export interface FleetGrant {
  generation: number;
  state: 'active' | 'draining' | 'revoked' | (string & {});
  reservation_state: 'known' | 'unknown' | (string & {});
  valid_until: string;
  max_browser_workloads: number;
  cpu_millis: number;
  memory_bytes: number;
  voice_capable: boolean;
}

export interface FleetReadiness {
  target: string;
  session_state: 'authenticated' | 'login_required' | 'unknown' | 'busy' | (string & {});
  count: number;
  checked_at: string;
}

export interface FleetNode {
  node_id: string;
  label: string;
  region: string;
  state: 'eligible' | 'ineligible' | 'draining' | 'lost' | 'unknown_reservation' | (string & {});
  ineligible_reason: 'revoked' | 'grant_expired' | 'no_capacity' | (string & {}) | null;
  heartbeat_at: string | null;
  grant: FleetGrant;
  usage: { workloads_in_use: number; admission_limit: number };
  pressure: { admission_reduced: boolean; recover_at: string | null };
  readiness: FleetReadiness[];
}

export interface FleetSummary {
  nodes_total: number;
  nodes_by_state: Record<string, number>;
  workload_limit_total: number;
  workloads_in_use_total: number;
  nodes_pressure_reduced: number;
  // Open set of fine-grained hold reasons; absent key means 0.
  held_by_reason: Record<string, number>;
}
