---
title: Webhooks
description: Signed callback delivery, retry, and replay model.
---

## Delivery

Webhooks are outbound signed HTTP POST deliveries emitted for job, workflow, adapter, session, and administrative events.

The implemented gateway slice supports job terminal callbacks configured on the
job request under `callbacks.webhook_url`, required
`callbacks.webhook_secret_id`, and optional `callbacks.event_types`. Callback
URLs are validated before job storage: HTTPS is required by default,
userinfo/fragments and secret-looking query keys are rejected, public hosts must
match `UBAG_WEBHOOK_ALLOWED_HOSTS` unless `UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST`
is explicitly enabled, and loopback/private/local/metadata hosts are blocked
unless an explicit operator policy enables them.

## Signature

v1 uses HMAC-SHA256 over timestamp, nonce, and body. Consumers must reject stale timestamps and replayed nonces.

The gateway sender signs `timestamp.nonce.body` and emits
`Ubag-Webhook-Signature`, `Ubag-Webhook-Timestamp`, and
`Ubag-Webhook-Nonce`. It also includes bounded identifiers such as delivery ID,
webhook ID, job ID, event type, event ID, trace ID, and API version. Signing
secrets are resolved from `UBAG_WEBHOOK_SECRET` or from the configured
`UBAG_WEBHOOK_SECRET_ENV_PREFIX` plus `callbacks.webhook_secret_id`; secrets are
not persisted in the outbox tables.

## Reliability

- Delivery attempts are persisted in `gateway_webhook_deliveries` and `gateway_webhook_attempts` when `UBAG_WEBHOOK_OUTBOX=postgres`.
- Retries use bounded exponential backoff with jitter through the opt-in worker enabled by `UBAG_WEBHOOK_WORKER_ENABLED=true`.
- The default worker refuses redirects, limits response reads, records bounded error classes, and dead-letters exhausted deliveries.
- Operators can replay an existing scoped delivery through `/v1/webhooks/replay`; fabricated delivery IDs are rejected.
- The in-memory outbox is available for local tests, but shared small-profile runs should use the Postgres outbox for durable retries.

## Audit

Creating endpoints, rotating secrets, disabling endpoints, and replaying deliveries are state-changing actions and must be audited.
