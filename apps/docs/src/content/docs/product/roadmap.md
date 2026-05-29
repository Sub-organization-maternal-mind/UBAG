---
title: Roadmap
description: Staged delivery plan from docs baseline to enterprise ecosystem.
---

## Milestone 0: Docs-first baseline

- PRD, progress ledger, ADRs, docs site, and coverage check.
- Architecture and public contract docs.
- Docs-first gate completed before product service code was introduced.

## v0: Edge MVP

- REST jobs API, SSE, app-secret auth, idempotency, stable errors.
- SQLite/localfs edge contracts; runtime gateway persistence wiring follows after the memory/Postgres/MinIO v0 slice.
- Mock target, generic adapter manifests, adapter SDK contract, and provider-safe manual-session stubs for DeepSeek, ChatGPT, Claude, Gemini, Mistral, and Perplexity.
- Basic worker safe-mode dispatch and manual login handoff events.
- Minimal CLI, sidecar, static dashboard prototype, guarded WebSocket baseline, built-in template catalog/application, cache status, and opt-in small-profile Postgres, NATS, MinIO, signed webhook outbox, observability, and TypeScript/Python/Go SDK wave.

## v1: Production OSS platform

- Production hardening for the implemented Docker Compose small profile, Postgres, MinIO, optional NATS, signed webhooks, observability, and first SDK wave.
- Runtime SQLite/localfs persistence, native gRPC/gRPC-Web serving, and richer bidirectional WebSocket/dashboard semantics.
- Live user-owned provider adapters, expanded workflow/template/cache behavior, normalization, and semantic cache hardening.
- RBAC/ABAC runtime hardening, audit export, rate limits, and secret rotation.
- Rust, .NET, Java, and additional SDK transport conformance.

## v2: Enterprise and ecosystem

- Remaining SDKs.
- WASM plugins and adapter marketplace.
- Helm, Terraform, installers, GitOps.
- SSO/SAML/SCIM, mTLS, multi-region, SIEM export, mobile monitor, DR.
