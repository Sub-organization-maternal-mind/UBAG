import { snapshots } from '../stores/snapshot';
import { api } from './client';
import type { FleetNode, FleetSummary } from './types';

/**
 * Operator fleet view (GET /v1/fleet/nodes, /v1/fleet/summary) and queue-reason
 * helpers. Fleet panels render only when the gateway answers 200: 501 (no fleet
 * source), 404 (older gateway), 403 (not an operator) and every failure all
 * read as "no panel", never as an error. Anything the gateway did not report is
 * shown as an em dash (design.md: no fabricated figures).
 */

export const EM_DASH = '—';

type UnknownRecord = Record<string, unknown>;

function record(value: unknown): UnknownRecord {
  return value != null && typeof value === 'object' && !Array.isArray(value)
    ? (value as UnknownRecord)
    : {};
}

const isNum = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);

function counts(value: unknown): Record<string, number> {
  return Object.fromEntries(
    Object.entries(record(value)).filter(([, n]) => isNum(n)),
  ) as Record<string, number>;
}

// --- Queue reasons -----------------------------------------------------------

// One-line meaning of each coarse queue_reason (job-response.schema.json).
export const QUEUE_REASON_HINT: Record<string, string> = {
  waiting_for_worker: 'Every worker is busy; the job is behind others.',
  waiting_for_identity:
    'The provider account or browser it needs is in use; one operation runs per identity at a time.',
  waiting_for_capacity:
    'Capacity assigned to this gateway, including helper capacity, is fully used or temporarily reduced.',
  waiting_for_node: 'Tied to a helper node that is not available right now.',
  retry_backoff: 'A retryable failure is waiting out its delay before the next attempt.',
  temporarily_unavailable: 'The gateway cannot start it right now and retries on its own.',
};

/** "waiting_for_worker" -> "Waiting for worker". Unknown tokens stay readable as they are. */
export function humanize(token: string): string {
  const spaced = token.replace(/_/g, ' ');
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

export interface ReasonRow {
  reason: string;
  label: string;
  hint: string;
  count: number;
}

/** Non-zero reason counts, largest first (ties by name). The reason set is open. */
export function queueReasonRows(byReason: Record<string, number> | null | undefined): ReasonRow[] {
  return Object.entries(byReason ?? {})
    .filter(([, n]) => isNum(n) && n > 0)
    .map(([reason, count]) => ({
      reason,
      label: humanize(reason),
      hint: QUEUE_REASON_HINT[reason] ?? '',
      count,
    }))
    .sort((a, b) => b.count - a.count || a.reason.localeCompare(b.reason));
}

// --- Formatting (all return an em dash for an unreported value) ----------------

export function fmtCount(v: unknown): string {
  return isNum(v) ? v.toLocaleString() : EM_DASH;
}

export function fmtTime(v: unknown): string {
  if (typeof v !== 'string' || !v) return EM_DASH;
  const t = new Date(v);
  return Number.isNaN(t.getTime()) ? EM_DASH : t.toLocaleString();
}

/** 1500 millicores -> "1.5 CPU". */
export function fmtCpu(millis: unknown): string {
  return isNum(millis) ? `${+(millis / 1000).toFixed(2)} CPU` : EM_DASH;
}

export function fmtBytes(bytes: unknown): string {
  if (!isNum(bytes)) return EM_DASH;
  const gib = bytes / 2 ** 30;
  return gib >= 1 ? `${+gib.toFixed(1)} GiB` : `${Math.round(bytes / 2 ** 20)} MiB`;
}

export function capacityPct(used: number, limit: number): number {
  if (!(limit > 0)) return 0;
  return Math.min(100, Math.round((used / limit) * 100));
}

export function barColor(p: number): string {
  if (p >= 90) return 'bg-danger';
  if (p >= 70) return 'bg-warning';
  return 'bg-success';
}

// --- Parsing (guards the contract shape; anything else hides the panel) ---------

/** A FleetSummary body, or null when it is not the contract shape. */
export function parseFleetSummary(body: unknown): FleetSummary | null {
  const raw = record(body);
  if (
    !isNum(raw.nodes_total) ||
    !isNum(raw.workload_limit_total) ||
    !isNum(raw.workloads_in_use_total) ||
    !isNum(raw.nodes_pressure_reduced)
  ) {
    return null;
  }
  return {
    nodes_total: raw.nodes_total,
    nodes_by_state: counts(raw.nodes_by_state),
    workload_limit_total: raw.workload_limit_total,
    workloads_in_use_total: raw.workloads_in_use_total,
    nodes_pressure_reduced: raw.nodes_pressure_reduced,
    held_by_reason: counts(raw.held_by_reason),
  };
}

/**
 * The nodes of a FleetNodeListResponse body, or null when it is not one. A node
 * missing what the panels draw (id, state, usage numbers, grant) is skipped,
 * the way normalizeJobs skips a job without an id; other fields are formatted
 * defensively, so an unreported one renders as an em dash.
 */
export function parseFleetNodes(body: unknown): FleetNode[] | null {
  const raw = record(body);
  if (!Array.isArray(raw.data)) return null;
  return raw.data.filter((n): n is FleetNode => {
    const node = record(n);
    const usage = record(node.usage);
    return (
      typeof node.node_id === 'string' &&
      node.node_id !== '' &&
      typeof node.state === 'string' &&
      isNum(usage.workloads_in_use) &&
      isNum(usage.admission_limit) &&
      Object.keys(record(node.grant)).length > 0
    );
  }).map((n) => ({
    ...n,
    label: typeof n.label === 'string' && n.label ? n.label : n.node_id,
    readiness: Array.isArray(n.readiness) ? n.readiness : [],
    pressure: record(n.pressure) as FleetNode['pressure'],
  }));
}

/** "chatgpt_web: authenticated ×2" per probed target/state; empty until probes are reported. */
export function readinessLines(node: FleetNode): string[] {
  return node.readiness
    .map(record)
    .filter((r) => typeof r.target === 'string' && typeof r.session_state === 'string' && isNum(r.count))
    .map((r) => `${r.target}: ${r.session_state} ×${r.count}`);
}

// --- Loading --------------------------------------------------------------------

async function read<T>(path: string, parse: (body: unknown) => T | null, force: boolean): Promise<T | null> {
  const res = await snapshots.get(path, () => api.get<unknown>(path), { ttlMs: 10_000, force });
  return res.status === 200 ? parse(res.data) : null;
}

/** The fleet summary, or null when the gateway does not serve one (hide the panel). */
export const loadFleetSummary = (force = false) => read('/v1/fleet/summary', parseFleetSummary, force);

/** The fleet nodes, or null when the gateway does not serve them (hide the panel). */
export const loadFleetNodes = (force = false) => read('/v1/fleet/nodes', parseFleetNodes, force);
