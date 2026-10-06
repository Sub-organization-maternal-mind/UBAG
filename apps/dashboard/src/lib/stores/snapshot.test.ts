import { describe, expect, it, vi } from 'vitest';
import { createSnapshotStore } from './snapshot';

const ok = (n: number) => ({ status: 200, data: { n } });

describe('snapshot store', () => {
  it('serves a fresh value from cache and refetches after the TTL', async () => {
    let t = 0;
    const store = createSnapshotStore(() => t);
    const fetcher = vi.fn(async () => ok(fetcher.mock.calls.length));

    expect((await store.get('k', fetcher, { ttlMs: 1000 })).data.n).toBe(1);
    t = 999;
    expect((await store.get('k', fetcher, { ttlMs: 1000 })).data.n).toBe(1);
    t = 1000;
    expect((await store.get('k', fetcher, { ttlMs: 1000 })).data.n).toBe(2);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('dedupes concurrent reads of one key into a single request', async () => {
    const store = createSnapshotStore();
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const fetcher = vi.fn(async () => { await gate; return ok(1); });

    const all = Promise.all([store.get('k', fetcher), store.get('k', fetcher), store.get('k', fetcher, { force: true })]);
    release();
    const results = await all;
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(new Set(results).size).toBe(1);
  });

  it('force bypasses a fresh entry', async () => {
    const store = createSnapshotStore();
    const fetcher = vi.fn(async () => ok(1));
    await store.get('k', fetcher);
    await store.get('k', fetcher, { force: true });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('never caches non-2xx or thrown results', async () => {
    const store = createSnapshotStore();
    const bad = vi.fn(async () => ({ status: 503, data: null }));
    await store.get('k', bad);
    await store.get('k', bad);
    expect(bad).toHaveBeenCalledTimes(2);

    const boom = vi.fn(async () => { throw new Error('x'); });
    await expect(store.get('t', boom)).rejects.toThrow('x');
    await expect(store.get('t', boom)).rejects.toThrow('x');
    expect(boom).toHaveBeenCalledTimes(2);
  });

  it('clear() drops entries and a pre-clear in-flight fetch does not repopulate', async () => {
    const store = createSnapshotStore();
    const fetcher = vi.fn(async () => ok(1));
    await store.get('k', fetcher);
    store.clear('k');
    await store.get('k', fetcher);
    expect(fetcher).toHaveBeenCalledTimes(2);

    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const slow = vi.fn(async () => { await gate; return ok(9); });
    const pending = store.get('s', slow);
    store.clear();
    release();
    await pending;
    await store.get('s', slow);
    expect(slow).toHaveBeenCalledTimes(2);
  });

  it('bounds the number of cached keys', async () => {
    const store = createSnapshotStore();
    const fetcher = vi.fn(async () => ok(1));
    for (let i = 0; i < 40; i++) await store.get(`k${i}`, fetcher);
    fetcher.mockClear();
    await store.get('k0', fetcher); // evicted
    await store.get('k39', fetcher); // still cached
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
