---
title: Security Implementation Contracts
description: Shared contract scaffolding for credentials, RBAC, rate limits, audit, and signed webhooks.
---

# Security Implementation Contracts

Status: v0 scaffolding. The shared TypeScript package is `@ubag/security`.

The package is gateway-adjacent and defines reusable contracts for the gateway, SDKs, workers, conformance runners, and dashboard code as the security model moves from documentation into implementation.

## Package Guardrails

- No runtime dependencies.
- No bundled secrets, environment lookups, or default production credentials.
- Node `crypto` is used for SHA-256 fingerprints, HMAC-SHA256, random token material, and timing-safe comparisons.
- Secret fingerprints are for lookup and verification contracts only; production password-equivalent storage still requires the approved secret store and rotation path.
- Audit metadata is redacted before persistence or export.

## App-Secret Contract

The v0 app-secret shape remains `Authorization: Bearer <credential>`; the
scheme is parsed case-insensitively while the credential remains exact.

The shared contract parses bearer credentials, rejects missing or malformed headers, and verifies presented credentials against a `sha256:` fingerprint supplied by the caller. The package does not read `UBAG_APP_SECRET`, persist raw values, or define a production storage backend.

Expected audit events:

- `auth.app_secret.accepted`
- `auth.app_secret.rejected`

## Device Token Contract

Device token scaffolding uses the token shape:

```text
ubag_dev.<token_id>.<secret_segment>
```

Only the token ID is suitable for lookup. The secret segment must be stored as a fingerprint or stronger approved hash, never as a raw value. Verification resolves the token record by ID, rejects revoked or expired records, and verifies the presented secret segment with timing-safe comparison.

Expected audit events:

- `auth.device_token.accepted`
- `auth.device_token.rejected`
- `device.token_issued`
- `device.token_revoked`

## RBAC And ABAC Contract

The implementation scaffold exports explicit role and action registries for RBAC checks:

- Roles: `viewer`, `developer`, `operator`, `admin`, `superadmin`, `support`, `service`.
- Privileged actions include `device:enroll`, `device:revoke`, `secret:rotate`, `webhook:configure`, `webhook:replay`, `audit:read`, `rate_limit:manage`, `role:manage`, `policy:manage`, `data:export`, and `support:access`.

ABAC checks deny tenant-boundary mismatches, disabled actors, unsupported secret data-class access, support access without a reason, and regulated-mode exports by non-superadmin actors.

Expected audit events:

- `authz.allow`
- `authz.deny`

## Rate-Limit Contract

Rate-limit helpers define tenant, app, device, actor, target, and global scopes with explicit limit, burst, window, remaining, reset, and retry-after semantics. The contract returns stable `allowed` and `limited` decisions so gateway, worker, and SDK retry logic can share the same envelope.

Expected audit events:

- `rate_limit.allowed`
- `rate_limit.rejected`

## Audit Contract

Audit helpers create structured events with actor, action, resource, tenant, result, trace ID, source, reason, privacy mode, data class, and redacted metadata.

The redactor blocks secret-adjacent fields such as authorization headers, cookies, credentials, keys, passwords, private values, secrets, sessions, and tokens. Chained audit digests use canonical JSON plus a previous digest so later implementations can preserve tamper-evidence without deciding storage in this package.

## Webhook Signing Contract

Signed outbound webhook delivery uses HMAC-SHA256 with the base string named `timestamp.nonce.body`:

```text
<unix_timestamp_seconds>.<nonce>.<raw_body>
```

Required headers:

- `Ubag-Webhook-Signature`: `v1=<base64url_hmac_sha256>`
- `Ubag-Webhook-Timestamp`: Unix timestamp in seconds.
- `Ubag-Webhook-Nonce`: Per-delivery nonce.

Consumers must reject malformed signatures, stale timestamps outside the five-minute default window, duplicate nonces, and body modifications.

Expected audit events:

- `webhook.delivery_signed`
- `webhook.delivery_replayed`
- `webhook.verification_failed`
- `webhook.replay_rejected`
- `webhook.secret_rotated`
