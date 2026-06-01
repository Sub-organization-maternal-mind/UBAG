export interface RetryPolicy {
  maxAttempts: number;
  baseDelayMs: number;
  maxDelayMs: number;
}

export const DEFAULT_RETRY_POLICY: RetryPolicy = {
  maxAttempts: 3,
  baseDelayMs: 1000,
  maxDelayMs: 60000,
};

const JITTER_FRACTION = 0.3;

// computeBackoff returns the delay before attempt `attempt` (0-based) using
// exponential backoff with +/-30% full jitter. `rand` is injectable for tests.
export function computeBackoff(
  policy: RetryPolicy,
  attempt: number,
  rand: () => number = Math.random,
): number {
  const exp = Math.pow(2, attempt) * policy.baseDelayMs;
  const base = Math.min(exp, policy.maxDelayMs);
  const lo = base * (1 - JITTER_FRACTION);
  const hi = base * (1 + JITTER_FRACTION);
  return Math.max(0, lo + rand() * (hi - lo));
}

// shouldRetry returns true when an error with the given code is retryable and
// the attempt budget is not yet exhausted.
export function shouldRetry(
  policy: RetryPolicy,
  attempt: number,
  retryable: boolean,
): boolean {
  return retryable && attempt < policy.maxAttempts - 1;
}
