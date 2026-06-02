// Standard gateway response shape
export interface GwResponse<T = unknown> {
  status: number;
  data: T | null;
  denied: boolean;
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

// Target types
export interface Target {
  id: string;
  name: string;
  url: string;
  adapter: string;
  status: string;
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
  id: string;
  status: string;
  context_count?: number;
}

export interface BrowserContext {
  id: string;
  instance_id: string;
  tab_count?: number;
}

export interface BrowserTab {
  id: string;
  context_id: string;
  url?: string;
  title?: string;
  status?: string;
}

export interface BrowserSummary {
  instances: number;
  contexts: number;
  tabs: number;
}

// Adapter
export interface Adapter {
  id: string;
  name: string;
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

// Template
export interface Template {
  id: string;
  name: string;
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
