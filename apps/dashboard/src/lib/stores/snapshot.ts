/**
 * Shared TTL snapshot store for read-only gateway GETs.
 *
 * Overview, Metrics and Failed all read the same endpoints (/v1/jobs/summary,
 * /v1/browser/summary, ...). Within `ttlMs` a repeat read returns the cached
 * response, and concurrent reads of one key share a single in-flight request.
 * Only successful (2xx) responses are cached, so errors/denials are never
 * pinned. In-memory and per tab; cleared when the gateway settings change.
 */

export interface SnapshotOptions {
  /** How long a cached value stays fresh (default 10 s). */
  ttlMs?: number;
  /** Skip the cache (manual Refresh) but still dedupe against an in-flight fetch. */
  force?: boolean;
}

const MAX_ENTRIES = 32;

const cacheable = (v: unknown): boolean => {
  const s = (v as { status?: unknown } | null)?.status;
  return typeof s === 'number' && s >= 200 && s < 300;
};

export function createSnapshotStore(now: () => number = Date.now) {
  const fresh = new Map<string, { at: number; value: unknown }>();
  const inflight = new Map<string, Promise<unknown>>();
  // Bumped by clear(): a fetch that started before a clear must not repopulate.
  let epoch = 0;

  return {
    async get<T>(key: string, fetcher: () => Promise<T>, opts: SnapshotOptions = {}): Promise<T> {
      const ttl = opts.ttlMs ?? 10_000;
      const hit = fresh.get(key);
      if (!opts.force && hit && now() - hit.at < ttl) return hit.value as T;
      const pending = inflight.get(key);
      if (pending) return pending as Promise<T>;

      const startedIn = epoch;
      const p = fetcher()
        .then((value) => {
          if (epoch === startedIn && cacheable(value)) {
            fresh.delete(key); // re-insert so Map order == age (oldest first)
            fresh.set(key, { at: now(), value });
            if (fresh.size > MAX_ENTRIES) fresh.delete(fresh.keys().next().value as string);
          }
          return value;
        })
        .finally(() => {
          if (inflight.get(key) === p) inflight.delete(key);
        });
      inflight.set(key, p);
      return p;
    },
    /** Drop one key, or everything (including in-flight sharing). */
    clear(key?: string) {
      if (key === undefined) {
        epoch++;
        fresh.clear();
        inflight.clear();
      } else {
        fresh.delete(key);
        inflight.delete(key);
      }
    },
  };
}

export const snapshots = createSnapshotStore();
