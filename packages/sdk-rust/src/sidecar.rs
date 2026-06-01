//! Loopback sidecar discovery.

pub const SIDECAR_URL: &str = "http://127.0.0.1:7878";

/// Returns the sidecar health-probe URL for a given base.
pub fn sidecar_health_url(base: &str) -> String {
    format!("{}/v1/health", base)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn builds_health_url() {
        assert_eq!(sidecar_health_url(SIDECAR_URL), "http://127.0.0.1:7878/v1/health");
    }
}
