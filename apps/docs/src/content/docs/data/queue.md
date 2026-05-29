---
title: Queue Abstraction
description: Queue interface and profile-specific implementations.
---

## Correction

The edge profile is SQLite-only. River is Postgres-backed, so edge cannot use River directly. UBAG must provide a SQLite queue adapter behind the same queue interface.

## Interface

The queue port supports enqueue, lease, acknowledge, reject, extend lease, cancel, dead-letter, and stats.

## Gateway dispatch runtime

The gateway now has an internal executor dispatch port for accepted jobs. The default `noop` dispatcher preserves local control-plane behavior, while `UBAG_EXECUTOR_MODE=file` writes one gateway-stamped dispatch envelope per accepted job into a local file spool. The file spool is a single-node dispatch bridge with `pending`, `leased`, `done`, `failed`, and `cancelled` directories. The embedded consumer can atomically lease envelopes, invoke the configured Python worker, and ingest normalized worker events/results into gateway job history.

`UBAG_EXECUTOR_MODE=nats` publishes accepted jobs and cancellation notices to NATS JetStream subjects using `UBAG_NATS_URL`, `UBAG_NATS_STREAM`, and `UBAG_NATS_SUBJECT`. When `UBAG_WORKER_CONSUMER_ENABLED=true`, the same embedded worker consumer creates/uses a durable pull consumer with `UBAG_NATS_WORKER_DURABLE`, `UBAG_NATS_WORKER_ACK_WAIT_MS`, `UBAG_NATS_WORKER_NAK_DELAY_MS`, `UBAG_NATS_WORKER_FETCH_WAIT_MS`, and `UBAG_NATS_WORKER_MAX_DELIVER`. Job messages are filtered on `<subject>.*` so cancellation notices on `<subject>.cancel.<jobID>` are not executed as work.

Before any job is stored or dispatched, the gateway rejects executable payloads containing credentials, cookies, tokens, API keys, browser storage/session state, client-supplied noVNC URLs, private keys, MFA/TOTP material, or CAPTCHA-solving instructions.

## Implementations

- `sqlitequeue`: v0 edge.
- `gateway file spool`: v0 local dispatch/consumer bridge, enabled with `UBAG_EXECUTOR_MODE=file`, `UBAG_EXECUTOR_SPOOL_DIR`, and optional `UBAG_WORKER_CONSUMER_ENABLED=true` under ignored runtime storage such as `var/executor-spool`.
- `pgqueue` or River-compatible adapter: local Postgres dev where useful.
- `natsqueue`: gateway dispatch and embedded durable worker consumption through NATS JetStream, enabled with `UBAG_EXECUTOR_MODE=nats` and optional `UBAG_WORKER_CONSUMER_ENABLED=true`.

## Required semantics

Priority lanes, delayed jobs, retries, deduplication, idempotency, DLQ, visibility timeout, cancellation, and backpressure stats must be tested across every adapter.
