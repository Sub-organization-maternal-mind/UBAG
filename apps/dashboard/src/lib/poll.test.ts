import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { pollWhileVisible } from './poll';

let hidden = false;
const listeners: Array<() => void> = [];

beforeEach(() => {
  vi.useFakeTimers();
  hidden = false;
  listeners.length = 0;
  vi.stubGlobal('document', {
    get hidden() {
      return hidden;
    },
    addEventListener: (_: string, l: () => void) => listeners.push(l),
    removeEventListener: (_: string, l: () => void) => {
      const i = listeners.indexOf(l);
      if (i >= 0) listeners.splice(i, 1);
    },
  });
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const setHidden = (h: boolean) => {
  hidden = h;
  listeners.slice().forEach((l) => l());
};

describe('pollWhileVisible', () => {
  it('fires immediately then on interval', async () => {
    const fn = vi.fn();
    const stop = pollWhileVisible(fn, 1000);
    await vi.advanceTimersByTimeAsync(0);
    expect(fn).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1000);
    expect(fn).toHaveBeenCalledTimes(2);
    stop();
    await vi.advanceTimersByTimeAsync(5000);
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it('immediate:false waits one interval', async () => {
    const fn = vi.fn();
    const stop = pollWhileVisible(fn, 1000, { immediate: false });
    await vi.advanceTimersByTimeAsync(999);
    expect(fn).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(fn).toHaveBeenCalledTimes(1);
    stop();
  });

  it('does not start while hidden, starts on visible', async () => {
    hidden = true;
    const fn = vi.fn();
    const stop = pollWhileVisible(fn, 1000);
    await vi.advanceTimersByTimeAsync(5000);
    expect(fn).not.toHaveBeenCalled();
    setHidden(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(fn).toHaveBeenCalledTimes(1);
    stop();
  });

  it('pauses when hidden', async () => {
    const fn = vi.fn();
    const stop = pollWhileVisible(fn, 1000);
    await vi.advanceTimersByTimeAsync(0);
    setHidden(true);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(fn).toHaveBeenCalledTimes(1);
    stop();
  });

  it('never overlaps a slow in-flight call', async () => {
    let running = 0;
    let max = 0;
    const fn = vi.fn(async () => {
      running++;
      max = Math.max(max, running);
      await new Promise((r) => setTimeout(r, 3500));
      running--;
    });
    const stop = pollWhileVisible(fn, 1000);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(max).toBe(1);
    stop();
  });

  it('backs off with jitter on 429 and honours retryAfterMs', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0);
    const fn = vi.fn(async () => ({ status: 429, retryAfterMs: 20_000 }));
    const stop = pollWhileVisible(fn, 1000);
    await vi.advanceTimersByTimeAsync(0);
    expect(fn).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(19_999);
    expect(fn).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(fn).toHaveBeenCalledTimes(2);
    stop();
  });

  it('backs off on thrown errors and resets after success', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(1);
    let fail = true;
    const fn = vi.fn(async () => {
      if (fail) throw new Error('boom');
      return { status: 200 };
    });
    const stop = pollWhileVisible(fn, 1000);
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(1999); // 1 failure -> 2000ms delay
    expect(fn).toHaveBeenCalledTimes(1);
    fail = false;
    await vi.advanceTimersByTimeAsync(1);
    expect(fn).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1000); // success -> normal interval
    expect(fn).toHaveBeenCalledTimes(3);
    stop();
  });
});
