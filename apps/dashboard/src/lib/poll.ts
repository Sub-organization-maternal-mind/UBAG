export interface PollOptions {
  /** Fire once right away (default true). Pass false when the caller already did the first load. */
  immediate?: boolean;
  /** Backoff ceiling after consecutive failures (default 5 min). */
  maxBackoffMs?: number;
}

/** Shape `fn` may return so the poller can back off: a GwResponse fits. */
interface PollResult {
  status?: number;
  retryAfterMs?: number;
}

const isFailure = (r: unknown): r is PollResult => {
  const s = (r as PollResult | null)?.status;
  return typeof s === 'number' && (s === 429 || s >= 500 || s < 0);
};

/**
 * Runs `fn` every `intervalMs` while the tab is visible. Never starts while
 * hidden; pauses when hidden and fires immediately on becoming visible. Ticks
 * never overlap (in-flight guard). When `fn` returns (or throws) a failure
 * (status 429 / >=500 / network), the next delay backs off exponentially with
 * jitter and honours `retryAfterMs`. Returns a cleanup function.
 */
export function pollWhileVisible(
  fn: () => Promise<unknown> | unknown,
  intervalMs: number,
  opts: PollOptions = {},
): () => void {
  const maxBackoff = opts.maxBackoffMs ?? 300_000;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let inFlight = false;
  let failures = 0;
  let stopped = false;

  const clear = () => {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  };

  const schedule = (retryAfterMs = 0) => {
    clear();
    if (stopped || document.hidden) return;
    let delay = intervalMs;
    if (failures > 0) {
      const exp = Math.min(maxBackoff, intervalMs * 2 ** failures);
      delay = Math.max(retryAfterMs, exp * (0.5 + Math.random() * 0.5));
    }
    timer = setTimeout(tick, delay);
  };

  const tick = async () => {
    timer = null;
    if (inFlight || stopped || document.hidden) return;
    inFlight = true;
    let retryAfter = 0;
    try {
      const r = await fn();
      if (isFailure(r)) {
        failures++;
        retryAfter = r.retryAfterMs ?? 0;
      } else failures = 0;
    } catch {
      failures++;
    } finally {
      inFlight = false;
    }
    schedule(retryAfter);
  };

  const onVisibility = () => {
    if (document.hidden) clear();
    else if (!inFlight) void tick();
  };

  document.addEventListener('visibilitychange', onVisibility);
  if (!document.hidden) {
    if (opts.immediate === false) schedule();
    else void tick();
  }

  return () => {
    stopped = true;
    document.removeEventListener('visibilitychange', onVisibility);
    clear();
  };
}

/** True when a newer load for the same key has started since this one. */
export function makeLoadGuard() {
  let current = 0;
  return () => ++current;
}
