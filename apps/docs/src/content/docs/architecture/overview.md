---
title: Architecture Overview
description: UBAG control plane, worker fleet, storage, and client architecture.
---

## System shape

UBAG separates durable control-plane state from browser execution.

- Clients use SDKs, REST, WebSocket, SSE, gRPC, CLI, sidecar, and webhooks.
- Caddy handles ingress for small+ deployments.
- The Go control plane validates requests, authenticates principals, enforces idempotency, queues work, streams events, dispatches webhooks, and owns durable state.
- Python browser workers execute adapter steps and emit events back to the control plane.
- Storage adapters keep the public API identical across edge and small+ profiles.

## Control-plane ownership

The Go side owns durable job state, audit, idempotency, tenancy, webhook delivery, and public contracts. Workers do not directly mutate core job tables.

## Worker ownership

Workers own browser lifecycle, session pools, adapter execution, artifact capture, drift evidence, and local resource governance.
