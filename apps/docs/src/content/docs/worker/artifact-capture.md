---
title: Artifact Capture
description: Baseline capture, storage, redaction, and retention contract for worker artifacts.
---

# Artifact Capture

Artifacts make browser automation debuggable without turning every job into an uncontrolled data dump. The worker captures only the artifacts requested by policy, redacts before persistence where possible, and returns object references instead of large inline payloads.

## Artifact types

| Type | Purpose | Default capture |
|---|---|---|
| `screenshot` | Human inspection of target state, adapter failures, and drift. | On failure and drift events. |
| `dom_snapshot` | Structural comparison for drift detection. | Key adapter states and synthetic checks. |
| `har` | Network diagnosis and target latency review. | On failure, sampled in success paths. |
| `playwright_trace` | Replayable execution debugging. | On failure and explicit debug jobs. |
| `video_recording` | Time-travel review of flaky flows after credential entry is complete. | Disabled for manual-login and CAPTCHA screens; explicit debug capture only after sensitive entry is complete. |
| `console_log` | Browser-side errors and warnings. | Failure and debug jobs. |
| `downloaded_file` | Target-produced outputs. | Only when the command expects files. |
| `normalized_output` | Adapter-normalized result payload. | Every successful job. |

## Naming scheme

Object keys must be deterministic enough for support and retention jobs, but not expose prompt text, user names, credentials, or target account identifiers.

```text
tenants/{tenant_id}/jobs/{yyyy}/{mm}/{dd}/{job_id}/
  screenshots/{sequence}_{state}.png
  dom/{sequence}_{state}.json
  har/{attempt_id}.har.zst
  traces/{attempt_id}.zip
  recordings/{attempt_id}.webm
  downloads/{artifact_id}/{safe_filename}
  output/{attempt_id}.json
```

For `edge` profile, the same logical key is stored under the local artifact root. For `small` and higher profiles, the key maps to MinIO, Garage, or another S3-compatible store.

Current gateway runtime exposes artifact storage through `/v1/jobs/{id}/artifacts[/{key}]`. `UBAG_ARTIFACT_STORE=minio` stores bytes in MinIO/S3-compatible storage and records metadata in memory by default or in Postgres `artifact_metadata` when `UBAG_GATEWAY_STORE=postgres` is active.

## Metadata record

Every artifact needs a metadata record that can be returned in job results and searched by operators.

```json
{
  "artifact_id": "art_01HX...",
  "job_id": "job_01HX...",
  "attempt_id": "att_01HX...",
  "tenant_id": "tenant_123",
  "target": "deepseek_web",
  "adapter": "deepseek_web@0.1.0",
  "type": "screenshot",
  "state": "submit_prompt_failed",
  "content_type": "image/png",
  "size_bytes": 188421,
  "sha256": "hex...",
  "storage_url": "s3://ubag-artifacts/tenants/tenant_123/jobs/...",
  "redaction_status": "applied",
  "retention_until": "2026-06-21T00:00:00Z",
  "created_at": "2026-05-22T00:00:00Z"
}
```

## Redaction rules

- Never log or persist raw secrets, cookies, authorization headers, local keychain values, 2FA codes, or typed passwords.
- HAR capture must strip request and response headers known to carry credentials before persistence.
- Screenshots and recordings of manual login are disabled by default unless an operator starts an explicit debug capture.
- DOM snapshots used for drift compare are structural by default: tags, roles, stable attributes, and selector-relevant metadata, not full user text.
- Downloaded files inherit the command data classification and must be encrypted at rest before they are exposed to clients.

## Capture policy

```yaml
artifact_policy:
  screenshots:
    on_failure: true
    on_drift: true
    sample_success_percent: 0
  dom_snapshots:
    key_states: true
    include_text: false
  har:
    on_failure: true
    redact_headers: true
  traces:
    on_failure: true
    debug_only: false
  video:
    manual_login_screens: false
    captcha_screens: false
    post_login_debug_only: true
    sample_canary_percent: 0
  max_total_bytes_per_job: 104857600
```

The gateway may set stricter policy. The worker may only reduce capture or fail the job with a named policy error; it must not silently exceed the limit.

## Retention

| Artifact | Development default | Production default |
|---|---:|---:|
| Screenshots | 14 days | 30 days |
| DOM snapshots | 30 days | 90 days for adapter baselines, 30 days for per-job snapshots |
| HAR files | 7 days | 14 days |
| Traces | 7 days | 14 days |
| Recordings | 7 days | 30 days only when enabled |
| Normalized outputs | Follows job retention | Follows tenant data policy |

These are baseline defaults for planning. Compliance modes may shorten, encrypt, or disable classes of artifacts.

## Failure behavior

- Artifact upload failure after job success should create `UBAG-ARTIFACT-UPLOAD-001` and mark the job `completed_with_warnings` if the missing artifact is diagnostic-only.
- Missing required output artifacts should fail the job with `UBAG-ARTIFACT-REQUIRED-001`.
- Redaction failure must fail closed with `UBAG-ARTIFACT-REDACT-001`.
- Retention policy failure must block persistence and emit an operator alert.

## Milestone 0 acceptance

- Artifact types, keys, metadata fields, and retention defaults are referenced by worker and adapter implementation tickets.
- Failure artifacts can be captured without returning binary data inline.
- Drift detection can consume structural DOM snapshots without storing sensitive prompt text by default.
