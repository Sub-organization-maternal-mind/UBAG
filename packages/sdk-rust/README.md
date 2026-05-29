# UBAG Rust SDK (`ubag`)

Idiomatic Rust client for the UBAG v0 REST gateway, with stable error
envelopes, automatic idempotency keys, and a pluggable transport for testing.

The crate is split into a toolchain-free request-construction core (always
compiled) and an optional [`reqwest`]-backed transport behind the `transport`
feature (enabled by default). Unit tests inject their own [`Transport`] so
request construction is validated without a live gateway or a network stack.

## Endpoint coverage

- **System:** `health`, `ready`, `version`, `metrics`
- **Jobs:** `create_job`, `get_job`, `list_jobs`, `cancel_job`, `retry_job`
- **Job events:** `list_job_events`, `stream_job_events_sse`
- **Artifacts:** `list_job_artifacts`, `get_job_artifact`, `put_job_artifact`,
  `delete_job_artifact`
- **Operator collections:** `list_workflows`, `list_templates`, `list_targets`,
  `list_adapters`, `list_apps`, `list_devices`, `list_webhooks`,
  `list_audit_events`, `list_events`
- **Webhook replay & cache:** `replay_webhook_delivery`, `cache_status`

## Invariants

- API version `2026-05-22` is sent on every request as `Ubag-Api-Version`.
- `Authorization: Bearer <app-secret>` is attached when an app secret is set.
- Mutating calls (`create_job`, `cancel_job`, `retry_job`, `put_job_artifact`,
  `delete_job_artifact`, `replay_webhook_delivery`) send an `Idempotency-Key`,
  auto-generating a 26-character ULID-style key when one is not supplied.
- Non-2xx responses parse the stable `{ "error": { code, message, ... } }`
  envelope into an [`ApiError`].
- Secrets are never logged or stored beyond the in-memory client.

## Example

```rust
use ubag::{Client, RequestOptions};
use serde_json::json;

fn main() -> Result<(), ubag::Error> {
    let client = Client::new("http://127.0.0.1:7878")?.with_app_secret("dev-secret");

    let mut body = serde_json::Map::new();
    body.insert("client".into(), json!({"app_id": "demo", "app_version": "0.0.0"}));
    body.insert(
        "job".into(),
        json!({"target": "mock_target", "command_type": "echo", "input": {"prompt": "Hello UBAG"}}),
    );

    let job = client.create_job(body, RequestOptions::new())?;
    println!("queued job: {}", job["job_id"]);
    Ok(())
}
```

## Tests

The unit tests inject a capturing transport and need no network at run time:

```powershell
cd packages/sdk-rust
cargo test --no-default-features
```

Run the full suite (compiling the default `reqwest` transport):

```powershell
cargo test
```

## Toolchain

- **Required:** `cargo` / `rustc` 1.70+.
- **Offline:** the *test logic* runs offline, but the first `cargo build` /
  `cargo test` must fetch crate dependencies (`serde`, `serde_json`, and—when
  the `transport` feature is enabled—`reqwest`) from crates.io. After the
  dependencies are vendored/cached, builds and tests run offline.
