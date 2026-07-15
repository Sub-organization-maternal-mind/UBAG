// Standard gateway response shape
export interface GwResponse<T = unknown> {
  status: number;
  data: T | null;
  denied: boolean;
  /** True when the request failed because no/invalid credentials were supplied (401). */
  unauthorized: boolean;
  error: string | null;
}

// Job types
export interface Job {
  id: string;
  target: string;
  command_type: string;
  status: string;
  created_at: string;
  updated_at: string;
  result?: unknown;
  error?: string;
}

export interface JobsResponse {
  jobs: Job[];
  next_cursor?: string;
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

// Browser
export interface BrowserInstance {
  instance_id: string;
  state: string;
  context_count?: number;
  tab_count?: number;
  engine?: string;
  worker_id?: string;
  remote_endpoint?: string;
  novnc_url?: string;
  // Legacy / optional
  id?: string;
  status?: string;
}

export interface BrowserContext {
  context_id: string;
  instance_id: string;
  tab_count?: number;
  target_id?: string;
  login_state?: string;
  // Legacy / optional
  id?: string;
}

export interface BrowserTab {
  tab_id: string;
  context_id: string;
  url?: string;
  title?: string;
  state?: string;
  // Legacy / optional
  id?: string;
  status?: string;
}

// BrowserSummary — real gateway shape from /v1/browser/summary
export interface BrowserSummary {
  total_instances: number;
  total_contexts: number;
  total_tabs: number;
  instances_by_state: Record<string, number>;
  contexts_by_login_state: Record<string, number>;
  tabs_by_state: Record<string, number>;
  // Legacy / optional
  instances?: number;
  contexts?: number;
  tabs?: number;
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

// Metrics
export interface MetricsResponse {
  jobs_total?: number;
  jobs_active?: number;
  jobs_failed?: number;
  targets_total?: number;
  browser_instances?: number;
  [key: string]: unknown;
}
