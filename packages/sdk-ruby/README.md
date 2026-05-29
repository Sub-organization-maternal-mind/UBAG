# UBAG Ruby SDK (`ubag`)

Idiomatic Ruby client for the UBAG v0 REST gateway, built on the standard
library (`net/http`, `json`, `securerandom`) with stable error envelopes,
automatic idempotency keys, and a pluggable transport for testing.

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

- API version `2026-05-22` sent on every request as `Ubag-Api-Version`.
- `Authorization: Bearer <app-secret>` attached when an app secret is set.
- Mutating calls send an `Idempotency-Key`, auto-generating a 26-character
  ULID-style key when one is not supplied.
- Non-2xx responses parse the stable `{ "error" => { "code", "message", ... } }`
  envelope into a `Ubag::ApiError`.
- Secrets are never logged or stored beyond the in-memory client.

## Example

```ruby
require "ubag"

client = Ubag::Client.new("http://127.0.0.1:7878", app_secret: "dev-secret")

job = client.create_job(
  "client" => { "app_id" => "demo", "app_version" => "0.0.0" },
  "job" => { "target" => "mock_target", "command_type" => "echo", "input" => { "prompt" => "Hello UBAG" } }
)
puts job["job_id"]
```

## Tests

```powershell
cd packages/sdk-ruby
ruby -Ilib -Itest test/test_client.rb
# or, with rake installed:
rake test
```

The test injects a capturing transport, so no network is contacted.

## Toolchain

- **Required:** Ruby 3.0+.
- **Offline:** `minitest` ships with Ruby, so `ruby -Ilib -Itest test/test_client.rb`
  runs fully offline with no gem installation. `rake test` additionally requires
  the `rake` gem.
