//! Webhook HMAC-SHA256 signature verification.

use hmac::{Hmac, Mac};
use sha2::Sha256;
use std::time::{SystemTime, UNIX_EPOCH};

type HmacSha256 = Hmac<Sha256>;

/// Verifies an HMAC-SHA256 signature over `${timestamp}.${body}` within
/// `tolerance_seconds`. Constant-time comparison via the `hmac` crate.
pub fn verify_webhook_signature(
    payload: &[u8],
    signature: &str,
    secret: &str,
    timestamp: &str,
    tolerance_seconds: i64,
) -> bool {
    let ts: i64 = match timestamp.parse() {
        Ok(v) => v,
        Err(_) => return false,
    };
    let now = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs() as i64).unwrap_or(0);
    if (now - ts).abs() > tolerance_seconds {
        return false;
    }
    let mut mac = match HmacSha256::new_from_slice(secret.as_bytes()) {
        Ok(m) => m,
        Err(_) => return false,
    };
    mac.update(timestamp.as_bytes());
    mac.update(b".");
    mac.update(payload);
    let sig_bytes = match hex::decode(signature) {
        Ok(b) => b,
        Err(_) => return false,
    };
    mac.verify_slice(&sig_bytes).is_ok()
}

#[cfg(test)]
mod tests {
    use super::*;
    use hmac::{Hmac, Mac};
    use sha2::Sha256;

    fn sign(secret: &str, ts: &str, body: &str) -> String {
        let mut mac = Hmac::<Sha256>::new_from_slice(secret.as_bytes()).unwrap();
        mac.update(ts.as_bytes());
        mac.update(b".");
        mac.update(body.as_bytes());
        hex::encode(mac.finalize().into_bytes())
    }

    fn now() -> String {
        std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs().to_string()
    }

    #[test]
    fn valid_signature() {
        let ts = now();
        let sig = sign("whsec", &ts, "body");
        assert!(verify_webhook_signature(b"body", &sig, "whsec", &ts, 300));
    }

    #[test]
    fn bad_signature() {
        let ts = now();
        assert!(!verify_webhook_signature(b"x", "deadbeef", "whsec", &ts, 300));
    }

    #[test]
    fn expired_timestamp() {
        let old = (std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() as i64 - 600).to_string();
        let sig = sign("whsec", &old, "body");
        assert!(!verify_webhook_signature(b"body", &sig, "whsec", &old, 300));
    }
}
