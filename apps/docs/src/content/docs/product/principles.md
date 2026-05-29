---
title: Engineering Principles
description: Non-negotiable engineering rules for UBAG.
---

## Principles

- Open-source components only; no required proprietary runtime dependency.
- API stability is a covenant; public surfaces are versioned and conformance tested.
- Idempotency by default for state-mutating operations.
- Backpressure everywhere: bounded queues, pools, memory, and request bodies.
- Observability is mandatory: logs, metrics, traces, and error context.
- Secrets never touch disk in plaintext.
- Configuration is data; code is logic.
- Lightweight mode must always work.
- No telemetry phones home unless explicitly enabled.
- Every error is named, documented, and stable.

## Implementation consequence

The control plane must be boring, strict, and measurable. Browser automation can be adaptive, but durable job state, audit, idempotency, and public contracts stay deterministic.
