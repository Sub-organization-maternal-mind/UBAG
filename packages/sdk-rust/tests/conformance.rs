//! Conformance: assert the fixtures have >=250 scenarios and the SDK exposes a
//! capability symbol for each category it claims.

use std::fs;
use std::path::Path;

#[test]
fn fixtures_have_250_scenarios() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../conformance/fixtures/v0/scenarios.json");
    let data = fs::read_to_string(&path).expect("read scenarios.json");
    let doc: serde_json::Value = serde_json::from_str(&data).expect("parse json");
    let scenarios = doc["coverage_scenarios"].as_array().expect("array");
    assert!(scenarios.len() >= 250, "only {} scenarios", scenarios.len());
}

#[test]
fn sdk_capabilities_compile() {
    // Reference each capability symbol so a missing one fails compilation.
    let _ = ubag::retry::RetryPolicy::default();
    let _ = ubag::webhooks::verify_webhook_signature;
    let _ = ubag::streaming::parse_sse_chunk;
    let _ = ubag::telemetry::build_traceparent;
    let _ = ubag::sidecar::sidecar_health_url;
}
