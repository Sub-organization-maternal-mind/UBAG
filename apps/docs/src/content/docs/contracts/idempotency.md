---
title: Idempotency
description: Safe retry semantics for every state-mutating operation.
---

## Scope

Idempotency keys are scoped by:

```text
tenant_id + app_id + operation + idempotency_key
```

## Storage

The idempotency store records canonical request hash, resource ID, status, replay response pointer, expiry, and conflict state.

## Behavior

- First request creates the resource.
- Same key and same payload returns the original resource/result with `idempotent_replay: true`.
- Same key and different payload returns `409` and `UBAG-VALIDATION-IDEMPOTENCY-CONFLICT-001`.
- SDKs, CLI, and sidecar auto-generate ULID keys.
- Raw mutating REST calls must provide `Idempotency-Key`.
