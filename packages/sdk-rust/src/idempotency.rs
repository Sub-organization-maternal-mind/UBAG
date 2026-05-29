//! Dependency-free ULID-style idempotency key generation.
//!
//! Keys are 26 characters of Crockford base32: 10 characters of millisecond
//! timestamp followed by 16 characters of entropy. This mirrors the key shape
//! used by the TypeScript, Python, and Go SDKs.

use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

const CROCKFORD_BASE32: &[u8; 32] = b"0123456789ABCDEFGHJKMNPQRSTVWXYZ";

static COUNTER: AtomicU64 = AtomicU64::new(0);

/// Generates a new ULID-style idempotency key.
pub fn generate_idempotency_key() -> String {
    let now_ms = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0);
    let mut output = encode_base32(now_ms, 10);
    let mut state = seed(now_ms);
    for _ in 0..16 {
        state = xorshift(state);
        let index = (state % 32) as usize;
        output.push(CROCKFORD_BASE32[index] as char);
    }
    output
}

fn seed(now_ms: u64) -> u64 {
    let counter = COUNTER.fetch_add(1, Ordering::Relaxed);
    let stack_marker = &counter as *const u64 as u64;
    let mut value = now_ms ^ (counter.wrapping_mul(0x9E37_79B9_7F4A_7C15)) ^ stack_marker;
    if value == 0 {
        value = 0x1234_5678_9ABC_DEF0;
    }
    value
}

fn xorshift(mut value: u64) -> u64 {
    value ^= value << 13;
    value ^= value >> 7;
    value ^= value << 17;
    value
}

fn encode_base32(mut value: u64, length: usize) -> String {
    let mut buffer = vec![b'0'; length];
    for slot in buffer.iter_mut().rev() {
        *slot = CROCKFORD_BASE32[(value % 32) as usize];
        value /= 32;
    }
    String::from_utf8(buffer).expect("crockford base32 is valid ascii")
}
