---
title: Error Catalog
description: Stable UBAG error format and namespace policy.
---

## Format

```json
{
  "error": {
    "code": "UBAG-ADAPTER-DRIFT-014",
    "category": "adapter",
    "message": "Target UI changed; submit button selector not found",
    "retryable": true,
    "retry_after_ms": 5000,
    "details": {
      "adapter": "deepseek_web@1.7.3",
      "step": "submit_prompt"
    },
    "doc_url": "https://docs.ubag.dev/errors/UBAG-ADAPTER-DRIFT-014",
    "trace_id": "trace-id"
  }
}
```

## Namespaces

`UBAG-AUTH`, `UBAG-AUTHZ`, `UBAG-VALIDATION`, `UBAG-QUOTA`, `UBAG-RATE`, `UBAG-QUEUE`, `UBAG-WORKER`, `UBAG-BROWSER`, `UBAG-ADAPTER`, `UBAG-TARGET`, `UBAG-TEMPLATE`, `UBAG-CACHE`, `UBAG-WEBHOOK`, `UBAG-SIDECAR`, and `UBAG-INTERNAL`.

## Queue holds are not errors

A queued job that is waiting for a worker, a provider identity, helper capacity or a node reports why through the optional `queue_reason` field (see [Job Lifecycle](/contracts/job-lifecycle#queue-reason)). That adds no error code and no error category. `UBAG-QUEUE-BACKPRESSURE-002` is still only the answer to a create request that finds the queue depth limit reached.

## Registry rule

Every code must be backed by a registry entry with HTTP status, gRPC mapping, retryability, operator guidance, and test coverage.
