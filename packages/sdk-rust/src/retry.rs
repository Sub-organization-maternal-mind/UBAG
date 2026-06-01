//! Retry policy with exponential backoff and full jitter.

#[derive(Clone, Copy, Debug)]
pub struct RetryPolicy {
    pub max_attempts: u32,
    pub base_delay_ms: u64,
    pub max_delay_ms: u64,
}

impl Default for RetryPolicy {
    fn default() -> Self {
        RetryPolicy { max_attempts: 3, base_delay_ms: 1000, max_delay_ms: 60000 }
    }
}

const JITTER_FRACTION: f64 = 0.3;

/// Returns the delay (ms) before attempt `attempt` (0-based). `rand` in [0,1)
/// is injectable for tests.
pub fn compute_backoff(p: &RetryPolicy, attempt: u32, rand: f64) -> u64 {
    let exp = 2f64.powi(attempt as i32) * p.base_delay_ms as f64;
    let base = exp.min(p.max_delay_ms as f64);
    let lo = base * (1.0 - JITTER_FRACTION);
    let hi = base * (1.0 + JITTER_FRACTION);
    let d = lo + rand * (hi - lo);
    if d < 0.0 { 0 } else { d as u64 }
}

pub fn should_retry(p: &RetryPolicy, attempt: u32, retryable: bool) -> bool {
    retryable && attempt < p.max_attempts - 1
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn backoff_increases() {
        let p = RetryPolicy { max_attempts: 5, base_delay_ms: 1000, max_delay_ms: 60000 };
        assert!(compute_backoff(&p, 1, 0.5) > compute_backoff(&p, 0, 0.5));
    }

    #[test]
    fn backoff_caps() {
        let p = RetryPolicy { max_attempts: 10, base_delay_ms: 1000, max_delay_ms: 2000 };
        assert!(compute_backoff(&p, 9, 1.0) <= (2000f64 * 1.3) as u64);
    }

    #[test]
    fn should_retry_budget() {
        let p = RetryPolicy { max_attempts: 3, base_delay_ms: 1, max_delay_ms: 1 };
        assert!(should_retry(&p, 0, true));
        assert!(!should_retry(&p, 2, true));
        assert!(!should_retry(&p, 0, false));
    }
}
