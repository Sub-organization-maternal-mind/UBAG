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

## Cancellation

Cancellation is cooperative first. The worker receives a cancel token and checks between adapter steps. Hard cancel kills the browser context after a grace period.

## Workflows

v1 introduces DAG workflows with per-step retries, workflow timeout, partial results, CEL conditions, and compensating steps.
