---
title: Control Plane
description: Go gateway and orchestration boundaries.
---

## Responsibility

The control plane owns all durable platform state and public API behavior.

## v0 shape

- One modular Go gateway binary implements the current public HTTP surface; separate orchestrator, outbox relay, and webhook dispatcher roles share the same contract boundaries.
- REST `/v1/jobs`, `/v1/health`, `/v1/ready`, `/v1/version`, metrics, collection endpoints for workflows, built-in templates, targets/adapters, apps, devices, webhooks, cache status, and audit, guarded WebSocket upgrade, and SSE job events first.
- App-secret authentication, request validation, idempotency, stable errors, job lifecycle, and audit-ready event flow.
- Internal executor dispatch is wired behind job creation and retry. It defaults to `noop`, can write local file-spool dispatch envelopes, can publish/consume NATS JetStream job messages, can run an embedded local worker consumer/result-ingestion loop, and refuses unsafe executable payloads before storage or enqueue.
- Template lookup/application is wired into job creation before payload validation, idempotency hashing, storage, and executor enqueue. The v0 catalog is memory-backed and built-in; durable versioned template storage and render dry-runs remain v1 work.

## v1 expansion

- Native gRPC/gRPC-Web implementations, batch jobs, richer workflow/cache execution, durable template management, and production-store hardening for the route surfaces already present in v0.
- Route-scope authorization plus service-layer ABAC.
- Expanded worker fleet scaling, outbox-driven queue publication hardening, and webhook delivery operations at production scale.

## Middleware order

Trace ID, recovery, body limit, structured log, API version, authentication, tenant/app/device status, route authorization, rate limit, decode, validation, ABAC, idempotency, handler transaction, audit/outbox.
