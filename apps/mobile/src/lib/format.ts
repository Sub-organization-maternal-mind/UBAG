import type { JobEventType, JobStatus } from "./types";

// Visual tone buckets reused across status badges and timeline dots.
export type Tone = "ready" | "running" | "warn" | "danger" | "muted";

const STATUS_TONE: Record<JobStatus, Tone> = {
  created: "muted",
  queued: "muted",
  assigned: "running",
  running: "running",
  token_streaming: "running",
  completing: "running",
  completed: "ready",
  completed_with_warnings: "warn",
  failed_retryable: "warn",
  failed_terminal: "danger",
  dead_letter: "danger",
  cancelled: "muted",
  timed_out: "danger",
};

export function statusTone(status: string): Tone {
  return STATUS_TONE[status as JobStatus] ?? "muted";
}

const EVENT_TONE: Partial<Record<JobEventType, Tone>> = {
  completed: "ready",
  completed_with_warnings: "warn",
  warning: "warn",
  blocked: "warn",
  "session.manual_action_required": "warn",
  failed_retryable: "warn",
  failed_terminal: "danger",
  dead_letter: "danger",
  timed_out: "danger",
  cancelled: "muted",
  running: "running",
  token_streaming: "running",
  token: "running",
};

export function eventTone(type: string): Tone {
  return EVENT_TONE[type as JobEventType] ?? "muted";
}

export function humanize(value: string): string {
  return value
    .replace(/[._]/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

export function formatTimestamp(iso?: string | null): string {
  if (!iso) {
    return "—";
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return iso;
  }
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export function relativeTime(iso?: string | null): string {
  if (!iso) {
    return "—";
  }
  const date = new Date(iso);
  const ms = Date.now() - date.getTime();
  if (Number.isNaN(ms)) {
    return iso;
  }
  const sec = Math.round(ms / 1000);
  if (sec < 60) {
    return `${sec}s ago`;
  }
  const min = Math.round(sec / 60);
  if (min < 60) {
    return `${min}m ago`;
  }
  const hr = Math.round(min / 60);
  if (hr < 24) {
    return `${hr}h ago`;
  }
  return `${Math.round(hr / 24)}d ago`;
}

export function formatNumber(value: number | null): string {
  if (value === null) {
    return "—";
  }
  return new Intl.NumberFormat().format(value);
}
