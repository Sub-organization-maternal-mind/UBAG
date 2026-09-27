/**
 * Runs `fn` on an interval while the tab is visible. Pauses entirely when the
 * tab is hidden and fires immediately (then resumes the interval) when the tab
 * becomes visible again. Returns a cleanup function for onMount/$effect.
 */
export function pollWhileVisible(
  fn: () => Promise<unknown> | unknown,
  intervalMs: number,
): () => void {
  let timer: ReturnType<typeof setInterval> | null = null;

  const tick = () => {
    void Promise.resolve()
      .then(fn)
      .catch(() => {});
  };

  const start = () => {
    if (timer !== null) return;
    timer = setInterval(tick, intervalMs);
    tick();
  };

  const stop = () => {
    if (timer !== null) {
      clearInterval(timer);
      timer = null;
    }
  };

  const onVisibility = () => {
    if (document.hidden) stop();
    else start();
  };

  document.addEventListener('visibilitychange', onVisibility);
  start();

  return () => {
    document.removeEventListener('visibilitychange', onVisibility);
    stop();
  };
}

/** True when a newer load for the same key has started since this one. */
export function makeLoadGuard() {
  let current = 0;
  return () => ++current;
}
