---
title: "ADR 0003: Idempotency First"
description: Every state-mutating operation is safely retryable.
---

## Status

Accepted.

## Decision

Mutating operations require idempotency keys. SDKs, CLI, and sidecar generate keys automatically.

## Consequences

Retries are safe, duplicate client submissions collapse to one resource, and payload mismatch returns a stable conflict error.
