---
title: Job Lifecycle
description: Job states, retries, cancellation, workflows, and DLQ semantics.
---

## States

```text
created -> queued -> assigned -> running -> token_streaming -> completing -> completed
                                  |             |               -> completed_with_warnings
                                  |             |               -> failed_terminal -> dead_letter
                                  |             -> failed_retryable -> queued
                                  -> cancelled
                                  -> timed_out
```

## Retry policy

Default retries use exponential backoff with jitter. Transient network, target busy, worker crash, and retryable browser failures are retried. Validation, quota, authorization, and permanent adapter failures are not retried.

## Current dispatch boundary

The v0 gateway still returns `202` with `queued` for accepted create and retry requests. It dispatches newly accepted work to an internal executor port exactly once per idempotent operation. With `UBAG_EXECUTOR_MODE=file`, pending dispatch envelopes can be leased by the embedded local worker consumer, executed through the Python worker, and finalized as gateway-authored lifecycle events/results. With `UBAG_EXECUTOR_MODE=nats`, the embedded consumer uses a durable JetStream pull consumer and acknowledges messages only after terminal worker events or a synthetic retryable failure are accepted by the job store. Public queue/executor internals do not leak into job status values; storage states such as `pending`, `leased`, `done`, `failed`, and `cancelled` are mapped back into the public lifecycle states above.

## Queue reason

A job that is still `queued` may carry two optional fields on its response:

- `queue_reason`: why it has not started yet, one of the coarse values below. It is computed when the job is read, not stored, and the gateway leaves it out (or sends `null`) when it has nothing to say, so a client must treat absence as "unknown", never as an error. It is only ever set while `status` is `queued`.
- `queue_reason_since`: when the current reason began. It restarts when the reason changes, so it is not the creation time.

| `queue_reason` | Meaning |
|----------------|---------|
| `waiting_for_worker` | Every worker is busy and this job is behind others. |
| `waiting_for_identity` | The provider account this job needs is running another job. One operation runs per identity at a time. |
| `waiting_for_capacity` | The capacity assigned to the gateway, including helper capacity, is fully used or temporarily reduced. |
| `waiting_for_node` | The job is tied to a helper node (a bound provider profile or an existing conversation) that is not available right now. |
| `retry_backoff` | A retryable failure is waiting out its delay before the next attempt. |
| `temporarily_unavailable` | The gateway cannot place work at the moment and retries on its own. |

The values are deliberately coarse. Tenants never see which node, which capacity reservation or which other project is involved; operators read the fine-grained reasons, as counts only, from `GET /v1/fleet/summary` (`held_by_reason`, needs the `fleet:read` action, held by the operator and admin roles). `GET /v1/jobs/summary` groups the queued jobs by the same coarse values in `queued_by_reason`.

Queue reasons add no job status, no event type and no error code. A queued job event may carry the same value as `data.reason`, and a provider block keeps using the `blocked` event with its own `data.reason`. Being held is not an error: `UBAG-QUEUE-BACKPRESSURE-002` stays the answer to a create request that finds the queue full.

## Cancellation

Cancellation is cooperative first. The worker receives a cancel token and checks between adapter steps. Hard cancel kills the browser context after a grace period.

## Workflows

v1 introduces DAG workflows with per-step retries, workflow timeout, partial results, CEL conditions, and compensating steps.
