---
title: Worker Architecture
description: Browser worker process boundaries and execution flow.
---

## Responsibility

The browser worker owns browser lifecycle, target adapter execution, token streaming, artifact capture, session health, and drift evidence. It does not own durable job state.

## Modules

- `queue_client`
- `session_pool`
- `browser_launcher`
- `adapter_runtime`
- `artifact_store`
- `telemetry`
- `watchdog`
- `control_plane`

## Execution protocol

Workers lease jobs only when capacity exists and the tenant/target breaker is closed. They emit step events and acknowledge the queue only after the completion or failure event is durably accepted by the control-plane path.

## Current gateway handoff

The v0 gateway includes an internal executor dispatch boundary. Accepted jobs are server-stamped with API version, tenant, app, job ID, trace ID, idempotency key, and command payload before dispatch. The default dispatcher is `noop`; the local file-spool dispatcher writes pending envelopes under `pending/`, and the NATS dispatcher publishes job envelopes to JetStream subjects while publishing cancellation notices on separate cancel subjects.

When `UBAG_WORKER_CONSUMER_ENABLED=true`, the gateway starts a local/dev worker consumer that can lease from either the file spool or the configured NATS durable consumer. It reconstructs the executable envelope from the persisted gateway job before invoking `UBAG_WORKER_PYTHON` with `UBAG_WORKER_SCRIPT`, reads bounded JSONL worker output, and asks the job store to append gateway-sequenced worker events/results before acknowledging the queue lease. File-spool terminal jobs are finalized under `done/`, `failed/`, or `cancelled`; NATS messages are acked only after terminal worker events or a synthetic retryable failure are durably accepted, transient store/setup failures are nacked with delay, and malformed or mismatched envelopes are terminated as poison messages. Live provider browser runtime remains follow-up work.

## Safe mode

Provider automation uses user-owned manual login, rate limits, audit, and no bundled CAPTCHA solver.
